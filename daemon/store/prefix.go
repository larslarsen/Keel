// SPDX-License-Identifier: Apache-2.0
// Prefix block caching (WO-052).
//
// A node never asks for one video's neighbourhood. It asks for every
// neighbourhood whose key falls in a prefix bucket, and takes all of them.
//
// This is not the same manoeuvre as decoy requests, and the difference is the
// reason it survives the intersection attack that sank the v1 k-anonymity
// buffer. Decoys hide a real request among fake ones, so repeated observation
// separates the signal statistically — the real target is the one that recurs.
// A prefix fetch has no real-versus-fake structure to separate: the node
// genuinely takes the whole bucket, so every key in it is equally consistent
// with what the user did. Repeating the request adds no information, because
// the same complete bucket comes back.
//
// The property this buys compounds with the product rather than fighting it:
// the blocks fetched for cover are exactly the blocks that make the node a
// useful mirror for other people. Level 2's privacy mechanism and Level 2's
// contribution are the same act, and the disk budget the user sets is the
// anonymity parameter.
//
// Prefixes are taken over a hash rather than the video id itself. YouTube ids
// are not uniformly distributed, so raw-id buckets would vary wildly in
// population and some would contain a single video — k-anonymity with k=1.
// Hashing evens the buckets out and stops anyone choosing an id that lands in a
// sparse one.
//
// **What this does not fix: identity linkability.** A relay hides the asking
// node's address, but the serving peer still learns its libp2p peer id. Each
// prefix is k-anonymous on its own; a sequence of prefixes tied to one stable
// identity is a trajectory, and trajectories re-identify. Rotating the swarm
// identity is what makes the relay do the work — see SwarmIdentity's ephemeral
// mode. Prefix caching and identity rotation are both required; neither is
// sufficient.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// DefaultPrefixBits is the bucket count exponent: 12 bits is 4,096 buckets.
//
// The trade is anonymity against bandwidth. Every request pulls a whole bucket,
// so the expected anonymity set is (videos in the network) / 2^bits and the
// expected transfer is the same figure in blocks. At a million videos that is
// ~244 neighbourhoods per request — a few MB — for k≈244.
//
// Fewer bits means larger k and larger transfers. This is the knob the disk
// budget should drive.
const DefaultPrefixBits = 12

// BlockPrefix returns the bucket a video's neighbourhood belongs to.
func BlockPrefix(videoID string, bits int) string {
	if bits <= 0 || bits > 64 {
		bits = DefaultPrefixBits
	}
	sum := sha256.Sum256([]byte(blockDomain + videoID))
	// Take whole bytes covering the requested bits, then mask the remainder so
	// the string is a faithful rendering of exactly `bits` bits.
	nbytes := (bits + 7) / 8
	buf := make([]byte, nbytes)
	copy(buf, sum[:nbytes])
	if rem := bits % 8; rem != 0 {
		buf[nbytes-1] &= byte(0xff << (8 - rem))
	}
	return fmt.Sprintf("%d:%s", bits, hex.EncodeToString(buf))
}

// PrefixOf parses the bit width out of a prefix string.
func PrefixOf(prefix string) (int, bool) {
	parts := strings.SplitN(prefix, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	var bits int
	if _, err := fmt.Sscanf(parts[0], "%d", &bits); err != nil {
		return 0, false
	}
	if bits <= 0 || bits > 64 {
		return 0, false
	}
	// A prefix with no payload is malformed: it names no bucket. Reject it so an
	// invalid key is never treated as a real bucket (WO-060: nodes must agree
	// on what constitutes a valid key).
	if parts[1] == "" {
		return 0, false
	}
	return bits, true
}

// LocalPrefixes lists the buckets this node holds anything in.
//
// This is what gets advertised, and it is the fix for a leak that survived an
// earlier attempt: advertising individual block keys discloses the videos the
// user watched, because a node caches what its user watches. A bucket
// containing thousands of videos discloses only that the node wanted something
// in it.
func (s *Store) LocalPrefixes(bits int, mirrorOnly bool) ([]string, error) {
	keys, err := s.LocalBlockKeys(mirrorOnly)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[BlockPrefix(k, bits)] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// BlocksInPrefix builds every block this node can serve within one bucket.
//
// limit bounds the response: a bucket on a large mirror could hold a great many
// neighbourhoods, and an unbounded reply is both a memory hazard and a way for
// one request to consume a node's upstream. Truncation is safe — the requester
// may not get the specific block it wanted, which is indistinguishable from the
// node not holding it.
func (s *Store) BlocksInPrefix(prefix string, cohort string, mirrorOnly bool, limit int) ([]Block, error) {
	bits, ok := PrefixOf(prefix)
	if !ok {
		return nil, fmt.Errorf("malformed prefix %q", prefix)
	}
	if limit <= 0 {
		limit = 256
	}
	keys, err := s.LocalBlockKeys(mirrorOnly)
	if err != nil {
		return nil, err
	}

	out := []Block{}
	for _, k := range keys {
		if BlockPrefix(k, bits) != prefix {
			continue
		}
		blk, err := s.buildBlock(k, cohort, mirrorOnly)
		if err != nil {
			continue
		}
		if len(blk.Edges) == 0 {
			continue
		}
		out = append(out, *blk)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
