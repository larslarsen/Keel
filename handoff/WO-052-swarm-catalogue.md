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

**3. Merge.** Straight into `peer_catalogue`, which already exists and is already
unioned into search (WO-031) and suggestions (WO-027). No new merge logic — this
is the consumption path those tables were built for.

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
- [ ] Search and suggestions improve measurably on the receiving node — compare
      `SearchVideos` hit counts and `Suggest` graph size before and after.
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

**Build this alongside the catalogue transport.** It reuses the same peer
connection, it is small, and it produces the single number that decides whether
the published dataset can live on free channels at all — a decision currently
blocking the STAR client design.

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
