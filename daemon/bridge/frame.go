// SPDX-License-Identifier: Apache-2.0
package bridge

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Native byte order length prefix (Chrome native messaging).
// Host → browser max 1 MiB; browser → host max 64 MiB.
const MaxHostToBrowser = 1 << 20
const MaxBrowserToHost = 64 << 20

// ReadMessage reads one framed message from r.
func ReadMessage(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.NativeEndian.Uint32(lenBuf[:])
	if n == 0 {
		return nil, fmt.Errorf("empty message")
	}
	if n > MaxBrowserToHost {
		return nil, fmt.Errorf("message too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// WriteMessage writes one framed message to w.
func WriteMessage(w io.Writer, payload []byte) error {
	if len(payload) > MaxHostToBrowser {
		return fmt.Errorf("response too large: %d", len(payload))
	}
	// One Write, not two (WO-095).
	//
	// The length prefix used to be written separately from the payload. With a
	// single writer that is invisible; with two goroutines sharing the stream —
	// which a streaming search's concurrent token workers are — the interleaving
	// `lenA, lenB, payloadA, payloadB` is possible, and it corrupts the stream
	// permanently rather than losing one message: every subsequent frame is read
	// at the wrong offset. main.go's syncWriter serializes each Write call, so
	// building the frame first is what makes that guarantee cover a whole
	// message instead of half of one.
	frame := make([]byte, 4+len(payload))
	binary.NativeEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	_, err := w.Write(frame)
	return err
}
