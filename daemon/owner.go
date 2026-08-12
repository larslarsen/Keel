// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

const ownerConnectTimeout = 8 * time.Second

func runOwnerCommand(args []string) int {
	mode := "run"
	if len(args) > 0 {
		mode = args[0]
	}
	switch mode {
	case "run":
		return runOwnerProcess()
	case "stop":
		if err := requestOwnerControl("shutdown"); err != nil {
			fmt.Fprintln(os.Stderr, "owner stop:", err)
			return 1
		}
		fmt.Println("Keel desktop owner stopped.")
		return 0
	case "status":
		if err := requestOwnerControl("status"); err != nil {
			fmt.Fprintln(os.Stderr, "owner status:", err)
			return 1
		}
		fmt.Println("Keel desktop owner is running.")
		return 0
	default:
		fmt.Fprintln(os.Stderr, "owner: expected run, stop, or status")
		return 2
	}
}

// runProxy is the default browser-launched mode. It owns no Store or swarm;
// after the authenticated owner handshake it forwards native frames without
// decoding or rewriting their application envelopes.
func runProxy() int {
	p, err := resolveOwnerPaths()
	if err != nil {
		return proxyStartupError("owner_paths", err)
	}
	secret, err := ownerSecret(p)
	if err != nil {
		return proxyStartupError("owner_secret", err)
	}
	conn, err := connectOrSpawnOwner(p, secret)
	if err != nil {
		return proxyStartupError("owner_unavailable", err)
	}
	defer conn.Close()

	errCh := make(chan error, 2)
	go func() { errCh <- copyNativeToOwner(os.Stdin, conn) }()
	go func() { errCh <- copyOwnerToNative(conn, os.Stdout) }()
	err = <-errCh
	_ = conn.Close() // wake the opposite copier
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		log.Printf("native proxy disconnected: %v", err)
	}
	return 0
}

func proxyStartupError(code string, err error) int {
	log.Printf("%s: %v", code, err)
	env, makeErr := bridge.NewEnvelope("0", "ERROR", bridge.ErrorPayload{
		Message: err.Error(), Code: code,
	})
	if makeErr == nil {
		if raw, encodeErr := env.Encode(); encodeErr == nil {
			_ = bridge.WriteMessage(os.Stdout, raw)
		}
	}
	return 1
}

func copyNativeToOwner(in io.Reader, out io.Writer) error {
	for {
		raw, err := bridge.ReadMessage(in)
		if err != nil {
			return err
		}
		if err := writeIPCFrame(out, raw, bridge.MaxBrowserToHost); err != nil {
			return err
		}
	}
}

func copyOwnerToNative(in io.Reader, out io.Writer) error {
	for {
		raw, err := readIPCFrame(in, bridge.MaxHostToBrowser)
		if err != nil {
			return err
		}
		if err := bridge.WriteMessage(out, raw); err != nil {
			return err
		}
	}
}

func connectOrSpawnOwner(p ownerPaths, secret string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	conn, err := dialOwnerEndpoint(ctx, p)
	cancel()
	if err == nil {
		if _, err := startOwnerHandshake(conn, secret, "session"); err != nil {
			_ = conn.Close()
			return nil, err // never replace or kill an incompatible/unknown owner
		}
		return conn, nil
	}
	if err := spawnOwner(p); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(ownerConnectTimeout)
	backoff := 20 * time.Millisecond
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		conn, lastErr = dialOwnerEndpoint(ctx, p)
		cancel()
		if lastErr == nil {
			if _, err := startOwnerHandshake(conn, secret, "session"); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		}
		time.Sleep(backoff)
		if backoff < 250*time.Millisecond {
			backoff *= 2
		}
	}
	return nil, fmt.Errorf("owner did not start within %s: %w", ownerConnectTimeout, lastErr)
}

func spawnOwner(p ownerPaths) error {
	if err := prepareOwnerPaths(p); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(p.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := secureOwnerPath(p.log, false); err != nil {
		_ = logFile.Close()
		return err
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		_ = logFile.Close()
		return err
	}
	cmd := exec.Command(exe, "owner", "run")
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachOwnerProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		_ = logFile.Close()
		return err
	}
	_ = cmd.Process.Release()
	_ = devNull.Close()
	_ = logFile.Close()
	return nil
}

