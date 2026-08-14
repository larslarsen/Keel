# WO-101 — Close distributed-search termination semantics

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Accepted** 2026-08-13 — WO-102 closed final cause-propagation findings; two-machine live QA pending |
| **Date** | 2026-08-13 |
| **Depends on** | WO-100 implementation at `a59953b` |
| **Source** | Architecture review of WO-100's landed concurrency paths |

## Outcome

Finish WO-100 without changing the distributed-search design. A search must
stop for the reason that actually occurred: bytes being temporarily reserved
are not bytes spent, an unresolved title is not a miss, a malformed response
is not an absent response, and cancelled work continues to occupy its job slot
until it retires.

This order changes no query semantics, key scheme, prefix broadness,
contribution entitlement, search UI, or wire capability revision. Keep the
stack on `wo-097-distributed-search-foundation`; do not merge it to `main` until
this order passes review.

## Confirmed gaps

### 1. A reservation is being mistaken for exhaustion

`budgetMeter.reserve()` subtracts a read's whole grant before the underlying
reader returns. If another concurrent reader sees `remaining == 0`, it latches
`exhausted` and cancels every stream immediately. The first reader may then
consume only part of its grant and refund the rest, but exhaustion is already
latched and the job reports `budget` with usable allowance remaining.

This is easiest to reproduce with two controlled readers: one holds the last
reservation, the other asks for capacity, and the first short-reads and
refunds. The waiting reader must receive the refunded capacity; it must not
cancel the job merely because the capacity was temporarily leased.

Make the meter distinguish at least:

- bytes committed by completed reads;
- bytes reserved by reads currently in flight; and
- capacity genuinely unavailable because the committed total reached the
  ceiling.

Maintain the invariant `committed + reserved <= limit`. When all remaining
capacity is reserved but not committed, another metered read waits for a
commit/refund or context cancellation. If the reservation is partly unused,
the waiter can proceed with the refund. If the final reservation is fully
consumed and a read needs more, return `ErrSearchBudget`, latch the budget
terminal, and cancel the outstanding search streams. Never call the
cancellation callback while holding the meter lock.

Do not turn this into a whole-query clock, a per-response allowance, or a read
past the ceiling. An exactly full response that completes the required work is
not retroactively a budget failure merely because the final byte used the
allowance; budget termination means the ceiling prevented further requested
work.

### 2. Unresolved catalogue candidates still advance saturation

`resolveTitles()` correctly leaves candidates retryable after an incomplete,
unavailable, or invalid catalogue traversal. `creditResponse()` then returns a
plain zero-gain map, and `runTokenWork()` unconditionally passes it to
`recordSaturation()`. The unresolved candidate therefore increments the word's
gainless streak even though WO-100 says it is not a miss. Three such outcomes
can satisfy saturation and stop below the retained word target.

Carry a candidate disposition through the completion barrier:

- **checked** — a local title was available and the complete query was tested;
- **absent** — a verified complete broad catalogue traversal established that
  the candidate has no title in the bucket; or
- **unresolved** — provider/resource termination prevented that decision.

The barrier must publish its disposition before waking joiners. A response may
increment a word's miss streak only when every candidate relevant to that
word is checked or absent. If it gained a new confirmed candidate, reset the
streak normally. If it gained nothing but any candidate remains unresolved,
leave the streak unchanged. A candidate already checked or absent before the
response began remains an honest zero gain.

Keep unresolved candidates retryable within the existing provider list and
job-wide byte ceiling. Do not issue a candidate-id request, narrow the broad
prefix, persist retry state, or count the same video twice.

### 3. Invalid paged replies are classified as unavailable

`fetchCataloguePagesFrom()` maps every `requestPaged()` error to
`catalogueUnavailable`. That includes a reply which sent bytes but failed JSON
framing, header/terminal validation, or terminal authentication. The enum and
WO-100 acceptance claim that invalid and unavailable are distinct, but the
transport boundary erases that distinction before catalogue code sees it.

Preserve a typed paged-response failure across `requestPaged()`:

- connection, stream-open, or request-write failure before any response is
  received is unavailable;
- a response that arrives but cannot be framed or authenticated is invalid;
- an authenticated `complete:false` terminal is incomplete;
- a verified `complete:true` terminal is complete; and
- `ErrSearchBudget` remains budget termination, not invalid or unavailable.

Both invalid and unavailable remain retryable. The correction is about honest
state propagation and the saturation decision, not penalizing a peer or
changing privacy behavior.

### 4. `stopAll` releases the global ceiling before jobs retire

`searchRegistry.stopAll()` snapshots the jobs and then replaces the registry
map with an empty map before calling `job.stop()`. Those goroutines are still
retiring, but `add()` can now admit another full ceiling of jobs. This bypasses
the identity-safe retirement rule that `cancelJob()` and `finishJob()` now
enforce.

Snapshot and stop the jobs without deregistering them. Only
`searchRegistry.remove(exactJob)` from the job's deferred retirement may free a
global slot. Repeated `stopAll()` calls remain safe and cancellation remains
idempotent. The entitlement gate still decides whether a later start is
allowed; the registry must not depend on that separate gate to keep its own
ceiling true.

## Tests and acceptance

