// SPDX-License-Identifier: Apache-2.0
// Contribution policy: what a running node is permitted to do
// (WO-077, WO-084, WO-085, WO-089).
//
// The capabilities below are selected independently rather than derived from
// one "swarm enabled" boolean, because they are not one decision. The pairing
// that matters most: joining a gossip topic and originating on it used to share
// a gate, so a node that served nothing still announced three-gram availability
// for blocks it did not have. ARCHITECTURE_CURRENT §3 fixes the boundary at
// the capability, not at the subsystem.
//
// The consumer/contributor split is the product boundary, and it does not run
// where a "do you share?" flag would put it. Level 1 fetches, pre-walks and
// downloads the whole word-telemetry pack, and uses all of it locally. What it
// does not do is serve blocks, announce itself as their provider, or join the
// three-gram topics — all of which exist to make *this node's* cache findable.
// Privacy is not a toll booth: withholding the local cache must not withhold
// the product.
//
// # What WO-089 moved, and the line it draws
//
// Level 1 used to run Live and answer the word-telemetry protocol. Both are
// now Level 2+, and the rule that decides it is simpler than the one it
// replaces: **anything derived from what this user was shown is Level 2.**
//
// The earlier reasoning asked whether a payload was aggregated enough, or
// authorless enough, to be harmless by default. That question has no stable
// answer — "no application-level sender" is not an anonymity proof against a
// direct neighbour with topology and timing, and a fixed-shape CMS still
// answers guesses about words in a personal corpus. The new line does not
// require the answer. A live sighting and a local word aggregate are both
// products of this user's feed, so neither leaves at the default, and consent
// copy has one honest sentence to say instead of a graded one.
//
// Consumption is unchanged: the seed, broad bucket fetch, graph pre-walk and
// the download of the global word statistic all remain Level 1, because none
// of them publishes anything of the user's. Requests still expose IP, timing
// and a coarse prefix, which the privacy policy states.
//
// # The capacity exception, which is not a disclosure boundary (WO-085)
//
// DistributedSearch is off at Level 1. That is a capacity boundary, not a
// price on privacy: voluntary contribution plus unlimited consumption has no
// equilibrium, and search is the demand with no natural bound, so if most
// users stay at the default a handful of Level-2 machines become the search
// backend for everyone. Level 1 keeps every bounded consumer capability,
// including local search over its own catalogue.
//
// # What WO-084 changed here, and why one boolean was not enough
//
// This file used to carry a single `PublishOwn`, true only at Level 3, with
// `MirrorOnly()` as its inverse — and every builder in the store package took
// that inverse as an argument. That encoded the wrong contract twice over.
//
// Level 2 contributes this node's own graph blocks. Their privacy mechanism is
// broadness: the request and response unit is a complete hashed-prefix bucket
// holding many neighbourhoods, so a served block is not a disclosure that this
// user watched anything in particular. It is not a funnel, an ordered history
// or a selected video among decoys, and it never was Level 3's business to
// gate. Level 3's boundary is STAR-protected cohort measurement.
//
// The single boolean also conflated the graph corpus, the catalogue corpus,
// provider announcements, three-gram telemetry, STAR and attributed
// publication. Flipping it at Level 2 would have switched graph service from
// "imported claims only" to "own edges only" — silently dropping the mirror
// half — because the builder it fed chose one branch rather than unioning. So
// the capabilities are split out here and the source selection is a
// store.SourceSet, which has no spelling for "own instead of imported".
package swarm

import (
	"errors"

	"github.com/keel-app/keel/daemon/store"
)

// ErrDistributedSearchNotPermitted is returned by PeerSearch when the running
// policy has no DistributedSearch capability (WO-085).
//
// A sentinel rather than a formatted error because the daemon has to map it
// onto a typed bridge code the interface acts on — "you are not entitled to
// this" and "the network could not answer" produce different UI, and a caller
// that cannot tell them apart shows the wrong one.
var ErrDistributedSearchNotPermitted = errors.New(
	"distributed peer search needs contribution level 2: searching other people's " +
		"shards is reciprocal with answering theirs")

