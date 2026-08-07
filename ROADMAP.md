# Roadmap

Updated 2026-08-03.

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
- **Contribution levels** — 1 (strictly personal, contributes nothing), 2, 3 (full funnel state).
  Level 1 must stay fully functional or the privacy promise becomes a toll booth.
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
| P2 — installer, utility plane | 2 and 4 | Not started |
| P3 — preservation, fingerprints, tombstones | 5 | Not started |
| P4+ — contribution, crypto, index | 5–7 | Not started |

Closed so far: WO-001 … WO-013.

## Standing debt

- `bumpCounts` attributes optimistic inserts to the WATCH_NEXT tile regardless of surface (WO-009
  review). Self-corrects within ~5 s.
- `watch_next_mixed.html` and `watch_next_compact.html` are hand-authored, covering a renderer now
  extinct on watch-next. Historical regression cover; cannot be recaptured.
- `channel_id` unavailable past the initial rail (WO-013, closed as documented-not-fixed). Constrains
  WO-015 and any channel-level analysis.
