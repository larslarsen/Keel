# WO-099 — Close streaming-search lifecycle and resource-accounting gaps

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Done** 2026-08-13 — every code acceptance box is covered by a test; the live-QA line stays open |
| **Date** | 2026-08-13 |
| **Depends on** | WO-095 implementation at `4455f76` |
| **Source** | Architecture review of the completed WO-095 implementation |

## Outcome

Keep WO-095's protocol and interface. Correct the places where its asynchronous
implementation can lose visible progress, misreport cancellation, exceed the
search budget, or permanently discard a candidate after a transient catalogue
failure.

This is a correctness follow-up, not a redesign. Do not change query semantics,
the key scheme, contribution entitlements, bar meanings, or the public search
interface.

## Confirmed gaps

### 1. Two pages on one native connection cancel each other

`bridgeSession.startJob()` cancels every existing job in the native session
before registering the new one. That is not the page-level rule in WO-095.
Every search page in one browser profile shares the service worker's native
connection, so a search opened in page B currently cancels page A even though
the extension router has separate Ports and routes for them.

Allow distinct `search_id`s on one negotiated native session to run
concurrently. Replacement remains a page decision: the same page Port cancels
its previous id before claiming another. Session teardown still cancels every
job belonging to that native connection.

Keep resource use bounded without restoring cross-page cancellation:

- use a named finite active-job limit and return a visible `search_busy`
  refusal before starting excess work; and
- make the four-response semaphore node/owner-wide, not one fresh semaphore per
  job, so multiple pages share the same total peer-load ceiling while each can
  still make progress.

Do not broadcast search events or add a browser-persisted page identifier. The
existing `(native session, search_id)` ownership plus the extension's Port
route is sufficient.

### 2. The start acknowledgement can erase earlier events

The page claims its route before `PEER_SEARCH`, correctly allowing daemon events
to arrive before the request promise resumes. It initially renders the daemon's
plan with unknown targets. When `PEER_SEARCH_STARTED` arrives, it calls
`renderPlan()` again. Any word count or token phase already applied to the first
render is reset.

Create the render state once. Applying the frozen targets from
`PEER_SEARCH_STARTED` must update the existing word entries and repaint them in
place without replacing DOM rows or resetting:

- word counts;
- token phase/cycle state;
- the last accepted sequence;
- streamed results; or
- terminal text.

An event is allowed to paint against an unknown target briefly; the later
acknowledgement supplies the denominator without rolling the numerator back.
If a terminal event wins the race, the late acknowledgement is inert.

### 3. A contribution downgrade is not a successful completion

`runTokenWork()` notices `MayDistributedSearch() == false` between responses,
but that reason remains local to the token worker. The job context is not
cancelled and `handlePeerSearchStart()` can consequently emit
`PEER_SEARCH_COMPLETE`.

A Level-2-to-Level-1 transition must promptly cancel queued and in-flight
distributed-search work and terminate each affected job with
`PEER_SEARCH_CANCELLED`, carrying a bounded machine reason such as
`contribution_downgrade`. The gate must still close before teardown. Do not
depend only on polling between peer requests.

Use one cancellation signal owned by the running swarm/node lifecycle or the
search-job coordinator, and make the terminal mapper distinguish:

- explicit page replacement/cancel;
- Port/native-session loss;
- contribution downgrade or consent withdrawal;
- owner/node shutdown;
- budget exhaustion; and
- genuine search failure.

Only the first four are cancellation. Budget exhaustion remains a visibly
incomplete successful terminal state; a protocol/verification error remains a
failure.

### 4. The aggregate budget counts only shard bytes

The current counter adds bytes returned by `fetchShardPagesCounted()`. Catalogue
string buckets fetched by `ResolveCandidateTitles()` are not charged even
though they can dominate a search's network and cache cost. The four-response
semaphore is also released before catalogue resolution, allowing every token
worker to start catalogue downloads outside the advertised bound.

The job budget must meter all paged payload bytes caused by the search:

- token-shard pages and terminals; and
- complete broad catalogue/string buckets used to resolve its candidates.

Meter bytes at the shared paged-reader boundary so rejected, malformed, and
partial replies still cost what was actually read. When the shared budget is
exhausted, cancel remaining network reads and finish with reason `budget`.
Bounded in-flight reads may consume a small explicitly tested reservation, but
workers may not independently observe the same remaining balance and each spend
it in full.

Keep the four-response permit through the logical response's candidate
resolution and local title checks. At no time may moving string resolution out
of the shard phase create more than four search-caused network responses in
flight.

Coalesce catalogue prefix work across the whole job, not only one call: two
concurrent token workers that nominate ids in the same missing prefix must join
one fetch. The request remains the complete broad prefix bucket.

### 5. A transient title-resolution failure must remain retryable

`creditResponse()` marks a candidate `checked` before its missing catalogue
bucket is resolved. If that fetch fails or returns no title, later nominations
skip the id permanently. The token accumulator also reports an id as newly
pending only once, so the same token cannot recover it on a later peer cycle.

Separate these states:

- resolving now;
- resolved and locally checked; and
- unresolved/retryable.

Only a candidate with a locally available title becomes checked. Concurrent
nominations join the in-flight prefix resolution. A failed/incomplete prefix
returns its candidates to retryable state, with retry attempts still bounded by
the provider list and aggregate resource budget. It must not create a narrow
per-video request.

### 6. Search diagnostics must not inherit identifier-rich catalogue logs

The shared catalogue fetcher currently logs catalogue prefixes and peer ids on
rejection. WO-095 permits only one aggregate terminal diagnostic and forbids
peer/result identifiers in search diagnostics.

