# WO-100 — Finish search-budget and resolution atomicity

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Done** 2026-08-13 — every automated acceptance box is covered by a test; the live-QA line stays open |
| **Date** | 2026-08-13 |
| **Depends on** | WO-099 implementation at `f45510c` |
| **Source** | Architecture review of WO-099's landed concurrency paths |

## Outcome

Keep WO-095 and WO-099's design, but make their resource and completion
boundaries true under the races they were intended to control.

The ordinary automated paths pass. The remaining defects occur at a budget
boundary, between concurrent candidate resolvers, after an explicitly
incomplete broad response, or while a cancelled job with a reused id is still
retiring. This order changes no query semantics, privacy entitlement, key
scheme, progress-bar meaning, or UI control.

## 1. Enforce the byte ceiling while reading, not after the response

`requestPaged()` currently reads and decodes the entire response through a
`countingReader`, then calls `budgetMeter.charge(counted.n)`. At the minimum
search budget the limit is 8 MiB, while one response may read as much as the
64-MiB transport cap. Cancellation after decoding therefore cannot make the
budget a hard ceiling or stop the response that crossed it.

Put the meter in the actual reader path:

- atomically reserve/read/refund against one job-wide remaining balance so
  four concurrent readers cannot each spend the same remainder;
- never read more search payload bytes than the balance granted to that read;
- charge malformed, rejected, and incomplete bytes exactly once;
- when no balance remains, return a typed budget termination and promptly
  reset/close every outstanding search stream rather than waiting for its
  ordinary network deadline; and
- leave non-search prewalk, seed, and ordinary catalogue sync unchanged.

The terminal reason must consult the meter directly. If the budget prevented
further work, `StreamingSearch` returns `budget` even when exhaustion happened
inside the last available provider response and no next loop iteration exists
to call `overBudget()`. It must not become `exhausted`, `cancelled`, or a generic
verification failure.

An external page cancellation, consent withdrawal, or contribution downgrade
remains cancellation. Tests must control which cause wins rather than relying
on scheduler timing.

## 2. Preserve complete versus incomplete catalogue outcomes

`pagedResponse` carries an authenticated `complete` flag, but
`fetchCataloguePagesFrom()` currently imports rows and returns only
`(rowCount, error)`. Its caller cannot distinguish:

- a verified complete bucket, including a valid empty bucket;
- verified partial pages with an `incomplete` terminal;
- no provider/response; and
- an invalid response.

Return a typed result that preserves those states through the quiet search
path and `prefixGroup`.

Verified partial pages may remain cached as public cover data, consistent with
WO-097's rotated pagination, but they do not mean the broad prefix is resolved.
Do not declare a missing candidate absent, count a zero-gain saturation step,
or memoize the prefix complete from an incomplete traversal. Continue bounded
rotated broad-prefix attempts until a complete traversal, provider exhaustion,
or the job budget ends. Never stop a broad response early because the desired
candidate happened to appear.

A valid complete empty response is completion, not “no peer.” Conversely,
falling through every provider with `(0, nil)` must not masquerade as a
successfully completed empty bucket.

## 3. Successful prefix coalescing lasts for the job

`prefixGroup.do()` removes every call from `inFlight` immediately after it
finishes. A second worker can compute the same missing-prefix plan before the
first import commits, reach `do()` just after the entry is removed, and perform
the same traversal again. The implementation comment says failed traversals are
not cached, but successful traversals are not cached either.

For the lifetime of one search job:

- concurrent callers join one in-flight traversal;
- a verified complete traversal is memoized, including complete-empty;
- an incomplete/unavailable/invalid traversal is released for a bounded retry;
  and
- terminal job cancellation wakes every waiter.

Do not create a durable browser or SQLite prefix-completion ledger in this
order. This is bounded in-memory job state.

## 4. A response cannot saturate while its candidate is resolving elsewhere

`creditResponse()` currently skips a candidate when `resolving[id]` is true.
That worker returns zero gain and can increment the word's saturation streak
while another worker is still fetching the same candidate's prefix. This
reintroduces the exact WO-095 rule WO-099 was meant to protect: a response is
not gainless until its candidate titles have finished resolving and have been
checked locally.

Concurrent nominations of the same candidate/prefix must join an in-memory
completion barrier. Each response waits until its candidates are one of:

- locally checked;
- definitively absent after a complete broad traversal; or
- explicitly unresolved because the job hit provider/resource termination.

Only then may that response update saturation. Global word counts remain
distinct by video id; joining must not double-count a result. If one concurrent
response confirms a new word while another relevant response is waiting, the
shared word's saturation state must observe that gain rather than recording a
simultaneous false miss.

An incomplete or unavailable catalogue outcome is not a miss. Keep the
candidate retryable within the existing provider and byte bounds.

## 5. Retire jobs by identity, not id alone

`cancelJob()` removes a job from the session and global registry before its
goroutine exits. The same UUID can then be accepted as a new job. When the old
goroutine runs `finishJob(id)`, it deletes the new job because completion is
keyed only by the reused string.

