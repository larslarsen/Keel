// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// TestHandleSuggestReturnsImmediately is WO-069's core regression: the
// bridge reads and handles one message at a time (see run()), so a
// synchronous handler here blocks every other RPC for as long as it takes.
// handleSuggest must return almost immediately regardless of how long the
// underlying walk takes, so the read loop can move on to the next message.
func TestHandleSuggestReturnsImmediately(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "suggest-async.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	env, err := bridge.NewEnvelope("req-1", "SUGGEST", bridge.SuggestPayload{Entropy: 50, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	var buf syncWriter
	buf.w = &bytes.Buffer{}

	start := time.Now()
	if err := handleSuggest(env, &buf, st); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("handleSuggest blocked the caller for %v — it must return immediately and reply from a goroutine", elapsed)
	}
}

// TestHandleSuggestDeliversResultAsync confirms the async path still
// actually delivers a correct reply, not just that it returns fast.
func TestHandleSuggestDeliversResultAsync(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "suggest-async-result.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	env, err := bridge.NewEnvelope("req-2", "SUGGEST", bridge.SuggestPayload{Entropy: 50, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	out := &syncWriter{w: &bytes.Buffer{}}
	if err := handleSuggest(env, out, st); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out.mu.Lock()
		n := out.w.(*bytes.Buffer).Len()
		out.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	out.mu.Lock()
	raw := out.w.(*bytes.Buffer).Bytes()
	out.mu.Unlock()
	if len(raw) == 0 {
		t.Fatal("no reply arrived within 2s")
	}
	framed, err := bridge.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("reply is not a validly framed message: %v", err)
	}
	got, err := bridge.ParseEnvelope(framed)
	if err != nil {
		t.Fatalf("reply is not a valid envelope: %v", err)
	}
	if got.Type != "SUGGEST_RESULT" {
		t.Fatalf("got envelope type %q, want SUGGEST_RESULT (payload: %s)", got.Type, got.Payload)
	}
	if got.ID != "req-2" {
		t.Errorf("reply lost the correlation id: want req-2, got %q", got.ID)
	}
}

// TestSyncWriterSerializesConcurrentWrites is the other half of the fix:
// once handleSuggest (and any future async handler) can reply from a
// goroutine while the main loop keeps processing other requests, more than
// one goroutine can be mid-write to stdout at once. A length-prefixed frame
// torn by an interleaved write corrupts the whole stream, not just one
// message — this asserts each concurrent Write's bytes land contiguously.
func TestSyncWriterSerializesConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	sw := &syncWriter{w: &buf}

	const writers = 20
	const payloadLen = 500 // large enough that a torn write would be obvious
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c := byte('A' + id%26)
			chunk := bytes.Repeat([]byte{c}, payloadLen)
			if _, err := sw.Write(chunk); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	got := buf.Bytes()
	if len(got) != writers*payloadLen {
		t.Fatalf("total bytes written = %d, want %d — a write was lost or corrupted", len(got), writers*payloadLen)
	}
	// Every payloadLen-sized run must be a single repeated byte — if two
	// writers interleaved, some window would mix two different bytes.
	for i := 0; i < len(got); i += payloadLen {
		chunk := got[i : i+payloadLen]
		want := chunk[0]
		for _, b := range chunk {
			if b != want {
				t.Fatalf("interleaved write detected at byte %d: chunk starting with %q contains %q",
					i, string(want), string(b))
			}
		}
	}
}

// TestSuggestTimeoutRepliesInsteadOfHanging documents the timeout guard's
// contract directly against its constant, since manufacturing a genuinely
// multi-second SuggestOn call isn't reproducible on demand (WO-069's own
// benchmarking found SuggestOn fast in every case tried — see the doc
// comment on suggestTimeout). This at least pins the constant's relationship
// to the client's cap so the two can't silently drift apart again.
func TestSuggestTimeoutUnderClientCap(t *testing.T) {
	const clientCapMs = 8000
	if suggestTimeout >= time.Duration(clientCapMs)*time.Millisecond {
		t.Fatalf("suggestTimeout (%v) must stay under the extension's %dms client-side request timeout, "+
			"or the daemon's own guard can never fire before the client gives up on its own",
			suggestTimeout, clientCapMs)
	}
}
