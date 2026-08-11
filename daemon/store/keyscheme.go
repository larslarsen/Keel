// SPDX-License-Identifier: Apache-2.0
// Key-deriving constants and their version (WO-060).
//
// A peer-to-peer network partitions silently if two nodes disagree about what
// key a piece of data is stored or fetched under. The failure is worse than an
// error: node A asks for bucket "12:a3f0", node B has the same data filed under
// "12:9c41" because it hashes with a different domain string, and both conclude
// the network is empty. Nothing logs, nothing retries, and the symptom is
// indistinguishable from having no peers (WO-058).
//
// So every constant that feeds a key derivation is gathered here, under one
// version number, and pinned by golden vectors in keyscheme_test.go. Changing
// any of them without bumping KeySchemeVersion fails that test — which is the
// point: the compiler cannot catch "this hash is now different", but a golden
// vector can.
//
// # Why these are not negotiated
//
// A node must not adapt any of this to what it sees on the network. Deciding a
// parameter from network state requires every node to agree on that state at
// the same moment, which is consensus, which is a blockchain — and Keel does
// not build one. Network measurements can *inform* a future decision ("buckets
// are uniformly huge, widen them"), but the change itself is a code release,
// applied uniformly because it is compiled in.
//
// # What a bump costs, and why it is cheap here
//
// Nothing persists a bucket key. Both BlockPrefix and CataloguePrefix are
// computed from the video id at request time, and LocalPrefixes recomputes the
// advertised set from the ids the node holds — there is no bucket column in
// SQLite and no index keyed by a prefix. So bumping the scheme re-keys a node's
// whole view for free on the next advertisement; there is no migration to run
// and no old-key data stranded on disk.
//
// That property is worth protecting. Persisting a computed bucket key would
// turn a scheme bump from a redeploy into a migration, so if a future change
// wants to cache prefixes, it should cache them alongside the scheme version
// that produced them and discard the cache on mismatch.
//
// What a bump does cost is peers: nodes on the old scheme are on different
// protocol ids and are simply not reachable. Where that matters, a release can
// register handlers for both schemes for one transition window and drop the old
// one afterwards — serving both is possible precisely because keys are derived
// rather than stored, so a node can answer under either.
//
// # What joined this file for WO-059, and what still hasn't
//
// ShardK, ShardM and shardDomain below are the tokenizer/shard half of what
// this comment used to only earmark — peers must tokenize a title and group
// tokens into shards identically, or a shard fetched from one node never
// matches what another node would have answered.
//
// Still missing: the yield-gossip dictionary's word list *and its ordering*
// (WO-067) — the yield vector is one bit per dictionary position, so a
// reordered dictionary makes bit N mean something different on each node. The
// dictionary never travels on the wire, which is exactly why it has to be
// identical off it.
package store

// KeySchemeVersion is the version of the whole set of constants below.
//
// Bump it whenever any of them changes. The bump is what stops a node from
// talking to peers that derive keys differently: swarm protocol IDs and the DHT
// domain carry this number, so a mismatch means the stream is never opened
// rather than opened and answered with nothing.
//
// History:
//
//	1 — initial: prefix buckets, catalogue buckets, DHT prefix domain.
//	    WO-059 added the shard tokenizer/grouping constants below without
//	    bumping this: they are a new domain, not a change to an existing one,
//	    so nothing that already agreed on scheme 1 stops agreeing.
const KeySchemeVersion = 1

// Domain separators. Each hash input is prefixed with a string naming what is
// being hashed, so a video id can never produce the same digest in two
// different roles — a catalogue bucket and a block bucket for one video are
// unrelated keys, and neither can be used to probe the other.
const (
	// blockDomain keys the recommendation-graph neighbourhood buckets.
	blockDomain = "keel/block/1/"
	// catalogueDomain keys the searchable-title buckets.
	catalogueDomain = "keel/catalogue/1/"
	// shardDomain keys the token-shard buckets (WO-059). Separate from
	// catalogueDomain even though both derive from titles: a token and a
	// video id must never land in the same hash space, or a shard fetch and
	// a catalogue fetch could be correlated against each other.
	shardDomain = "keel/shard/1/"
)

// ShardK is the tokenizer's fixed window width: every text is normalized
// (lowercased, non-letters collapsed to single spaces, padded with a leading
// and trailing space) and then sliced into every consecutive ShardK-character
// run, e.g. " rec" from " recommendation " — a plain fixed-size sliding
// window over space-including text, not a per-word scheme. See
// daemon/store/shard.go's tokenize doc comment for the exact algorithm.
//
// Fixed at 3 — WO-059's measurement found it the bandwidth/privacy knee:
// smaller k keeps per-token buckets more private but multiplies per-query
// bytes roughly 6x going from k=3 to k=2; larger k roughly halves bytes again
// but leaks more of a token's content per window (handoff/
// WO-059-distributed-peer-search.md, "Empirical tokenizer evaluation"). A
// change is a protocol version bump, never runtime — see KeySchemeVersion's
// history above, and never a per-query fallback either: every node's
// ShardSlice tokenizes titles at exactly this k, so a client using a
// different k for some queries would search shards no server populates at
// that width.
const ShardK = 3

// ShardM is how many shards tokens are grouped into: shard = hash(token) mod
// ShardM. Measured near-uniform (CV 0.15-0.58) at M=64-256 on a 4,527-title
// corpus (same doc, "Grouping tokens into uniform shards"); much larger and
// shards go emptier than the token vocabulary can fill, which is exactly the
// per-token rarity leak grouping exists to remove. Version-locked with ShardK.
const ShardM = 256

// PrefixDomain keys the DHT provider records that announce which buckets a node
// holds. Exported because the announcement is made in the swarm package, while
// the bucket it names is computed here — and the two must not drift apart.
const PrefixDomain = "keel/prefix/1/"
