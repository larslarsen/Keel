# WO-085 — Enforce peer-search reciprocity and contribution-level incentives

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Done 2026-08-12** — see the implementation record below |
| **Date** | 2026-08-11 |
| **Source** | Lars architecture clarification, 2026-08-11 |

## Fixed level semantics

These are decided and are not part of the open question:

- **Level 1 — Personal:** no graph/catalogue/search block service. Live notices
  and whole word HLL/CMS exchange remain explicit narrow outbound products.
- **Level 2 — Broad sharing:** publishes public broad buckets of locally
  produced and cached graph information. The complete bucket/shard is the
  privacy unit; see WO-084.
- **Level 3 — STAR:** contributes edges through STAR anonymity and earns access
  to cohort/funnel views computed from that protected population. The intended
  product is a strong visual comparison of other people's funnels/cohorts.
- **Level 4 — attributed transparency:** deliberately public attribution to the
  contributor, principally for researchers, journalists and auditors. It needs
  no artificial reward.

## Capacity problem

The present contract gives Level 1 unrestricted distributed peer search while
Level 1 serves no search shards. If most users stay at the default and only a
few choose Level 2, those few machines can become the search backend for the
entire network. Voluntary contribution plus unlimited consumption has no
capacity equilibrium.

This is especially acute for search rather than ordinary graph pre-walk:
queries are arbitrary, user-triggered and repeatable, while the seed and graph
cache bound much of the normal suggestion path. Three Level-2 nodes should not
be expected to host every Level-1 user's searches.

UI gating cannot stop a modified client, so this is an honest-client product
contract, not an anti-abuse security boundary. Every serving node still needs
concurrency, byte and request-rate limits.

## Recommendation

Adopt reciprocal distributed search:

| Capability | Level 1 | Level 2+ |
|---|---|---|
| Local search over this device | On | On |
| Seed consumption, graph fetch/pre-walk and shared suggestions | On | On |
| Live feed and word statistics | On | On |
| “Search other people's recommendations” | Off, with an explanation | On |
| Serve broad graph/catalogue/search buckets | Off | On |

This keeps the core recommendation product and privacy inspection available at
Level 1 while putting the most open-ended network cost behind the level that
also supplies that cost. It is a reciprocal capability, not an unrelated perk:
Level 2 can search the public graph because Level 2 also hosts public graph and
search shards.

Do not describe Level 1 as crippled or offline. Its local search, shared
suggestions, seed, pre-walk, Live and global word statistics still work. The one
disabled control must say why: distributed search needs contributors able to
answer other searches.

## Additional Level-2 feedback (separate delivery)

Give Level 2 a contribution-impact view using only local aggregate counters:

- broad graph blocks and catalogue/search entries currently eligible to serve;
- novel blocks/edges learned from peers versus contributed locally;
- buckets announced, requests answered and bytes served; and
- network reach gained after enabling broad sharing.

This is not a privacy-sensitive request log. Do not retain remote queries,
peer trajectories or per-peer histories. Search requests already name only a
broad shard; aggregate counters are sufficient. WO-086 owns this view; it is
not required to land the reciprocal-search boundary safely.

## Alternatives considered

### Keep full distributed search at Level 1

Possible only with a capacity source. Options are a Keel-operated service,
sponsors, or making Level 1 re-serve fetched public search shards. The first two
change the no-server architecture. The third makes Level 1 a bandwidth
contributor and contradicts its current “serves no graph/catalogue/search data”
boundary unless that boundary is deliberately changed.

### Level-1 quotas or lower priority

This delays overload rather than fixing reciprocity and is trivial for a
modified client to bypass. Per-node limits are still required for safety, but
they are not a contribution model.

### Upload credits/proof of service

Rejected for now. Credits add accounting, identity and Sybil pressure precisely
where Keel is trying to avoid durable user identities. The simple level gate is
auditable and understandable.

## Decision — 2026-08-11, Lars

- [x] **Selected:** distributed peer search is Level 2+, while Level 1 keeps
      local search and shared graph suggestions/pre-walk.
