// SPDX-License-Identifier: Apache-2.0
// A self-test that runs the whole chain, so the machine reports the fault
// instead of the user having to describe it.
//
// Every failure in this system arrives at the browser as one of two sentences:
// "Specified native messaging host not found" or "the desktop app is not
// running". A missing registry key, a manifest the browser cannot read, a
// blocked executable, an unwritable state directory, a database that will not
// open and a daemon that dies during startup all look identical from there.
// Diagnosing that from the outside means guessing, and guessing means shipping
// builds at someone who has to install each one.
//
// So the installer tests itself: it opens the store, then launches its own
// executable exactly as a browser does — the origin in argv[1], framed JSON on
// stdio — and completes a HELLO negotiation. Every stage prints PASS or FAIL
// with the real error, to the console and to install-report.txt.
package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// checkResult is one stage of the chain and what happened to it.
type checkResult struct {
	name   string
	detail string
	err    error
}

func (c checkResult) line() string {
	status := "PASS"
	detail := c.detail
	if c.err != nil {
		status, detail = "FAIL", c.err.Error()
	}
	return fmt.Sprintf("%-4s %-22s %s", status, c.name, detail)
}

// selfTest runs each stage in the order the browser triggers them, so the first
// FAIL is the cause and everything after it is a consequence.
func selfTest(exe string) []checkResult {
	var out []checkResult

	p, err := resolveOwnerPaths()
	if err != nil {
		return append(out, checkResult{name: "state directory", err: err})
	}
	out = append(out, checkResult{
		name:   "state directory",
		detail: p.configDir,
	})

	// The store is where a startup failure actually lands, and its error is the
	// one the owner logs before exiting.
	st, err := store.Open(p.dbPath)
	if err != nil {
		out = append(out, checkResult{name: "database", err: err})
		return out
	}
	dbPath := st.Path()
	_ = st.Close()
	out = append(out, checkResult{name: "database", detail: dbPath})

	out = append(out, launchCheck(exe))
	return out
}

// launchCheck starts this executable the way a browser starts a native
// messaging host and negotiates HELLO over stdio.
//
// This is the whole chain in one step — proxy, owner spawn, credential, store,
// negotiation — exercised through the same entry point the browser uses. If it
// passes and the browser still reports the host as missing, the fault is on the
// browser's side of the boundary (which registry key it reads, which extension
// ID it presents) and not in anything the daemon does.
func launchCheck(exe string) checkResult {
	const name = "browser-style launch"

	cmd := exec.Command(exe, chromeOrigin(DefaultExtensionID))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return checkResult{name: name, err: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return checkResult{name: name, err: err}
	}
	if err := cmd.Start(); err != nil {
		return checkResult{name: name, err: fmt.Errorf("could not start: %w", err)}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	hello, err := json.Marshal(map[string]any{
		"v": bridge.ProtocolV, "id": "selftest", "type": "HELLO",
		"payload": map[string]any{
			"client":         "keel-installer-selftest",
			"client_version": version,
			"api":            map[string]int{"min": bridge.APIMin, "max": bridge.APIMax},
			"required":       map[string]int{bridge.CapCore: 1},
		},
	})
	if err != nil {
		return checkResult{name: name, err: err}
	}
	if err := bridge.WriteMessage(stdin, hello); err != nil {
		return checkResult{name: name, err: fmt.Errorf("could not send HELLO: %w", err)}
	}

	type reply struct {
		raw []byte
		err error
	}
	ch := make(chan reply, 1)
	go func() {
		raw, err := bridge.ReadMessage(stdout)
		ch <- reply{raw, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return checkResult{name: name,
				err: fmt.Errorf("the host exited without answering: %w", r.err)}
		}
		var env bridge.Envelope
		if err := json.Unmarshal(r.raw, &env); err != nil {
			return checkResult{name: name, err: fmt.Errorf("unreadable reply: %w", err)}
		}
		switch env.Type {
		case "ERROR":
			// The startup failure, verbatim, naming its own stage.
			var p bridge.ErrorPayload
			_ = json.Unmarshal(env.Payload, &p)
			return checkResult{name: name,
				err: fmt.Errorf("the desktop app refused to start — %s: %s", p.Code, p.Message)}
		case "HELLO_ACK":
			var ack bridge.HelloAckPayload
			if err := json.Unmarshal(env.Payload, &ack); err != nil {
				return checkResult{name: name, err: err}
			}
			if !ack.Compatible {
				return checkResult{name: name,
					err: fmt.Errorf("negotiation refused — %s: %s", ack.Code, ack.Reason)}
			}
			return checkResult{name: name, detail: "HELLO negotiated; the desktop app answers"}
		default:
			return checkResult{name: name, err: fmt.Errorf("unexpected first reply %q", env.Type)}
		}
	case <-time.After(20 * time.Second):
		return checkResult{name: name, err: fmt.Errorf("no answer within 20s")}
	}
}
