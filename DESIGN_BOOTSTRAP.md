# Design note — catalogue vs funnel, and the bootstrap engine

Status: **draft for review.** Not a work order. Written 2026-08-03 from a discussion with Lars.

Answers one question: **what makes Keel useful to user number one, before any peer data exists?**

---

## 1. The corpus is two datasets, not one

`impressions` currently mixes two kinds of fact with very different sensitivity. Separating them is
the key move.

| | **Catalogue** | **Funnel edges** |
|---|---|---|
| Content | `video_id` → `title`, `channel_id`, `duration_s`, `view_count`, `published_at`, `badges` | `context_video_id → video_id`, `slot_index`, `surface`, time |
| What it asserts | "This video exists and has these properties" | "Somebody was shown B after A" |
| About a person? | **No.** True regardless of who observed it | **Yes.** It is an observation of one person's session |
| Machinery needed to share | Merge, dedupe, strip observation times | STAR threshold, OPRF helper, cohorts |

**This is the load-bearing distinction.** The catalogue is a mirror of YouTube's public metadata.
Merged across contributors and stripped of *when* it was seen, it carries no personal information —
it is the same class of data as a library catalogue.

Two guardrails, without which the claim above is false:

- **Strip observation timestamps** from catalogue rows before sharing. First-seen times leak
  browsing patterns even when every row is public fact.
- **Merge, never publish per-contributor.** "Everything this node saw" is a browsing history no
  matter how public each individual row is.

### Consequence

Search and graph-walk suggestions run mostly on the catalogue. **They do not need STAR.** The
threshold, the OPRF helper and Prio are load-bearing for *audit claims* — what YouTube pushes, to
whom, how often. Gating the usable product behind that machinery delays it by years for no privacy
benefit.

## 2. Why "just use your own data" is not a bootstrap

A solo user's graph is a record of what they were already shown. Recommending from it returns them
to the same neighbourhood — it *is* the filter bubble, re-served. It can do useful things (find that
video you saw last week, block a channel, show why something was recommended) but it cannot deliver
what `User Utility Architecture.md` §3 promises: discovery of things YouTube would not surface.

Bootstrap therefore needs a corpus the user did not generate.

## 3. Four stages of usefulness

| Stage | Data source | What works | Peers needed |
|---|---|---|---|
| **B0** | Local corpus only | Search your own history; funnel inspector; channel blocks | 0 |
| **B1** | **Seed catalogue shipped with the app** | Real search and graph suggestions on day one | 0 |
| **B2** | Merged contributed catalogue + edges | Graph grows with the network; niche coverage improves | a few |
| **B3** | STAR aggregates | Audit claims; slot and cohort breakdowns | ~50 per measurement |

**B1 is the bootstrap.** The project harvests a catalogue and edge graph itself, ships it as a
downloadable dataset, and every install starts with a usable graph containing no user data at all.
It is the same content the crawler would collect anyway, and it costs nothing to distribute —
`DESIGN_v2.md` §7 already specifies free channels (BitTorrent, Academic Torrents) for exactly this.

Contributors then improve the graph rather than being required to constitute it. That inverts the
cold-start problem: the network makes a working thing better, instead of a broken thing viable.

## 4. What the bootstrap algorithm is

Deliberately unsophisticated. No ML, no embeddings, no training. It runs in the daemon over SQLite.

**Search** — inverted index over `title` and `channel_id`. Rank by term match, then `view_count`
and recency. This is a solved problem and does not need to be clever to beat YouTube's search, which
is optimised for monetisation rather than for finding things (`User Utility Architecture.md` §1).

**Suggestions** — weighted random walk over the co-recommendation graph, seeded from the current
video (or from the Local Interest Vector when that exists). Edge weight from repetition and
`slot_index` — a video that repeatedly appears at slot 0 is a stronger edge than one that appeared
once at slot 18.

**The entropy slider falls out of this naturally.** `User Utility Architecture.md` §3 asks for a
0–100% focus-to-serendipity control, and a random walk already has the parameter:

- **0% (Focus)** — walk length 1, high restart probability. Immediate neighbours only.
- **100% (Serendipity)** — longer walks, low restart, and prefer *low* `view_count` nodes so the walk
  surfaces niche content rather than falling into popularity gravity wells.

