# Design note — what each contribution level earns

Drafted 2026-08-05, from Lars: each level should carry an incentive, except
Level 4, which does not need one.

## The rule this has to obey

An incentive must be a benefit that is **intrinsically** coupled to the thing
contributed — not an unrelated working feature withheld to force a trade. A
Level 1 node may receive the seed, fetch shared graph/catalogue/search data for
suggestions and pre-walk the graph without serving blocks, because "the privacy
promise is not a toll booth." WO-085 draws a narrower capacity boundary:
arbitrary user-triggered distributed search is reciprocal at Level 2 because
those users also host the broad search corpus that answers it.

That constraint is worth keeping. Keel's pitch is that recording is for the
user; Level 1 therefore keeps its local history, search and recommendation
paths. Reciprocity applies only to the open-ended distributed query path.

Fortunately the architecture supplies real intrinsic differences.

## Level 1 — personal

**Earns: the funnel inspector.** Already built (WO-018). Your own recommendation
history, searchable, with channel blocking and hiding.

This is the personal product: local search and funnel inspection, shared
suggestions, graph pre-walk and fetched global word statistics. The shared Live
feed and search across other people's recommendations are not part of Level 1;
they begin at Level 2 under WO-089 and WO-085 respectively.

Level 1 is not offline. It makes whole-prefix requests and fetches WO-068's
whole fixed-shape word HLL/CMS aggregate. It does not participate in Live,
answer the word protocol, include its local corpus in an outbound word pack,
serve graph/catalogue/search blocks, or join the three-gram yield/token-sketch
topics that advertise and size those blocks. See `ARCHITECTURE_CURRENT.md` §3.

## Level 2 — broad sharing

**Earns: the shared Live feed, distributed peer search, a warm cache, and
visible contribution impact.**

Level 2 holds and serves complete hashed-prefix buckets containing both its own
aggregated, stringless graph blocks and blocks cached from peers. Broadness is
the privacy mechanism: the unit exposed and transferred is the whole bucket,
never one selected neighbourhood. It contributes data as well as storage and
bandwidth; copy must not claim that nothing local leaves.

The incentive is unusually clean because **the cost and the benefit are the same
object**. A Level-2 node hosts broad graph/catalogue/search buckets and may ask
other Level-2 nodes to perform arbitrary searches over theirs. The disk-space
slider also sizes an LRU cache of neighbourhoods; that cache makes the user's
own hops instant and is simultaneously what other people fetch from.

Note that a Level 1 node still fetches blocks and still gets working suggestions
(§5d: "out of the box for every user"). Level 2's normal suggestion benefit is
latency and depth of cache; its additional reciprocal capability is
user-triggered distributed search. Level 2 begins serving its full eligible local-plus-cached
graph, catalogue and search corpus and originates the three-gram availability/
sketch signals that describe exactly that served corpus.

WO-086 adds a local aggregate impact view: eligible material, buckets announced,
requests answered and bytes served, without query logs or peer histories.

## Level 3 — cohort

**Earns: comparison — the visualizer.**

An earlier draft claimed this was the first level where the walk leaves the
user's own history. That is wrong: block fetch does that at any level, per §5d.
Level 2 already adds broad aggregated graph blocks to the shared pool. What
Level 3 adds is STAR-protected cohort measurement and the comparison baseline,
not the first locally derived edge.

The real reward here is that a **visualizer** becomes possible and not before:
"how does my
feed compare to everyone else's" requires a cohort to compare against. At Levels
1–2 there is nothing on the other side of the comparison. This is intrinsic, not
withheld — the feature cannot exist without the data.

Strongest candidate for the reward Lars is after. It is visual, it is the
project's whole thesis rendered for one person, and it is impossible alone.

## Level 4 — transparency

**Earns nothing, by design.** People who publish an attributed funnel do it
because they want it public — researchers, journalists, anyone demonstrating
where a feed leads. Attaching a perk would attract contributors who have not
thought about permanence, which is the population this level should least
attract.

## Why this ladder is coherent

Levels 1 and 2 are self-incentivising because the thing the user gives is the
thing the user gets: the funnel inspector runs on their own recording, while
Level 2 contributes distributed-search capacity and receives it. Level 3 earns
the comparison product that only a protected cohort can create. Level 4 is
chosen by people who already want the public outcome.

A previous draft proposed sketch-based peer ranking as an emergent incentive.
Dropped: §5d addresses blocks by key, so there is no peer ranking to exploit.

## Recorded future delivery

The visualizer is a real build — comparison needs a cohort baseline, a diff, and
a way to render it that a non-technical person reads in seconds. Worth its own
work order once Level 3 has data to draw on, and not before, since building it
against an empty cohort would design it blind. WO-087 records that dependency.
