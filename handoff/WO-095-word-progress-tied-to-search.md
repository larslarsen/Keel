# WO-095 — Stream distributed search into a responsive search UI

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Accepted** 2026-08-13 — WO-099/100/101/102 closed review findings; two-machine key-scheme-2 live QA pending |
| **Date** | 2026-08-13 |
| **Depends on** | WO-097 — complete distributed-search index, pagination, and retained word targets |
| **Source** | Lars's distributed-search design, clarified against live `world` / `wor` / `ld ` QA |
| **Amends** | WO-068's static word display and WO-070's one-shot peer search |
| **Folds in** | WO-096's live starvation diagnosis |

## Outcome

Distributed search becomes a cancellable background job owned by the daemon.
Local results appear immediately. Peer work runs in bounded parallel and emits
progress while it is happening. Each locally verified network result appears as
soon as its title proves the complete query, without waiting for unrelated
tokens or peers.

The UI shows two deliberately different signals:

- colored token bars animate the broad peer responses being downloaded; and
- one word bar counts how many distinct candidates from this search have been
  confirmed to contain that word, against the frozen global estimate supplied
  by WO-097.

This completes an existing feature. It does not change contribution
entitlements, privacy policy, browser permissions, or the matching key scheme
defined by WO-097.

## Settled language

Keep these concepts separate in code, UI copy, and review:

- A **token shard** maps a broad hashed three-character-token bucket to
  candidate video ids.
- A **catalogue/string bucket** supplies public title strings for candidate
  ids. It is a different prefix namespace and payload from graph data and token
  shards.
- A **word target** is an approximate global count from the retained word
  telemetry snapshot. It is neither a download percentage nor an absolute
  stopping ceiling.
- A **token bar** is a schematic animation of peer-response work. It is not a
  count, byte meter, or coverage estimate.
- A **word bar** is completion/yield for this search. It does not count random
  titles that happened to arrive as cover traffic in a broad string bucket.

Do not introduce names such as “broad request only” that hide which dataset is
being requested.

## Confirmed defect in the running owner

The Level-2 owner was connected to one Keel peer and had published all 256
shard keys. Direct probes gave:

| Probe | Elapsed | Result |
|---|---:|---|
| `world` | 6.020 s | `wor` fetched 18; `ld ` fetched 0 |
| `wor` | 6.019 s | fetched 13 and returned 13 hits |
| `ld` | 6.016 s | its own bounded shard search ran |

`handlePeerSearchContext()` puts one six-second deadline over the whole query,
while `PeerSearch()` fetches tokens sequentially. The first provider walk can
consume the deadline before a later token starts. The daemon then sends one
final response and the page silently catches failures.

Parallel workers alone do not repair the product: a one-shot reply would still
make every bar and result appear atomically at the end.

## 1. Use WO-097's query and render plan

The daemon's normal local `SEARCH_RESULT` carries WO-097's canonical query
plan, even when peer search is disabled or unavailable. The extension never
retokenizes the query.

The render plan contains:

- normalized display words in order, with stable opaque `word_id`s;
- fixed non-overlapping token occurrences in continuous character order;
- an opaque `token_id` and `color_slot`, shared by repeated occurrences of the
  same token value;
- the word fragments intersected by each token's character range; and
- the deterministic word under which each token bar is placed.

Repeated display occurrences of the same normalized word share one word target
and cumulative count. A video can advance that shared word only once.

Token ranges are constructed before word layout. A token crossing a space
therefore colors parts of both words. Place its one bar under the first word
whose letters it covers; if it begins in a space and covers letters only in the
next word, place it under that next word. The placement has no search meaning.

The normalized display words cross only the local native bridge. They must not
be logged, persisted, broadcast to another page, or placed in browser storage.

## 2. Query semantics stay local

The peer index is candidate discovery, not the final matcher.

For the current interface:

- unquoted normalized words are all required, in any order;
- quoted text is an exact adjacent normalized phrase;
- word matching respects normalized word boundaries (`world` matches
  `the world today` or `world-star`, not `worldwide`); and
- stopwords remain part of the final local match even though WO-097 may omit
  stopword-only discovery tokens.

Keep the request/query-plan shape extensible so a later UI can expose exact
versus any-order modes without changing the network index. Do not add that
control in this order.

Candidate sets from distinct token shards are **unioned**. Never discard an id
because another token shard did not contain it, and never wait for all token
sets to form a network intersection. Missing shard membership is not proof that
the title fails the query.

For each candidate id whose title is absent locally, obtain the complete broad
catalogue/string prefix bucket through WO-097's paged path. Coalesce candidates
that share a string prefix. All rows in the broad response may be cached under
the existing public-catalogue rules, but only candidate ids produced by this
search enter its matcher or counters.

