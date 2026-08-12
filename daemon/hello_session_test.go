// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
)

func encodeEnv(t *testing.T, id, typ string, payload any) []byte {
	t.Helper()
	env, err := bridge.NewEnvelope(id, typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readAck(t *testing.T, buf *bytes.Buffer) (*bridge.Envelope, bridge.HelloAckPayload) {
	t.Helper()
	framed, err := bridge.ReadMessage(buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridge.ParseEnvelope(framed)
	if err != nil {
		t.Fatal(err)
	}
	var ack bridge.HelloAckPayload
	if err := json.Unmarshal(got.Payload, &ack); err != nil {
		t.Fatal(err)
	}
	return got, ack
}

func TestSessionRequiresHello(t *testing.T) {
	sess := &bridgeSession{}
	var buf bytes.Buffer
	raw := encodeEnv(t, "s1", "STATS", map[string]any{})
	if err := handleRawContext(t.Context(), raw, &buf, nil, sess); err != nil {
		t.Fatal(err)
	}
	framed, err := bridge.ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridge.ParseEnvelope(framed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "ERROR" {
		t.Fatalf("type=%q want ERROR", got.Type)
	}
	var ep bridge.ErrorPayload
	_ = json.Unmarshal(got.Payload, &ep)
	if ep.Code != bridge.CodeHelloRequired {
		t.Fatalf("code=%q", ep.Code)
	}
}

func TestSessionHelloCompatible(t *testing.T) {
	sess := &bridgeSession{}
	var buf bytes.Buffer
	raw := encodeEnv(t, "h1", "HELLO", bridge.HelloPayload{
		ClientVersion: "0.1.0",
		API:           &bridge.APIRange{Min: 1, Max: 1},
		Required:      map[string]int{bridge.CapCore: 1},
		Optional:      map[string]int{bridge.CapQueue: 1, bridge.CapPeerSearch: 1},
	})
	if err := handleRawContext(t.Context(), raw, &buf, nil, sess); err != nil {
		t.Fatal(err)
	}
	got, ack := readAck(t, &buf)
	if got.Type != "HELLO_ACK" || !ack.Compatible || !sess.helloOK {
		t.Fatalf("ack=%+v sess=%+v", ack, sess)
	}
	if sess.caps[bridge.CapQueue] != 1 {
		t.Fatalf("caps=%v", sess.caps)
	}
}

func TestSessionHelloIncompatibleDoesNotArm(t *testing.T) {
	sess := &bridgeSession{}
	var buf bytes.Buffer
	raw := encodeEnv(t, "h2", "HELLO", bridge.HelloPayload{
		ClientVersion: "0.1.0",
		API:           &bridge.APIRange{Min: 99, Max: 99},
		Required:      map[string]int{bridge.CapCore: 1},
	})
	if err := handleRawContext(t.Context(), raw, &buf, nil, sess); err != nil {
		t.Fatal(err)
	}
	_, ack := readAck(t, &buf)
	if ack.Compatible || sess.helloOK {
		t.Fatalf("must not arm: ack=%+v", ack)
	}
	if ack.Code != bridge.CodeAPINonOverlap {
		t.Fatalf("code=%q", ack.Code)
	}
}

func TestSessionDuplicateHello(t *testing.T) {
	sess := &bridgeSession{helloOK: true, caps: bridge.DaemonCaps()}
	var buf bytes.Buffer
	raw := encodeEnv(t, "h3", "HELLO", bridge.HelloPayload{
		ClientVersion: "0.1.0",
		API:           &bridge.APIRange{Min: 1, Max: 1},
		Required:      map[string]int{bridge.CapCore: 1},
	})
	if err := handleRawContext(t.Context(), raw, &buf, nil, sess); err != nil {
		t.Fatal(err)
	}
	_, ack := readAck(t, &buf)
	if ack.Compatible || ack.Code != bridge.CodeDuplicateHello {
		t.Fatalf("got %+v", ack)
	}
}

func TestSessionOptionalCapGate(t *testing.T) {
	// Negotiated without peer_search — PEER_SEARCH must fail closed.
	sess := &bridgeSession{
		helloOK: true,
		caps:    map[string]int{bridge.CapCore: 1, bridge.CapQueue: 1},
	}
	var buf bytes.Buffer
	raw := encodeEnv(t, "p1", "PEER_SEARCH", map[string]any{"query": "x"})
	if err := handleRawContext(t.Context(), raw, &buf, nil, sess); err != nil {
		t.Fatal(err)
	}
	framed, err := bridge.ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridge.ParseEnvelope(framed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "ERROR" {
		t.Fatalf("type=%q", got.Type)
	}
	var ep bridge.ErrorPayload
	_ = json.Unmarshal(got.Payload, &ep)
	if ep.Code != bridge.CodeCapabilityUnavailable {
		t.Fatalf("code=%q", ep.Code)
	}
}
