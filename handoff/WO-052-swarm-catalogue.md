# WO-052 — Level 2: catalogue sharing over the swarm

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Open — current** |
| **Date** | 2026-08-05 |
| **Source** | Lars, 2026-08-05; Jr Dev's neighbour-crawl plan |

Make Level 2 real: nodes share the **catalogue** with each other automatically,
through the app. No file transfer, no user copying anything, no trusting a
person.

## Scope — catalogue only

**Shared:** `video_id → title, channel_id, channel_name, duration_s, view_count,
published_at`.

**Never shared at this level:** edges. Not `(from, to)`, not slots, not counts,
not days. Those are an observation of a person and wait for STAR.

This boundary is the entire safety argument, and it is what makes the rest of
this ticket acceptable. It must be enforced in the code that builds the payload,
not by convention — a single test asserting no edge field can appear in a Level 2
publication.

## Why unretractability is tolerable here

Swarm data cannot be recalled once fetched, and `DESIGN_v2` §6 is explicit about
that for attributed publication. It does not bite at Level 2: catalogue rows are
public facts about public videos — YouTube's own metadata, true regardless of who
observed them. If they persist forever, nothing personal persists.

Lars's staging plan — publish broadly now, tighten later — is therefore sound
**at this level and no higher**. Do not let it become a precedent for edges.

## What to build

**1. Publish.** A node offers its catalogue as a content-addressed, signed
artifact (WO-036/037 already produce both digest and signature). Content
addressing means transport is interchangeable and nobody has to trust the source.

**2. Discover and fetch — the neighbour crawl.** Jr's idea: rather than pulling a
global catalogue, fetch metadata for videos *near where the user currently is in
the graph*. The walk already knows the current seed and its neighbourhood, so
requests are naturally scoped and small.

This is the right shape because it makes the useful case cheap: a node needs
titles for the few dozen videos a walk might surface, not for all of YouTube.

**3. Merge.** Straight into `peer_catalogue` via `ImportEdges`
(`daemon/store/peers.go:24`), which already handles both tables, replace-not-
accumulate semantics, and malformed-row rejection. **No merge logic is needed —
the entire consumption side already exists.** This ticket is fetch only.

### What Level 2 actually buys, and what it does not

`peerGraph()` (`peers.go:155`) queries `peer_edges` exclusively. Nothing in the
suggestion walk reads `peer_catalogue`.

So catalogue-only sharing improves:

- **Search** — more videos become findable.
- **Labels** — "channel unknown" rows resolve.

and does **not** improve suggestions at all, because the graph the walk traverses
never grows. An earlier draft of this ticket listed "suggestions improve
measurably" as an acceptance criterion; that is unachievable at Level 2 and has
been removed.

This is the concrete form of the objection that catalogue alone is a dictionary
rather than a dataset. Prefetching a peer's neighbourhood is the right *fetch
strategy*, but at Level 2 it can only prefetch labels for videos the local walk
already reaches. Extending the walk into territory the user has never visited
requires `peer_edges`, which means Level 3 or 4.

## Transport — decide before building

The daemon is Go, so `go-libp2p` is the mature option: AutoNAT, circuit relay v2
and DCUtR hole punching are solved there, and content addressing comes free.
Alternatives are an IPFS node (heavier) or plain HTTPS from a project host
(simplest, least decentralised).

**Recommendation: libp2p, with a project-run node as the bootstrap peer and
first pin.** `DESIGN_v2` §7's cost ledger already budgets one modest VPS.

Whichever is chosen, **record the decision and why** before writing the client —
this is the choice that is expensive to reverse.

## Constraints

- **Level gate.** Nothing is published unless the stored contribution level is
  ≥ 2. Raise `MaxImplementedLevel` to 2 in the same change, not before.
- **Off by default.** Level 1 stays the default and must remain fully functional.
- **No edges, ever, at this level.** Enforced by a test.
- **Fetching is not publishing.** A Level 1 node may still *consume* the shared
  catalogue — it contributes nothing and receives the benefit. That asymmetry is
  deliberate: the privacy promise is not a toll booth.

## Acceptance

- [ ] Two daemons on the same machine (separate `KEEL_DB`) discover each other
      and exchange catalogue entries with no user action.
- [ ] A Level 1 node fetches and benefits, and publishes nothing — verified by
      inspecting what it offers.
- [ ] No edge data appears in any published artifact; asserted by test.
- [ ] Published artifacts are content-addressed and signed.
- [ ] Search improves measurably on the receiving node — compare `SearchVideos`
      hit counts before and after. **Suggestions are expected not to change**;
      assert that too, so the boundary is visible rather than mistaken for a bug.
