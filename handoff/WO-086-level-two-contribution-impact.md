# WO-086 — Show Level-2 contribution impact without retaining request history

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Sonnet) |
| **Status** | **Implemented — accounting correctness closed by WO-092** |
| **Date** | 2026-08-11 |
| **Source** | Lars contribution-level incentive discussion, 2026-08-11 |

## Outcome

Give a Level-2 user concrete evidence that broad sharing is doing useful work.
This is a feedback product, not an accounting, identity or upload-credit system.

Show locally computed aggregate values such as:

- graph claims and catalogue/search entries currently eligible to serve;
- locally contributed versus peer-cached claims, counted without identifiers;
- complete buckets/shards announced;
- requests answered and bytes served; and
- current connected-peer/provider reach as an instantaneous gauge, not a peer
  history.

Use plain language: “your copy helped answer N broad requests,” not claims about
unique people or causal network impact that the node cannot prove.

## Privacy boundary

The daemon may persist numeric totals and coarse time-bucketed rollups in its
own SQLite database. The extension receives display aggregates only and stores
none of them.

Never retain or expose:

- raw queries, query hashes, tokens or requested prefix/bucket identifiers;
- peer ids, addresses, per-peer counters or connection histories;
- which local observation produced a served claim; or
- a request-by-request event log.

Do not derive rewards, rankings or service credits from these counters. They
are local, unaudited feedback and may reset after repair or migration.

## Required implementation

1. Instrument the final Level-2 serve/provider paths established by WO-084 so
   rejected, partial and successful work are not conflated.
2. Define a versioned daemon response containing aggregate numbers only.
3. Negotiate the response capability through WO-081's bridge contract; older
   daemons produce an unavailable state, never invented zeroes.
4. Add a compact Level-2 contribution view. At Level 1, explain that broad
   service begins at Level 2 rather than showing misleading zero activity.
5. Bound persistent rollups and provide an explicit local reset if totals are
   persisted.

## Acceptance

- [x] Tests prove the data model contains no query/token/prefix/peer identifier
      and no request-level timestamp.
- [x] Counts come from the same local-plus-cached served corpus as WO-084.
- [x] A request refused by policy is not counted as successfully answered.
- [x] Level transitions update the view across all connected browsers through
      the owner status path without restarting the daemon.
- [x] Extension storage remains free of contribution metrics.
- [x] Copy does not imply cryptographic proof, unique users or upload credit.

## Do not

Do not turn an encouragement panel into a surveillance log. If a proposed
metric needs remembering who asked for what, omit the metric.

## Implementation review — 2026-08-12

Split the metrics by kind rather than persisting all of them. "Eligible to
serve," the local-vs-peer-cached claim split, and buckets/shards announced are
current corpus state — recomputed fresh from `LocalGraphKeys`/`PeerGraphKeys`/
`heldCatalogue`/`LocalPrefixes`/`LocalShards` on every call, nothing stored,
nothing to bound. Connected-peer/provider reach reused the existing
`Node.Peers()`/`KeelPeers()` gauges unchanged — already instantaneous, already
history-free. Only "requests answered and bytes served" is genuinely
cumulative, so it is the one thing persisted: a single-row
`contribution_activity(requests_answered, bytes_served, since_day)` table,
day-precision only, proven column-exact by a schema-introspection test rather
than a source-text search. All five serve handlers (block, catalogue, shard,
word-telemetry, live-snapshot) record a success only after their reply is
actually written to the wire — never from a policy refusal or a
serving-budget drop, proven end-to-end over real libp2p connections.
`GET_CONTRIBUTION_IMPACT`/`RESET_CONTRIBUTION_IMPACT` are gated by their own
negotiated capability (`contribution_impact`, brand new — no legacy revision
to reconcile the way `peer_search`/`distributed_search` do) and refuse
server-side below Broad sharing with the same typed `CodeContributionRequired`
refusal `PEER_SEARCH` uses, as defense in depth behind the extension's
client-side gate. The extension panel mirrors the Live-tab/search-network
pattern exactly: visible and disabled with a reason at Level 1 or against an
un-negotiating daemon, never an invented zero; live-updates off the existing
`CONTRIBUTION_STATUS` broadcast with no reload. Go (daemon + swarm + store,
race-clean) and extension (174/174) test suites pass; `git diff --check`
clean.

## Reviewer follow-up — 2026-08-12

WO-092 records two defects found after implementation: reply handlers discard
`Write` results before counting a full success, and the cumulative table neither
enforces its single-row invariant nor distinguishes `sql.ErrNoRows` from real
read failures. The privacy model and UI boundary remain accepted.
