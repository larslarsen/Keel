// SPDX-License-Identifier: Apache-2.0
// End-to-end: build the real binary, register it, and talk to it exactly the
// way a browser does.
//
// Every test in this package until now exercised a function. None of them ran
// the program. A whole day of live QA went into failures that this test would
// have caught in seconds on any machine — the owner dying at startup, the proxy
// reporting a bare EOF, the database refusing to open — because all of them
// happen in the chain between "the browser launches the binary" and "HELLO_ACK
// comes back", and nothing here had ever executed that chain.
//
// Platform-specific pieces (the Windows registry, DACLs) cannot run here. The
// launch → proxy → owner → store → HELLO path is identical everywhere, and that
// is where the failures were.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// buildHost compiles the daemon exactly as a release would.
func buildHost(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	out := filepath.Join(t.TempDir(), "keel-host")
	cmd := exec.Command("go", "build", "-o", out, ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	return out
}

// isolatedEnv points the binary at throwaway state. The runtime directory is
// created separately and kept short: a Unix socket path has a hard length limit
// that a nested test temp directory blows straight through.
func isolatedEnv(t *testing.T) []string {
	t.Helper()
	data := t.TempDir()
	runtime, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	return append(os.Environ(),
		"KEEL_DATA_DIR="+data,
		"KEEL_RUNTIME_DIR="+runtime,
	)
}

// TestNativeMessagingHandshakeEndToEnd launches the binary the way a browser
// does — argv[1] is the caller's origin, the protocol is framed JSON on stdio —
// and completes a HELLO negotiation.
//
// This covers, in one run: the proxy starting, the owner being spawned,
// resolveOwnerPaths choosing a directory, the owner credential being created
// and validated, the store opening, and HELLO being negotiated. Each of those
// failed in production today, and each failure reached the browser as a symptom
// several layers away from its cause.
func TestNativeMessagingHandshakeEndToEnd(t *testing.T) {
	host := buildHost(t)
	env := isolatedEnv(t)

	cmd := exec.Command(host, "chrome-extension://"+DefaultExtensionID+"/")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		stop := exec.Command(host, "owner", "stop")
		stop.Env = env
		_ = stop.Run()
	})

	hello, err := json.Marshal(map[string]any{
		"v": bridge.ProtocolV, "id": "1", "type": "HELLO",
		"payload": map[string]any{
			"client":         "keel-extension",
			"client_version": "0.1.0",
			"api":            map[string]int{"min": bridge.APIMin, "max": bridge.APIMax},
			"required":       map[string]int{bridge.CapCore: 1, bridge.CapNetworkConsent: 1},
			"optional":       map[string]int{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.WriteMessage(stdin, hello); err != nil {
		t.Fatalf("write HELLO: %v (stderr: %s)", err, stderr.String())
	}

	type result struct {
		raw []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		raw, err := bridge.ReadMessage(stdout)
		ch <- result{raw, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("no reply from the host: %v\nstderr: %s", r.err, stderr.String())
		}
		var env bridge.Envelope
		if err := json.Unmarshal(r.raw, &env); err != nil {
			t.Fatalf("reply is not an envelope: %v (%s)", err, r.raw)
		}
		// A startup failure arrives as ERROR with id "0" — the exact shape live
		// QA kept seeing. Quote it: it names the stage that failed.
		if env.Type == "ERROR" {
			t.Fatalf("the host refused to start: %s\nstderr: %s", r.raw, stderr.String())
		}
		if env.Type != "HELLO_ACK" {
			t.Fatalf("first reply is %q, want HELLO_ACK: %s", env.Type, r.raw)
		}
		var ack bridge.HelloAckPayload
		if err := json.Unmarshal(env.Payload, &ack); err != nil {
			t.Fatalf("bad HELLO_ACK payload: %v (%s)", err, env.Payload)
		}
		if !ack.Compatible {
			t.Fatalf("negotiation refused: %s — %s", ack.Code, ack.Reason)
		}
		if ack.Capabilities[bridge.CapCore] < 1 {
			t.Errorf("core capability missing from %v", ack.Capabilities)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the host never answered HELLO\nstderr: %s", stderr.String())
	}
}

// TestInstallThenLaunchEndToEnd runs the real install and then launches the
// binary as a browser would, so registration and the runtime path are exercised
// against each other rather than in isolation.
func TestInstallThenLaunchEndToEnd(t *testing.T) {
	host := buildHost(t)
	env := append(isolatedEnv(t), "HOME="+t.TempDir())

	install := exec.Command(host, "install", "-all")
	install.Env = env
	out, err := install.CombinedOutput()
	if err != nil {
		t.Fatalf("install -all failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("installed")) {
		t.Errorf("install wrote nothing:\n%s", out)
	}

	launch := exec.Command(host, "chrome-extension://"+DefaultExtensionID+"/")
	launch.Env = env
	stdin, _ := launch.StdinPipe()
	stdout, _ := launch.StdoutPipe()
	var stderr bytes.Buffer
	launch.Stderr = &stderr
	if err := launch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = launch.Process.Kill()
		_, _ = launch.Process.Wait()
		stop := exec.Command(host, "owner", "stop")
		stop.Env = env
		_ = stop.Run()
	})

	hello, _ := json.Marshal(map[string]any{
		"v": bridge.ProtocolV, "id": "1", "type": "HELLO",
		"payload": map[string]any{
			"client": "keel-extension", "client_version": "0.1.0",
			"api":      map[string]int{"min": bridge.APIMin, "max": bridge.APIMax},
			"required": map[string]int{bridge.CapCore: 1, bridge.CapNetworkConsent: 1},
		},
	})
	if err := bridge.WriteMessage(stdin, hello); err != nil {
		t.Fatalf("write HELLO: %v (stderr: %s)", err, stderr.String())
	}

	done := make(chan []byte, 1)
	go func() {
		raw, _ := bridge.ReadMessage(stdout)
		done <- raw
	}()
	select {
	case raw := <-done:
		if len(raw) == 0 {
			t.Fatalf("host exited without answering\nstderr: %s", stderr.String())
		}
		var e bridge.Envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("reply is not an envelope: %v", err)
		}
		if e.Type != "HELLO_ACK" {
			t.Fatalf("after a real install the host answered %q: %s\nstderr: %s",
				e.Type, raw, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("no answer after a real install\nstderr: %s", stderr.String())
	}
}

// TestHandshakeSurvivesAnUnwritableStateDirectory is the live-QA failure,
// reproduced against the real binary.
//
// The reported chain was: the state directory could not be written ("Access is
// denied"), so the owner died during startup, so the proxy reported EOF, so the
// panel said the desktop app was not running. Four layers, each one further
// from the cause. The fallback exists so that first step cannot stop the
// daemon — and until now nothing proved it works in the program, only in a
// function.
func TestHandshakeSurvivesAnUnwritableStateDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	host := buildHost(t)

	// A config directory that cannot be written into: the exact condition.
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	runtimeDir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	env := append(os.Environ(),
		"KEEL_DATA_DIR=", // unset: the fallback only applies to a location Keel chose
		"KEEL_DB=",
		"XDG_CONFIG_HOME="+locked,
		"HOME="+locked,
		"KEEL_RUNTIME_DIR="+runtimeDir,
	)

	cmd := exec.Command(host, "chrome-extension://"+DefaultExtensionID+"/")
	cmd.Env = env
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		stop := exec.Command(host, "owner", "stop")
		stop.Env = env
		_ = stop.Run()
	})

	hello, _ := json.Marshal(map[string]any{
		"v": bridge.ProtocolV, "id": "1", "type": "HELLO",
		"payload": map[string]any{
			"client": "keel-extension", "client_version": "0.1.0",
			"api":      map[string]int{"min": bridge.APIMin, "max": bridge.APIMax},
			"required": map[string]int{bridge.CapCore: 1, bridge.CapNetworkConsent: 1},
		},
	})
	if err := bridge.WriteMessage(stdin, hello); err != nil {
		t.Fatalf("write HELLO: %v (stderr: %s)", err, stderr.String())
	}

	done := make(chan []byte, 1)
	go func() {
		raw, _ := bridge.ReadMessage(stdout)
		done <- raw
	}()
	select {
	case raw := <-done:
		if len(raw) == 0 {
			t.Fatalf("host exited without answering; the fallback did not save it\nstderr: %s",
				stderr.String())
		}
		var e bridge.Envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("reply is not an envelope: %v", err)
		}
		if e.Type == "ERROR" {
			// Whatever it says, it must at least name the stage — that is the
			// other half of what was missing.
			t.Fatalf("an unwritable state directory still stopped the daemon: %s\nstderr: %s",
				raw, stderr.String())
		}
		if e.Type != "HELLO_ACK" {
			t.Fatalf("answered %q, want HELLO_ACK: %s", e.Type, raw)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("no answer with an unwritable state directory\nstderr: %s", stderr.String())
	}
}

// TestSelfTestReportsEachStage: the installer's self-test must run the real
// chain and name the stage that fails, because from the browser every failure
// in it looks like the same sentence.
func TestSelfTestReportsEachStage(t *testing.T) {
	host := buildHost(t)
	data := t.TempDir()
	runtimeDir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	install := exec.Command(host, "install", "-all")
	install.Env = append(os.Environ(),
		"KEEL_DATA_DIR="+data, "KEEL_RUNTIME_DIR="+runtimeDir, "HOME="+t.TempDir())
	out, err := install.CombinedOutput()
	t.Cleanup(func() {
		stop := exec.Command(host, "owner", "stop")
		stop.Env = install.Env
		_ = stop.Run()
	})
	if err != nil {
		t.Fatalf("install -all failed: %v\n%s", err, out)
	}

	// The self-test must have run, and every stage must have passed — this is
	// the whole chain the browser depends on.
	for _, want := range []string{
		"Checking that the desktop app actually runs",
		"state directory",
		"database",
		"browser-style launch",
		"HELLO negotiated",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("self-test output is missing %q:\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("FAIL")) {
		t.Errorf("a stage failed on a clean install:\n%s", out)
	}
}

// TestInstallRepairsAnUnusableDatabase: the machine this is for cannot have
// text copied off it, so an error the user has to read back to somebody is
// worth nothing. The install has to fix what it finds and say what it fixed.
func TestInstallRepairsAnUnusableDatabase(t *testing.T) {
	host := buildHost(t)
	data := t.TempDir()
	runtimeDir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	// A file that is not a database, exactly where the daemon expects one.
	if err := os.WriteFile(filepath.Join(data, "keel.sqlite"),
		[]byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	install := exec.Command(host, "install", "-all")
	install.Env = append(os.Environ(),
		"KEEL_DATA_DIR="+data, "KEEL_RUNTIME_DIR="+runtimeDir, "HOME="+t.TempDir())
	out, err := install.CombinedOutput()
	t.Cleanup(func() {
		stop := exec.Command(host, "owner", "stop")
		stop.Env = install.Env
		_ = stop.Run()
	})
	if err != nil {
		t.Fatalf("install did not recover from an unusable database: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("repair:")) {
		t.Errorf("no repair was attempted:\n%s", out)
	}
	if !bytes.Contains(out, []byte("HELLO negotiated")) {
		t.Errorf("the chain never came up after the repair:\n%s", out)
	}
}

// TestInstallEscapesAnUnopenableDatabase: the live-QA dead end, against the
// real binary. A writable directory holding a keel.sqlite this user cannot open
// must not trap the daemon there — it has other locations available and must
// use one.
func TestInstallEscapesAnUnopenableDatabase(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open anything")
	}
	host := buildHost(t)
	root := t.TempDir()

	cfg := filepath.Join(root, "cfg")
	poisoned := filepath.Join(cfg, "keel")
	if err := os.MkdirAll(poisoned, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(poisoned, "keel.sqlite")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00 pretend"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(db, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(db, 0o600) })

	runtimeDir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	install := exec.Command(host, "install", "-all")
	install.Env = append(os.Environ(),
		"KEEL_DATA_DIR=", "KEEL_DB=",
		"XDG_CONFIG_HOME="+cfg,
		"LOCALAPPDATA="+filepath.Join(root, "local"),
		"HOME="+root,
		"KEEL_RUNTIME_DIR="+runtimeDir,
	)
	out, err := install.CombinedOutput()
	t.Cleanup(func() {
		stop := exec.Command(host, "owner", "stop")
		stop.Env = install.Env
		_ = stop.Run()
	})
	if err != nil {
		t.Fatalf("install trapped on an unopenable database: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("HELLO negotiated")) {
		t.Fatalf("the chain never came up:\n%s", out)
	}
	// The poisoned path appears in the lines explaining the escape, so check the
	// verdict itself: the database stage must not have adopted it.
	for _, line := range bytes.Split(out, []byte("\n")) {
		if bytes.Contains(line, []byte("PASS database")) &&
			bytes.Contains(line, []byte(poisoned)) {
			t.Errorf("adopted the unopenable database anyway: %s", line)
		}
	}
}

// TestInstallDoesNotCreateTheDatabase.
//
// The installer used to open the store to check it, and store.Open creates
// keel.sqlite when it is missing. That made the installer's process the file's
// creator — and an installer double-clicked through SmartScreen can hold a
// different token than the host the browser later launches, so the host got
// "Access is denied" on a file the installer had just made for it. Every
// install re-created it, which is why each new fallback location failed in turn.
//
// The daemon owns its database. The installer must not bring one into being.
func TestInstallDoesNotCreateTheDatabase(t *testing.T) {
	host := buildHost(t)
	data := t.TempDir()
	runtimeDir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	db := filepath.Join(data, "keel.sqlite")
	if _, err := os.Stat(db); err == nil {
		t.Fatal("setup: the database already exists")
	}

	install := exec.Command(host, "install", "-all", "-dry-run")
	install.Env = append(os.Environ(),
		"KEEL_DATA_DIR="+data, "KEEL_RUNTIME_DIR="+runtimeDir, "HOME="+t.TempDir())
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("dry-run install failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(db); err == nil {
		t.Error("a dry run created the database")
	}

	// A real install starts the daemon, which legitimately creates it — but the
	// file must be made by the daemon, not by the installer's own store.Open.
	// Proven by the self-test reporting it as the desktop app's to create.
	install = exec.Command(host, "install", "-all")
	install.Env = append(os.Environ(),
		"KEEL_DATA_DIR="+data, "KEEL_RUNTIME_DIR="+runtimeDir, "HOME="+t.TempDir())
	out, err := install.CombinedOutput()
	t.Cleanup(func() {
		stop := exec.Command(host, "owner", "stop")
		stop.Env = install.Env
		_ = stop.Run()
	})
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("created by the desktop app")) {
		t.Errorf("the installer still claims the database as its own:\n%s", out)
	}
	if !bytes.Contains(out, []byte("HELLO negotiated")) {
		t.Errorf("the chain did not come up:\n%s", out)
	}
}

// TestInstallDeletesAPoisonedDatabaseItself.
//
// The user cannot type: five seconds per character on that machine. So a fix
// that requires "just run del" is not a fix. An install that meets a database
// it cannot open has to clear it and carry on, by itself.
func TestInstallDeletesAPoisonedDatabaseItself(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open anything")
	}
	host := buildHost(t)
	data := t.TempDir()
	runtimeDir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	// A database that exists, is non-empty, and denies this user.
	db := filepath.Join(data, "keel.sqlite")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00 poisoned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(db, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(db, 0o600) })

	install := exec.Command(host, "install", "-all")
	install.Env = append(os.Environ(),
		"KEEL_DATA_DIR="+data, "KEEL_RUNTIME_DIR="+runtimeDir, "HOME="+t.TempDir())
	out, err := install.CombinedOutput()
	t.Cleanup(func() {
		stop := exec.Command(host, "owner", "stop")
		stop.Env = install.Env
		_ = stop.Run()
	})
	if err != nil {
		t.Fatalf("install did not clear the poisoned database on its own: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("HELLO negotiated")) {
		t.Fatalf("the chain never came up:\n%s", out)
	}
	// And it must be a working database afterwards, not just an absent one.
	fi, err := os.Stat(db)
	if err != nil {
		t.Fatalf("no database after the install: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("the database was left empty")
	}
}