That last clause is what delivers the anti-popularity promise. It is a ranking choice, not a crypto
property — worth noting, because the current design leans on STAR cohorts to escape popularity bias
when a walk parameter does it more directly.

## 5. Honest limits

- **The seed catalogue reflects whoever built it — but not for long.** *Corrected 2026-08-03 by
  Lars: the dataset is the swarm, and it grows.* Organic browsing generates observations regardless
  of what Keel suggests, so gaps fill in from real use rather than from the seed. The residual
  concern is narrow: to the extent suggestions steer browsing, early bias is mildly self-reinforcing.
  Organic browsing dilutes it. Seeding strategy affects the first weeks, not the long run.
- **`channel_id` is missing past the initial rail** (WO-013), so channel-level graph features are
  incomplete on scrolled cards.
- **No engagement signal.** Nothing records what was watched or valued, so "successful user paths"
  (`User Utility Architecture.md` §3) is not computable today. Watch time might be derivable later;
  it is a threat-model decision, not a schema change.
- **Catalogue rows go stale.** Titles change, videos are removed. Removal is itself a signal worth
  keeping (§7 preservation) but staleness needs a policy.

## 5b. RESOLVED 2026-08-03 — swarm is the distribution channel, not the write path

Lars, 2026-08-03: *"The DHT is the dataset… the dataset is the swarm so it's always growing."*

`DESIGN_v2.md`'s correction table (line 23) rejects precisely this, as a v1 claim that v2 overturned.
Restated without the jargon, because the original entry is hard to follow and Lars has said he did
not fully understand it when he changed it — **neither position should be treated as settled**:

**What is being proposed:** a shared database with no server. Everyone runs a copy, the copies sync
to each other, anyone can write.

**Why v2 rejected it as the write path:**

- **Everyone stores everything.** There is no "just my slice". As the dataset grows, every user's
  disk fills with every other user's data. Workable for a small shared list; not for every
  recommendation every user has ever seen.
- **Anyone can write anything.** No access control, no spam resistance. A recommendation edge is not
  verifiable — it is a claim that YouTube showed B after A — so injected garbage is indistinguishable
  from real observations.
- **It is not durable by default.** Helia, the JavaScript IPFS implementation, keeps data in memory
  unless configured otherwise.

**What "decentralize the publication, not the write path" means:** rather than everyone writing into
one live shared database, the project produces dataset *releases* — snapshots — distributed over free
channels (BitTorrent, Academic Torrents). Anyone can fetch, mirror and verify a copy; nobody can
poison the source. Decentralise the handing-out, keep control of what goes on the shelf.

**Settled 2026-08-03.** Lars reaffirmed the v2 decision after re-reading it: *"everyone with the same
database is wrong. I thought we could do it with DHT (which is only some data for each node) but it's
not persistent."*

### The distinction that was being blurred

- **Full replication (OrbitDB)** — every peer holds the entire log. This is what v2 rejected, and the
  rejection is right: disks fill with other people's data.
- **A DHT** — shards the data; each node holds a slice plus a routing table. This is what Lars
  originally meant, and it is a genuinely different design, which is why the objection felt
  misapplied.

### But a DHT does not store anything

**IPFS's DHT is a directory, not storage.** It maps content hashes to peers claiming to hold the
data. The data exists only while someone keeps it — pinned or seeded. If nobody does, it is garbage
collected and the routing entry points at nothing.

So the swarm cannot be relied on to *remember*. Durability comes from seeders:

- A project-run pinning node — the floor, so content never drops to zero holders.
- Volunteer seeders — mirrors, the same model as any torrent-distributed dataset.
- BitTorrent and Academic Torrents for bulk snapshots (`DESIGN_v2.md` §7, free channels).

### Where that leaves stage 5

Catalogue snapshots distributed over the swarm and torrents, kept alive by seeders. Funnel
observations never enter that path — they are aggregated through STAR and only counts are published.
No shared live write path, and no server for the part users actually touch.

**The catalogue/funnel split may resolve it**, because the objection lands unevenly:

