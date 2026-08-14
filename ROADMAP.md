# Roadmap

Updated 2026-08-13.

## Current stabilization queue

The normative architecture is `ARCHITECTURE_CURRENT.md`. Every original code
order from the architecture review has landed: WO-077–081, WO-083, WO-084,
WO-085 and WO-088. WO-082's final policy pass then found one new runtime order
and its small review follow-up; both have landed:

- **WO-089 — observation-derived Live and word sharing start at Level 2.**
  Implemented. Level 1 keeps discovery, seed/bucket fetch, graph pre-walk and
  fetched global word statistics, but has no Live capability and sends no local
  word aggregate. The daemon proves the corrected initial recording/consumer-
  network consent before starting Level 1. Two-machine wire inspection remains.
- **WO-090 — remove stale copy claiming Live works at Level 1.** Done. The two
  full-page strings and their DOM regression tests now match WO-089.

WO-082's architecture audit is closed. No architecture implementation order is
open from this review.

The current implementation queue is:

- **WO-097 — complete distributed-search foundation.** **Implemented**
  2026-08-13. Scheme 2 uses one continuous fixed query grid and every title
  alignment. Inverted-index generation drops only stopword-only occurrences,
  while meaningful and boundary windows remain. Token and catalogue/string
  buckets are completely pageable instead of silently ending at 4,096 rows.
  Retained HLL/CMS refresh snapshots supply immediate overlap-adjusted word
  targets without a word dictionary.

  **Rollout note — this release partitions the swarm.** `KeySchemeVersion` is
  one swarm-wide fence, so bumping it to 2 makes scheme-1 and scheme-2 builds
  mutually unreachable on *every* scheme-versioned protocol, graph and
  catalogue included, even though only the tokenizer changed. That is the
  deliberate cost of refusing silently incompatible shard data (WO-097 §5) and
  it lasts until installs have updated. `swarm.VersionView` reports
  `key_scheme`, `incompatible` and `update_required` so the state is
  diagnosable rather than looking like an empty network (WO-058). Live QA must
  put **both** machines on the new build; a mixed pair will connect at the
  transport layer and exchange nothing.

- **WO-095 — responsive streaming peer search and UI.** **Accepted; two-machine
  live QA ran 2026-08-14 and failed (0 results, 484 s exhausted).**
  Streaming UI, session routing and terminal events worked; shard provider
  discovery is DHT-only with no fallback to connected peers, so a degraded DHT
  starved the search despite a live peer. Details and evidence in the ticket.
  The start RPC acknowledges immediately; four bounded peer responses run
  independently. Candidate sets are unioned, broad string buckets resolve
  missing titles, and the daemon streams a result as soon as local full-query
  matching proves it. Schematic colored token bars show response cycles; live
  word bars count distinct confirmed candidates against frozen targets. Work
  stops on target plus saturation or a hard resource bound, never target or
  saturation alone. WO-096 is folded into these two orders and must not be
  implemented separately.

- **WO-111 — connected peers must not wait behind DHT discovery.**
  **Implemented; two-machine rerun pending.** This is the immediate
  release-blocking response to WO-095's
  failed live QA. Distributed search must try connected exact-protocol Keel
  peers and verified remembered peers before public-DHT expansion for both
  shard candidates and broad catalogue title resolution. DHT discovery,
  Level-2 reciprocity, broad request units, search budgets and network-health
  meanings remain unchanged.

- **WO-099 — streaming-search lifecycle and resource correction.** Implemented.
  Independent page jobs, early-event preservation and prompt downgrade
  cancellation landed; WO-100 closed its first review findings.

- **WO-100 — finish search-budget and resolution atomicity.** Implemented.
  Metering moved into reads, catalogue outcomes and successful prefix
  completion remain explicit, concurrent nominations join, and ordinary job
  cancellation retires by identity. Architecture review found the four final
  termination boundaries now isolated in WO-101.

