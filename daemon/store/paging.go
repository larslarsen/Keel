// SPDX-License-Identifier: Apache-2.0
// Bounded logical responses for broad-bucket streams (WO-097 §6).
//
// # What this replaces
//
// Both the token-shard path and the catalogue path used to stop at 4,096 rows
// and return success. Nothing carried a continuation, so every row past the cap
// was permanently unreachable — not slow, not retried later, unreachable. The
// shard path was worse than the catalogue path: it selected rows while
// iterating an unordered map and sorted the survivors afterwards, so *which*
// 4,096 rows a peer got was arbitrary and the sort made the result look
// deliberate.
//
// # Why pagination here is not a narrower request
//
// The obvious repair — let the requester ask for the rows it is missing — is
// precisely the disclosure the broad bucket exists to prevent. A page is not a
// smaller query. One request names one broad logical bucket (a shard number, a
// catalogue prefix) and the provider answers it as one logical response made of
// several bounded frames on the same stream. The requester never names a token,
// a candidate id, a title, or a narrower key, and a page boundary is a property
// of the answer, not of the question.
//
// # What the frames have to prove
//
// A response split into frames can fail in ways a single blob cannot: a frame
// can be dropped, duplicated, reordered, or the stream can simply stop early
// and look finished. So the terminal frame carries the ordered list of page
// digests and the page count, and is signed. A requester that has validated the
// terminal knows exactly which pages should have arrived, in what order, and
// whether the provider considered the traversal complete. A truncated stream
// has no valid terminal at all and is reported as incomplete rather than as an
// empty success.
//
// Resource-budget termination is an explicit `complete: false` with a reason.
// Silence that reads as success is the failure mode this whole file exists to
// remove.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// pageSchemaVersion is the wire version of the framing itself, independent of
// the payload schema each stream carries.
const pageSchemaVersion = 1

// MaxPageEntries bounds one page of any broad-bucket response.
//
// Small enough that a page is a modest allocation on both sides, large enough
// that an ordinary bucket is one or two frames. It is not a limit on what is
// reachable — the traversal continues across pages until the bucket is
// exhausted or the serving budget stops it and says so.
const MaxPageEntries = 512

// MaxResponsePages is a defensive ceiling on one logical response. Reaching it
// terminates the traversal as explicitly incomplete, exactly like running out
// of byte budget: at MaxPageEntries each, this is far above any real bucket, so
// hitting it means something pathological rather than something large.
const MaxResponsePages = 512

// Termination reasons. Carried on the terminal frame so an incomplete response
// says why, and so the requester can distinguish "this peer ran out of budget"
// from "this peer has nothing more".
const (
	ReasonComplete = "complete"
	ReasonBudget   = "budget"
	ReasonPageCap  = "page_cap"
)

// PageHeader opens a logical response. Advisory: everything it states is
// restated in the signed terminal, so a requester never has to trust it.
type PageHeader struct {
	Kind          string `json:"t"`
	SchemaVersion int    `json:"schema_version"`
	Bucket        string `json:"bucket"`
	Total         int    `json:"total"`
	Offset        int    `json:"offset"`
}

// PageTerminal closes a logical response and is what makes the whole thing
// verifiable — see the file comment.
type PageTerminal struct {
	Kind          string   `json:"t"`
	SchemaVersion int      `json:"schema_version"`
	Bucket        string   `json:"bucket"`
	Total         int      `json:"total"`
	Pages         int      `json:"pages"`
	Complete      bool     `json:"complete"`
	Reason        string   `json:"reason,omitempty"`
	PageDigests   []string `json:"page_digests"`

	ContentSHA256 string `json:"content_sha256"`
	Signature     string `json:"signature,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	Algorithm     string `json:"signature_alg,omitempty"`
}

// canonicalTerminalPayload is the exact byte sequence a terminal's digest and
// signature cover. Everything a requester uses to detect a gap, a duplicate, a
// reordering or a truncation is inside it.
func canonicalTerminalPayload(t *PageTerminal) ([]byte, error) {
	return json.Marshal(struct {
		Bucket      string   `json:"bucket"`
		Total       int      `json:"total"`
		Pages       int      `json:"pages"`
		Complete    bool     `json:"complete"`
		Reason      string   `json:"reason"`
		PageDigests []string `json:"page_digests"`
	}{t.Bucket, t.Total, t.Pages, t.Complete, t.Reason, t.PageDigests})
}

// SignTerminal builds and signs the closing frame of a logical response.
func (s *Store) SignTerminal(bucket string, total, pages int, complete bool, reason string, pageDigests []string) (*PageTerminal, error) {
	if pageDigests == nil {
		pageDigests = []string{}
	}
	t := &PageTerminal{
		Kind:          "end",
		SchemaVersion: pageSchemaVersion,
		Bucket:        bucket,
		Total:         total,
		Pages:         pages,
		Complete:      complete,
		Reason:        reason,
		PageDigests:   pageDigests,
	}
	payload, err := canonicalTerminalPayload(t)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	t.ContentSHA256 = hex.EncodeToString(sum[:])
	if t.Signature, t.PublicKey, err = s.signPayload(payload); err != nil {
		return nil, err
	}
	t.Algorithm = signAlgorithm
	return t, nil
}

// VerifyTerminal checks a terminal's digest and, when present, its signature.
//
// An unsigned terminal is accepted here for the same reason VerifyShardPack
// accepts an unsigned pack — the caller decides what unsigned is worth — but
// the digest is mandatory, because the digest is what binds the page list to
// the counts.
func VerifyTerminal(t *PageTerminal) error {
	if t == nil {
		return fmt.Errorf("response ended without a terminal frame")
	}
	if t.SchemaVersion > pageSchemaVersion {
		return fmt.Errorf("page framing schema %d is newer than this build understands (%d)",
			t.SchemaVersion, pageSchemaVersion)
	}
	if t.Pages != len(t.PageDigests) {
		return fmt.Errorf("terminal claims %d pages but lists %d digests", t.Pages, len(t.PageDigests))
	}
	payload, err := canonicalTerminalPayload(t)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != t.ContentSHA256 {
		return fmt.Errorf("terminal frame does not match its digest")
	}
	if t.Signature != "" || t.PublicKey != "" {
		if err := verifyPayload(payload, t.Signature, t.PublicKey); err != nil {
			return fmt.Errorf("terminal frame: %w", err)
		}
	}
	return nil
}

// PageStart is the traversal offset a request nonce selects over a stable
// ordering.
//
// Without it, every partial-budget traversal of a large bucket returns the same
// first rows and everything after them is reachable only in theory. The nonce
// carries no token, title or id — it is a random number whose only job is to
// move the starting point — and a traversal that runs to completion returns the
// same set whatever the offset was, because the rotation is a rotation and not
// a filter.
func PageStart(total int, nonce uint64) int {
	if total <= 0 {
		return 0
	}
	return int(nonce % uint64(total))
}

// rotate returns n indices starting at offset and wrapping, so a full
// traversal visits every row exactly once regardless of where it began.
func rotate(total, offset int) []int {
	if total <= 0 {
		return nil
	}
	if offset < 0 || offset >= total {
		offset = 0
	}
	out := make([]int, 0, total)
	for i := 0; i < total; i++ {
		out = append(out, (offset+i)%total)
	}
	return out
}