Keep a cancelled job's id reserved until its goroutine terminates, and make
finish/remove conditional on the exact job object registered under that id.
Cancellation remains idempotent. A stopped-but-not-finished job continues to
count against the active-job ceiling; it must not create a temporary hole that
admits unbounded retiring work.

## Tests and acceptance

- [x] A reader with 1 KiB remaining cannot consume a larger response; total
      bytes across four concurrent readers never exceed the explicit tested
      allowance.
- [x] Budget exhaustion resets outstanding reads promptly and reports
      `PEER_SEARCH_COMPLETE` with reason `budget`, including when it occurs in
      the last provider response.
- [x] Malformed and rejected response bytes are charged incrementally; ordinary
      non-search paged requests remain unmetered by a search budget.
- [x] Catalogue fetch results distinguish complete-empty, complete-with-rows,
      authenticated-incomplete, unavailable, and invalid.
- [x] Incomplete catalogue pages may cache verified public rows but cannot
      resolve the prefix, prove an absent candidate, or advance saturation.
- [x] Two workers with stale plans for one prefix perform one traversal after a
      verified complete result; an incomplete/failed result can be retried.
- [x] Prefix waiters wake on success, failure, budget stop, page cancellation,
      downgrade, and shutdown without deadlock or goroutine leakage.
- [x] A response sharing an in-flight candidate cannot be recorded gainless
      until resolution finishes; concurrent confirmation resets the relevant
      shared word's saturation state exactly once.
- [x] A transiently unresolved candidate can later match and stream once, with
      no narrow request and no duplicate word/result count.
- [x] Cancel then immediately reuse the same UUID is refused until the old job
      has terminated; the old deferred finish cannot delete the later job.
- [x] The identifier-free diagnostic test invokes the actual quiet search
      resolver and checks every captured search-path line, rather than invoking
      the verbose ordinary catalogue path and filtering its output away.
- [x] Go, race, extension, revision-2 compatibility, Level-1 refusal, and all
      WO-095/097/099 tests remain green.
- [ ] Only after these automated checks pass, perform the pending two-machine
      search QA with both machines on key scheme 2.

## Do not

- Do not replace the byte budget with a whole-query clock.
- Do not discard verified public partial pages merely to simplify state; retain
  their explicit incomplete status.
- Do not treat “candidate found” as permission to stop a broad traversal.
- Do not serialize the entire search or remove four-way bounded concurrency.
- Do not persist raw query, job, retry, prefix-completion, or progress state.
- Do not change Level 1, contribution consent, or prefix broadness.

## Stop conditions for the implementer

Return to architecture review if satisfying this order appears to require a
narrow candidate-id request, a durable query/job journal, a new key scheme, or
a change to contribution-level policy.

---

## Implementation note (Sr Dev, 2026-08-13)

All five were real, and all five were in code WO-099 had just landed.

| Gap | Correction |
|---|---|
| 1 byte ceiling after the fact | `budgetReader` reserves from one job-wide balance *before* each read and refunds the unused grant; exhaustion returns `ErrSearchBudget` and cancels the job, and `requestPaged` now resets outstanding streams on cancellation instead of letting them drain to their deadline. `outcome()` reads exhaustion from the meter, so a budget spent inside the last provider response still reports `budget` |
| 2 untyped catalogue result | `catalogueResult{Outcome, Rows}` with `unavailable` / `invalid` / `incomplete` / `complete`, carried through the quiet search path. Verified rows from an incomplete traversal are still imported as public cover data; the prefix is simply not resolved |
| 3 coalescing only covered exact overlap | `prefixGroup.resolve` memoizes verified complete traversals (including complete-empty) for the job, leaves incomplete/unavailable/invalid retryable, and wakes waiters on the job context |
| 4 gainless while resolving elsewhere | `candidateState` barrier: a response waits for candidates another worker claimed, and gain is counted for any of its candidates not already checked when it began — so a concurrent confirmation is observed as gain by both, and word counts stay sets of video ids so neither double-counts |
| 5 retirement keyed by string | a cancelled id stays reserved until its goroutine exits; `finishJob(id, job)` removes only the exact object; retiring jobs keep counting against the ceiling |

Three notes for the reviewer:

1. **`complete-empty` and `unavailable` are now genuinely different values**, and
   the fall-through case returns the *strongest outcome any provider gave*
   rather than a fabricated success. Falling through every provider is
   `unavailable`. This is what stops a search declaring a candidate absent that
   nothing ever looked for — which was the mechanism by which gap 2 fed gap 4.

2. **Only a complete traversal may retire a candidate.** `searchState.absent`
   exists so a candidate a complete bucket genuinely lacks is not re-nominated
   for the rest of the job; everything weaker leaves it retryable, bounded by
   the provider list and the byte budget as the order requires.

3. **The diagnostics test was weak and is now driving the real thing.** It used
   to invoke the verbose ordinary catalogue path and filter its output, which
   proved nothing. It now calls `searchState.resolveTitles` and asserts on every
   line captured while it runs.

Not done, and not code: WO-095's two-machine live QA, both machines on key
scheme 2.