The daemon applies the complete query to the resolved title. A passing result
is definitive and is emitted immediately; it is never speculative or later
retracted. A candidate that matches only one word can advance that word's bar
without becoming a final result.

## 3. Asynchronous, session-scoped job

Negotiate the streaming contract as `peer_search:3`.

`PEER_SEARCH` validates bounds, the effective Level-2+ entitlement,
capability revision, `search_id`, and presentation limit. It creates the job
and promptly replies `PEER_SEARCH_STARTED`; it does not hold the native request
open for the search.

The initiating authenticated owner/native session subsequently receives only
its own events:

- `PEER_SEARCH_PROGRESS` — token response state;
- `PEER_SEARCH_WORD_PROGRESS` — confirmed per-word counts;
- `PEER_SEARCH_RESULT` — one newly verified result;
- `PEER_SEARCH_COMPLETE`;
- `PEER_SEARCH_CANCELLED`; or
- `PEER_SEARCH_FAILED`.

Every event carries `search_id` and a monotonically increasing `seq`. Envelope
ids use a reserved event prefix and cannot resolve an unrelated native request.
A job failure uses `PEER_SEARCH_FAILED`, not an unsolicited generic `ERROR`.

Revision 2 retains its atomic response behavior and receives no revision-3
events. A revision-3 extension may fall back visibly to revision 2, but must not
fabricate streaming progress.

## 4. Page orchestration and cancellation

Each submission gets a fresh `search_id` from `crypto.randomUUID()` and
replaces the page's prior job.

1. Cancel the prior job and mark the new page generation active.
2. Issue local `SEARCH`; render its local hits and query/render plan immediately.
3. If network search is selected and entitled, start `PEER_SEARCH` as soon as
   the plan is available. Do not issue or await a fresh global-stat network
   request: the start snapshot comes from WO-097's retained cache.
4. Apply only events whose `search_id`, `seq`, and page generation are current.

The search page owns a named runtime Port. A small
`background/search_sessions.js` module owns Port-to-search routing; `sw.js`
only composes it. Install the route before sending the start request so an early
event cannot race the acknowledgement. Search events are never published
through the owner-wide contribution-status hub.

Cancel on replacement, explicit cancel, switching network search off, Port
close, native disconnect, owner shutdown, or contribution downgrade below
Level 2. Cancellation stops queued and running work. One page owns at most one
active distributed job, entirely in bounded memory. Nothing is restored from
browser storage after page closure or service-worker eviction.

## 5. Bounded parallel network work

- Refuse more than 16 distinct discovery tokens before peer contact.
- Initially run at most four token/peer responses concurrently. Keep the limit
  a named daemon constant so later measurement can tune it.
- Each response has its own network deadline. Never place one deadline over the
  whole query or make later tokens inherit elapsed time from earlier ones.
- The disk/network budget slider is the job's hard aggregate backstop. Do not
  add a fixed 24-second whole-query cutoff that can contradict target and
  saturation rules.
- Repeated token occurrences share one fetch state.
- Preserve serving limits, pack signatures, poisoning resolution, and the
  current yield-vector top-half eligibility screen.

Within the eligible peer set, prefer a peer not already used by another token
in this search and randomize selection rather than always choosing the largest
yield. The same peer may answer multiple tokens when alternatives are
insufficient. Broad token-prefix hashing supplies privacy; peer diversity is a
load-spreading preference, not a correctness or privacy requirement.

WO-097 may deliver several bounded pages inside one logical peer response. That
is still one token-bar cycle: do not expose page internals as new query tokens.

## 6. Token bars show peer-response activity

There is one colored reusable bar for each displayed token occurrence; repeated
values share state and color. Bars remain in query order.

Whenever work for a token starts against another peer:

1. reset that token's bar to 0;
2. animate it schematically while the response is pending; and
3. snap it to 100 when the logical response terminates and validates.

If the planner tries another peer for the same token, reuse and reset the same
bar. The animation need not estimate bytes or real completion. An empty valid
response still completes visibly. Distinguish queued, active, completed,
cancelled, and failed states without pretending that bar length measures
corpus coverage.

Progress events need only opaque `token_id`, response cycle, phase, and terminal
reason. Coalesce animation updates; deliver start and terminal transitions
immediately. Never put token text, shard id, peer identity, title, or video id
in a progress event.

## 7. Word bars show search completion

At job start, snapshot each distinct non-stopword query word's current global target
from WO-097. Freeze it for that job. A later telemetry refresh affects the next
search only.

The numerator is the number of distinct candidate video ids from this search
whose resolved title locally confirms that word. Count a video once per word,
even if the title repeats the word. Do not count unrelated titles delivered in
the same broad string bucket.

