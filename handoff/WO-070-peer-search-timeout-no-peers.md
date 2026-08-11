# WO-070 — PEER_SEARCH times out on multi-word queries with no/empty peers (8s bridge cap)

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Date:** 2026-08-11
**Source:** Lars, 2026-08-11 — reproducible. Data page shows NO peers. Single word
"machine" → 28 local hits, no timeout. Two-word "machine learning" (no quotes) →
"search timed out". The search UI sometimes draws a per-token progress bar as if
downloading from a peer, but no peers exist so it never completes. Same 8s
client-cap class as WO-069, but on the SEARCH/PEER_SEARCH path.

## Root cause (confirmed in code)

- The native bridge has a hard **8-second client timeout** on every request:
  `extension/lib/native.js:125` `function request(type, payload, timeoutMs = 8000)`
  rejects with `new Error("timeout")` after 8000ms if no reply. This is the
  "search timed out" the user sees (sw.js:386 rethrows `env.payload?.message ||
  "SEARCH failed"` / :405 for PEER_SEARCH).
- A single word tokenizes to few 3-gram tokens. `handleSearch` (main.go:554) answers
  from the LOCAL catalogue and returns in <8s → "machine" works (28 local hits).
- A two-word query tokenizes to ~2× the tokens. `handlePeerSearch`
  (main.go:576) calls `swarmNode.PeerSearch(ctx, query)` (shard.go:306), which
  fetches EACH token's shard and intersects. Even with no peers, the fetch path
  still iterates tokens and attempts shard fetches that hit `requestTimeout = 20s`
  (swarm.go:83) per shard. The whole PEER_SEARCH RPC is bounded by
  `peerSearchTimeout` (main.go:593, ctx with that timeout) — if that internal
  timeout is >= 8s, the daemon does NOT reply within the 8s client cap, so the
  bridge rejects first. Hence "machine learning" (many tokens) times out while
  "machine" (few tokens) does not.
- The per-token progress bar (`renderPeerProgress`, page/index.js) draws because the
  UI starts a PEER_SEARCH and shows tokens; with no peers those tokens never
  complete, so it hangs at 0 — the "phantom download" the user saw. (Distinct from
  WO-068's word/token telemetry bars; this is WO-067's per-query coverage bar.)

## The real defect

A query that SHOULD return "no peers / no data" FAST instead hangs until the 8s
client cap. The daemon already short-circuits when `swarmNode == nil`
(main.go:584 → `Available: false`, empty hits) — but "peers: 0 with a running
swarm" is a DIFFERENT state that does not hit that early return, so it falls into
the slow shard-fetch path and times out.

## What to fix (Opus decides)

1. **Fast-path when there is nothing to fetch.** If `swarmNode.Peers() == 0` (or the
   token estimates are all unknown / no shards reachable), `PeerSearch` must return
   immediately with `Available: true, Hits: []` (or `Available: false` if the swarm
   itself is down) — NOT iterate tokens against dead fetches. The UI then shows
   "no peers" instead of hanging.
2. **Bound the client cap vs the daemon timeout.** Ensure the daemon replies well
   under 8s for a no-peer query (the internal `peerSearchTimeout` must be < 8000ms,
   or the no-peer fast-path in #1 makes it moot). Do NOT just raise the global 8s
   default (weakens every RPC's protection).
3. **UI: don't draw a "downloading from peer" bar when peers == 0.** The search UI
   already knows the peer count (swarm status). Suppress or label the per-token
   progress bar as "no peers" instead of animating a phantom.

## Verification

- Reproduce: ensure data page shows peers: 0. Open search, query a single word
  (works, local hits). Query a two-word phrase → currently TIMES OUT; after fix,
  returns promptly with empty/no-peer result, no 8s hang.
- Regression test: `handlePeerSearch` with a running swarm but `Peers() == 0`
  returns within the client cap (inject a fake swarm with zero peers; assert reply
  time < 8000ms and `Available` set, hits empty). Lock with failing-then-passing.
- Confirm single-word local SEARCH still returns <8s (no regression on "machine").

## Relation to WO-069

WO-069 is the SUGGEST instance of the same 8s-bridge-cap class (synchronous graph
walk on cold DB). This is the SEARCH/PEER_SEARCH instance (multi-token query stalls
on empty/failed shard fetches). Both stem from the single-threaded bridge + 8s
client cap; fix them with the same principle: the daemon must reply within the cap,
and must fast-fail when there is nothing to fetch.
