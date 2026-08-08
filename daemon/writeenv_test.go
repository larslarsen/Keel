// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
)

// TestWriteEnvOversizedReturnsError verifies BUG 3 fix: a response exceeding
// the 1 MiB host→browser native-messaging cap must not be dropped silently
// (which left the client's request() promise hanging until its 8s timeout).
// Instead writeEnv must emit a small ERROR envelope carrying the original
// correlation id, so the client rejects cleanly.
func TestWriteEnvOversizedReturnsError(t *testing.T) {
	// A payload larger than the 1 MiB cap.
	big := make([]byte, bridge.MaxHostToBrowser+1024)
	for i := range big {
		big[i] = 'x'
	}
	env, err := bridge.NewEnvelope("req-123", "ANALYSIS_RESULT", map[string]any{
		"padding": string(big),
	})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeEnv(&buf, env); err != nil {
		t.Fatalf("writeEnv returned error: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("writeEnv wrote nothing for an oversized response — client would hang")
	}

	// The bytes on the wire must be a complete, decodable envelope…
	raw, err := bridge.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("written bytes are not a valid framed message: %v", err)
	}
	got, err := bridge.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("written envelope failed to parse: %v", err)
	}
	// …that is an ERROR carrying the original correlation id.
	if got.Type != "ERROR" {
		t.Fatalf("expected ERROR envelope, got type %q", got.Type)
	}
	if got.ID != "req-123" {
		t.Fatalf("ERROR envelope lost the correlation id: want %q, got %q", "req-123", got.ID)
	}
}

// TestWriteEnvNormalSizeUnchanged ensures the happy path still writes the exact
// envelope (no behavioural change for in-bounds responses).
func TestWriteEnvNormalSizeUnchanged(t *testing.T) {
	env, err := bridge.NewEnvelope("ok-1", "STATS_RESULT", map[string]any{"total": 1})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeEnv(&buf, env); err != nil {
		t.Fatalf("writeEnv returned error: %v", err)
	}
	raw, err := bridge.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("written bytes are not a valid framed message: %v", err)
	}
	got, err := bridge.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("written envelope failed to parse: %v", err)
	}
	if got.Type != "STATS_RESULT" || got.ID != "ok-1" {
		t.Fatalf("happy-path envelope changed: type=%q id=%q", got.Type, got.ID)
	}
}