| | Catalogue | Funnel edges |
|---|---|---|
| Size | Bounded by YouTube's catalogue; one row per video, deduped — **~0.1–0.5 TB/year** (§5c) | One row per observation per user — unbounded, grows with users × browsing — **~80 TB/year per million users** (§5c) |
| Spam resistance | Rows are **verifiable against YouTube** — a fake title is checkable | A fabricated edge is not distinguishable from a real one |
| Access control | Not needed; every row is public fact | Needed; rows are observations of people |
| Replication burden | Every peer holding the full catalogue is reasonable | Every peer holding every observation is what §7.3 says does not scale |

So **"the DHT is the dataset" holds for the catalogue** — bounded, public, dedupable, verifiable —
and §7.3's objection lands squarely on the **funnel edges**, which are unbounded, personal, and
unverifiable. That is the same seam as §1 of this note, arrived at from a different direction, which
is weak evidence it is the right seam.

If that holds, stage 5 becomes: catalogue in the swarm, funnel aggregates through STAR. No
contradiction, and no server for the part users actually touch.

## 5c. How big this gets — sized 2026-08-04

§5b's objection — "every peer holds every observation, disks fill" — is quantifiable, so it does not
have to be taken on faith. Fermi estimate, **metadata only** (Keel fetches thumbnails on demand,
WO-040; no video bytes ever enter the corpus).

**Assumptions.** Per-record: catalogue row ~200 B (video_id, title, channel, duration, view count,
date); edge row ~50 B (context → video, count, slot stats); observation row ~220 B. YouTube (2026):
~5 B discoverable videos, ~1 B hrs watched/day ≈ 5 B views/day, Shorts ≈ 75% of views by count.
Keel observes long-form watch/home/search only — Shorts would be a large separate term. **The product
is English-first, and YouTube's rails are largely language-locked, so the catalogue and graph scale
with the *English* active inventory (~20–30% of global; music is the leaky cross-language category).
Read the top two table rows low-side — the global-inventory figures above would overshoot.**

| Dataset | What it feeds | 30 days | 90 days | 1 year |
|---|---|---|---|---|
| **Catalogue** (distinct videos watched or suggested, deduped) | Search | ~0.3–0.6 B rows · **60–120 GB** | ~0.5–1 B · **100–200 GB** | ~1.5–2.5 B · **300–500 GB** |
| **Edge graph** (deduped co-recommendation edges + counts) | SUGGEST walk | ~30–40 B edges · **~2 TB** | ~60–100 B · **3–8 TB** | ~300–700 B · **15–35 TB** |
| **Funnel stream** (every observation, all levels — **never stored centrally**) | Feeds the two above via STAR; discarded on aggregation | ~6 TB @ 1 M users | ~20 TB @ 1 M users | ~80 TB @ 1 M users |

The funnel stream is linear in contributors: 10 M users ≈ 800 TB/year — which is the size of the
shared-DB anti-pattern (§5b), **not a storage requirement**. No raw observation is ever stored
centrally: the numbers are stream *throughput* for sizing the aggregation pipeline.

**Readings**

- **Catalogue and edge graph saturate.** Once contributors cover the active inventory, dedup caps
  the size — a few million contributors approaches full-YouTube coverage, and adding users stops
  growing the dataset. But saturation is exactly why distribution has to cover the *deduplicated*
  sizes too: the graph, at ~2–35 TB, still exceeds a desktop drive. §5d is how it is served without
  being held.
- **The funnel stream is the unbounded term** — the concrete version of §5b's "everyone stores
  everything." ~80 TB/year per million contributors is what a *shared live database* would force
  every peer to hold; the figure is the anti-pattern's price tag, not a budget. The stream is
  aggregated into the two datasets above at STAR and discarded — **no raw observation is ever stored
  centrally**, by privacy policy (`DESIGN_v2.md` §6.0/§6.5: raw trails never leave the device; only
  counts are published). The durable copy is each contributor's own local record, kept on their
  machine.
- **L3 is a small opt-in subset, not the stream.** Transparency contributors consent to attribution;
  their attributed edges flow through the same threshold path, and the durable proof of what they
  contributed is their own signed local record, verified on demand — not a central archive
  (`DESIGN_v2.md` §6.5). "Attributed" means accountable, not centrally stored.
- **The window is the control** for catalogue and graph (≈2–5× between 30 days and a year); the
  funnel stream moves linearly in both window and contributors.