func requestOwnerControl(action string) error {
	p, err := resolveOwnerPaths()
	if err != nil {
		return err
	}
	secret, err := readOwnerSecret(p.secret)
	if err != nil {
		return fmt.Errorf("not installed or owner credentials unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialOwnerEndpoint(ctx, p)
	if err != nil {
		return fmt.Errorf("not running: %w", err)
	}
	defer conn.Close()
	_, err = startOwnerHandshake(conn, secret, action)
	return err
}

func runOwnerProcess() int {
	p, err := resolveOwnerPaths()
	if err != nil {
		log.Printf("owner paths: %v", err)
		return 1
	}
	secret, err := ownerSecret(p)
	if err != nil {
		log.Printf("owner secret: %v", err)
		return 1
	}
	ln, err := listenOwnerEndpoint(p)
	if errors.Is(err, errOwnerAlreadyRunning) {
		return 0 // another connect-or-spawn contender won
	}
	if err != nil {
		log.Printf("owner endpoint: %v", err)
		return 1
	}
	defer ln.Close()

	// Ownership is established before SQLite or libp2p is touched. Losing
	// candidates therefore cannot become secondary database/swarm owners.
	st, err := store.Open(os.Getenv("KEEL_DB"))
	if err != nil {
		log.Printf("store open: %v", err)
		return 1
	}
	defer st.Close()

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()
	swarmCtx, stopSwarm := context.WithCancel(context.Background())
	swarmDone := make(chan struct{})
	go func() {
		defer close(swarmDone)
		startSwarm(swarmCtx, st)
	}()

	err = serveOwner(ctx, ln, secret, st)
	stopSwarm()
	<-swarmDone
	// The supervisor owns the node: it shuts the outbound gate, cancels the
	// node's goroutines and closes the host, in that order (WO-077).
	supervisor.stopAll()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
		log.Printf("owner: %v", err)
		return 1
	}
	return 0
}

// serveOwner multiplexes independent native-host sessions over one Store and
// swarm owner. Closing a client removes only that connection; owner shutdown
// closes the listener and all sessions, then waits for their bridge loops.
func serveOwner(ctx context.Context, ln net.Listener, secret string, st *store.Store) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stopOnce sync.Once
	requestStop := func() { stopOnce.Do(cancel) }

	var mu sync.Mutex
	connections := map[net.Conn]struct{}{}
	hub := newOwnerSessionHub()
	var sessions sync.WaitGroup
	acceptErr := make(chan error, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			mu.Lock()
			connections[conn] = struct{}{}
			mu.Unlock()
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				defer func() {
					mu.Lock()
					delete(connections, conn)
					mu.Unlock()
					_ = conn.Close()
				}()
				action, err := acceptOwnerHandshake(conn, secret)
				if err != nil {
					log.Printf("owner client rejected: %v", err)
					return
				}
				switch action {
				case "shutdown":
					requestStop()
				case "status":
					return
				case "session":
					sessionCtx, cancelSession := context.WithCancel(ctx)
					writer := &syncWriter{w: conn}
					// Joined only once the browser side has negotiated
					// (WO-081), not at accept. A hub member receives
					// unsolicited owner-wide events, so registering before
					// HELLO would push application frames at a client that has
					// not agreed a schema — including one we just told it is
					// incompatible. `owner_ipc:1` authenticates the transport;
					// it does not say what the browser can parse.
					defer hub.remove(writer)
					sessionCtx = withContributionPublisher(sessionCtx, hub)
					runSession(sessionCtx, conn, writer, st, func() { hub.add(writer) })
					cancelSession()
				}
			}()
		}
	}()

	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-acceptErr:
	}
	_ = ln.Close()
	mu.Lock()
	for conn := range connections {
		_ = conn.Close()
	}
	mu.Unlock()
	sessions.Wait()
	return err
}

// ownerSessionHub carries owner-wide state changes to every authenticated
// browser session. Application replies retain their request id; these events
// use a reserved owner-event id so they cannot resolve another session's RPC.
type ownerSessionHub struct {
	mu       sync.RWMutex
	sessions map[*syncWriter]struct{}
	nextID   atomic.Uint64
}

func newOwnerSessionHub() *ownerSessionHub {
	return &ownerSessionHub{sessions: make(map[*syncWriter]struct{})}
}

func (h *ownerSessionHub) add(w *syncWriter) {
	h.mu.Lock()
	h.sessions[w] = struct{}{}
	h.mu.Unlock()
}

func (h *ownerSessionHub) remove(w *syncWriter) {
	h.mu.Lock()
	delete(h.sessions, w)
	h.mu.Unlock()
}

func (h *ownerSessionHub) publishContribution(state contributionState) {
	env, err := bridge.NewEnvelope(
		fmt.Sprintf("owner-event-%d", h.nextID.Add(1)),
		"CONTRIBUTION_STATUS",
		contributionPayload(state),
	)
	if err != nil {
		log.Printf("owner contribution event: %v", err)
		return
	}
	h.mu.RLock()
	writers := make([]*syncWriter, 0, len(h.sessions))
	for w := range h.sessions {
		writers = append(writers, w)
	}
	h.mu.RUnlock()
	for _, w := range writers {
		if err := writeEnv(w, env); err != nil {
			log.Printf("owner contribution event delivery: %v", err)
		}
	}
}