Update the word bar immediately after each title is resolved and checked:

- show `found / ~target` when the target is known;
- allow the count and fill to exceed 100%, preserving the target marker at
  100%;
- when unknown, show the found count plus `target unknown` and no fake marker;
- do not render target/completion bars for stopwords.

For `red world`, a candidate containing only `red` advances only red. A
candidate containing both advances both and, if it satisfies the entire query,
also streams as a result. These un-intersected per-word counts are intentionally
compared with un-intersected per-word global estimates.

## 8. Saturation and stopping

Saturation is measured only after every candidate being credited to a response
has had its missing catalogue/string bucket resolved and its title checked.
A token-shard response is not zero-gain merely because its strings are still in
flight.

For a word with a known frozen target:

- below target and saturated: **keep going**;
- at or above target while valid new matches still arrive: **keep going**;
- stop work useful only to that word when it is at or above target **and then
  saturates**.

For an unknown target, use the existing bounded saturation fallback. Provider
exhaustion or the hard disk/network budget may end a word incomplete.

When one word completes, stop peer work useful only to that word and continue
work for incomplete words. A boundary token relevant to two words remains
eligible while either word is incomplete. The presentation result cap does not
terminate discovery or word counting; the resource budget does.

## 9. Results and visible states

Deduplicate results by `video_id`. Keep the local row and local provenance when
the same id arrives from the network; append network-only rows in stable arrival
order. Local rows survive empty peer results, unavailability, cancellation, and
failure.

Required network states are:

- not selected;
- contribution level required;
- daemon/swarm unavailable;
- no peers yet;
- searching;
- completed, including whether the target was met;
- cancelled/replaced; and
- failed while local results remain visible.

Do not silently turn failure or budget exhaustion into an empty successful
search.

## 10. Privacy and diagnostics

- Never store a raw search query in browser storage, SQLite, cache files, or a
  durable job journal (`DESIGN_v2.md` §2.1 and §4.2).
- Never log the raw/normalized query, token strings, shard ids, titles, result
  ids, peer ids, or event payloads.
- Do not add gossip or alter Level-1/Level-2 policy.

One aggregate terminal diagnostic may contain only search id, elapsed
milliseconds, word/token counts, response count, terminal state, and result
count.

## Implementation boundaries

| Area | Owner |
|---|---|
| Query/render-plan consumption and local full-query matcher | daemon store/search layer |
| Job lifecycle and RPC/event payloads | `daemon/main.go`, `daemon/bridge` |
| Parallel planning, peer selection, saturation | `daemon/swarm` |
| Candidate string resolution | daemon catalogue/swarm layer |
| Per-session delivery and cancellation | daemon bridge/owner session |
| Unsolicited native event dispatch | `extension/lib/native.js` |
| Port/search routing | new `extension/background/search_sessions.js`, wired by `sw.js` |
| Rendering and stale-generation guard | extension search page modules |

## Acceptance

- [ ] `world` starts independent work for `wor` and `ld `; neither inherits the
      other's elapsed deadline and no more than four responses—including
      catalogue resolution—run at once. **Shard concurrency is covered; WO-099
      must bring catalogue work under the same bound.**
- [x] Candidate sets are unioned. A title discovered through any one token can
      stream once the local matcher proves the whole query.
- [ ] Missing titles arrive only through complete broad catalogue/string
      buckets; cover rows never enter this search's counters or result matcher.
      **WO-100 must preserve authenticated complete/incomplete state through
      candidate resolution.**
- [x] A verified network result appears before unrelated token work ends and is
      never retracted.
- [x] Token bars animate/reset once per logical peer response and do not claim
      count, bytes, or target coverage.
- [x] A cross-word token colors both word fragments and has one deterministic
      bar placement. Repeated token values share color and live state.
- [x] Word bars count distinct locally checked candidates, update live, can
      exceed 100%, and show `target unknown` without a fake marker.
- [ ] Tests prove the stop matrix: below+saturated continues; above+productive
      continues; above+saturated stops; hard budget and exhaustion end visibly
      incomplete. **The matrix passes; WO-099 must charge catalogue bytes to
      the hard budget.**
- [ ] Saturation cannot be declared before candidate string resolution finishes.
      **WO-100 adds the join barrier for a candidate concurrently resolving in
      another worker.**
- [x] Peer diversity is preferred within the eligible yield set but lack of a
      second peer never breaks correctness.
- [ ] Start acknowledgement is prompt; two sessions never receive one another's
      events; replacement and every listed lifecycle transition cancel the
      correct job. **WO-099 covers multiple page jobs sharing one native
      session, acknowledgement/event ordering, and contribution-downgrade
      cancellation.**
- [x] Local results survive visible peer unavailability, cancellation, empty
      response, budget exhaustion, and failure.
