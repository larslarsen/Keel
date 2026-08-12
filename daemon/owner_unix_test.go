// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

func testOwnerPaths(t *testing.T) ownerPaths {
	t.Helper()
	dir := t.TempDir()
	return ownerPaths{
		configDir:  dir,
		runtimeDir: filepath.Join(dir, "runtime"),
		endpoint:   filepath.Join(dir, "runtime", "owner.sock"),
		guard:      filepath.Join(dir, "runtime", "election"),
		secret:     filepath.Join(dir, "owner.secret"),
		log:        filepath.Join(dir, "owner.log"),
	}
}

func TestOwnerEndpointElectsExactlyOneWinner(t *testing.T) {
	p := testOwnerPaths(t)
	const contenders = 10
	start := make(chan struct{})
	results := make(chan struct {
		ln  net.Listener
		err error
	}, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ln, err := listenOwnerEndpoint(p)
			results <- struct {
				ln  net.Listener
				err error
			}{ln, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var winner net.Listener
	for r := range results {
		if r.err == nil {
			if winner != nil {
				t.Fatal("more than one owner won endpoint election")
			}
			winner = r.ln
			continue
		}
		if !errors.Is(r.err, errOwnerAlreadyRunning) {
			t.Fatalf("losing contender: %v", r.err)
		}
	}
	if winner == nil {
		t.Fatal("no owner won endpoint election")
	}
	fi, err := os.Stat(p.runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("runtime directory mode = %o", fi.Mode().Perm())
	}
	fi, err = os.Lstat(p.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("owner socket mode = %o", fi.Mode().Perm())
	}
	if err := winner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerEndpointRecoversOwnedStaleSocket(t *testing.T) {
	p := testOwnerPaths(t)
	if err := prepareOwnerPaths(p); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: p.endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p.endpoint); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
	ln, err := listenOwnerEndpoint(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
}

func TestOwnerMultiplexesSessionsAndSurvivesClientEOF(t *testing.T) {
	p := testOwnerPaths(t)
	ln, err := listenOwnerEndpoint(p)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(p.configDir, "owner.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serveOwner(ctx, ln, "shared-secret", st) }()

	const clients = 10
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := dialTestOwner(p, "shared-secret", "session")
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			id := "client-" + string(rune('A'+i))
			env, _ := bridge.NewEnvelope(id, "HELLO", map[string]any{})
			raw, _ := env.Encode()
			if err := writeIPCFrame(conn, raw, bridge.MaxBrowserToHost); err != nil {
				errs <- err
				return
			}
			response, err := readIPCFrame(conn, bridge.MaxHostToBrowser)
			if err != nil {
				errs <- err
				return
			}
			got, err := bridge.ParseEnvelope(response)
			if err != nil || got.ID != id || got.Type != "HELLO_ACK" {
				errs <- errors.New("response crossed owner sessions")
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	// Every original native client is gone; the owner must still accept a new
	// session and expose the same Store.
	writer, err := dialTestOwner(p, "shared-secret", "session")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	set, _ := bridge.NewEnvelope("set", "SET_CONTRIBUTION", map[string]any{"level": 2})
	if err := sendAndExpectID(writer, set, "set"); err != nil {
		t.Fatal(err)
	}
	reader, err := dialTestOwner(p, "shared-secret", "session")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	get, _ := bridge.NewEnvelope("get", "GET_CONTRIBUTION", map[string]any{})
	response, err := sendOwnerRequest(reader, get)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Level int `json:"level"`
	}
	if err := json.Unmarshal(response.Payload, &payload); err != nil || payload.Level != 2 {
		t.Fatalf("shared contribution level = %d, err = %v", payload.Level, err)
	}

	shutdown, err := dialTestOwner(p, "shared-secret", "shutdown")
	if err != nil {
		t.Fatal(err)
	}
	_ = shutdown.Close()
	select {
	case err := <-served:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serveOwner shutdown = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not stop after authenticated shutdown")
	}
}

func dialTestOwner(p ownerPaths, secret, action string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dialOwnerEndpoint(ctx, p)
	if err != nil {
		return nil, err
	}
	if _, err := startOwnerHandshake(conn, secret, action); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func sendOwnerRequest(conn net.Conn, env *bridge.Envelope) (*bridge.Envelope, error) {
	raw, err := env.Encode()
	if err != nil {
		return nil, err
	}
	if err := writeIPCFrame(conn, raw, bridge.MaxBrowserToHost); err != nil {
		return nil, err
	}
	response, err := readIPCFrame(conn, bridge.MaxHostToBrowser)
	if err != nil {
		return nil, err
	}
	return bridge.ParseEnvelope(response)
}

func sendAndExpectID(conn net.Conn, env *bridge.Envelope, id string) error {
	got, err := sendOwnerRequest(conn, env)
	if err != nil {
		return err
	}
	if got.ID != id {
		return errors.New("wrong response correlation id")
	}
	return nil
}