The search path must use identifier-free errors and logs. A failure may record
only bounded reason/category and aggregate counts. Do not log a query, token,
shard, catalogue prefix, title, video id, or peer id. Add a captured-logger test
covering failed shard and catalogue responses.

### 7. Validate the revision-3 job id completely

`search_id` currently has only a maximum-length check. Reject an empty or
malformed id before target lookup or peer contact. Revision 3 may require the
canonical UUID shape generated by `crypto.randomUUID()`. Reject a duplicate
live id on the same native session rather than silently replacing or rerouting
another page's job.

## Acceptance

- [x] Two search-page Ports sharing one native session can run different ids at
      the same time; starting page B neither cancels nor reroutes page A.
- [x] Replacing a search cancels only the prior id owned by that page, while
      native-session teardown cancels all of that session's remaining jobs.
- [x] Active jobs have a named bound and excess starts receive `search_busy`;
      all jobs on the node share one four-response peer-load ceiling.
- [x] A progress event delivered before `PEER_SEARCH_STARTED` survives target
      application: its count, token cycle, result list, and sequence do not
      reset.
- [x] A terminal event delivered before the acknowledgement remains terminal;
      the late acknowledgement cannot recreate active state.
- [x] A live Level-2-to-Level-1 downgrade cancels queued and in-flight work and
      emits `PEER_SEARCH_CANCELLED`, never `COMPLETE` or `FAILED`.
- [x] Consent withdrawal and node/owner teardown obey the same prompt
      cancellation boundary without allowing another peer request.
- [x] One job never has more than four search-caused logical responses in
      flight, including catalogue resolution.
- [x] The aggregate meter includes shard and catalogue bytes, including bytes
      read from rejected responses, and budget exhaustion cancels outstanding
      reads with a visible `budget` terminal state.
- [x] Concurrent candidates sharing a catalogue prefix cause one complete broad
      prefix traversal, not duplicate or per-video fetches.
- [x] A candidate whose first catalogue resolution fails can succeed after a
      later eligible response; it is neither lost nor counted before its title
      is locally checked.
- [x] Captured search logs contain no query, token, shard, catalogue prefix,
      title, video id, or peer id on success or failure.
- [x] Empty, malformed, and duplicate live `search_id`s fail before peer
      contact; ordinary `crypto.randomUUID()` ids continue to work.
- [x] Existing revision-2 atomic search, revision-3 wire shapes, local-result
      precedence, target/saturation matrix, and privacy tests remain green.
- [ ] After the automated correction, perform WO-095's pending two-machine live
      QA with both machines on key scheme 2.

## Do not

- Do not serialize all token work to solve accounting.
- Do not restore one-job-per-native-session behavior as the resource bound.
- Do not narrow catalogue requests to candidate ids.
- Do not turn the byte budget back into a whole-query time limit.
- Do not persist jobs, query text, progress, or retry state.
- Do not change Level 1 or let a downgraded job finish its current request by
  policy.
- Do not add new UI controls or search modes.

## Stop conditions for the implementer

Return to architecture review if the correction appears to require changing
the prefix-broadness privacy rule, the scheme-2 index, contribution-level
entitlements, or the meaning of either progress bar.

---

## Implementation note (Sr Dev, 2026-08-13)

Every gap was real. Where the corrections are:

| Gap | Correction |
|---|---|
| 1 cross-page cancellation | `bridgeSession.startJob` no longer cancels siblings; `liveSearches` registry with `maxActiveSearchJobs` and a `search_busy` refusal; `Node.searchSem` replaces the per-job semaphore |
| 2 acknowledgement erases progress | `renderPlan` runs once; new `applyTargets` updates word entries in place and is inert once a terminal has cleared `active` |
| 3 downgrade reported as completion | `searchJob.stop(reason)` + `stopDistributedSearches`, called from `downgrade`, `stopForWithdrawnConsent` and `stopAll`; terminal mapper separates cancellation from budget (incomplete success) and failure; `PeerSearchCancelledPayload.Reason` |
| 4 budget missed catalogue bytes | `budgetMeter` charged inside `requestPaged`, so shard *and* catalogue *and* rejected reads all count; permit now held through candidate resolution; `prefixGroup` coalesces traversals job-wide |
| 5 candidates lost on transient failure | three states — `checked` / `resolving` / retryable — and `creditResponse` re-nominates from `acc.ids()` each cycle |
| 6 identifier-rich logs | `fetchCataloguePrefixQuiet` for the search path; captured-logger test |
| 7 weak id validation | canonical UUID pattern, duplicate-live rejection, both before target lookup or peer contact |

Two notes for the reviewer:

1. **I had written a test asserting the wrong behaviour.**
   `TestStartingAJobCancelsTheOneItReplaces` encoded gap 1 as if it were the
   requirement. It is replaced by
   `TestConcurrentPagesOnOneSessionDoNotCancelEachOther`, which states the
   corrected rule and why the old reading was wrong — the mistake was reading
   WO-095 §4's "a page owns at most one active job" as a *session* rule when
   every page in a profile shares one native connection.

2. **The budget is metered at `requestPaged`, not at its callers.** That is what
   makes rejected and malformed responses cost what they actually read; a budget
   charging only accepted responses is one a hostile peer walks through by
   sending garbage. Exhaustion cancels the job's context rather than setting a
   flag, because four workers each checking a shared remaining balance can each
   see room for one more and each spend it.

Not done, and not code: WO-095's two-machine live QA, with both machines on key
scheme 2.
