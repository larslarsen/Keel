# WO-067 — Distributed search: yield-gossip, global count, coverage UI, hardening

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Done** (2026-08-11) |
| **Date** | 2026-08-11 |
| **Source** | Split from WO-059 when its Phase 1+2 (tokenizer, shard index, serve/fetch RPC, minimal search UI) shipped — see WO-059's "What was built" section and `handoff/WO-059-distributed-peer-search.md` for the full original design, which this ticket does not repeat. |

## What WO-059 shipped, in one paragraph

`daemon/store/shard.go` (tokenizer + shard grouping, `ShardK=3`, `ShardM=256`,
versioned in `keyscheme.go`), `daemon/swarm/shard.go` (`ShardProtocol`,
`handleShardRequest`, `FetchShard` with tag-self-filter + union, `PeerSearch`
intersecting tokens), a `PEER_SEARCH` daemon RPC, and a "search the network"
checkbox on the full page. Peer selection was naive (DHT-provider + known-peers);
the stop condition was 3-consecutive-miss saturation or a 20-peer cap — both
upgraded by this ticket.

## Disposition of all 8 deferred items

### Built

**#2 Yield-vector gossip.** `TokenDictAlphabet`/`TokenDictSize` +
`tokendict.go` (base-27 index, no shipped wordlist). `store.LocalYieldVector`,
`YieldThreshold` in `keyscheme.go`. `daemon/swarm/yield.go` on a shared
gossipsub instance (refactored out of `live.go` so live/yield/sketch share one
`*pubsub.PubSub`). `FetchShard` skips a peer only when yield is known-low;
unknown behaves as before.

**#3 Global per-keyword distinct count.** Push-only gossiped per-token HLL
sketches — not a request/response "ask for a sketch of K" (that would leak K).
Mid-session correction: the original cut assumed pull; gossip never names K on
the wire as a request. `TokenSketchP=8`, `sketch_store.go`
(`MergeTokenSketch`, `TokenEstimate`, `RecordTokenSearch` with drift-based
`due_at`), `daemon/swarm/sketch.go` (publish/receive rate limits). `FetchShard`
stop condition uses `TokenEstimate`: saturation alone no longer stops when a
target is known and unmet; `maxPeers` remains the hard backstop. No-known-target
falls back to pure saturation.

**#4 Coverage-bar UI (query-scoped).** `PEER_SEARCH_RESULT.progress[]` carries
`{token_index, fetched, target, known}` — never token text.
`extension/page` `renderPeerProgress` draws one shuffled, unlabeled, color-coded
segment per walked token (CVD-safe palette). Distinct from WO-068's two-tier
**corpus-wide** word/char-token telemetry bars — separate renderer, separate data
(this search's fetch coverage vs global frequency). Do not conflate them.

**#5 Disk-slider tie-in (sketch cache).** Raw shard-fetch results stay
transient (unchanged). The **token sketch cache** is under the same
`DiskBudget` / `evictCache` path as thumbnails (`token_sketches` table, shared
LRU). Coverage target via slider for *shard result* retention remains N/A until
those are persisted; sketches are the refetchable half that actually hits disk.

**#6 Shard-reply signing + cross-peer poison detection.** `store.ShardPack`
(mirrors `CataloguePack`), `BuildShardPack`/`VerifyShardPack`. `FetchShard`
verifies and resolves cross-peer claims via `resolveShardEntries`: equal-trust
disagreement drops the video; signed overrides unsigned.

### Decided against / deferred (documented, no code)

**#1 Per-fetch ephemeral identity.** Keep per-daemon-start rotation
(`EphemeralIdentity`). WO-059 attack #1 already treats a uniform shard fetch as
near-zero-info per request; rotation breaks cross-session trajectory, not one
fetch. Per-fetch needs a throwaway non-DHT host — real cost for a gap the
original analysis does not require closing at this threat level.

**#7 Live title backfill.** Keep `TitlesFor` as designed: no catalogue fetch
bound to exactly the search-hit id set (`MissingCataloguePrefixes`). Decoy
padding was considered and rejected as the wrong pattern here; untitled hits
remain valid rows.

**#8 Fetch-count padding/batching.** Deferred, lower priority. Temporal leak
of query structure via fetch volume/timing is real but judged not worth the
bandwidth cost until measurement says otherwise. Revisit with live traffic.

## What was built (file map)

| Area | Path |
|---|---|
| Token dictionary | `daemon/store/tokendict.go`, `keyscheme.go` (`TokenDict*`, `YieldThreshold`, `TokenSketchP`) |
| Yield vector | `daemon/store/yield.go`, `daemon/swarm/yield.go` |
| Token sketches | `daemon/store/sketch_store.go`, `daemon/swarm/sketch.go`, `thumbs.go` cache union |
| Signing / poison | `daemon/store/shard.go` (`ShardPack`), `daemon/swarm/shard.go` (`resolveShardEntries`) |
| Stop condition | `daemon/swarm/shard.go` (`shouldStopOnSaturation`, `TokenEstimate`) |
| Progress wire + UI | `daemon/bridge/protocol.go` (`TokenProgress`), `daemon/main.go`, `extension/page/{index.html,index.js,style.css}` |

## Acceptance

- [x] Per-fetch identity: **documented decision** — per-start remains sufficient.
- [x] Yield-vector gossip: dictionary, threshold, channel, `FetchShard` screening.
- [x] Global per-keyword count: gossiped sketch merge + privacy stance (push-only, no K in a request).
- [x] Coverage bar in search UI, wired to pre-fetch `TokenEstimate` + fetched count.
- [x] Disk-slider governs sketch cache (shard replies still transient).
- [x] Shard replies signed; cross-peer disagreement treated as poison.
- [x] Title backfill: **documented decision** — no backfill.
- [x] Fetch-count padding: **deferred** with rationale.

## Pushback invited (closed with answers)

- Per-fetch identity: confirmed not worth building without measurement that
  per-start fails. Answer recorded under #1.
- Sketch privacy: pull would leak K; push-only gossip does not name K as a
  request. Serving a sketch still reveals cardinality shape of a token the node
  chose to re-gossip (drift-scheduled, rate-limited) — accepted for stop/bar use;
  word-level corpus stats are a separate transport (WO-068 direct fetch).
