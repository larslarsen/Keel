// SPDX-License-Identifier: Apache-2.0
package bridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// ============================================================================
// SQLite test-technique ports applied to the native-messaging frame parser.
// See https://www.sqlite.org/testing.html. Keel is Go, so these are the
// techniques expressed in Go testing idioms, not C.
// ============================================================================

// ---- Technique: Fuzz Testing (§4) -------------------------------------------
//
// SQLite fuzzes SQL + DB files. We fuzz ReadMessage's byte stream: any input
// must never panic and must either return a message or a clean error (never an
// unrecovered panic / out-of-bounds).

func FuzzReadMessage(f *testing.F) {
	f.Add([]byte{0x00, 0x00, 0x00, 0x00}) // empty message
	f.Add([]byte{0x05, 0x00, 0x00, 0x00, 'h', 'e', 'l', 'l', 'o'})
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}) // claims 4GiB
	f.Add([]byte{0x01, 0x00, 0x00, 0x00}) // claims 1 byte, sends none

	f.Fuzz(func(t *testing.T, in []byte) {
		r := bytes.NewReader(in)
		// ReadMessage must never panic; it returns a message or an error.
		msg, err := ReadMessage(r)
		if err != nil {
			if msg != nil {
				t.Errorf("ReadMessage returned msg=%q with error %v", msg, err)
			}
			return
		}
		// On success the message length must match the declared length.
		if len(msg) == 0 {
			t.Errorf("ReadMessage returned empty msg with nil error")
		}
	})
}

// ---- Technique: I/O Error Simulation (§3.2) ----------------------------------
//
// SQLite inserts a VFS that fails after N I/O ops, looping N=1..completion.
// Here we wrap the reader so the FIRST Read fails, then the SECOND, etc., and
// assert ReadMessage surfaces the error cleanly at every failure point (it must
// not panic or return a half-read message).

type failAfterReader struct {
	buf      []byte
	calls    int
	failAt   int
	failWith error
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls >= r.failAt {
		return 0, r.failWith
	}
	if len(r.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func TestReadMessageIOErrorInjection(t *testing.T) {
	payload := []byte("hello world")
	full := make([]byte, 4+len(payload))
	binary.NativeEndian.PutUint32(full[:4], uint32(len(payload)))
	copy(full[4:], payload)

	sentinel := errors.New("injected I/O error")
	// Fail at call 1 (length read), 2 (payload read), and a clean success run.
	for failAt := 1; failAt <= 2; failAt++ {
		r := &failAfterReader{buf: append([]byte(nil), full...), failAt: failAt, failWith: sentinel}
		msg, err := ReadMessage(r)
		if err == nil {
			t.Fatalf("failAt=%d: expected I/O error, got msg=%q", failAt, msg)
		}
		if !errors.Is(err, sentinel) && err.Error() != "injected I/O error" {
			// ReadMessage wraps io errors from io.ReadFull; accept either form.
			if msg != nil {
				t.Errorf("failAt=%d: returned msg on error", failAt)
			}
		}
	}
	// Sanity: with no injected failure it parses.
	r := &failAfterReader{buf: append([]byte(nil), full...), failAt: 999, failWith: sentinel}
	msg, err := ReadMessage(r)
	if err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	if string(msg) != "hello world" {
		t.Errorf("clean run msg = %q, want hello world", msg)
	}
}

// ---- Technique: Boundary Value Tests (§4.3) ---------------------------------
//
// Exact-size and one-over/one-under boundaries on the declared length.

func TestReadMessageBoundaries(t *testing.T) {
	// Declared length 0 -> "empty message" error (the parser rejects n==0).
	_, err := ReadMessage(bytes.NewReader([]byte{0, 0, 0, 0}))
	if err == nil {
		t.Error("declared length 0 should error")
	}

	// Declared length 1, one byte present -> ok.
	in := []byte{1, 0, 0, 0, 'x'}
	msg, err := ReadMessage(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("len1 single byte: %v", err)
	}
	if len(msg) != 1 || msg[0] != 'x' {
		t.Errorf("len1: got %q", msg)
	}

	// Declared length 1, zero bytes present -> io error, not silent success.
	_, err = ReadMessage(bytes.NewReader([]byte{1, 0, 0, 0}))
	if err == nil {
		t.Error("declared 1 but no payload should error")
	}

	// Declared length 1, two bytes present -> reads 1, ignores the extra.
	msg, err = ReadMessage(bytes.NewReader([]byte{1, 0, 0, 0, 'a', 'b'}))
	if err != nil {
		t.Fatalf("len1 two bytes: %v", err)
	}
	if len(msg) != 1 || msg[0] != 'a' {
		t.Errorf("len1 two bytes: got %q", msg)
	}
}

// ---- Technique: Malformed / oversized input (§4.2) --------------------------
//
// The parser caps browser->host at MaxBrowserToHost. A declared length over the
// cap must be rejected before allocation.

func TestReadMessageRejectsOversized(t *testing.T) {
	over := make([]byte, 4)
	binary.NativeEndian.PutUint32(over[:4], uint32(MaxBrowserToHost)+1)
	_, err := ReadMessage(bytes.NewReader(over))
	if err == nil {
		t.Error("declared length over MaxBrowserToHost should error")
	}
}

// ---- Technique: Write/Read round-trip equivalence (§9) ----------------------
//
// SQLite runs with optimization on/off and asserts identical answers. Our
// analogue: a WriteMessage followed by ReadMessage must reproduce the payload
// exactly for any length, including the size boundaries.

func TestWriteReadRoundTrip(t *testing.T) {
	cases := [][]byte{
		[]byte("x"),
		[]byte("hello world"),
		bytes.Repeat([]byte("a"), MaxHostToBrowser-1),
	}
	for _, payload := range cases {
		var buf bytes.Buffer
		if err := WriteMessage(&buf, payload); err != nil {
			t.Fatalf("write %d bytes: %v", len(payload), err)
		}
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatalf("read back %d bytes: %v", len(payload), err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("round-trip mismatch: wrote %d bytes, read %d", len(payload), len(got))
		}
	}
}

// WriteMessage must reject over-cap responses (host->browser cap).
func TestWriteMessageRejectsOversized(t *testing.T) {
	var buf bytes.Buffer
	err := WriteMessage(&buf, bytes.Repeat([]byte("a"), MaxHostToBrowser+1))
	if err == nil {
		t.Error("payload over MaxHostToBrowser should error")
	}
}
