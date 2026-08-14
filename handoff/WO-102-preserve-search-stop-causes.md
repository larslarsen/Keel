# WO-102 — Preserve distributed-search stop causes end to end

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Done** 2026-08-13 — every automated acceptance box is covered by a test; the stack remains unmerged for architecture review |
| **Date** | 2026-08-13 |
| **Depends on** | WO-101 implementation at `db31531` |
| **Source** | Architecture review of WO-101's landed error and cancellation paths |

## Outcome

Keep WO-101's meter, candidate barrier, saturation rule, and identity-safe job
retirement. Close the three places where a stop or failure can still be
reported as a different fact: budget exhaustion is swallowed as provider
unavailability, a local title-read failure can make a candidate absent, and a
cancelled meter waiter can acquire newly refunded capacity.

This is a narrow correctness correction. It changes no query semantics, key
scheme, prefix broadness, contribution entitlement, UI, capability revision,
or persistence model. Keep it on `wo-097-distributed-search-foundation`; the
stack remains unmerged until review accepts this order.

## Confirmed gaps

### 1. The catalogue path still turns budget into unavailable

WO-101 says `ErrSearchBudget` remains budget termination, not invalid or
unavailable. The implementation does the opposite:

- `classifyPagedError(ErrSearchBudget)` returns `catalogueUnavailable`;
- its unit test explicitly requires that incorrect result;
- `fetchCataloguePrefixLogging()` then handles the error like an ordinary
  provider failure, continues discovery/fallback work, and ultimately returns
  `(best, nil)`; and
- the claimed end-to-end test only checks `errors.Is()` on a synthetic wrapped
  value. It never drives budget exhaustion through `requestPaged()`,
  `fetchCataloguePagesFrom()`, or the prefix traversal.

The meter currently rescues the final job reason, but that does not make the
typed path truthful or prompt. In particular, swallowing the sentinel allows
the known-peer fallback loop to continue after the job budget has already
cancelled its context.

Preserve the cause through the full call chain:

- pre-response transport failure is unavailable;
- a received but unverifiable response is invalid;
- authenticated `complete:false` is incomplete;
- authenticated `complete:true` is complete; and
- `ErrSearchBudget` short-circuits the catalogue traversal as budget
  termination. It must not be ranked, cached, retried, or returned as a
  provider outcome.

An external page cancellation, downgrade, consent withdrawal, or shutdown
remains cancellation and must not be relabeled budget. Provider failures remain
bounded retries. Add a context check before and during the known-peer fallback
so a terminated traversal does not walk the whole peer catalogue.

### 2. A local title-read failure can prove a false absence

`resolveTitles()` builds `settled` from complete broad-prefix traversals and
then calls `Store.TitlesFor(ids)`. If that local read fails, it returns
`(nil, settled)`. `creditResponse()` interprets every settled id without a
returned title as `absent`, publishes that disposition to joiners, and prevents
the candidate from being retried for the rest of the job.

A complete network traversal proves what arrived from the bucket; it does not
prove that a candidate was absent when the local database failed before the
resolver could inspect the imported/local rows. On any local planning or title
read error, every candidate whose title could not be checked remains
`unresolved`. It must not enter `absent`, advance saturation, or be suppressed
from a later retry.

Keep diagnostics identifier-free. Do not add a database retry loop or durable
search state; the next eligible peer response already supplies the bounded
retry opportunity.

### 3. Cancellation must win before a waiter leases more bytes

`budgetMeter.reserve(ctx, want)` checks capacity before checking `ctx`. A
waiter selecting on both a settlement and cancellation may take the settlement
case, loop, and lease refunded capacity even though the job was already
cancelled. In production the stream-reset watcher usually makes the subsequent
read fail, but the meter itself has admitted more work after page replacement,
downgrade, consent withdrawal, or shutdown.

Check cancellation before granting a lease and again after every wake. If
settlement and cancellation race, the next loop observes the cancelled context
before touching `reserved`. A cancelled waiter returns the context cause and
never invokes its underlying reader. Preserve the existing invariant
`committed + reserved <= limit` and the exactly-full-response behavior.

## Tests and acceptance

- [x] A metered real paged request that reaches its allowance preserves
      `ErrSearchBudget` through catalogue-page fetch and prefix traversal; it
      is never `catalogueUnavailable` or `catalogueInvalid`.
