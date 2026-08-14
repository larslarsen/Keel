# WO-092 — Make contribution-impact counters truthful under write and database failures

| | |
|---|---|
| **Addressee** | Sr Dev (Grok 4.6 Extra High) |
| **Status** | **Accepted 2026-08-13 — awaiting commit** |
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

- [x] Concurrent first increments leave exactly one row and preserve every
      successful increment and byte.
- [x] Concurrent reset/increment behavior is race-tested and has a documented,
      transactionally valid result.
- [x] A forced non-`ErrNoRows` read failure reaches the caller as an error, not
      zero activity.
- [x] A full writer counts exactly one request and its exact bytes.
- [x] Injected error, short-write and budget-refusal paths count nothing unless
      the complete logical response, including its terminal, was written.
- [x] The persisted schema still contains no observation, query, prefix,
      request, peer or application identity and no request-level timestamp.
- [x] Existing Level-1 refusal, reset scope and live corpus-state calculations
      remain unchanged.
- [x] `go test ./...`, `go test -race ./...`, `go vet ./...`, `npm test`, and
      `git diff --check` pass.

## Do not

- Do not add a request log or per-peer/per-protocol counters.
- Do not count attempted, refused, budget-dropped, failed or partial replies.
- Do not make contribution accounting part of the network response protocol.
- Do not widen WO-086 into rewards, credits or rankings.

## Challenge

“The write method was called” is not the metric. The claim shown to the user is
“your copy helped answer,” so only a complete successful reply may increment it.

## Implementation record — 2026-08-13

`contribution_activity` now has a `singleton INTEGER PRIMARY KEY … CHECK
(singleton = 1)` column. That value is a schema constant for the upsert
target, not an identity. Existing three-column tables are rebuilt on open:
duplicate rows are summed, the earliest `since_day` is kept, and an empty
table stays empty.

`RecordContributionServe` and `ResetContributionActivity` are single
`INSERT … ON CONFLICT(singleton) DO UPDATE` statements. Concurrent first
increments leave one row and keep every increment. Concurrent reset and
increment serialize; the last committed statement wins, and
`bytes_served == 100 * requests_answered` holds when every increment is 100
bytes.

`ContributionActivity()` still treats `sql.ErrNoRows` as a honest zero. Any
other read error is returned so the panel cannot invent a valid zero.

Block, word-telemetry and live-snapshot handlers write through
`replyAndRecord`. Catalogue and shard frames write through `writeFull`. A
short write or a non-nil error is not answered and adds neither a request
nor bytes. A counter error after a completed write is logged and does not
write again.

Level-1 refusal, reset scope and live corpus-state snapshot queries are
unchanged.

## Reviewer finding — 2026-08-13

The singleton migration, atomic upserts, read-error propagation and the three
single-payload handlers are sound. All reported suites pass independently.
The implementation is not accepted because the catalogue/shard paged-response
accounting still treats byte-budget refusal as successful completion.

`writeFrame()` returns `(-1, nil)` when `chargeBytes()` refuses a frame.
`servePagedResponse()` does not handle that sentinel consistently:

1. A refused header is accepted as `written = -1`, after which the function
   continues trying to write pages and a terminal.
2. A refused page is added to `written` before the negative sentinel is tested,
   undercounting an otherwise valid incomplete response by one byte when its
   signed terminal fits and is written.
3. A refused terminal returns the bytes from earlier frames with a nil error.
   Both handlers then call `RecordContributionServe(written)` even though the
   requester received no authenticated terminal and therefore no valid logical
   response.

The last case directly violates this order's metric: a budget-dropped partial
response is counted as “your copy helped answer.” The first case can also pass
a negative byte count into the cumulative counter. Existing tests exercise
`writeFrame()` in isolation but never prove the catalogue/shard handler's
accounting decision after header, page or terminal budget refusal.

### Required correction

- A refused header must end the request without contribution accounting.
- Never add the `-1` budget sentinel to the wire-byte total.
- Count a page-limited or budget-limited traversal only when its signed
  `complete=false` terminal was fully written; then count exactly the header,
  written pages and terminal bytes.
- If the terminal is refused, short or failed, record neither a request nor
  bytes even if earlier frames left the machine.
- Add deterministic paged-response/handler tests for header refusal, page
  refusal followed by a successful terminal, terminal refusal after earlier
  frames, and short/error writes. Assert both the returned byte total and the
  persisted contribution counters.

Do not change paging anonymity, response framing or the serving budget. This
is only the completion/accounting boundary WO-092 already owns.

Independent review gates before this missing regression was added:

```text
npm test                 21/21 test files pass
go test ./...            all packages pass
go test -race ./...      all packages pass
go vet ./...             pass
git diff --check         pass
```

## Correction record — 2026-08-13

`writeFrame` now returns `errFrameBudget` and zero bytes when `chargeBytes`
refuses a frame. `servePagedResponse` treats that as:

- header: stop immediately, return 0, no accounting;
- page: stop the traversal, write a `complete=false` terminal, and count
  only if that terminal is fully written;
- terminal: return the earlier wire bytes with an error so
  `commitPagedServe` records nothing.

Catalogue and shard handlers share `commitPagedServe`. Tests cover header
refusal, page refusal with a successful terminal, terminal refusal after
earlier frames, and short/error writes, asserting both the returned total
and the persisted counters. Paging anonymity, framing and the serving
budget are unchanged.

## Final reviewer acceptance — 2026-08-13

Accepted. The reviewer independently verified the singleton migration,
concurrent upserts, non-`ErrNoRows` propagation, and all five serving paths.
The paged correction closes the final boundary:

- header refusal writes and counts nothing;
- page refusal can count only after a fully written signed
  `complete=false` terminal, using the exact on-wire byte total;
- terminal refusal records neither a request nor bytes; and
- short and failed writes record nothing.

No privacy schema, contribution level, paging anonymity, response framing or
serving-budget policy changed.

Independent final gates:

```text
npm test                 21/21 test files pass
go test ./...            all packages pass
go test -race ./...      all packages pass
go vet ./...             pass
git diff --check         pass
```

The accepted implementation remains uncommitted in the working tree.
