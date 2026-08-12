# WO-084 — Level 2 serves broad buckets containing its own graph blocks

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Implemented 2026-08-11** — automated acceptance closed; two-machine network inspection outstanding |
| **Date** | 2026-08-11 |
| **Source** | Lars correction, 2026-08-11 |

## Problem

WO-077 encoded the wrong Level-2 boundary. It made Level 2 serve only rows
mirrored from other peers and deferred every locally derived recommendation
edge to Level 3. The standing documents and user-facing copy repeated that
mistake.

Level 2 is supposed to contribute this node's own graph blocks. Their privacy
mechanism is Lars's broadness construction: the network request and response
unit is a complete hashed-prefix bucket containing many neighbourhoods, never a
selected watched video with fake decoys. The locally derived blocks are
aggregated and stringless. Level 3 is for STAR-protected cohort measurement; it
is not the first level at which any local edge enters the shared graph.

The current seam makes a naive one-line fix unsafe:

- `Policy.PublishOwn` becomes true only at Level 3 and `MirrorOnly()` is its
  inverse for graph, catalogue, shards, yield and sketches at once.
- `buildBlock(..., false)` builds **own edges only**, while
  `buildBlock(..., true)` builds **mirrored edges only**. Merely flipping
  `PublishOwn` at Level 2 would stop re-serving mirrored graph data instead of
  serving the required union.
- Tests and comments explicitly assert the incorrect mirror-only rule.
- Re-aggregating and re-signing imported claims can amplify counts when blocks
  make a relay loop. Adding local claims must not turn that existing risk into
  multiplicative graph growth.

## Selected contract

### Level 1

Unchanged. It fetches/pre-walks, receives the seed, participates fully in Live,
and exchanges the whole word HLL/CMS pack. It serves no graph/catalogue/search
bucket, announces no provider record and joins no three-gram telemetry topic.

### Level 2 — broad sharing

Level 2 serves the complete eligible contents of each advertised bucket:

- graph neighbourhoods derived from this node's local `impressions`;
- graph neighbourhoods cached/imported from peers;
- the public catalogue rows needed to render the served graph; and
- broad search shards plus yield/token-sketch telemetry derived from the full
  corpus it actually serves.

An own graph block contains only the existing aggregated edge shape
`(from, to, surface, slot_bucket, day_bucket, cohort, count)`. It contains no
page-load id, raw observation timestamp, title string, query, per-impression
row or ordered watch trail. Catalogue metadata remains public video metadata
and travels through its separate complete-prefix namespace. Search remains a
complete uniform shard, never a per-token response.

The server advertises only bucket/shard identifiers. It never advertises one
video id and never answers one requested video. A response must not label which
members came from this user's observations and which came from cache.

Broadness does not mean zero disclosure. The recipient sees the complete set
the peer returned; connection metadata and signing identifiers can link
deliveries. Privacy/consent copy must say that Level 2 sends aggregated
recommendation blocks through broad buckets. Do not claim that no personal data
leaves, that Level 2 only passes on other people's data, or that broadness hides
the existence of the serving peer.

The existing install-wide block signing key is not acceptable for this Level-2
construction: putting the same public key on every locally produced
neighbourhood lets a recipient join broad buckets back into one publisher's
graph. Level-2 graph claims need an unlinkable identity per neighbourhood
(for example, a per-block signing key), stable only so later versions of that
same claim replace it. It must not equal the libp2p identity or an install-wide
bundle/transparency key. Mirrors preserve that opaque claim identity and exact
claim; they do not re-sign it as a new observation. Level 4 may deliberately use
an attributed identity, but that is a different publication path.

### Levels 3 and 4

- Level 3 adds STAR-protected cohort measurements and the comparison product.
  It does not switch ordinary graph blocks from mirror-only to local; Level 2
  already contributes those broad blocks.
- Level 4 adds deliberately attributable funnel publication. It remains the
  only level whose product claim is public attribution.

## Required implementation

Graphify's refreshed dependency graph confirms this crosses the policy,
block/signature/import, prefix, catalogue/shard, yield/sketch and swarm-handler
communities. Treat it as a protocol/storage migration, not a policy-boolean
cleanup.

1. Replace the `PublishOwn`/`MirrorOnly()` coupling with explicit capabilities
   that distinguish at least:
   - serve broad graph blocks;
   - include locally derived graph blocks;
   - include local public catalogue/search material;
   - announce providers;
   - join/originate three-gram telemetry;
   - publish STAR cohort measurements; and
   - publish an attributed funnel.
2. Give graph construction an explicit **union** mode. For a key held locally
   and through peers, serve the intended local-plus-imported claims without
   dropping either source. Do not implement union as `own OR mirror`.