// Policy is the effective capability set of one running node.
//
// Every field is a separate question. Adding a capability here means deciding
// what it is at each level in PolicyForLevel, which is the point: a new
// network behaviour cannot quietly inherit an existing level's answer.
type Policy struct {
	// Live receives, relays and originates live gossip, and serves the whole
	// live snapshot. Level 2+ (WO-089).
	//
	// It was on at every level, on the reasoning that whole-feed gossip has no
	// per-query disclosure and that a listen-only node would take without
	// giving. WO-089 overturns that, and the reason is the disclosure, not the
	// reciprocity: a live notice is *derived from what this user was shown*.
	// "No application-level author" is not an anonymity proof — a direct
	// neighbour can use connection topology and timing to infer who originated
	// a sighting probabilistically. So publishing one is observation-derived
	// sharing, and observation-derived sharing is what Level 2 means.
	//
	// There is deliberately no receive-only or relay-only Live at Level 1.
	// Relaying is publishing to the next hop, and a subscriber is visible on
	// the mesh; a half-measure would put a Level-1 node on a topic while the
	// interface told the user nothing of theirs was on the network.
	Live bool

	// Fetch requests graph/catalogue/search-shard blocks from peers and
	// pre-walks the graph.
	//
	// On at Level 1. This is the capability most often mis-gated: a request
	// does disclose a coarse bucket interest to the peer answering it, but
	// that exposure is the privacy policy's, not a contribution. Gating it
	// would make Level 1 "offline" rather than "personal" and would withhold
	// peer search and pre-walk from everyone who declines to serve.
	Fetch bool

	// DistributedSearch runs a user-triggered search across other people's
	// shards: PeerSearch, and the shard fetches it is built from.
	//
	// Level 2+ (WO-085), and the one consumer-side capability that is not on
	// at Level 1. It is deliberately separate from Fetch even though it is
	// implemented with fetches, because the two have different cost shapes.
	// Seed, pre-walk and suggestion fetches are bounded by what the user is
	// actually watching and are largely answered from the seed pack and the
	// local cache; a search is arbitrary, user-triggered and repeatable, and
	// its per-token shard walk is the most open-ended demand one node can
	// place on the serving population.
	//
	// The pairing with ServeBroadBuckets is the whole point: a node may search
	// the public graph because it also hosts the public graph and answers
	// other people's searches. Level 1 stays a full consumer of everything
	// bounded — local search, seed, pre-walk, shared suggestions and fetched
	// word statistics — and is personal, not offline. Live and outbound word
	// contribution begin at Level 2 (WO-089).
	//
	// UI gating cannot stop a modified client, so this is an honest-client
	// product contract rather than a security boundary. The serving limits in
	// limits.go are what bound a dishonest one, and they apply at every level.
	DistributedSearch bool

	// ServeBroadBuckets answers block/catalogue/shard requests at all.
	//
	// It says *whether* this node answers, never *from what*: the corpus is
	// GraphSources/CatalogueSources below. Level 2+. A request is always for a
	// whole prefix bucket or a whole shard, and the answer is always the
	// complete eligible contents of it — there is no single-video and no
	// single-token path to gate separately, by construction.
	ServeBroadBuckets bool

	// IncludeLocalGraph includes neighbourhoods derived from this node's own
	// `impressions` in the buckets it serves. Level 2+ (WO-084).
	//
	// This is the capability WO-077 mistakenly deferred to Level 3. What
	// leaves is the aggregated edge shape only —
	// (from, to, surface, slot_bucket, day_bucket, cohort, count) — inside a
	// bucket holding many neighbourhoods, signed by a per-neighbourhood claim
	// identity that links to nothing else this node publishes.
	IncludeLocalGraph bool

	// IncludeLocalCatalogue includes public video metadata and the search
	// material derived from it (token shards, yield bits, token sketches)
	// drawn from this node's own observations. Level 2+ (WO-084).
	//
	// Separate from IncludeLocalGraph because they are separate namespaces on
	// separate prefixes, and a future level could reasonably move one without
	// the other. Today they move together: a bucket of edges nobody can label
	// is not the product.
	IncludeLocalCatalogue bool

	// AnnounceProviders publishes DHT provider records saying this node holds
	// a bucket. Separate from ServeBroadBuckets on purpose: stopping new
	// arrivals (announcements) and refusing the ones already en route
	// (service) are different actions, and a downgrade needs both.
	//
	// The set announced must be derived from the same SourceSet the serving
	// path uses. A record advertising material the stream would refuse to
	// return is a lie that costs the requester a round trip and tells an
	// observer the two sets differ.
	AnnounceProviders bool

	// JoinSearchTelemetry joins, relays and originates the three-gram
	// YieldTopic and SketchTopic.
	//
	// Level 2+ only, because both topics exist to locate and size *served*
	// blocks. A Level-1 fetcher treats a missing yield/count as unknown and
	// searches anyway — the optimization is absent, the capability is not.
	JoinSearchTelemetry bool

	// FetchWordTelemetry requests the WO-068 word-level HLL/CMS pack from
	// peers, for the corpus bars under the search box.
	//
	// On at every level. Asking for a fixed-shape aggregate is consumption,
	// and the whole pack is the request — there is no per-word or per-query
	// interest disclosed by making it.
	FetchWordTelemetry bool

	// ServeWordTelemetry answers the word-telemetry stream, with a pack that
	// includes this node's own corpus. Level 2+ (WO-089).
	//
	// Split from fetching because they are opposite directions with opposite
	// disclosures, and the old combined capability got that wrong. Answering
	// the protocol means sending an aggregate *derived from the titles this
	// user was shown*. The pack carries no plaintext words, video ids, edges
	// or query — but a guessed word can be tested against a CMS, so it is
	// aggregate metadata about a personal corpus, not zero disclosure.
	//
	// The consequence is accepted deliberately: a Level-1 node reads a global
	// statistic it does not contribute to, so the statistic under-counts
	// non-sharing installs. Under-reporting a display aggregate is the correct
	// trade against publishing something derived from observation by default.
	ServeWordTelemetry bool

	// PublishCohortMeasurements publishes STAR-protected cohort measurements.
	// Level 3+, and unimplemented — no STAR client exists.
	//
	// This is Level 3's actual boundary. It is a different construction from
	// broad bucket service, not a larger dose of it: a measurement is
	// recoverable only once enough independent nodes report the same value,
	// where a bucket is served in the clear and made private by its breadth.
	PublishCohortMeasurements bool

	// PublishAttributedFunnel publishes funnel state under a durable,
	// deliberately identifiable name. Level 4+, and unimplemented.
	//
	// The one publication path whose product claim is attribution, and the
	// only one that may use a stable network identity — see
	// Config.EphemeralIdentity.
	PublishAttributedFunnel bool
}

