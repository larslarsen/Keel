# Design note — what each contribution level earns

Drafted 2026-08-05, from Lars: each level should carry an incentive, except
Level 4, which does not need one.

## The rule this has to obey

An incentive must be a benefit that is **intrinsically** unavailable without
contributing — not a working feature withheld to force a trade. `WO-052` already
commits to the latter being off the table: a Level 1 node may consume the shared
catalogue and contribute nothing, because "the privacy promise is not a toll
booth."

That constraint is worth keeping. Keel's pitch is that recording is for the user;
the moment privacy costs features, the pitch inverts and every level above 1
reads as a fee.

Fortunately the architecture supplies real intrinsic differences.

## Level 1 — personal

**Earns: the funnel inspector.** Already built (WO-018). Your own recommendation
history, searchable, with channel blocking and hiding.

This is the product. It is complete at Level 1 and must stay that way.

## Level 2 — catalogue

**Earns: search that reaches past your own history, and correct labels.**

`peerGraph()` (`daemon/store/peers.go:155`) reads `peer_edges` only — nothing in
the suggestion walk touches `peer_catalogue`. So Level 2 delivers exactly two
things: more videos become findable, and "channel unknown" rows resolve.

Honest framing matters here. Level 2 is a shared dictionary, and overselling it
as better recommendations will be immediately falsified by the user's own panel.

## Level 3 — cohort

**Earns: suggestions that leave your own history, and comparison.**

This is the first level where the walk genuinely changes, because aggregate edges
land in `peer_edges` and the graph grows past where the user has personally been.

It is also where a **visualizer** becomes possible and not before: "how does my
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

## The incentive nobody has to design

Sketch-based peer routing (`WO-052` Part 2) ranks peers by estimated overlap with
where the user currently is. A node holding nothing useful ranks low everywhere,
so peers stop selecting it and it gets slower, thinner service.

That is not a rule and not a punishment — it is what ranking by usefulness does.
It is the one incentive that needs no policy, cannot be argued about, and does
not require withholding anything from anyone.

## Open question for Lars

The visualizer is a real build — comparison needs a cohort baseline, a diff, and
a way to render it that a non-technical person reads in seconds. Worth its own
work order once Level 3 has data to draw on, and not before, since building it
against an empty cohort would design it blind.