- [ ] Transport decision recorded in `DESIGN_v2` or a design note.

---

# Part 2 — measuring overlap without publishing edges

Lars is right that catalogue alone compares nothing. It is a shared dictionary,
not a dataset: the research payload is edges, and so is the cross-user overlap
number from `DESIGN_BOOTSTRAP`'s aggregation appendix.

But **measuring overlap does not require publishing edges.** Each node computes a
HyperLogLog sketch over its own edge set; nodes exchange sketches; merging them
gives `|A ∪ B|`, which against `|A| + |B|` is exactly the number needed.

Why this is the right tool here:

- Sketches are a few KB regardless of corpus size.
- They cannot be enumerated. An HLL register array answers "roughly how many
  distinct items" and cannot be reversed into which items — so exchanging one
  reveals no edge, no video, and no viewing history.
- It needs no threshold, no population, and no trust in the peer. Two nodes are
  enough, which means it works *now*, during bootstrap, when STAR cannot.

## Sketches are also how the crawl picks peers

This is the reason to build sketching first rather than treating it as a
measurement side-quest.

The neighbour crawl has to answer "which peers are worth asking?" Without an
answer it either asks everyone — which does not scale and leaks interest in every
direction — or picks arbitrarily. A sketch of a peer's video set, merged against
the sketch of the walk's current neighbourhood, estimates overlap in a few KB.
That estimate *is* graph proximity, and it is the routing metric the crawl needs.

So one primitive does two jobs:

1. **Routing** — rank peers by estimated overlap with where the user currently
   is, and ask the top few.
2. **Research** — the same merge, run across whole corpora, yields the
   cross-user overlap figure that decides whether published output fits on free
   channels.

**Build it alongside the catalogue transport**, before the crawl's peer-selection
logic, since that logic depends on it.

## Why not private set intersection

PSI answers the same question exactly rather than approximately, and it was the
earlier proposal. It is deferred:

- It needs an interactive protocol per pair, so it cannot be used for cheap
  routing across many candidate peers.
- The routing use only needs a ranking, where an approximate answer is
  sufficient.
- Sketching is a few hundred lines and reuses one code path for both jobs.

Revisit PSI if an exact private intersection is ever needed for its own sake.
Nothing here forecloses it.

Acceptance: two nodes with disjoint-ish corpora exchange sketches and report
`|A|`, `|B|`, `|A ∪ B|` and the implied overlap fraction, with the estimate
checked against an exact union computed locally in the test.

---

# Part 3 — the Level 4 bootstrap question (decide, do not build yet)

Lars's plan: during bootstrap, offer "publish everything" (Level 4, full
attributed funnel), gather a real dataset, then move to STAR once there is a
population, turning Level 4 off and deleting what was published.

The sequencing reasoning is sound and solves a genuine chicken-and-egg — STAR
needs enough reporters to clear a threshold, so a small network can publish
nothing and can therefore never bootstrap. Early open participation is the
conventional way out.

**The deletion step works, on one condition: stay off permanent hosting.**

An earlier draft of this ticket claimed publication could not be undone. That is
true of *archival* infrastructure — IPFS pinning services, Zenodo DOIs, Academic
Torrents, Internet Archive — where the whole value proposition is that content
outlives the publisher. It is not true of a node serving files from its own disk.
If Lars runs the only serving node, deleting the files deletes them.

This inverts the framing in `DESIGN_v2` §7.3, which treats durable third-party
hosting as the goal. During bootstrap it is a liability, and the design principle
is the opposite:

> **Bootstrap data is served from participants' own disks and nowhere else.** No
> pinning service, no DOI, no archive. Losing a pilot corpus costs nothing;
> being unable to withdraw one costs everything. Durable hosting is adopted only
> once the published artifact is STAR output, which is safe to be permanent.

The residual is ordinary and small: a peer that has already fetched and merged
holds its own copy, which the publisher cannot reach. During bootstrap the
fetchers are a handful of known collaborators, so this is the same risk as
sending a colleague a file — not the un-recallable global archive the earlier
draft described.

Consent copy should therefore say withdrawal removes the data from distribution
but cannot reclaim copies already fetched. That is both accurate and a promise
that can be kept.

Do not raise `MaxImplementedLevel` past 2 in this ticket. Level 4 needs its own
work order and its own consent review.

**Also flag:** `PRIVACY.md` currently states "nothing is sent today at any
setting" and describes contribution as not built. Both become false the moment
this ships. The policy must be updated in the same release, and the extension is
supposed to announce material changes before they take effect.