- **WO-101 — close distributed-search termination semantics.** Implemented.
  Bytes temporarily reserved are distinct from bytes spent; unresolved
  catalogue work no longer advances saturation; invalid and unavailable paged
  outcomes split; globally stopped jobs keep their slots until exact
  retirement. Review accepted those mechanisms and isolated the remaining
  cause-propagation gaps in WO-102.

- **WO-102 — preserve distributed-search stop causes end to end.** **Accepted;
  the stacked search branch is merge-ready.** Budget termination survives and
  short-circuits the full catalogue traversal; local title-read failures leave
  candidates unresolved; cancelled meter waiters cannot lease refunded
  capacity; and malformed versus silent peers are distinguished through the
  real transport. Merge the whole dependent branch as one unit, then run
  WO-095's two-machine key-scheme-2 QA. **QA ran 2026-08-14 and failed — see
  the WO-095 entry above; follow-up tickets are with the reviewer.**

- **WO-098 — TikTok Explore, Following, and Live discovery.**
  **Code accepted 2026-08-13; interactive QA completed in WO-103 and exposed
  implementation corrections in WO-104.**
  Preserve the three real feed surfaces instead of mapping them to `HOME`.
  Explore and any future proven Following video cards are durable ordinary
  observations; the currently observed `/following` creator wall emits none.
  `LIVE`/`LIVE_ROOM` are a disjoint ephemeral sighting schema and can never
  enter SQLite. Canonical
  `@creator/live` identity requires Live topic/snapshot v2. Static bridge
  support and dynamic Level-2 entitlement are separate gates, with one shared
  200-record browser reconnect bound. This supersedes WO-076 and repairs the
  dropped TikTok platform, idle live-room and 11-character snapshot bugs.

- **WO-103 — interactive TikTok surface QA.** **Review complete.** Interactive
  Brave proved the Explore root and title-precedence defects, proved that the
  observed `/following` page is a zero-impression creator wall, and captured
  both an active room and the six-to-eight-second inactive automatic-
  replacement state.

- **WO-104 — repair TikTok Explore and Live-room extraction.** **Accepted
  2026-08-13; committed as `0f16d9c`.** Explore cards keep the full grid
  item so creator names come from the author row, not the like overlay. A
  `LIVE_ROOM` sighting requires matching route locator, rendered header and
  room-scoped playing video; late player insertion or a later
  `xgplayer-playing` class change both schedule a scan. Room badges stay
  empty so replacement-card badges cannot attach. `live_sightings:2` is
  unchanged.

- **WO-105 — stale generated extension manifest.** **Accepted** 2026-08-14.
  The installer refreshes Chromium drift. A
  generated `manifest.json` may match either authoritative template; anything
  else fails and names both `prepare:chrome` and `prepare:firefox`. Release
  and extension CI stay on `prepare:chrome`. Brave loaded the corrected path
  and the new TikTok observer armed; the remaining selector failure is WO-106.

- **WO-106 — preserve selector platform through the extension router.**
  **Accepted** 2026-08-14. Brave Explore logged `selectors v1 for tt from
  daemon` and `observer armed`. After consent and a current owner binary,
  `tt`/`EXPLORE` rows landed. Unrelated live-QA edits are separated into
  WO-107/108.

- **WO-108 — clean up WO-106 scope leakage.** **Accepted** 2026-08-14. The
  truthful Counts corrections are covered through the real full-page and
  side-panel DOM paths. The unproved unconditional service-worker startup
  rearm is gone; WO-008's watchdog remains the sole rearm owner.

- **WO-107 — platform-correct selector fallback.** **Code accepted**
  2026-08-14; daemon-unavailable live QA pending. The observer chooses a
  platform-correct bundle before its RPC and rejects a valid configuration for
  the other platform. The bundled TikTok data is closed against the daemon
  embed and extracts the real Explore fixture. Chromium/Firefox content-module
  closure is preserved.

- **WO-092 — contribution-impact accounting correctness.** **Accepted
  2026-08-13; committed as `8ff4dd7`.** A paged catalogue/shard reply is counted
  only when its signed terminal was fully written. Header or terminal budget
  refusal records nothing; a refused page may still count if the incomplete
  terminal lands. The `-1` budget sentinel is no longer added to wire totals.