- [x] Raw query/token text reaches no browser storage, persistence, progress
      envelope, or log line.
- [ ] Search diagnostics contain no peer, shard, catalogue-prefix, title, or
      result identifiers. **WO-099 removes inherited identifier-rich catalogue
      logging from this path.**
- [ ] Two-machine live QA visibly advances token-response cycles and word counts
      while results arrive, rather than painting one final snapshot.

## Do not

- Do not intersect token-shard candidate sets.
- Do not wait for all tokens before emitting a locally verified result.
- Do not count every title in a broad string bucket.
- Do not use token bars as coverage meters or word bars as download meters.
- Do not stop merely because a target was reached or merely because work
  saturated below target.
- Do not wait for a telemetry network fetch before starting search.
- Do not keep one native RPC open for the job or use one query-wide deadline.
- Do not retokenize, color, or calculate search counts in the extension.
- Do not add a new matching checkbox, permission, dependency, or storage owner.
- Do not broadcast page-search events through the owner-wide hub.
- Do not log or persist raw queries.

## Stop conditions for the implementer

Return to architecture review if implementation appears to require:

- a raw query or token string in persistence, logs, or progress events;
- browser storage for job recovery;
- a narrower-than-WO-097 catalogue/string request;
- result retraction;
- a MAIN-world script, new permission, framework, bundler, or runtime dependency;
- contribution entitlement changes; or
- a change to WO-097's key scheme, pagination, targets, or query semantics.

---

## Implementation note (Sr Dev, 2026-08-13)

Landed as specified. No stop condition was hit. Where the work is:

| Requirement | Where |
|---|---|
| §1 render plan on every local search | `planWire` in `daemon/searchjob.go`, `SEARCH_RESULT.plan` |
| §2 union, broad string resolution, local matcher | `daemon/swarm/search.go` `creditResponse` |
| §3 job lifecycle, `peer_search:3`, event payloads | `daemon/searchjob.go`, `daemon/bridge/` |
| §4 page orchestration, Port routing, cancellation | `extension/background/search_sessions.js`, `extension/page/search_stream.js` |
| §5 bounded parallel work, peer diversity | `StreamingSearch`, `runTokenWork`, `orderPeers` |
| §6 token bars | `PeerSearchProgressPayload` phases + `paintToken` |
| §7 word bars | `PeerSearchWordProgressPayload` + `paintWord` |
| §8 saturation and stopping | `wordDone`, `tokenSatisfied`, `recordSaturation` |
| §9 results and visible states | `appendResult`, `completionText` |

Four things worth flagging to the architect:

1. **A latent framing bug had to be fixed first, and it was not in this
   order's scope.** `bridge.WriteMessage` wrote the 4-byte length prefix and
   the payload as two separate `Write` calls. With one writer that is
   invisible; a streaming job emits from concurrent token workers, and
   `main.go`'s `syncWriter` only serializes each call — so `lenA, lenB,
   payloadA, payloadB` was reachable, which corrupts the stream *permanently*
   rather than losing one message, because every later frame is then read at
   the wrong offset. `WriteMessage` now builds the frame and writes it once.
   `TestEventSequenceIsMonotonicUnderConcurrency` found it; it was a real
   pre-existing hazard on the WO-070 goroutine path too, just much harder to
   hit.

2. **`emit` holds one lock across sequence assignment *and* the write.** The
   client discards any event not ahead of the last sequence it applied — that
   is what makes a replaced job's late events harmless — so an event that
   reaches the wire out of sequence order is not reordered, it is *dropped*,
   and the symptom is a bar that silently stops. Assigning under one lock and
   writing outside it would do exactly that under load.

3. **The presentation cap bounds streaming, not discovery.** §8 says the result
   cap must not terminate discovery or word counting, so the job keeps two
   counters (`matched`, `streamed`) and only the second respects `limit`. Word
   bars therefore keep moving after the visible list is full, which is the
   intended behaviour and will look odd to anyone expecting them to stop.

4. **An explicitly incomplete peer response does not feed the saturation
   streak** (carried over from WO-097's equivalent decision). A peer that
   stopped on its own budget still holds rows, so counting it as a miss would
   let rate-limiting read as "the network is exhausted".

The extension no longer tokenizes, colours or counts anything.
`test/word-corpus-coloring.test.js` was rewritten rather than deleted: its old
assertion pinned the page's own three-character chopping, which WO-097 made
impossible to keep (a scheme-2 token can straddle a space and belong to two
words), so the successor property is that the page paints exactly what the plan
says — including the cross-word case the old chopping could not express.

Two-machine live QA remains the only open acceptance line, and it needs both
machines on the new build for WO-097's key-scheme reason.