- [x] A controlled short read can hold the last reservation while another
      reader waits; refund wakes the waiter and does not latch exhaustion.
- [x] If the held final reservation is fully consumed, a waiting/follow-up
      read gets `ErrSearchBudget`, outstanding streams are cancelled promptly,
      and total committed bytes never exceed the tested limit under `-race`.
- [ ] Meter waiters wake on page cancellation, downgrade, consent withdrawal,
      shutdown, and budget termination without goroutine leakage.
- [x] An incomplete, unavailable, or invalid catalogue outcome with no gain
      leaves the relevant saturation streak unchanged and the candidate
      retryable.
- [x] A later complete retry can check or retire that candidate; a later match
      streams once and advances each distinct word count once.
- [x] A complete-empty traversal makes the candidate absent and permits an
      honest miss; a previously checked/absent candidate also permits an honest
      miss.
- [x] Two responses joining one candidate observe the published checked,
      absent, or unresolved disposition before either updates saturation.
- [ ] End-to-end paged catalogue tests distinguish no response, malformed
      framing, invalid terminal/authentication, authenticated incomplete,
      complete-empty, and complete-with-rows. The budget sentinel is preserved.
- [x] `stopAll()` leaves every stopped job in the global registry until the
      exact job retires; new starts cannot use those slots early, and repeated
      stops/removals are safe under `-race`.
- [x] `go test ./...`, `go test -race ./...` from `daemon/`, and `npm test` from
      the repository root pass.
- [ ] Leave the entire dependent stack unmerged and ready for architecture
      review. After acceptance, the branch can be merged to `main` as one unit
      and WO-095's pending two-machine search QA can run with both machines on
      key scheme 2.

## Do not

- Do not change the continuous query grid, matcher, result-union rule, retained
  word targets, or target-plus-saturation stop matrix.
- Do not expose tokens, prefixes, candidate ids, peer ids, queries, or titles in
  search diagnostics.
- Do not narrow catalogue requests or stop a broad traversal when the wanted
  candidate appears.
- Do not serialize the whole search or remove the node-wide four-response
  ceiling.
- Do not add a persistent job, query, retry, or prefix-completion ledger.
- Do not change Level 1 or contribution consent.

## Stop conditions for the implementer

Return to architecture review if the correction appears to require a new wire
revision, a key-scheme change, a narrow candidate lookup, persistent search
state, or a contribution-policy change.

---

## Implementation note (Sr Dev, 2026-08-13)

All four were real, and all four were in code WO-100 had just landed.

| Gap | Correction |
|---|---|
| 1 lease mistaken for spend | `budgetMeter` tracks `committed` and `reserved` separately with `committed + reserved <= limit`. A read that finds every remaining byte leased WAITS on a settlement channel instead of latching exhaustion; only `committed` reaching the limit with nothing outstanding is exhaustion. `settle(grant, used)` commits what was read and refunds the rest. The cancellation callback is invoked after the lock is released |
| 2 unresolved advanced saturation | `candidateState.disposition` (`checked`/`absent`/`unresolved`) is published before joiners wake; `creditResponse` returns a `responseCredit` carrying `unresolved`, and `recordSaturation` leaves the streak unchanged on a gainless-but-undecided response |
| 3 invalid flattened to unavailable | `errNoResponse` marks pre-response transport failures; `classifyPagedError` maps the rest to `invalid`, and `ErrSearchBudget` stays budget termination rather than a verdict about the peer |
| 4 `stopAll` freed the ceiling early | it stops without deregistering; only the job's own identity-keyed retirement frees a slot |

Two notes for the reviewer:

1. **The meter now blocks, which is new behaviour worth knowing about.** A read
   that arrives when every remaining byte is leased parks on a channel until a
   settlement or context cancellation. That is what makes a short read's refund
   reach the waiter instead of being wasted, and it is bounded: every waiter
   also selects on the job context, so page cancellation, downgrade, consent
   withdrawal, shutdown and budget termination all wake it.

2. **§1's "exactly full response" clause needed no special case.**
   `readPagedResponse` stops as soon as it decodes the authenticated terminal
   frame and never reads on to EOF, so a response that consumes the last byte
   and completes its work issues no further read and cannot retroactively
   become a budget failure.

Not done, and not code: WO-095's two-machine live QA, both machines on key
scheme 2. The whole stack — WO-097, WO-095, WO-099, WO-100, WO-101 — is on
`wo-097-distributed-search-foundation` and deliberately unmerged.

---

## Architecture review (2026-08-13)

The lease/spend meter, unresolved saturation barrier, invalid/unavailable
split, and stop-all retirement repair are accepted in principle. WO-102 is
still required because:

1. `ErrSearchBudget` is explicitly classified as `catalogueUnavailable` and
   swallowed by the provider loop instead of surviving as the job stop cause;
2. the purported end-to-end invalid/budget test calls the classifier directly
   and only tests `errors.Is()` on a synthetic value; a local `TitlesFor` error
   can still turn every settled candidate into `absent`; and
3. `reserve()` may lease refunded capacity after its waiter context has already
   been cancelled.

The suites are green, but they do not exercise those races through the real
call chain. The branch remains unmerged pending WO-102 and review.