**Empirical anchor — measured 2026-08-04 from a live corpus** (4,126 impressions, 44 watched videos,
3,160 WATCH_NEXT rows):

| Metric | Measured |
|---|---|
| Walk edge table (whole history) | ~106 KB (1,894 distinct edges) |
| Distinct neighbours per context video | median 35 · mean 43 · **max 270** |
| Edge weights | mean 1.67 impressions/edge · max 20 |
| Single-user dedup ratio | distinct edges = 0.60 × impressions |
| Search payload | titles 0.2 MB; thumbnails 6.1 MB of the 7.4 MB corpus (excluded from releases) |

Readings:

- **The "graph is megabytes" claim holds at the user level.** A full suggestion edge table is
  ~100 KB; the corpus's bulk is thumbnails and text. Confirms the reviewer's read: search is the
  text, the graph is ids and weights.
- **Per-context neighbourhoods are bounded, not unbounded** — one user already saw 270 distinct
  recommendations for a single video. The sub-linear term is real: the aggregate edge table is
  capped by active inventory × neighbourhood size, not by contributor count.
- **The single-user dedup ratio (0.60) does NOT extrapolate to the aggregate.** It is dominated by
  new-context acquisition (each new watched video adds ~43 edges). The aggregate dedup comes from
  *cross-user* overlap — popular contexts watched by many, heavily overlapping rails — which one
  machine cannot measure. **That factor decides whether the aggregate edge table is ~TB
  (publishable, servable) or tracks the funnel stream's linear growth; it is the open item to resolve
  before the STAR path (§5d open questions).**

## 5d. Suggestions and search without holding the graph — designed 2026-08-04

Lars, 2026-08-04: *"I'm talking about 'everything' meaning everything you need for search."* This
corrects §5c's framing: the useful dataset is the problem, not just an audit trail. At full scale the
deduped graph is ~2–35 TB — already past a desktop drive. Distribution has to cover the useful
datasets (graph + catalogue), not merely an archive nobody can hold anyway.

**Key structural fact: a random walk needs neighbourhoods, not the graph.** SUGGEST walks from the
current video, and each hop only needs the edges *out of the video it is standing on* — one video's
neighbourhood is ~15–30 KB (hundreds of edges × ~50 B). The 35 TB is everyone's neighbourhoods at
once; any one user only touches roughly tens of MB (up to ~1 GB after years of heavy watching).

**So the graph is served, not held:**

1. **Block sharding.** The graph is cut into blocks keyed by `context_video_id`
   (`neighbours(v)` per block), signed and hash-verifiable. L3 edges are attributable, so a forged
   block is detectable — the write-path poison objection (§5b) weakens when publication is signed
   and verification is cheap.
2. **Fetch-on-demand.** A walk hop that misses the local cache fetches its block. The daemon holds
   an **LRU cache of the neighbourhoods the user actually uses** — bounded by usage, not by the
   graph. Warm hops are instant; cold hops are one small fetch.
3. **Background prewarm.** The observer already tells the daemon which video is being watched
   (observations flow through it), so the seed's block is prefetched when the watch page loads —
   before the panel's SUGGEST arrives. First request on a brand-new topic: seconds, once.

Consequence: suggestions work out of the box for every user — no download, no local graph, no
centralised service. The swarm holds the graph; users hold the slice they've touched.

**Search is two-tier:**

