# WO-086 — Show Level-2 contribution impact without retaining request history

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Sonnet) |
| **Status** | **Architecture ready — implement after WO-084 and WO-085** |
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

- [ ] Tests prove the data model contains no query/token/prefix/peer identifier
      and no request-level timestamp.
- [ ] Counts come from the same local-plus-cached served corpus as WO-084.
- [ ] A request refused by policy is not counted as successfully answered.
- [ ] Level transitions update the view across all connected browsers through
      the owner status path without restarting the daemon.
- [ ] Extension storage remains free of contribution metrics.
- [ ] Copy does not imply cryptographic proof, unique users or upload credit.

## Do not

Do not turn an encouragement panel into a surveillance log. If a proposed
metric needs remembering who asked for what, omit the metric.
