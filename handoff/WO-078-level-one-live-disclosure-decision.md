# WO-078 — Resolve the Level-1/live-gossip privacy-contract contradiction

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Sonnet/Opus) |
| **Status** | **Historical — implemented 2026-08-11, then Level-1 Live decision superseded by WO-089** |
| **Date** | 2026-08-11 |
| **Source** | Architecture review, 2026-08-11 |

## Problem

`DESIGN_v2.md` §6.1 defines Level 1 as “Nothing, ever” leaves the device. §7.5
and the current implementation publish a livestream's ID and title at every
level, including Level 1. A message without an application-level author is not
proof that its origin cannot be inferred from peer topology, connection metadata
or timing.

The privacy policy discloses an exception, but the product still calls Level 1
“Strictly Personal” and says recordings do not leave. These statements cannot
all be true as written.

## Decision — 2026-08-11, Lars

> **Superseded 2026-08-12:** WO-089 moves the entire Live gossip/snapshot
> capability and outbound word telemetry to Level 2+. Level 1 keeps
> fetch/pre-walk and may fetch global word statistics, but has no Live receive,
> relay, origination or snapshot path and serves no word pack. The text below
> records the earlier decision and why it changed; it is not normative.

> **Later capability correction:** WO-085 keeps the fetch/pre-walk and
> disclosure boundary decided here but makes user-triggered distributed peer
> search reciprocal at Level 2+. This does not reopen the Live/word decision.

**Level 1 is a full consumer with two narrow outbound data products.** A Level-1 node receives the common seed, fetches whole-prefix
graph/catalogue/search data and pre-walks the graph so the full consumer product
works. It does not serve cached blocks, announce itself
as their provider, or serve/publish its own recommendation observations.

It also discovers peers for the live network, receives and serves the whole
live index/snapshot, relays gossip, and originates notices for livestreams it
observes. This is what lets the Live tab contain long-tail streams at the
default level.

The Level-1 outbound contract is:

- A live notice contains platform, video id, title/channel when known, and a
  coarse sighting time.
- It contains no recommendation edge, watched-video context, slot, query,
  stable application author or watch trail.
- Level 1 may fetch graph, catalogue, search-shard and word data. Requesting
  whole prefix buckets exposes peer participation and coarse bucket interests;
  privacy copy must disclose that cost.
- Level 1 does not serve graph, catalogue or search-shard blocks, including rows
  it previously fetched. It makes no provider announcements.
- Level 1 does not join, relay or originate the three-gram `YieldTopic` or
  `SketchTopic`, because it serves no searchable blocks. Its fetch path treats
  the missing optimization/count signals as unknown and continues. Level 2
  joins those topics for mirrored blocks it actually serves.
- Level 1 fetches and serves the separate WO-068 whole word-level HLL/CMS pack,
  including aggregate local-corpus input. It contains no plaintext words, ids,
  edges or query, but its CMS permits estimates for guessed words. Treat this as
  an explicit aggregate disclosure, not as “nothing leaves.”
- Authorless gossip reduces durable attribution but does not erase connection
  metadata. A direct neighbour may infer an origin from topology and timing;
  user-facing copy must not claim otherwise.

“Strictly Personal” therefore describes what the node does not contribute:
neither raw blocks from the durable corpus nor its recommendation trail is
served. It does not mean offline or zero network traffic. Every short-form claim
must name peer requests, live notices and the word-level aggregate in the same
context.

## Required after the decision

- Make `DESIGN_v2.md` §6, §7.4 and §7.5, `PRIVACY.md`, consent copy, level UI,
  and tests say the same thing.
- State the residual network-metadata claim conservatively; do not claim that
  unsigned gossip makes an origin undiscoverable.
- Add an automated test proving the selected Level-1 outbound behavior.

## Do not

- Do not treat a missing payload author as a complete anonymity proof.
- Do not describe “Strictly Personal” as zero network traffic or zero outbound
  data; name live notices and the word-level aggregate.
- Do not change this contract through UI copy alone.

## Acceptance

- [x] One unambiguous Level-1 contract exists in all standing and user-facing
      documents.
- [x] A Level-1 test proves graph/catalogue/search consumption, pre-walk and
      live receive/relay/origination all work, and the policy does not gate the
      deferred seed-consumer seam.
- [x] The same test proves every graph/catalogue/search block-serve and provider
      path remains disabled, including for data cached by Level 1.
- [x] The Level-1 node does not join, relay or originate the three-gram yield/
      token-sketch topics; peer fetch/search still completes without them.
- [x] A Level-1 word-telemetry test proves the whole HLL/CMS pack is exchanged
      while no plaintext word, id, edge or query appears on the wire.
- [x] No live payload contains context video, slot, query or stable author.

## Challenge

If the live payload needs another field, justify why the feed cannot work
without it and update the privacy copy before code ships.

## What was closed (2026-08-11)

The runtime policy this ticket needed (`daemon/swarm/policy.go`, the
supervisor in `daemon/contribution_runtime.go`) already existed from WO-077's
implementation — `ARCHITECTURE_CURRENT.md` §3 was written as the single
normative statement of it during that same pass. What remained here was
closing the standing-document contradiction the ticket named and proving the
one acceptance item nothing yet pinned:

- **`PRIVACY.md`** still said, in its own "Contributing" section, *"Nothing you
  record leaves your device, and Keel asks the network for nothing"* for Level
  1 — directly contradicting the shipped policy (Level 1 fetches whole prefix
  buckets) and the rest of this page's own "Network activity" section, which
  correctly named the live notice. Rewrote the short version, "Network
  activity" and "Contributing" sections to name all three Level-1 outbound
  behaviors together (peer requests for shared data, live notices, the
  word-popularity aggregate) every time Level 1 is described, per the
  decision's "every short-form claim must name..." requirement. No more
  "asks the network for nothing" or "nothing... at the default setting"
  claims remain on the page.
- **`extension/page/index.js`** — the Level-1 consent-slider copy named only
  the live notice, not the word-telemetry pack; fixed. A dead-but-wrong
  fallback string ("Keel sends nothing anywhere today") in `refreshContribution`
  was also corrected — it would have been false the moment it became reachable.
- **`handoff/WO-051-contribution-levels.md`** — the original ticket's own
  table still asserted "Nothing." for Level 1 as fact, not framed as
  superseded history. Gave it the same current-contract pointer note WO-052
  already carries.
- **`DESIGN_v2.md`, `AGENTS.md`, `ROADMAP.md`, `DESIGN_INCENTIVES.md`,
  `DESIGN_BOOTSTRAP.md`, `daemon/README.md`, `ARCHITECTURE_CURRENT.md`** were
  already consistent with this decision as of this session's prior (uncommitted)
  documentation pass — verified by reading each, not just trusting the diff.
- **New test:** `TestLiveRecordWireShapeCarriesNoFunnelState`
  (`daemon/swarm/live_test.go`) marshals a `LiveRecord` — the actual argument
  to `topic.Publish` in `Publish` — and asserts by allow-list that the wire
  JSON contains only `v/t/c/s/p/b`, failing loudly if a future field adds
  context video, query, slot or a stable author without an explicit
  privacy-copy update. The existing `policy_test.go`/`contribution_runtime_test.go`
  suite from WO-077 already covered the block-serve, three-gram-topic and
  word-telemetry acceptance items; verified by reading them rather than
  re-deriving.

Full suite green: `go test ./...` and `npm test` (99/99), both before and
after these edits.