- [ ] Keep distributed peer search at Level 1 and explicitly choose its serving
      capacity source.
- [ ] Change Level 1 so it re-serves fetched public search shards, without
      publishing locally derived graph/search material.

The selected boundary is normative. The alternatives remain only as the
decision record.

## Required implementation

Graphify's refreshed code graph traces this change through
`handlePeerSearchContext`/`handlePeerSearch`, `swarm.Node.PeerSearch`,
`PolicyForLevel`, the contribution supervisor/status broadcasts, the service
worker and side-panel control. Keep those as one negotiated behavior change.

1. Add a distinct distributed-search entitlement to the daemon's effective
   contribution capabilities; do not overload ordinary `Fetch`, because Level
   1 still needs fetch for seed/suggestions/pre-walk, and do not gate only the
   checkbox in JavaScript.
2. Return a typed `contribution_required` result for `PEER_SEARCH` at Level 1.
   Local `SEARCH`, `SUGGEST`, pre-walk, Live and `WORD_STATS` remain available.
3. Negotiate the capability through WO-081 so old extension/daemon pairs do not
   disagree about whether the control should exist.
4. Disable the peer-search checkbox at Level 1 with concise reciprocal-capacity
   copy and a direct route to the contribution setting.
5. Add per-node concurrency/byte/rate limits independent of contribution level.
6. Test runtime 1↔2 transitions: an open search UI changes immediately across
   every browser session through WO-079's status broadcast.
7. Split the existing `TestLevelOneStillFetchesAndSearches` assertion: Level 1
   still fetches for permitted consumer paths, but distributed `PEER_SEARCH`
   is rejected before peer contact. Keep direct swarm-search tests at a Level-2
   policy so they continue to test the transport rather than the entitlement.
   *Done: it is now `TestLevelOneStillFetchesAndPreWalks`, and the six direct
   search/shard transport tests run at Level 2. Two of them additionally turn
   `JoinSearchTelemetry` off, because they assert a client that has heard
   nothing about a token and a subscribed Level-2 node receives the server's
   sketch gossip inside their own polling window.*

## Acceptance after implementation

- [x] Level 1 local search, suggestions, seed/pre-walk, Live and word statistics
      remain functional.
      `TestLevelOneStillFetchesAndPreWalks` (fetch + the local walk it feeds),
      `TestLevelOneStillAnswersWordTelemetry`, `TestLevelOneJoinsNoSearchTopics`
      (Live on), and the local-search assertion in
      `TestLevelOnePeerSearchIsRefusedNotEmptied`.
- [x] Level 1 `PEER_SEARCH` returns `contribution_required` without contacting a
      peer; Level 2 succeeds and serves the same class of broad shards it uses.
      `TestLevelOnePeerSearchIsRefusedNotEmptied` (RPC shape, code and detail)
      and `TestLevelOneDistributedSearchIsRefusedBeforePeerContact`, which
      refuses against an *already connected* peer holding the match and then
      finds it from a Level-2 node over the same transport.
- [x] Changing contribution level updates the control in all connected browser
      profiles without a restart.
      `TestRuntimeLevelChangeFlipsTheSearchEntitlement` drives a real 1→2→1
      transition; `test/search-entitlement.test.js` drives the page's real
      `CONTRIBUTION_STATUS` listener both ways with no reload.
- [x] Load tests prove serving limits bound concurrent searches and bytes even
      for a modified client.
      `TestServingLimitsBoundAModifiedClient` runs 32 unpaced parallel
      requesters against a real serving node for 5s: ~22 answered against
      ~127k refused, no leaked slots, bytes inside budget. The unit tests
      alongside it pin each bound separately, including that a refusal spends
      no token (so a brief overrun is not a lockout).
      **Still wanted:** a two-machine run over a real uplink; loopback cannot
      show what the byte budget feels like on a domestic connection.
- [x] No query, per-peer trajectory or remote-request history is persisted.
      Nothing was added to SQLite. The limiter's per-peer state is transient,
      in-memory, and holds only a token count, an in-flight count and a
      last-seen instant — no query, no shard number, no sequence — and idle
      entries are swept. That is the minimum a per-peer rate limit can hold; it
      is not a request log and it does not survive the process.