1. **Global-slice index** — inverted index over the popular/verified catalogue. Compressed
   ~50–200 GB for a year-long window: an optional, deliberate download (Lars: *"200 GB compressed is
   doable"*). Complete local search over everything the slice covers.
2. **Long-tail posting lists on demand** — the same block-fetch mechanism serves term posting lists
   outside the slice. Users who skip the download get slice coverage plus on-demand tail.

Local search over your own corpus is unchanged and already works with zero network (§5 — B0).

**What this removes:** the centralised search website. Lars, 2026-08-04: *"I was thinking we would
have to monetize a centralised search website with ads or something to pay for the
hosting/storage."* No: hosting is volunteer mirrors plus the free channels of `DESIGN_v2.md` §7.3
(Zenodo, GitHub, BitTorrent/Academic Torrents — all zero-cost). No raw observations are stored
anywhere; contributors keep their own local records, and the only shared data is the deduped,
policy-safe datasets above (§5c).

**Open questions (not settled by this note):**

- **Substrate.** Block lookup needs a mechanism — a DHT for discovery plus mirrors for durability,
  or signed block URLs from seeders. Latency and churn differ; nothing is chosen yet.
- **Cross-user dedup factor — the gate before STAR.** The aggregate edge table's size hinges on how
  many *distinct* edges the network adds per new user once popular contexts are covered. One machine
  cannot measure it (single-user ratio is 0.60, dominated by new-context acquisition — §5c empirical
  anchor); it needs multi-user data or a crawl. If edges dedup hard across users, the aggregate is
  ~TB and STAR's output is publishable/servable. If they do not, it tracks the funnel stream's linear
  growth and the L2
  distribution shape changes. Resolve this before committing STAR.
- **Bootstrap.** A brand-new user's first walk is cold — one or a few block fetches. Whether a small
  shipped seed of popular neighbourhoods (B1-style) is needed, or fetch-on-first-use is enough.
- **Staleness.** Neighbourhoods drift as YouTube retrains. Who re-hashes blocks, and how often?
- **Long-tail serving.** On-demand tail requires *someone* to hold and serve it — a mirror choice,
  not a business.

## 6. What this changes in the roadmap

Stage 2 (local utility) and a new B1 seed-catalogue effort become the near-term product. Stage 5's
crypto stops being a prerequisite for anything a user can see, and becomes what it should be: the
machinery behind the audit claims.

## Open questions for Lars

1. ~~Is the swarm the dataset or the publication channel?~~ **Resolved 2026-08-03 — see §5b.**
   Distribution channel, kept alive by seeders. The v2 decision stands.
2. **Who builds the seed catalogue, and from what seeds?** Affects the first weeks, not the long run.
3. **Is a shipped dataset acceptable**, or must everything be peer-derived on principle? *§5d now
   answers the search half — an optional download — but the principle is worth stating.*
4. **Watch time** — worth pursuing, or is impression-only collection the permanent boundary?
5. **§5d substrate and bootstrap** — the fetch-on-demand graph needs a block-lookup mechanism and a
   first-run story. The five open questions in §5d; the **cross-user dedup factor is the one to
   resolve first** — it gates the STAR path (§5c empirical anchor).


---

# Appendix — the frozen extension

Lars, 2026-08-03: *"we don't want to ever have to update the extension. It should be the same
forever, bug free, with no changes forever."*

A sound goal. Extension updates mean store review — slow, rejectable, and not reliably picked up by
users. Everything that can live in the daemon should.

**But there is a direct conflict, and it should be faced rather than assumed away.** `AGENTS.md`
calls `extract.js` *"the code that rots when YouTube changes their DOM."* This project has already
been broken three times by exactly that: the lockup component swap (WO-005), the nested shelf grid
(WO-010), and the metadata-row change that nulled every `view_count` (WO-005 review). Each needed an
extension change. A frozen extension that parses YouTube's DOM is not achievable — the parsing is
the part that rots.

Two ways out.

## Option A — ship the page state to the daemon, parse there

The extension becomes a pipe: grab `ytInitialData` and the relevant DOM subtree, send them over,
done. Selectors live in the daemon and update freely.

**`DESIGN_v2.md` §5.1 item 8 rejected this**, listing it as a v1 defect: *"The protocol ships whole
`ytInitialData`… It is often multiple MB, and it hands the daemon raw page state for no reason.
Normalize first."*

Note the wording — **"for no reason."** A frozen extension is a reason, so the objection is
negotiable rather than absolute. But the two costs are real: multi-MB messages per navigation, and
the daemon receiving raw page state, which is a larger blast radius if the daemon is ever
compromised.

## Option B — data-driven selectors *(recommended)*

The extension keeps its parsing engine but holds **no selectors**. It receives a selector
configuration from the daemon: which elements are cards, which child yields the title, which yields
the href. YouTube changes, the daemon ships new config, the extension binary never changes.

- No raw page state crosses the bridge; §5.1 item 8's objection does not apply.
- Payload stays small — the normalized records it already sends.
- The rot moves to the daemon, which is exactly where Lars wants it.
- `extract.js` stays a pure function testable against fixtures (`AGENTS.md`), with the selector table
  as an input rather than a constant.

### The line that decides whether this is publishable

Chrome Web Store forbids **remotely hosted code**: an extension may not fetch instructions and
execute them. Breaking that rule gets the extension removed, which is the entire distribution
channel. `DESIGN_v2.md` §2 already states the rule ("no remotely hosted code"); this is what it means
in practice here.

**The extension may download data. It may never download logic.**

*Data — acceptable:*

```json
{ "card":  "yt-lockup-view-model",
  "title": "h3",
  "link":  "a[href*='watch?v=']" }
```

The shipped extension already knows what to *do* — "find every element matching `card`; inside each,
find `title` and take its text content." The downloaded table only says **where to look**. Every
behaviour was reviewed at submission.

*Code — forbidden:*

```json
{ "extract": "el => el.querySelector('h3').textContent.trim()" }
```

```json
{ "if": "hasClass('live')", "then": "skip", "else": "parse" }
```

Both send *instructions* for the extension to interpret. It does not matter that the second is not
literally JavaScript: once there is branching or expression evaluation, it is a program and the
extension has become an interpreter for code that never passed review.

**The test:** can the downloaded config make the extension do something its shipped code does not
already know how to do? Yes → code. Only redirect existing behaviour at different elements → data.

**Practical constraints for the implementer:**

- A fixed, known set of field names. Unknown keys are rejected, not ignored.
- Each field maps to a CSS selector string, optionally plus an attribute name. Nothing else.
- Validate against a schema at load; refuse the whole config on any violation rather than partially
  applying it.
- **No daemon-supplied regexes.** Arguably data, but it is the first step down the slope, and a
  pathological pattern can hang the page.
- The parsing behaviours themselves — take text, read an attribute, walk to a parent — stay compiled
  into the extension and are only *selected* by the config, never *defined* by it.

## What is genuinely frozen either way

Neither option freezes the extension completely. Manifest changes, permission changes and browser
API deprecations still force updates. The realistic goal is **no update caused by YouTube changing
their page**, which is the only cause that recurs on a schedule nobody controls.

## Open question for Lars

Option B, or is shipping page state to the daemon acceptable after all? B is more work up front and
keeps the bridge small; A is simpler and puts every future change in the daemon. Neither is written
as a ticket yet.

---

# Appendix — does aggregation actually shrink the problem?

Measured 2026-08-05 against a live 7,187-impression corpus.

| | Count | Per impression |
|---|---|---|
| Raw impressions | 7,187 | — |
| §6.2 tuples (with `day_bucket`) | 4,624 | 0.64 |
| Same, ignoring `day_bucket` | 4,563 | 0.63 |
| Distinct `(from, to)` edges | 4,196 | 0.58 |

**Within one user, aggregation compresses by about a third and no more.** The
ratio is flat as the corpus grows — it did not decline across eight successive
slices — because one person rarely encounters the same recommendation pair
twice. Roughly 0.58 of every impression is a brand-new edge.

`day_bucket` is not the culprit: removing it changes almost nothing here, though
that is partly because this corpus spans two days. Over months it would matter
more.

## What this means

**The earlier claim that aggregate size grows sub-linearly with users is
unsupported.** It was reasoning, not measurement: the argument was that the tuple
space is bounded by videos × videos × buckets, so at scale the same edges recur.
That may still be true — but *this data cannot show it*, because all compression
of that kind comes from **cross-user overlap**, and there is only one user here.

So the position is:

- Per-user compression is ~35%. Do not plan around aggregation shrinking the
  problem.
- The ~80 TB/year per million users figure stands unless cross-user overlap is
  large.
- Whether it is large is **the open question**, and it decides whether §7.3's
  free channels can carry the published dataset at all.

## How to actually answer it

Two corpora from different people, over the same period, would settle it in
minutes: aggregate each, then measure how much the union is smaller than the sum.
That is one number, and it is worth getting before any STAR client is written —
the whole publication story rests on it.

Failing that, an estimate from YouTube's own structure (how concentrated
recommendation graphs are) would bound it, but a real measurement from two nodes
is far better and now costs nothing but a second install.
