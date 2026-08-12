# WO-092 — Make contribution-impact counters truthful under write and database failures

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Sonnet) |
| **Status** | **Ready after WO-091** |
| **Date** | 2026-08-12 |
| **Source** | Reviewer verification of WO-086 |

## Problem

WO-086's privacy shape is correct, but two implementation details violate its
own “successfully answered” and “one row” contracts.

### Reply writes are not checked

All five instrumented handlers use this shape:

```go
_, _ = s.Write(raw)
RecordContributionServe(len(raw))
```

A failed or short write is therefore recorded as one fully answered request.
Putting the counter after `Write` is not enough when both return values are
discarded. The affected block, catalogue, shard, word-telemetry and
live-snapshot handlers must share one full-write rule.

### The database does not enforce one row and hides read errors

`contribution_activity` has no primary key, uniqueness constraint or seeded
singleton row. `RecordContributionServe()` and
`ResetContributionActivity()` perform separate `UPDATE`, row-count check, and
`INSERT` statements. Concurrent first calls can both observe an empty table and
insert two rows. The sequential row-count test does not exercise that race.

`ContributionActivity()` also turns every query error into `(0, 0, "", nil)`.
Only `sql.ErrNoRows` means a fresh counter; corruption, schema and I/O errors
must propagate instead of being displayed as an honest zero.

## Required change

1. Add a database-enforced singleton invariant without adding any user, peer,
   query, prefix or request identifier. A constant unique expression index is
   acceptable if it preserves the existing three-column privacy schema; a
   fixed sentinel key is also acceptable if tests state explicitly that it is
   a schema constant, not an identity.
2. Make increment and reset atomic upserts. Repair any zero-row or duplicate-row
   state produced by the current implementation without discarding the summed
   counters; retain only coarse day precision for the counting window.
3. In `ContributionActivity()`, return zero only for `sql.ErrNoRows`. Propagate
   every other database error to the RPC so the UI cannot invent a valid zero.
4. Centralize reply writing for the five serve handlers. Count a request only
   when the entire payload was written successfully. A short write or non-nil
   error is not answered and adds neither a request nor bytes.
5. Keep accounting failure non-fatal to an already completed network reply:
   log the counter error, but do not retry or send a second response.

## Acceptance

- [ ] Concurrent first increments leave exactly one row and preserve every
      successful increment and byte.
- [ ] Concurrent reset/increment behavior is race-tested and has a documented,
      transactionally valid result.
- [ ] A forced non-`ErrNoRows` read failure reaches the caller as an error, not
      zero activity.
- [ ] A full writer counts exactly one request and its exact bytes.
- [ ] Injected error and short writers count nothing for each shared write path.
- [ ] The persisted schema still contains no observation, query, prefix,
      request, peer or application identity and no request-level timestamp.
- [ ] Existing Level-1 refusal, reset scope and live corpus-state calculations
      remain unchanged.
- [ ] `go test ./...`, `go test -race ./...`, `go vet ./...`, `npm test`, and
      `git diff --check` pass.

## Do not

- Do not add a request log or per-peer/per-protocol counters.
- Do not count attempted, refused, budget-dropped, failed or partial replies.
- Do not make contribution accounting part of the network response protocol.
- Do not widen WO-086 into rewards, credits or rankings.

## Challenge

“The write method was called” is not the metric. The claim shown to the user is
“your copy helped answer,” so only a complete successful reply may increment it.
