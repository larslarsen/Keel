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
//
// `sources` must be the same SourceSet BlocksInPrefix will serve under
// (Policy.GraphSources). Announcing a wider set than the stream returns is a
// record that costs the requester a round trip and tells an observer the two
// sets differ; announcing a narrower one hides material this node is serving
// anyway (WO-084 requirement 4).
func (s *Store) LocalPrefixes(bits int, sources SourceSet) ([]string, error) {
	keys, err := s.LocalBlockKeys(sources)
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

// BucketAnonymityFloor is the smallest reply size this node will serve a bucket
// under at all.
//
// A bucket's privacy is its population: the requester's interest is hidden
// among the neighbourhoods it did not want. Answering with a handful of blocks
// gives that away, so a caller that asks for a reply cap below this gets an
// error instead of a small honest-looking answer. WO-084's rule is to adjust
// the bucket or the version, never to quietly shrink the anonymity set.
const BucketAnonymityFloor = 32

// BlockBucket is one complete prefix bucket, as served.
//
// It replaced a bare `[]Block` at BlockProtocol 3.0.0 for one reason:
// truncation used to be invisible. A capped reply is indistinguishable from a
// small bucket to whoever receives it, so a recipient could not tell whether
// the anonymity set it was reasoning about was the real one. Held and Truncated
// say so outright.
type BlockBucket struct {
	SchemaVersion int    `json:"schema_version"`
	Prefix        string `json:"prefix"`
	// Held is how many claims this node actually has in the bucket, before
	// any cap. Bucket-level and therefore coarse by construction — it counts
	// a set of thousands of possible videos, not a video.
	Held      int     `json:"held"`
	Truncated bool    `json:"truncated"`
	Blocks    []Block `json:"blocks"`
}

// BlocksInPrefix returns every claim this node can serve within one bucket.
//
// This is the union WO-084 requires, and it is a union of *claims*, not of
// edges: this node's own neighbourhood claim for a key and every peer claim it
// holds for that same key all appear, side by side, unmerged. Merging them
// would be this node asserting a total it cannot support, and dropping either
// side is the mirror-only bug the ticket exists to correct.
//
// Nothing in the reply says which claims came from this user's observations.
// They are sorted by (key, claim identity) — deterministic, and independent of
// who asked — and a local claim's identity is a per-neighbourhood key
// indistinguishable in kind from a peer's (claim.go).
//
// limit bounds the response: a bucket on a large node could hold a great many
// neighbourhoods, and an unbounded reply is both a memory hazard and a way for
// one request to consume a node's upstream. Truncation is deterministic rather
// than requester-influenced — there is no subset a peer can steer this toward —
// and is declared in the reply rather than hidden.
func (s *Store) BlocksInPrefix(prefix string, cohort string, sources SourceSet, limit int) (*BlockBucket, error) {
	bits, ok := PrefixOf(prefix)
	if !ok {
		return nil, fmt.Errorf("malformed prefix %q", prefix)
	}
	if limit <= 0 {
		limit = 256
	}
	if limit < BucketAnonymityFloor {
		return nil, fmt.Errorf(
			"reply cap %d is below the bucket anonymity floor of %d: widen the bucket or raise the cap",
			limit, BucketAnonymityFloor)
	}
	bucket := &BlockBucket{
		SchemaVersion: blockSchemaVersion,
		Prefix:        prefix,
		Blocks:        []Block{},
	}
	if sources.Empty() {
		return bucket, nil
	}

	keys, err := s.LocalBlockKeys(sources)
	if err != nil {
		return nil, err
	}
	inBucket := make([]string, 0, len(keys))
	for _, k := range keys {
		if BlockPrefix(k, bits) == prefix {
			inBucket = append(inBucket, k)
		}
	}

	all := []Block{}
	if sources.Local {
		for _, k := range inBucket {
			blk, err := s.BuildBlock(k, cohort)
			if err != nil {
				continue
			}
			if len(blk.Edges) == 0 {
				continue // an empty claim helps nobody and costs bytes
			}
			all = append(all, *blk)
		}
	}
	if sources.Peers {
		claims, err := s.PeerClaimsForKeys(inBucket)
		if err != nil {
			return nil, err
		}
		for _, c := range claims {
			if len(c.Edges) == 0 {
				continue
			}
			all = append(all, c)
		}
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].Key != all[b].Key {
			return all[a].Key < all[b].Key
		}
		return all[a].ClaimID() < all[b].ClaimID()
	})

	bucket.Held = len(all)
	if len(all) > limit {
		all = all[:limit]
		bucket.Truncated = true
	}
	bucket.Blocks = all
	return bucket, nil
}
