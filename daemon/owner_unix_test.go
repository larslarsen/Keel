// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
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
	// Keep this transport test off the public network while still exercising a
	// successful runtime transition through the same supervisor path as the
	// owner. WO-077's real-node policy transitions have their own tests.
	oldSupervisor := supervisor
	testSupervisor := &swarmSupervisor{
		effective: store.LevelPersonal, transition: transitionIdle,
	}
	testSupervisor.launchFn = func(
		context.Context, *store.Store, int,
	) (*swarm.Node, context.CancelFunc, error) {
		return nil, func() {}, nil
	}
	supervisor = testSupervisor
	t.Cleanup(func() {
		testSupervisor.stopAll()
		supervisor = oldSupervisor
	})
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
			if err := negotiateOwnerSession(conn, id); err != nil {
				errs <- fmt.Errorf("response crossed owner sessions or failed to negotiate: %w", err)
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
	observer, err := dialTestOwner(p, "shared-secret", "session")
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if err := negotiateOwnerSession(observer, "observer-ready"); err != nil {
		t.Fatal(err)
	}
	// The writer negotiates too. SET_CONTRIBUTION is gated on
	// contribution_runtime, and an un-negotiated session is not in the
	// broadcast hub at all (WO-081), so skipping this would leave the reads
	// below waiting forever on an event that is correctly never sent.
	if err := negotiateOwnerSession(writer, "writer-ready"); err != nil {
		t.Fatal(err)
	}
	set, _ := bridge.NewEnvelope("set", "SET_CONTRIBUTION", map[string]any{"level": 2})
	if err := sendAndExpectID(writer, set, "set"); err != nil {
		t.Fatal(err)
	}
	// The requester gets its correlated result first, then every authenticated
	// session—including an otherwise idle browser—gets the owner-wide event.
	for name, conn := range map[string]net.Conn{"requester": writer, "observer": observer} {
		event, err := readOwnerEnvelope(conn)
		if err != nil {
			t.Fatalf("%s contribution event: %v", name, err)
		}
		if event.Type != "CONTRIBUTION_STATUS" {
			t.Fatalf("%s event type = %q, want CONTRIBUTION_STATUS", name, event.Type)
		}
		var status struct {
			Effective int `json:"effective_level"`
		}
		if err := json.Unmarshal(event.Payload, &status); err != nil || status.Effective != 2 {
			t.Fatalf("%s event effective level = %d, err = %v", name, status.Effective, err)
		}
	}
	get, _ := bridge.NewEnvelope("get", "GET_CONTRIBUTION", map[string]any{})
	response, err := sendOwnerRequest(observer, get)
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
	return readOwnerEnvelope(conn)
}

func readOwnerEnvelope(conn net.Conn) (*bridge.Envelope, error) {
	response, err := readIPCFrame(conn, bridge.MaxHostToBrowser)
	if err != nil {
		return nil, err
	}
	return bridge.ParseEnvelope(response)
}

// testHelloPayload is a compatible browser HELLO (WO-081). Written out rather
// than reusing the daemon's own map so a change to DaemonCaps that silently
// drops a capability shows up here as a behavioural difference, not as two
// constants agreeing with each other.
func testHelloPayload() map[string]any {
	return map[string]any{
		"client_version": "0.1.0",
		"api":            map[string]any{"min": bridge.APIMin, "max": bridge.APIMax},
		"required":       map[string]any{bridge.CapCore: 1},
		"optional": map[string]any{
			bridge.CapSelectors: 1, bridge.CapTikTok: 1, bridge.CapScrollHistory: 1,
			bridge.CapPeerSearch: 1, bridge.CapWordStats: 1, bridge.CapQueue: 1,
			bridge.CapContributionRuntime: 1,
		},
	}
}

