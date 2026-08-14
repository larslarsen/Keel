// SPDX-License-Identifier: Apache-2.0
// Full-write accounting for contribution impact (WO-092).
//
// "Your copy helped answer" is only true of a reply that actually left the
// machine. A failed or short Write is not an answered request and must not
// increment either counter. Accounting failure after a completed write is
// logged and is never turned into a second response.
package swarm

import "io"

// writeFull writes every byte of p. A short write with a nil error is treated
// as io.ErrShortWrite so callers cannot mistake a partial send for success.
func writeFull(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err != nil {
		return err
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}

// replyAndRecord writes one complete serve payload and, only then, records
// one answered request of exactly those bytes. A short or failed write is
// not answered. A counter error after a successful write is logged and is
// not retried.
func (n *Node) replyAndRecord(w io.Writer, payload []byte, logPrefix string) {
	if err := writeFull(w, payload); err != nil {
		n.logf("%s: write reply: %v", logPrefix, err)
		return
	}
	if err := n.st.RecordContributionServe(len(payload)); err != nil {
		n.logf("%s: recording contribution activity: %v", logPrefix, err)
	}
}
