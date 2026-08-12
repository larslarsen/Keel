// SPDX-License-Identifier: Apache-2.0
// Contribution policy: what a running node is permitted to do (WO-077).
//
// The capabilities below are selected independently rather than derived from
// one "swarm enabled" boolean, because they are not one decision. The pairing
// that matters most: joining a gossip topic and originating on it used to share
// a gate, so a node that served nothing still announced three-gram availability
// for blocks it did not have. ARCHITECTURE_CURRENT §3 fixes the boundary at
// the capability, not at the subsystem.
//
// The consumer/contributor split is the product boundary, and it does not run
// where a "do you share?" flag would put it. Level 1 fetches, pre-walks,
// searches peers, exchanges the whole word-telemetry pack and both receives and
// originates live gossip. What it does not do is serve blocks, announce itself
// as their provider, or join the three-gram topics — all of which exist to make
// *this node's* cache findable. Privacy is not a toll booth: withholding the
// local cache must not withhold the product.
package swarm

import "github.com/keel-app/keel/daemon/store"

// Policy is the effective capability set of one running node.
//
// Every field is a separate question. Adding a capability here means deciding
// what it is at each level in PolicyForLevel, which is the point: a new
// network behaviour cannot quietly inherit an existing level's answer.
type Policy struct {
	// Live receives, relays and originates live gossip, and serves the whole
	// live snapshot. On at every level: the shared live index is whole-feed
	// gossip whose query privacy comes from holding the whole index, so a
	// node that only listened would be taking without giving for no privacy
	// gain. Its outbound disclosure is stated in the consent copy.
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

	// ServeMirrors answers block/catalogue/shard requests from the cache of
	// public blocks this node mirrored from other people. Level 2+.
	ServeMirrors bool

	// AnnounceProviders publishes DHT provider records saying this node holds
	// a bucket. Separate from ServeMirrors on purpose: stopping new arrivals
	// (announcements) and refusing the ones already en route (service) are
	// different actions, and a downgrade needs both.
	AnnounceProviders bool

	// JoinSearchTelemetry joins, relays and originates the three-gram
	// YieldTopic and SketchTopic.
	//
	// Level 2+ only, because both topics exist to locate and size *served*
	// blocks. A Level-1 fetcher treats a missing yield/count as unknown and
	// searches anyway — the optimization is absent, the capability is not.
	JoinSearchTelemetry bool

	// ExchangeWordTelemetry fetches and answers the WO-068 word-level HLL/CMS
	// pack.
	//
	// On at every level, and deliberately not tied to ServeMirrors: it is a
	// fixed-shape display aggregate with no plaintext words, ids, edges or
	// query, and it is whole-pack, so answering it discloses no per-item
	// interest the way a block request would. At Level 1 the pack still
	// includes the local corpus, so the global statistic actually covers it.
	ExchangeWordTelemetry bool

	// PublishOwn includes this node's own observed edges in what it serves.
	// Level 3+ only. This is the step that publishes a funnel.
	PublishOwn bool
}

// PolicyForLevel maps a stored contribution level onto capabilities.
//
// The table here is ARCHITECTURE_CURRENT §3's, in code. An unknown or
// out-of-range level falls back to the Level-1 consumer policy rather than
// erroring: an unreadable setting must never be read as consent to serve.
func PolicyForLevel(level int) Policy {
	p := Policy{
		Live:                  true,
		Fetch:                 true,
		ExchangeWordTelemetry: true,
	}
	if level >= store.LevelMirror {
		p.ServeMirrors = true
		p.AnnounceProviders = true
		p.JoinSearchTelemetry = true
	}
	if level >= store.LevelCohort {
		p.PublishOwn = true
	}
	return p
}

// MirrorOnly reports whether served payloads must exclude this node's own
// observations. It is the inverse of PublishOwn, named for how every builder
// call site reads it.
func (p Policy) MirrorOnly() bool { return !p.PublishOwn }

// ServesAnyBlocks reports whether any block-serving stream handler should be
// registered at all.
func (p Policy) ServesAnyBlocks() bool { return p.ServeMirrors }