// PolicyForLevel maps a stored contribution level onto capabilities.
//
// The table here is ARCHITECTURE_CURRENT §3's, in code. An unknown or
// out-of-range level falls back to the Level-1 consumer policy rather than
// erroring: an unreadable setting must never be read as consent to serve.
func PolicyForLevel(level int) Policy {
	p := Policy{
		Fetch:              true,
		FetchWordTelemetry: true,
	}
	if level >= store.LevelBroad {
		// Everything derived from what this user was shown starts here
		// (WO-089): live sightings and the local word aggregate join the
		// graph blocks that were already Level 2's.
		p.Live = true
		p.ServeWordTelemetry = true
		// Reciprocal, and set here beside the serving capabilities rather than
		// with the consumer ones above so the pairing is visible in the code:
		// the level that may search other people's shards is the level that
		// answers shard requests (WO-085).
		p.DistributedSearch = true
		p.ServeBroadBuckets = true
		p.IncludeLocalGraph = true
		p.IncludeLocalCatalogue = true
		p.AnnounceProviders = true
		p.JoinSearchTelemetry = true
	}
	if level >= store.LevelCohort {
		p.PublishCohortMeasurements = true
	}
	if level >= store.LevelTransparency {
		p.PublishAttributedFunnel = true
	}
	return p
}

// GraphSources is the corpus this node's graph buckets are built from.
//
// A node that serves nothing selects nothing, so a builder handed this can
// never produce a payload a non-serving policy would have refused. Above that,
// the imported half is unconditional and the local half follows
// IncludeLocalGraph: serving is *union*, and there is deliberately no way to
// ask for own-instead-of-imported.
func (p Policy) GraphSources() store.SourceSet {
	if !p.ServeBroadBuckets {
		return store.SourceSet{}
	}
	return store.SourceSet{Local: p.IncludeLocalGraph, Peers: true}
}

// CatalogueSources is the corpus the catalogue, token shards, yield vector and
// token sketches are all built from.
//
// One accessor for all four on purpose (WO-084): they are announced
// separately but must describe the same held material, or a provider record
// names a bucket the stream will not return.
func (p Policy) CatalogueSources() store.SourceSet {
	if !p.ServeBroadBuckets {
		return store.SourceSet{}
	}
	return store.SourceSet{Local: p.IncludeLocalCatalogue, Peers: true}
}

// ServesAnyBlocks reports whether any block-serving stream handler should be
// registered at all.
func (p Policy) ServesAnyBlocks() bool { return p.ServeBroadBuckets }
