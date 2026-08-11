# WO-067 — Distributed search: yield-gossip, global count, coverage UI, hardening

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Open** |
| **Date** | 2026-08-11 |
| **Source** | Split from WO-059 when its Phase 1+2 (tokenizer, shard index, serve/fetch RPC, minimal search UI) shipped — see WO-059's "What was built" section and `handoff/WO-059-distributed-peer-search.md` for the full original design, which this ticket does not repeat. |

## What WO-059 shipped, in one paragraph

`daemon/store/shard.go` (tokenizer + shard grouping, `ShardK=3`, `ShardM=256`,
versioned in `keyscheme.go`), `daemon/swarm/shard.go` (`ShardProtocol`,
`handleShardRequest`, `FetchShard` with tag-self-filter + union, `PeerSearch`
intersecting tokens), a `PEER_SEARCH` daemon RPC, and a "search the network"
checkbox on the full page. Peer selection is naive (DHT-provider + known-peers,
same as block fetch); the stop condition is 3-consecutive-miss saturation or a
20-peer cap, not the target-based one the original design specifies.

## What is deliberately not built yet

1. **Per-fetch ephemeral identity.** Current rotation is per-daemon-start
   (`EphemeralIdentity`, `swarm_runtime.go`), same as block/catalogue fetch.
   WO-059's security analysis (attack #1) accepts this as sufficient — a
   uniform shard is near-zero-info per fetch, so rotation exists to break
   cross-session trajectory, not to hide one fetch. Per-fetch would need a
   lightweight throwaway host (not the full DHT-joining `Start` path) — nothing
   in the codebase builds one today.

2. **Yield-vector gossip.** A 1-bit-per-token, dictionary-indexed vector,
   pushed via bounded-fanout rebroadcast, so a client can skip shards unlikely
   to yield before spending a fetch on them. Needs: a fixed local dictionary
   (never on the wire — reordering it silently breaks what bit N means, per
   `keyscheme.go`'s WO-059 note), a gossip channel (mirror `LiveTopic`'s
   gossipsub setup in `daemon/swarm/live.go`, which already proves the
   bounded-fanout model works at this project's scale), and a threshold policy
   (top 50%/10%/80%, a protocol constant like `ShardK`/`ShardM`). Without it,
   `FetchShard`'s peer selection is blind — every candidate looks the same
   until fetched.

3. **Global per-keyword distinct count.** The original design needs this as
   the coverage-bar denominator and (with saturation) the real stop condition.
   **Not actually unsolved as a primitive** — `daemon/store/sketch.go` already
   has `(*Sketch).Merge(other *Sketch) error` (exact HLL register-wise union)
   and `Overlap`, built for WO-052's peer/network-size estimation. What is
   missing is the wiring: each node would need to build a local sketch over
   its own K-matching video ids per token/shard, serve the sketch (not the
   raw ids) alongside or instead of a shard fetch, and have the client merge
   sketches from several peers to estimate the network-wide count. Whether
   serving a per-token sketch leaks more than serving the uniform shard itself
   needs the same "server-side interest-blind" analysis WO-059's empirical
   section gave grouping — not evaluated here.

4. **Coverage-bar UI.** Depends on #3. Until then the search UI has no
   completeness signal at all — a caller cannot tell "found everything" from
   "found what the first few peers happened to hold."

5. **Disk-slider tie-in.** WO-059's retention-classes section puts shard
   fetches in the same refetchable/evictable bucket as thumbnails and peer
   blocks. Nothing currently sizes or evicts shard-fetch results against
   `store.DiskBudget`/`SetDiskBudget` (`thumbs.go`) — Phase 1+2 doesn't
   persist shard replies at all (see #7), so there is nothing to evict yet,
   but the coverage-target-via-slider mechanism the original design specifies
   is unbuilt.

6. **Catalogue-poisoning cross-peer consensus.** A malicious peer can lie in
   a shard reply (claim/omit a video). Because results union across multiple
   peers, one liar is diluted but never caught or flagged. WO-059's mitigation
   #5 proposes extending the seed-pack per-block signature pattern
   (`seed.go`) to shard replies and treating cross-peer disagreement as a
   poison signal — not built. Shard replies are currently **unsigned**
   (see `store.ShardSlice`'s doc comment for why that was an acceptable v1
   simplification, not an oversight: nothing is persisted or re-served from a
   shard reply today, so the blast radius of trusting one is "one search
   result is wrong," not "poison propagates").

7. **Live title backfill for search hits.** `store.TitlesFor` (Phase 1+2)
   deliberately does not fetch a title for a video this node found via
   PeerSearch but has never catalogued — doing so naively (fetch the
   catalogue bucket for exactly the matched ids) would bind that fetch
   pattern to the search result, which is exactly the correlation
   `MissingCataloguePrefixes`' doc comment warns against (it requires a whole
   graph bucket's targets, never a subset). If title backfill is wanted, it
   needs its own privacy analysis — e.g. padding the id set with unrelated
   ids before requesting catalogue buckets for them, or accepting the
   correlation as a stated tradeoff — not a drive-by fix.

8. **Rate-limited/padded fetch counts.** WO-059 attack #4: fetch volume and
   timing can leak query structure (single word ≈ N tokens) even without
   revealing which word. No padding or batching exists.

## Acceptance (split from WO-059's original list — items already met are
omitted; see that ticket for full context)

- [ ] Per-fetch identity rotation, or a documented decision that per-start
      remains sufficient long-term.
- [ ] Yield-vector gossip: dictionary, threshold constant, gossip channel,
      `FetchShard` peer screening using it.
- [ ] Global per-keyword count: sketch-serving RPC, cross-peer merge, and a
      privacy evaluation of what serving a per-token sketch discloses.
- [ ] Coverage bar in the search UI, wired to the above.
- [ ] Disk-slider governs shard-fetch caching (once shard fetches are cached
      at all — currently transient).
- [ ] Shard replies signed; cross-peer disagreement flagged as a poison
      signal.
- [ ] A considered decision on title backfill for untitled peer-search hits.
- [ ] Fetch-count padding/batching, if the temporal leak is judged worth the
      bandwidth cost.

## Pushback invited

- Is per-fetch identity actually worth building, or does per-start hold up
  under real-network measurement? WO-059's own analysis leans toward "good
  enough" — worth confirming rather than assuming this ticket must build it.
- The sketch-merge primitive existing does not mean serving it is free
  privacy-wise — treat #3 as "wiring plus a fresh privacy review," not "just
  wiring."