// negotiateOwnerSession performs the HELLO exchange a real browser session must
// complete before any application RPC. Every owner test drives it explicitly:
// the gate is the thing under test, so folding it into dialTestOwner would hide
// exactly the step that can regress.
func negotiateOwnerSession(conn net.Conn, id string) error {
	hello, err := bridge.NewEnvelope(id, "HELLO", testHelloPayload())
	if err != nil {
		return err
	}
	got, err := sendOwnerRequest(conn, hello)
	if err != nil {
		return err
	}
	if got.ID != id || got.Type != "HELLO_ACK" {
		return fmt.Errorf("HELLO answered with %s/%s", got.ID, got.Type)
	}
	var ack bridge.HelloAckPayload
	if err := json.Unmarshal(got.Payload, &ack); err != nil {
		return err
	}
	if !ack.Compatible {
		return fmt.Errorf("negotiation failed: %s: %s", ack.Code, ack.Reason)
	}
	return nil
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

// TestUnnegotiatedSessionReceivesNoOwnerBroadcast is the hub half of WO-081's
// gate.
//
// Authenticating the transport (`owner_ipc:1`) says a local process is entitled
// to talk to this owner. It says nothing about what the *browser* behind it can
// parse. Owner-wide events are unsolicited application frames, so a session
// that has not agreed an API and capability set — or one we have just told is
// incompatible — must not be in the hub at all. Otherwise "no RPC is accepted
// until negotiation succeeds" holds only for the direction the client drives.
func TestUnnegotiatedSessionReceivesNoOwnerBroadcast(t *testing.T) {
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

	oldSupervisor := supervisor
	testSupervisor := &swarmSupervisor{
		effective: store.LevelPersonal, transition: transitionIdle,
	}
	testSupervisor.launchFn = func(
		context.Context, *store.Store, int,
	) (*swarm.Node, context.CancelFunc, error) {
		return nil, func() {}, nil
	}
	supervisor = testSupervisor
	t.Cleanup(func() {
		testSupervisor.stopAll()
		supervisor = oldSupervisor
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = serveOwner(ctx, ln, "shared-secret", st) }()

	// silent has completed the owner IPC handshake and nothing more.
	silent, err := dialTestOwner(p, "shared-secret", "session")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	// stale speaks the pre-WO-081 HELLO: a client string and no capability
	// negotiation. It gets an ack saying so, and must be treated as absent.
	stale, err := dialTestOwner(p, "shared-secret", "session")
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	legacy, _ := bridge.NewEnvelope("legacy", "HELLO",
		map[string]any{"client": "keel-extension", "version": "0.1.0"})
	got, err := sendOwnerRequest(stale, legacy)
	if err != nil {
		t.Fatal(err)
	}
	var ack bridge.HelloAckPayload
	if err := json.Unmarshal(got.Payload, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Compatible {
		t.Fatal("a pre-WO-081 HELLO negotiated successfully; it declares no capabilities")
	}

	driver, err := dialTestOwner(p, "shared-secret", "session")
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if err := negotiateOwnerSession(driver, "driver-ready"); err != nil {
		t.Fatal(err)
	}
	set, _ := bridge.NewEnvelope("set", "SET_CONTRIBUTION", map[string]any{"level": 2})
	if err := sendAndExpectID(driver, set, "set"); err != nil {
		t.Fatal(err)
	}
	// The driver is negotiated, so it is in the hub and does get the event.
	event, err := readOwnerEnvelope(driver)
	if err != nil || event.Type != "CONTRIBUTION_STATUS" {
		t.Fatalf("negotiated session missed its own broadcast: %v / %v", event, err)
	}

	// Neither of the others may have been sent anything. Read with a deadline:
	// a timeout is the pass condition, and any frame at all is the failure.
	for name, conn := range map[string]net.Conn{"silent": silent, "pre-WO-081": stale} {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		frame, err := readOwnerEnvelope(conn)
		if err == nil {
			t.Errorf("%s session received %q without negotiating", name, frame.Type)
			continue
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Errorf("%s session read failed for the wrong reason: %v", name, err)
		}
	}
}