The remaining operational checks are:

- WO-079 — Windows live QA.
- WO-080 — multi-tab live QA.
- WO-084 — two-machine network inspection.
- WO-085 — two-machine serving-limit load check against a real uplink;
  the automated proof is loopback only.
- WO-093 — remaining public-DHT acceptance gate. WO-109 is accepted: Live
  wording is provenance-neutral and only the shared presence key claims node
  discoverability. A live run has now shown a real publish timeout transition
  to `retrying`, followed by automatic shared-key publication and recovery to
  `ready` without restart. The current-binary two-machine proof and a controlled
  three-failure `fault` to recovery sequence remain open.

WO-107's remaining check is manual browser QA, not more implementation. The
shared Keel discovery key, not content-provider counts or raw DHT connections,
owns the bounded network-health state.

WO-111 is implemented; its two-machine rerun is the immediate release check.
WO-110 is code accepted; its deliberately stale revision-1 live check remains.
There is no implementation order behind either result: the next correction, if
one is needed, comes from the remaining live-QA evidence rather than from an
already assigned ticket.

Nothing here should be read as "ready to publish": the live QA above is the
gate, and the release rule below it still stands.

These are corrections to privacy, process ownership and release boundaries.
They take precedence over the older “Near queue” preserved below as phase
history.

The product is **not** "record what YouTube showed you." That is the substrate. The product is a
recommendation engine drawing on other people's observations to suggest things YouTube would never
surface — `User Utility Architecture.md` §3, `/home/lars/EEE/old-strategy/masterplan.md`, `/home/lars/EEE/old-strategy/Master Strategy Document.md`.

An earlier version of this file listed everything past an installer as "out of scope, do not start."
That was wrong. It confused *not in this phase* with *not in this product*.

---

## The arc

| Stage | What it is | State |
|---|---|---|
| **1. Collection** | Local corpus of what was recommended, per surface, with true slot positions | **Done** |
| **2. Local utility** | Hiding, channel blocks, funnel inspector, search, analysis | **Done** |
| **3. Local intelligence** | Graph-walk suggestions with an entropy control (WO-023). Local Interest Vector still to come | **Started** |
| **4. Distribution** | `keel-host install` registers with every detected browser (WO-020) | **Started** — packaging and signing remain |
| **5a. Published releases** | Aggregate bundle export and signed-release primitives (WO-028/034). Person-to-person exchange is rejected; see `DESIGN_SHARING.md` | **Built — release transport pending** |
| **5b. The swarm** | libp2p transport, stringless graph blocks, catalogue sync, prefix bucketing, ephemeral identity (WO-052). See `DESIGN_v2.md` §7.4 | **Built — untested between two machines** |
| **5b′. Live index** | Gossiped livestream feed, whole-index subscription, local search, cold-start backfill, Live tab (WO-052). See §7.5 | **Built — the one visible swarm feature** |
| **5c. Shared index** | STAR aggregation, then Prio; delay buffer. Gated on the cross-user dedup measurement | Not started |
| **6. Peer recommendations** | Semantic graphing over peer paths; entropy slider; serendipity | Not started |
| **7. Audit** | Cross-peer suppression and shadowban detection | Not started |

Stages 5–7 are the point. Stages 1–4 are what make them possible.

**The one thing blocking 5b from being finished is not code — it is testers,
plural.** The transport, both datasets, bucketing and identity rotation are built
and tested against loopback. NAT traversal, relay, hole punching and DHT provider
records all behave differently in the wild.

Two installs is not the bar. Two machines behind one router say nothing about NAT
traversal; a two-node gossip mesh exercises no routing; and the cross-user
overlap figure that gates STAR needs several independent corpora, since two give
one number and one number could be a coincidence. Different operating systems and
browsers matter too — the installer fails on machines we do not have.

So the milestone is a handful of people on different networks, and it is reached
by recruiting rather than by writing another ticket.

