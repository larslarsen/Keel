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
| Size | Bounded by YouTube's catalogue; one row per video, deduped | One row per observation per user — unbounded, grows with users × browsing |
| Spam resistance | Rows are **verifiable against YouTube** — a fake title is checkable | A fabricated edge is not distinguishable from a real one |
| Access control | Not needed; every row is public fact | Needed; rows are observations of people |
| Replication burden | Every peer holding the full catalogue is reasonable | Every peer holding every observation is what §7.3 says does not scale |

So **"the DHT is the dataset" holds for the catalogue** — bounded, public, dedupable, verifiable —
and §7.3's objection lands squarely on the **funnel edges**, which are unbounded, personal, and
unverifiable. That is the same seam as §1 of this note, arrived at from a different direction, which
is weak evidence it is the right seam.

If that holds, stage 5 becomes: catalogue in the swarm, funnel aggregates through STAR. No
contradiction, and no server for the part users actually touch.

## 6. What this changes in the roadmap

Stage 2 (local utility) and a new B1 seed-catalogue effort become the near-term product. Stage 5's
crypto stops being a prerequisite for anything a user can see, and becomes what it should be: the
machinery behind the audit claims.

## Open questions for Lars

1. ~~Is the swarm the dataset or the publication channel?~~ **Resolved 2026-08-03 — see §5b.**
   Distribution channel, kept alive by seeders. The v2 decision stands.
2. **Who builds the seed catalogue, and from what seeds?** Affects the first weeks, not the long run.
3. **Is a shipped dataset acceptable**, or must everything be peer-derived on principle?
4. **Watch time** — worth pursuing, or is impression-only collection the permanent boundary?


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

---

# Appendix — is a seed pack affordable? Measured 2026-08-05

§5d left "whether a small shipped seed of popular neighbourhoods is needed" open.
It is needed — it is the only mitigation for the query leak that survives
scrutiny — and it is cheap.

Measured by building a real pack from an 89-context corpus:

| | Size | Per edge |
|---|---|---|
| Blocks as served (full §6.2 tuples) | 1,281 KB | 327 B |
| Stripped to `(to, weight)` | 69 KB | 18 B |
| Stripped + gzip | 19 KB | 5 B |
| Titles, deduped + gzip | 45 KB | — |
| **Total** | **63 KB** | **20× smaller** |

Projected: **10k videos ≈ 10 MB, 100k ≈ 80 MB, 1M ≈ 760 MB.**

A million-video seed fits on a phone. This is affordable at any scale the project
will plausibly reach.

**What stripping removes.** A walk needs `(to, weight)` and nothing else. Surface,
slot bucket, day bucket and cohort exist for *research* on the published
aggregate; they are dead weight in a seed whose only job is to answer hops. That
one change is 18× of the 20×.

**Titles dominate at scale** — 45 KB of the 63 KB here. They are what the panel
renders, so they cannot simply be dropped, but they are a separate concern from
the graph and should be a separate, optional download.

## Why this is the mitigation that works

The leak is that asking a peer for video V tells that peer you are interested in
V. Three families of answer:

- **Obscuring the query** — decoy requests, batched region fetches. Both fail to
  intersection attacks: repeated sets from one address converge on the real
  element. This is the flaw that sank the v1 k-anonymity buffer and it is not
  fixed by making the sets bigger.
- **Breaking the link** — relay routing, so the serving peer sees a relay rather
  than the asker. Sound, and complementary.
- **Removing the query** — the seed. Every node downloads the identical file, so
  there is no per-user variation to attack, and afterwards the covered videos
  generate no requests at all.

The long tail still needs fetch-on-demand, and hiding *that* query needs private
information retrieval — the technique Signal-class systems use for metadata. Real,
much larger, and not required before the seed exists.

## Consequence for the levels

This makes Level 1 a clean promise rather than a mostly-true one: **a Level 1
node asks the network for nothing**, so nothing leaves it, questions included. It
runs on the seed plus its own recording. Level 2 is where a user opts into the
query-based system, and the seed is what keeps that exposure to unusual videos.