3. Replace re-aggregation/re-signing of imported graph rows with preserved
   claims:
   - a locally produced neighbourhood gets an opaque per-neighbourhood claim
     identity/signing key, unlinkable to other neighbourhoods and to the
     install/network identity;
   - importing stores enough of the verified claim to re-serve it unchanged;
   - a bucket may contain several independent claims for the same graph key;
   - `(claim identity, graph key)` replacement/dedup makes an updated claim
     replace its prior version; and
   - A→B→A and A→B→C→A relay cycles do not increase counts or mint sources.
   Bumping the block schema/protocol revision is expected if the current row
   store cannot express this honestly.
4. Compute `LocalPrefixes`, catalogue prefixes, shards, yield and sketches from
   the same Level-2 served corpus. A provider record or telemetry claim must
   never advertise material the corresponding stream will refuse to return.
5. Keep broadness load-bearing:
   - request and serve whole prefix buckets/shards;
   - never add a single-video fast path;
   - never emit per-video provider records;
   - never truncate a response below the documented anonymity floor silently;
     adjust the bucket/version or fail closed instead; and
   - keep network identity ephemeral below Level 4.
6. Preserve WO-077's runtime transition ordering. A 2→1 downgrade closes the
   service/provider/three-gram gate before returning success; a 1→2 upgrade is
   not effective until locally produced blocks are actually eligible to serve.
7. Update `PRIVACY.md`, consent, contribution UI, `DESIGN_v2.md`,
   `DESIGN_BOOTSTRAP.md`, `DESIGN_INCENTIVES.md`, daemon comments and tests in
   the same implementation change. User-facing copy must describe shipped
   behavior, so do not land copy ahead of the runtime policy.

## Do not

- Do not set `PublishOwn=true` at Level 2 and leave `buildBlock(false)` as the
  own-only branch; that silently stops mirroring.
- Do not preserve “Mirror,” “other people's data only,” or “nothing you
  recorded is published” as the Level-2 product promise.
- Do not equate broad aggregated blocks with a raw funnel or ordered history.
- Do not move Live or whole word HLL/CMS exchange out of Level 1.
- Do not make Level 3 redundant: its boundary is STAR cohort measurement.
- Do not let provider announcements, served data and three-gram telemetry use
  different source sets.

## Acceptance

- [x] A Level-2 node containing only local observations advertises graph
      prefixes and returns locally derived blocks when a peer requests the
      matching complete prefix.
      — `swarm.TestLevelTwoServesLocalAndImportedTogether` (over the wire),
      `swarm.TestLevelTwoAnnouncesEverythingItServes` (announced set).
- [x] A Level-2 node containing both local and imported graph data returns both;
      a test fails if the implementation merely flips the old `mirrorOnly`
      boolean and drops either source.
      — same wire test asserts both halves against one node;
      `store.TestLocalBlockKeysFollowsItsSourceSet` and
      `store.TestServedCorpusMatchesWhatIsAnnounced` cover the store seam.
- [x] The response is demonstrably a complete broad bucket rather than a
      selected video plus decoys, and provider records expose only bucket keys.
      — `store.TestBlocksInPrefixReturnsTheWholeBucket` (whole bucket, `Held`
      equals the served count, no truncation), `TestLocalPrefixesDoNotNameVideos`
      (announcements are hashed buckets). There is no single-video request path
      to test: `handleBlockRequest` reads a prefix and nothing else.
- [x] Level-2 graph payload tests reject page-load ids, raw timestamps, titles,
      queries and ordered impression rows while accepting the aggregated edge
      fields.
      — `store.TestLevelTwoGraphPayloadCarriesOnlyAggregatedEdges`, an
      allow-list over the encoded JSON rather than the struct.
- [x] Catalogue/search/yield/sketch source sets match what Level 2 serves, and
      no single-video or single-token request path is introduced.
      — `swarm.TestLevelTwoAnnouncesEverythingItServes` walks all four
      namespaces; `Policy.CatalogueSources` is the single selector behind
      catalogue, shard, yield and sketch.
- [x] A three-node relay-cycle test proves repeated import/re-serve cycles do
      not increase an unchanged claim's count or create endless new sources.
      — `store.TestRelayCycleDoesNotAmplify`, three laps of A→B→C→A.
- [x] Two locally produced neighbourhoods carry unlinkable claim identities;
      neither identity equals the install signing key or libp2p peer id, while
      an update to one neighbourhood still replaces its earlier claim.
      — `store.TestClaimIdentitiesAreUnlinkable`,
      `store.TestUpdatedClaimReplacesItsPredecessor` (both delivery orders).
