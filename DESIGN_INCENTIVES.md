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

## Level 2 — mirror the public dataset

**Earns: a warm cache. Instant suggestions instead of a fetch on every cold hop.**

Per `DESIGN_BOOTSTRAP` §5d, Level 2 is not "publish your catalogue" — it is
holding and serving blocks of the public aggregate. Nothing personal leaves. The
node contributes storage and bandwidth, the way a seeder does.

The incentive is unusually clean because **the cost and the benefit are the same
object**. The disk-space slider sizes an LRU cache of neighbourhoods; that cache
is what makes the user's own hops instant, and it is simultaneously what other
people fetch from. Giving more storage directly buys a better local experience.

No withholding is involved, which is why this rung needs no policy at all.

Note that a Level 1 node still fetches blocks and still gets working suggestions
(§5d: "out of the box for every user"). Level 2's benefit is latency and depth of
cache, not access.

## Level 3 — cohort

**Earns: comparison — the visualizer.**

An earlier draft claimed this was the first level where the walk leaves the
user's own history. That is wrong: block fetch does that at any level, per §5d.
What Level 3 adds is the user's own aggregated edges to the shared pool, which
deepens the dataset everyone draws on rather than unlocking anything locally.

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

## Why this ladder needs almost no policy

Only Level 3 requires a designed reward. Levels 1 and 2 are self-incentivising
because the thing the user gives is the thing the user gets — the funnel
inspector runs on their own recording, and the block cache is both contribution
and local speed. Level 4 is chosen by people who already want the outcome.

A previous draft proposed sketch-based peer ranking as an emergent incentive.
Dropped: §5d addresses blocks by key, so there is no peer ranking to exploit.

## Open question for Lars

The visualizer is a real build — comparison needs a cohort baseline, a diff, and
a way to render it that a non-technical person reads in seconds. Worth its own
work order once Level 3 has data to draw on, and not before, since building it
against an empty cohort would design it blind.
