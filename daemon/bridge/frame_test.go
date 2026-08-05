// SPDX-License-Identifier: Apache-2.0
package bridge

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	env := Envelope{V: 2, ID: "abc", Type: "HELLO", Payload: json.RawMessage(`{"x":1}`)}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, raw); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseEnvelope(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Type != "HELLO" || parsed.ID != "abc" {
		t.Fatalf("%+v", parsed)
	}
}

func TestDropNonJSON(t *testing.T) {
	_, err := ParseEnvelope([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDropBadVersion(t *testing.T) {
	_, err := ParseEnvelope([]byte(`{"v":1,"id":"x","type":"HELLO","payload":{}}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDropMissingID(t *testing.T) {
	_, err := ParseEnvelope([]byte(`{"v":2,"id":"","type":"HELLO","payload":{}}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Oversized length prefix is rejected before body read (stream may desync —
// host exits; extension reconnects). WO-011 junk-input audit.
func TestRejectOversizedBrowserToHost(t *testing.T) {
	var buf bytes.Buffer
	// Claim MaxBrowserToHost+1 bytes without writing them.
	var lenBuf [4]byte
	// binary.NativeEndian
	n := uint32(MaxBrowserToHost + 1)
	lenBuf[0] = byte(n)
	lenBuf[1] = byte(n >> 8)
	lenBuf[2] = byte(n >> 16)
	lenBuf[3] = byte(n >> 24)
	buf.Write(lenBuf[:])
	_, err := ReadMessage(&buf)
	if err == nil {
		t.Fatal("expected oversized error")
	}
}

func TestWriteMessageRejectsOverHostCap(t *testing.T) {
	big := make([]byte, MaxHostToBrowser+1)
	var buf bytes.Buffer
	if err := WriteMessage(&buf, big); err == nil {
		t.Fatal("expected write reject")
	}
}

// A bad envelope between two good ones is droppable at ParseEnvelope; the
// framed stream still yields the next frame.
func TestFramedStreamSurvivesBadEnvelope(t *testing.T) {
	good1, _ := (&Envelope{V: 2, ID: "1", Type: "HELLO", Payload: json.RawMessage(`{}`)}).Encode()
	bad := []byte(`not-json{{{`)
	good2, _ := (&Envelope{V: 2, ID: "2", Type: "STATS", Payload: json.RawMessage(`{}`)}).Encode()
	var buf bytes.Buffer
	for _, p := range [][]byte{good1, bad, good2} {
		if err := WriteMessage(&buf, p); err != nil {
			t.Fatal(err)
		}
	}
	// 1 good
	raw, err := ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEnvelope(raw); err != nil {
		t.Fatal(err)
	}
	// 2 bad JSON — still a complete frame
	raw, err = ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEnvelope(raw); err == nil {
		t.Fatal("expected parse fail on non-JSON frame")
	}
	// 3 good again
	raw, err = ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	env, err := ParseEnvelope(raw)
	if err != nil || env.ID != "2" {
		t.Fatalf("third frame: %+v %v", env, err)
	}
}