- [x] Level 2→1 immediately stops local and cached block service, provider
      announcements and three-gram telemetry while retaining all Level-1
      consumer/live/word capabilities.
      — WO-077's gate-first ordering is untouched, so
      `TestDowngradeShutsTheGateBeforeAnythingElse` stands unchanged;
      `TestDowngradedNodeKeepsEveryConsumerCapability` was extended to assert
      the new capability set is off (which covers local *and* cached service,
      since one flag now gates the handler and a separate one the local corpus).
- [x] Level 2 copy explicitly says locally derived broad graph blocks leave the
      device; no standing or user-facing document calls it mirror-only.
      — `PRIVACY.md`, the consent screen, the contribution control
      ("Broad sharing"), `ARCHITECTURE_CURRENT.md` §3, `DESIGN_v2.md` §6.1/§7.4,
      `DESIGN_BOOTSTRAP.md`, `README.md`, `store.LevelBroad`.
- [x] Level 3 remains unavailable and separately gated on STAR; enabling broad
      Level-2 blocks does not pretend STAR exists.
      — `MaxImplementedLevel` is still `LevelBroad`;
      `PublishCohortMeasurements` is off at Level 2 and drives nothing yet.
- [x] Full Go tests, race tests, property/fuzz regressions, extension tests pass
      (`go test ./...`, `go test -race ./...`, `FuzzBlocksInPrefix` and
      `FuzzShardSlice` re-run against the new envelope, 100/100 `npm test`).
- [ ] **Two-machine network inspection.** Outstanding — needs two hosts and a
      packet capture. What to check: a Level-2 node with only local
      observations is fetched from successfully; the reply is one bucket
      envelope whose `held` matches the blocks in it; two blocks from that node
      carry different `public_key` values, and neither is its libp2p peer id;
      and a 2→1 downgrade stops answers mid-session.

## Implementation notes

Where the required changes landed, for the WO-082 audit:

| Requirement | Landed in |
|---|---|
| 1 — capability split | `daemon/swarm/policy.go`: `ServeBroadBuckets`, `IncludeLocalGraph`, `IncludeLocalCatalogue`, `AnnounceProviders`, `JoinSearchTelemetry`, `PublishCohortMeasurements`, `PublishAttributedFunnel`, plus `GraphSources()`/`CatalogueSources()`. `MirrorOnly()` and `PublishOwn` are gone. |
| 2 — explicit union | `daemon/store/sources.go` (`SourceSet`) and `Store.BlocksInPrefix`, which runs both queries and concatenates claims. The type has no spelling for "own instead of imported". |
| 3 — preserved claims | `daemon/store/claim.go` (per-neighbourhood keys, revisions, own-claim registry), `daemon/store/blocks.go` (schema 3, `PeerClaimsForKeys`, `ImportBlock`), `peer_blocks`/`local_claims` in `sqlite.go`. |
| 4 — one corpus | `LocalPrefixes`, `LocalCataloguePrefixes`, `LocalShards`, `LocalYieldVector` and every serve path take the same `SourceSet` from the policy. `heldCatalogue` became a real union — the old non-mirror branch silently dropped every imported row. |
| 5 — broadness | `BlockBucket` envelope with `Held`/`Truncated`; `BucketAnonymityFloor` fails closed rather than shrinking a reply; still one prefix in, one bucket out. |
| 6 — transition order | Unchanged from WO-077. |
| 7 — docs and copy | Listed in the copy acceptance item above. |

Two decisions worth flagging to the architect:

- **The block reply is now an envelope, not a bare array.** Truncation was
  invisible: a capped reply and a genuinely small bucket looked identical to the
  recipient, so the anonymity set it was reasoning about could be smaller than
  it thought. `held` and `truncated` say so outright. This is part of why the
  protocol went to `3.0.0`.
- **Schema-3 blocks must be signed.** An unsigned claim has no identity to
  replace or deduplicate against, so accepting one would reintroduce exactly the
  accumulation preserved claims exist to stop. Schema ≤2 blocks already on disk
  keep the old allowance.
- **Rows imported before this migration are not advertised.** They live in
  `peer_edges` and still feed the local graph walk, but the claims that would
  have to be re-served were never kept, so `PeerGraphKeys` reads `peer_blocks`
  only. Announcing them would advertise material the stream must refuse.

## Challenge

Broadness is a protocol invariant, not marketing shorthand. Revise the wire
format if necessary; the current install-wide signature and re-sign-on-relay
shape are not constraints worth preserving. Do not weaken the bucket invariant
or call a selected response anonymous to avoid that work.