## Why the order is what it is

**Nothing peer-to-peer works with one user.** Stage 5 needs stage 4: a swarm of one is a database
with extra steps. That is the concrete reason the installer sits where it does — not because
distribution is more interesting than the index, but because the index is inert without it.

**Stage 3 must precede 6.** The Local Interest Vector is what ranks the global graph against *your*
trajectory (`User Utility Architecture.md` §4), and its privacy guarantee — the profile never leaves
the machine — is what makes peer recommendations anonymous. Build the ranking locally first, then
point it at a bigger corpus.

**Stage 2 needs no network at all** and is the fastest route to Keel being worth using day to day. A
persistent channel blocklist and a "why was this recommended" inspector work today, on the corpus
that already exists.

## Release sequencing — decided 2026-08-05

**Build the full product before applying to the Chrome Web Store.** Lars: so the
reviewers see how it works, and so we hopefully never have to apply again.

This reverses an earlier reviewer suggestion to submit early. The deciding
argument is the frozen extension: contribution levels (WO-051) are extension UI,
so submitting before they exist guarantees a second submission. One application
with the final extension is worth the delay.

Consequence: WO-048 (privacy policy), WO-049 (consent screen) and WO-051
(contribution levels) are still required, but they no longer gate anything
today — they gate the submission, which now comes after the contribution
pipeline. Development attention moves to stage 5.

Caveat kept for honesty: reviewers will not run the daemon, so the extension is
reviewed in its disconnected state regardless. That is accepted and specified
(`BUILD_P0` §9). "They can see how it works" means the listing and the source,
not a live demo.

## Near queue

### WO-014 — SidePanel: suggestions first *(done 2026-08-03)*

List under the connection banner; Counts / Hide / Your data collapsed via `<details>`.

### WO-015 — Channel hard block *(superseded by WO-016)*

Page-level CSS hide was reversed: daemon owns the list; panel filters its own view only.

### WO-016 — Daemon owns blocking + channel catalogue backfill *(done 2026-08-03)*

SQLite `channel_blocklist`; SidePanel display filter only. Insert + open-time `video_id → channel_id`
backfill. No YouTube DOM card suppression.

### WO-017 — Funnel inspector *(stage 2, not written)*

`User Utility Architecture.md` §6: "why was this recommended." The corpus already holds
`context_video_id` → recommended-video edges with slot positions. A query and a UI; no new
collection.

### WO-018 — Retention setting *(P1 debt, not written)*

Deferred from WO-012. User-settable cap, default **off**. Retention was removed deliberately
(WO-002); this returns it as a choice, never a default.

### WO-019 — Installer *(stage 4, not written)*

Native-messaging host registration across Chromium variants, Firefox, three OSes.
`PUBLISH_CHECKLIST.md` §3: the host manifest ships as a template with no machine-specific values.

## Partner requirements — long lead time, start early

Stages 5–7 depend on **other organisations operating services**, not on code we write. These take
months to arrange and are the most likely thing to be discovered late.

| What | Who runs it | Needed for | Why it cannot be us |
|---|---|---|---|
| STAR aggregation server | **Us** | Stage 5 | Untrusted by design — it is incapable of reading below the threshold, so it does not need to be trusted |
| **STAR OPRF randomness helper** | **An independent third party** | Stage 5 — **critical path** | If one party holds both the aggregator and the helper it can brute-force its way past the threshold and the guarantee collapses (§6) |
| Prio aggregator A | **Us** | Stage 5, after STAR | — |
| **Prio aggregator B** | **A second independent party** | Stage 5, after STAR | Prio's guarantee is that two aggregators do not collude. Both being us means the math runs and the privacy does not |

**The OPRF helper is the first and smallest ask, and it is on the critical path.** One independent
operator, one service. Worth approaching candidates well before the code exists — EFF has been raised
as a possibility. The ask is "will you run a randomness helper", not "will you co-run our telemetry
pipeline", and the distinction matters when asking a nonprofit for indefinite operational commitment.

