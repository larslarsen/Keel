// SPDX-License-Identifier: Apache-2.0
// The yield vector (WO-067): a gossiped hint that lets a searching node skip
// a shard fetch to a peer unlikely to have anything for the token it wants,
// without ever telling that peer which token it is. See tokendict.go for the
// index every bit is keyed by, and daemon/swarm/yield.go for the gossip side
// that publishes and consumes this.
package store

// YieldBitSet reports whether bit idx is set in a yield vector, treating a
// too-short vector as all-zero (a peer that has not gossiped anything yet, or
// gossiped a truncated vector, is simply unknown-yield everywhere — not a
// panic).
func YieldBitSet(vec []byte, idx int) bool {
	byteIdx := idx / 8
	if byteIdx < 0 || byteIdx >= len(vec) {
		return false
	}
	return vec[byteIdx]&(1<<uint(idx%8)) != 0
}

// LocalYieldVector computes this node's own yield vector: for every token
// this node holds any data on, the bit is set when the fraction of that
// token's shard covered by this node's own videos is at least
// YieldThreshold.
//
// `sources` follows the same rule as ShardSlice/heldCatalogue (catalogue.go
// rule 2), and must be the same set ShardSlice serves under. A yield bit is a
// claim about what a shard fetch from this node would return, so computing it
// over a different corpus than the one served is a claim that does not hold —
// it either sends peers on pointless fetches or hides material this node is
// already answering with (WO-084 requirement 4).
//
// A token that never occurs in the held corpus is left at 0 — most of the
// TokenDictSize-bit space for any one node — rather than computed
// individually, since "never seen" and "seen but below threshold" both mean
// the same thing to a reader of the vector: not worth fetching from here.
func (s *Store) LocalYieldVector(sources SourceSet) ([]byte, error) {
	all, err := s.heldCatalogue(sources)
	if err != nil {
		return nil, err
	}

	// tokenVideos[t] = distinct videos whose title tokenizes to include t.
	// shardVideos[g] = distinct videos with at least one token landing in
	// shard g — the denominator ShardSlice would actually serve.
	tokenVideos := map[string]map[string]bool{}
	shardVideos := map[int]map[string]bool{}
	for _, c := range all {
		if c.Title == "" {
			continue
		}
		seen := map[string]bool{}
		for _, t := range tokenize(c.Title, ShardK) {
			seen[t] = true
		}
		for t := range seen {
			if tokenVideos[t] == nil {
				tokenVideos[t] = map[string]bool{}
			}
			tokenVideos[t][c.VideoID] = true
			g := ShardOf(t)
			if shardVideos[g] == nil {
				shardVideos[g] = map[string]bool{}
			}
			shardVideos[g][c.VideoID] = true
		}
	}

	vec := make([]byte, YieldVectorBytes)
	for t, videos := range tokenVideos {
		idx, ok := TokenDictIndex(t)
		if !ok {
			// tokenize/normalize only ever emit TokenDictAlphabet runes at
			// exactly ShardK length, so this should be unreachable — skip
			// rather than panic if that invariant is ever violated.
			continue
		}
		total := len(shardVideos[ShardOf(t)])
		if total == 0 {
			continue
		}
		if float64(len(videos))/float64(total) >= YieldThreshold {
			vec[idx/8] |= 1 << uint(idx%8)
		}
	}
	return vec, nil
}
