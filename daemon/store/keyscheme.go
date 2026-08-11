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
// # What must join this file when it is built
//
// WO-059's distributed search adds two more, and they belong here rather than
// beside the search code:
//
//   - the tokenizer's k, normalisation, and letters-only flag — peers must
//     tokenize a title identically or the bucket keys never meet;
//   - the yield-gossip dictionary's word list *and its ordering* — the yield
//     vector is one bit per dictionary position, so a reordered dictionary makes
//     bit N mean something different on each node. The dictionary never travels
//     on the wire, which is exactly why it has to be identical off it.
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
)

// PrefixDomain keys the DHT provider records that announce which buckets a node
// holds. Exported because the announcement is made in the swarm package, while
// the bucket it names is computed here — and the two must not drift apart.
const PrefixDomain = "keel/prefix/1/"
