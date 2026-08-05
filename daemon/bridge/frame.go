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
	var lenBuf [4]byte
	binary.NativeEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