**Prio needs a second independent operator.** ISRG — the nonprofit behind Let's Encrypt — runs
**Divvi Up**, a privacy-preserving telemetry service that exists precisely to be the second
aggregator so projects do not have to find one. That is the obvious first approach, with EFF or a
university group as alternatives.

Note both asks are **standing infrastructure commitments** — running a service, holding key material,
staying up — not endorsements or letters of support.

## Bootstrap — see `DESIGN_BOOTSTRAP.md`

Draft design note, 2026-08-03. The corpus is two datasets: a **catalogue** (video metadata — public
fact, no personal content once merged and timestamp-stripped) and **funnel edges** (who was shown
what — genuinely personal). Search and graph-walk suggestions run mostly on the catalogue and **do
not need STAR**; the threshold, OPRF helper and Prio are load-bearing for audit claims only.

A solo user's graph is their own bubble re-served, so bootstrap means shipping a **seed catalogue**
with the app — harvested by the project, containing no user data, distributed over the free channels
`DESIGN_v2.md` §7 already specifies. Contributors then improve a working graph rather than being
required to constitute one.

This moves a usable product substantially earlier than the stage table implies. Three open questions
for Lars at the end of that note.

## Beyond the near queue — design work, not tickets

Each needs a design document before any work order:

- **Local Interest Vector** — what the profile is, how it is derived, how it stays local
  (`User Utility Architecture.md` §4).
- **Shared index** — contribution schema, how the delay buffer defeats timing de-anonymisation
  (`/home/lars/EEE/old-strategy/masterplan.md`), and publication (§7.3 decentralises *publication*, not the write path — OrbitDB
  as a global write path is explicitly rejected in `DESIGN_v2.md` §7.3).
- **Prio, alongside STAR** — decided 2026-08-03. STAR ships first; Prio follows in the same stage
  because STAR is blind below K and the findings that matter — rare, harmful pathways — live in the
  tail. Needs a second non-colluding aggregator (see partner requirements).
- **Contribution levels** — 1 (personal product; live + word aggregate outbound,
  no block service or user-triggered distributed search), 2 (public broad
  local-plus-cached graph buckets and reciprocal distributed search), 3 (STAR
  cohort measurements and funnel comparison), 4 (attributed transparency).
  See `ARCHITECTURE_CURRENT.md` §3.
- **Semantic graphing and the entropy slider** — how peer paths become a ranking, and what 0% versus
  100% actually computes.
- **Suppression detection** — the statistical claim being made, and what sample size supports it.

**The daemon owns all of this, not the extension.** The extension is a thin client with no
persistence and a minimal permission set (`AGENTS.md`); nothing in stages 5–7 should change that. A
P2P layer inside a browser extension would wreck both the threat model and the store listing.

## Phase mapping

`BUILD_P0.md` §11 uses P0–P4+. That numbering is still valid; this file is the same thing expressed
as product stages.

| BUILD_P0 phase | Stage here | State |
|---|---|---|
| P0 — WATCH_NEXT collection | 1 | Closed 2026-08-03 (WO-011) |
| P1 — HOME, export/wipe | 1 | Done. SEARCH cut (WO-010 §5) |
| P2 — installer, utility plane | 2 and 4 | **Started** — local utility and installer exist; packaging/signing remain |
| P3 — preservation, fingerprints, tombstones | 5 | Not started |
| P4+ — contribution, crypto, index | 5–7 | **Started** — Level-2 broad swarm and Live index built; STAR/Prio and audit layers not started |

Closed so far: WO-001 … WO-013.

## Standing debt

- `bumpCounts` now increments only the combined impressions total; per-surface
  tiles wait for STATS (WO-009 review). Counts also show EXPLORE.
- `watch_next_mixed.html` and `watch_next_compact.html` are hand-authored, covering a renderer now
  extinct on watch-next. Historical regression cover; cannot be recaptured.
- `channel_id` unavailable past the initial rail (WO-013, closed as documented-not-fixed). Constrains
  WO-015 and any channel-level analysis.