- [x] Budget exhaustion short-circuits provider discovery and known-peer
      fallback, cancels outstanding streams, and produces the existing
      `PEER_SEARCH_COMPLETE` reason `budget`.
- [x] A real malformed response that sends bytes reaches
      `fetchCataloguePagesFrom()` and returns `catalogueInvalid`; a real
      no-response path returns `catalogueUnavailable`. Do not satisfy this by
      calling the classifier directly.
- [x] Authenticated incomplete, complete-empty, and complete-with-rows remain
      distinct and retain WO-100/101 behavior.
- [x] If `MissingCataloguePrefixes` or `TitlesFor` fails locally, affected
      candidates publish `unresolved`, remain retryable, and do not advance
      saturation or enter `absent`.
- [x] After the local store becomes readable, a later retry can check/stream
      the candidate once and advance each distinct word count once.
- [x] A waiter racing refund with cancellation returns the context cause,
      acquires no lease, and never calls the underlying reader. Cover page
      replacement, downgrade/consent/shutdown cancellation through the shared
      context mechanism rather than separate implementations.
- [x] The committed/reserved ceiling, short-read refund, exact-full response,
      concurrent-reader, prefix-coalescing, candidate-barrier, and retiring-job
      tests from WO-100/101 remain green under `-race`.
- [x] `go test ./...`, `go test -race ./...` from `daemon/`, `npm test` from the
      repository root, and `git diff --check main...HEAD` pass.
- [ ] Leave the dependent branch unmerged for architecture review. After
      acceptance, merge the whole stack as one unit and run WO-095's pending
      two-machine search QA with both machines on key scheme 2.

## Do not

- Do not change the continuous query grid, local matcher, union rule,
  target-plus-saturation matrix, or progress UI.
- Do not add a catalogue outcome which can accidentally outrank `complete`;
  budget is a job stop cause, not a stronger provider answer.
- Do not expose query, token, prefix, candidate, title, or peer identifiers in
  diagnostics.
- Do not narrow a catalogue request or stop its broad response after the wanted
  candidate appears.
- Do not serialize the whole search, remove the node-wide response ceiling, or
  persist job/retry state.
- Do not change Level 1 or contribution consent.

## Stop conditions for the implementer

Return to architecture review if the correction appears to require a wire or
key-scheme revision, a narrow candidate lookup, persistent search state, or a
contribution-policy change.


---

## Implementation note (Sr Dev, 2026-08-13)

All three were real.

| Gap | Correction |
|---|---|
| 1 budget swallowed as unavailable | `ErrSearchBudget` is removed from `classifyPagedError` entirely — callers test it and short-circuit first. `fetchCataloguePagesFrom` returns it unranked, and both the provider-discovery loop and the known-peer fallback return immediately on it; the fallback also checks the context before it starts and on every iteration |
| 2 local read failure proved absence | `resolveTitles` returns an EMPTY `settled` map on either local failure, so no candidate can become `absent` on evidence that was never gathered; the response reports `unresolved` and the candidate stays retryable |
| 3 cancellation lost a race to a refund | `reserve` checks `ctx.Err()` at the top of the loop, so a waiter woken by a settlement observes the cancelled context before touching `reserved` |

Three notes for the reviewer:

1. **The classifier no longer has a budget branch at all**, rather than having a
   corrected one. Giving budget termination *any* `catalogueOutcome` invites the
   ranking that §1 forbids, so it is expressed only as an error and the
   accompanying `Outcome` is the floor value purely so it cannot outrank
   anything.

2. **I had again written a test that required the wrong answer.**
   `TestPagedErrorsAreClassified` asserted `ErrSearchBudget → catalogueUnavailable`,
   directly contradicting the comment above the function it was testing. The
   case is deleted and replaced by
   `TestBudgetShortCircuitsTheCatalogueTraversal`, which drives real exhaustion
   through `requestPaged`, `fetchCataloguePagesFrom` and the prefix traversal.

3. **The malformed/silent distinction is now driven through the transport**, by
   overriding the server's stream handler — a peer that sends unframeable bytes
   returns `catalogueInvalid`, one that accepts the stream and says nothing
   returns `catalogueUnavailable`. The previous test called the classifier
   directly and proved nothing about the path.

`git diff --check main...HEAD` is clean. Not done, and not code: WO-095's
two-machine live QA. The stack — WO-097, WO-095, WO-099, WO-100, WO-101,
WO-102 — is on `wo-097-distributed-search-foundation`, unmerged.