- [x] Level 3's documented reward is the STAR-derived cohort/funnel
      visualization; Level 4 remains an explicit public-attribution choice.
      `DESIGN_INCENTIVES.md` already said so; the in-product level copy now
      says it too, including that Level 4 deliberately unlocks nothing.

## Challenge — answered

The challenge asks for a concrete serving population if search stays universal.
No such population can be named, which is why the reciprocal boundary was taken
rather than argued around. What the implementation adds to that decision is a
second, independent bound: the level gate is an honest-client contract and a
modified client ignores it, so `daemon/swarm/limits.go` bounds every serve path
at every level — 8 concurrent serves globally, 2 per peer, ~10 requests/second
per peer after a burst, and 64 MiB per rolling minute across all peers. The
byte budget is global rather than per peer on purpose: peer identities are free
to mint, so a per-peer byte cap bounds nothing an attacker cannot multiply.
Refusal is silence, matching what a node holding nothing already looks like, so
a prober cannot measure how loaded a serving node is.

The residual risk is stated rather than solved: nothing here stops a
sufficiently distributed set of modified clients from consuming a serving
node's whole budget, and the honest users of that node then see refusals. The
limits bound the damage to that node's own resources, which is what a per-node
limit can do; a network-wide answer would need the identity/credit machinery
this ticket deliberately rejected.

## Implementation record — 2026-08-12

**Daemon**
- `swarm/policy.go` — new `Policy.DistributedSearch`, set at Level 2+ beside
  `ServeBroadBuckets` so the reciprocity is visible in the code, plus the
  `ErrDistributedSearchNotPermitted` sentinel.
- `swarm/swarm.go` — `Node.MayDistributedSearch` is gate-aware, so a downgrade
  stops searches synchronously rather than when teardown finishes.
- `swarm/shard.go` — `PeerSearch` refuses on its first statement, before
  tokenizing and before the zero-peer fast path, so no caller can reach a peer.
  `FetchShard` refuses too: the shard corpus is fetched from nowhere else, so a
  future caller has to decide about the entitlement rather than inherit an
  exemption from wherever it happened to be checked.
- `main.go` / `contribution_runtime.go` — `PEER_SEARCH` refuses with
  `bridge.CodeContributionRequired` and a `ContributionRequiredDetail` before
  the node is touched. With a node running, the entitlement is read from the
  node itself; with none, from the stored level, so a Level-2 user whose swarm
  failed to start is told the network is unavailable rather than that they have
  not opted in.
- `contributionPayload` carries `distributed_search` (from the *effective*
  level) and `distributed_search_min_level`, so the existing WO-079 status
  broadcast is enough to re-render the control everywhere.
- `swarm/limits.go` — the per-node serving limits, applied to all five serve
  paths including the Level-1 word-telemetry and live-snapshot streams.

**Bridge**
- `peer_search` is offered at revision `2` (`PeerSearchRevReciprocal`). The
  revision, not a new capability name, because the RPC's shapes are unchanged
  and only the rule about when the daemon answers moved. Enforcement never
  depends on the negotiated revision.

**Extension**
- `CLIENT_OPTIONAL.peer_search = 2`; the SW returns a `contribution_required`
  refusal as state rather than throwing, because the extension-message channel
  carries only `{ok:false, error}` and the code and detail would be lost.
- The page disables the checkbox with copy naming Broad sharing, saying what
  still works, and a "Change contribution level" link that switches tabs and
  focuses the Level-2 radio. Against a daemon that negotiated `peer_search:1`
  the control is left enabled — imposing a rule that daemon does not enforce is
  the disagreement the revision exists to prevent.

**Tuning note.** The per-peer burst started at 12 and broke
`TestFetchTruncatesLargeBucketSafely`: an honest client catching up on a cold
cache issues three or more requests per neighbourhood back to back, so a burst
sized for one watch page refuses real users while barely inconveniencing an
attacker. It is 64 with a 100ms refill; the bounds that actually stop an
attacker are the concurrency caps and the byte budget.
