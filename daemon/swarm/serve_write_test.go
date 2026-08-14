// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{ n int }

func (w shortWriter) Write(p []byte) (int, error) {
	if w.n > len(p) {
		return len(p), nil
	}
	return w.n, nil
}

type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

type onceWriter struct {
	writes int
}

func (w *onceWriter) Write(p []byte) (int, error) {
	w.writes++
	return len(p), nil
}

func TestWriteFullRejectsErrorAndShortWrites(t *testing.T) {
	payload := []byte("hello-serve")
	if err := writeFull(&countingWriter{}, payload); err != nil {
		t.Fatalf("full write: %v", err)
	}
	if err := writeFull(errWriter{err: errors.New("boom")}, payload); err == nil {
		t.Fatal("error writer must fail")
	}
	if err := writeFull(shortWriter{n: 3}, payload); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer: %v", err)
	}
}

func TestReplyAndRecordCountsOnlyCompleteWrites(t *testing.T) {
	payload := []byte(`{"served":true}`)

	fullSt := newStore(t, "full-write.sqlite")
	full := &Node{st: fullSt}
	cw := &countingWriter{}
	full.replyAndRecord(cw, payload, "full")
	if cw.n != len(payload) {
		t.Fatalf("wrote %d bytes, want %d", cw.n, len(payload))
	}
	answered, bytesServed, _, err := fullSt.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if answered != 1 || bytesServed != int64(len(payload)) {
		t.Fatalf("full write counted answered=%d bytes=%d", answered, bytesServed)
	}

	errSt := newStore(t, "err-write.sqlite")
	failing := &Node{st: errSt}
	failing.replyAndRecord(errWriter{err: errors.New("boom")}, payload, "err")
	if answered, bytesServed, _, err := errSt.ContributionActivity(); err != nil || answered != 0 || bytesServed != 0 {
		t.Fatalf("error write counted answered=%d bytes=%d err=%v", answered, bytesServed, err)
	}

	shortSt := newStore(t, "short-write.sqlite")
	short := &Node{st: shortSt}
	short.replyAndRecord(shortWriter{n: 1}, payload, "short")
	if answered, bytesServed, _, err := shortSt.ContributionActivity(); err != nil || answered != 0 || bytesServed != 0 {
		t.Fatalf("short write counted answered=%d bytes=%d err=%v", answered, bytesServed, err)
	}
}

func TestWriteFrameCountsOnlyCompleteWrites(t *testing.T) {
	st := newStore(t, "frame-write.sqlite")
	n := &Node{st: st, serve: newServeLimiter()}
	frame := map[string]string{"t": "header"}

	if written, err := n.writeFrame(&countingWriter{}, frame); err != nil || written <= 0 {
		t.Fatalf("full frame write: written=%d err=%v", written, err)
	}

	if written, err := n.writeFrame(errWriter{err: errors.New("boom")}, frame); err == nil || written != 0 {
		t.Fatalf("error frame write: written=%d err=%v", written, err)
	}

	if written, err := n.writeFrame(shortWriter{n: 1}, frame); !errors.Is(err, io.ErrShortWrite) || written != 0 {
		t.Fatalf("short frame write: written=%d err=%v", written, err)
	}

	refused := &Node{st: st, serve: newServeLimiter()}
	if !refused.serve.chargeBytes(serveByteBudget) {
		t.Fatal("could not fill the serve budget")
	}
	if written, err := refused.writeFrame(&countingWriter{}, frame); !errors.Is(err, errFrameBudget) || written != 0 {
		t.Fatalf("budget refusal: written=%d err=%v", written, err)
	}
}

func TestReplyAndRecordDoesNotRetryAccountingFailure(t *testing.T) {
	st := newStore(t, "acct-fail.sqlite")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	n := &Node{st: st}
	w := &onceWriter{}
	n.replyAndRecord(w, []byte("done"), "closed-store")
	if w.writes != 1 {
		t.Fatalf("accounting failure wrote %d times, want the original reply only", w.writes)
	}
}

// TestServeHandlersDoNotDiscardWriteResults is the ratchet for WO-092: every
// serve path must go through writeFull / replyAndRecord, never `_, _ = s.Write`.
func TestServeHandlersDoNotDiscardWriteResults(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	dir := filepath.Dir(file)
	for _, name := range []string{"swarm.go", "words.go", "live.go", "shard.go", "paging.go"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)
		if strings.Contains(src, "_, _ = s.Write") || strings.Contains(src, "_, _ = w.Write") {
			t.Errorf("%s still discards Write results", name)
		}
	}
}

func TestWriteFullEmptyPayloadIsACompleteWrite(t *testing.T) {
	if err := writeFull(&countingWriter{}, nil); err != nil {
		t.Fatal(err)
	}
}
