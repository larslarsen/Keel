# Keel

**Keel replaces YouTube's recommendations with your own.**

It hides the suggestion rail and puts a different list there — one computed on
your machine, from what you have actually been shown, with a control that runs
from *focus* to *serendipity*. Turn it up and it stops handing you the obvious
next thing.

That is the point. A recommendation engine optimised for watch time will find
whatever keeps you there, and you have no say in it and no record of it. Keel
gives you both.

## What it does

- **Suggests, rather than repeating.** A random walk over your own recorded
  graph. YouTube's rail is how the walk travels, never what it recommends — if
  a video was just offered to you, it is not a suggestion.
- **Keeps the receipts.** Every recommendation you were shown, on which surface,
  in which position, and when. Searchable. That record is the thing nobody
  otherwise has, including researchers.
- **Finds livestreams nobody else surfaces.** A live index shared between Keel
  users, showing streams *actually* live now rather than the popular handful
  YouTube's own live search returns. Most livestreams have almost no audience,
  which is exactly why they are worth finding.
- **Blocks channels properly.** Not "show fewer" — gone, everywhere.
- **Shows you what is being pushed hardest**, and which video led to which.

## Where your data goes

Nowhere, by default. The recording lives in a SQLite file on your machine, there
is no account, and there is no server to send it to — none exists.

Above the default you can lend disk space to fetch and re-serve the recommendation
graph *other* people have published. That is what will let your suggestions walk
past your own history — but at v0.1.0 there is no published seed and no automatic
peer data for the *suggestion walk*, so it stays within your own history until a
seed or bundle is imported. Search is different: turning on "search the network"
reaches peers on demand, with no seed needed — the first search for a term
populates it for whoever searches it next, so coverage grows from use rather than
from a published dataset. At every level that ships, nothing you
recorded leaves your machine; publishing your own observations would need
threshold encryption (Levels 3–4), which is not built yet.

The one exception, at every setting: when Keel sees a livestream it tells other
Keel users the stream exists. That notice carries no sender — not your address,
not an identifier for your copy of Keel. It says a stream is on, not who saw it.

Full detail in [PRIVACY.md](PRIVACY.md).

## Install

See **[INSTALL.md](INSTALL.md)** — build it yourself in about ten minutes. Not
in the Chrome Web Store yet.

## What you're installing

Keel asks you to build and run a program on your own machine, so here is what
that program is made of. Every claim below is checkable from this repository.

**The browser extension has no third-party code at all.** No framework, no
bundler, no runtime dependencies — plain ES modules, all of it in `extension/`.
There is nothing in it to compromise upstream, because there is no upstream.

**The desktop app has 7 direct dependencies**, listed in `daemon/go.mod`: the
libp2p networking stack, its Kademlia DHT and pubsub, multiformats, and a pure-Go
SQLite. Those pull in about 110 more transitively, all pinned by hash in
`daemon/go.sum` — a pinned dependency cannot be swapped for something else
without the build failing.

**The toolchain is pinned too.** `go.mod` names an exact Go release, so everyone
builds with the same compiler and standard library rather than whatever their
system happens to have. This is not cosmetic: it was worth 13 fixed
vulnerabilities the day it was added.

### What we check, and what we do not claim

On every push, and weekly regardless:

- a **CycloneDX software bill of materials** is generated and published as a
  build artifact, so you can see exactly what went into a binary;
- **`govulncheck`** reports known vulnerabilities, including whether our code
  actually reaches them rather than merely depends on something affected;
- **module hashes** are verified against `go.sum`;
- **direct dependencies** are compared against a written list, so a name nobody
  chose — a typo, or one invented by an AI writing an import — shows up.

**None of this blocks a release, on purpose.** One known issue
([GO-2024-3218](https://pkg.go.dev/vuln/GO-2024-3218), DHT censorship) has no
published fix, so a blocking scan would sit permanently red and everyone would
learn to ignore it. A check nobody reads is worse than no check. The reports are
there to be looked at, and that issue is written up in `DESIGN_v2.md` §7.4a —
what it costs us, what it does not, and what we did about it.

We are not claiming the dependency tree is audited line by line. It contains
libp2p, which is large. What we are claiming is that you can see all of it,
that it cannot change underneath you without the build breaking, and that we
look at it on a schedule and publish what we find.

## How it is built

- **Extension** — reads the *rendered* page only. No MAIN-world scripts, no
  `fetch`/XHR interception, no YouTube Data API calls. It sees the same rail you
  do and hands structured records to the daemon. It stores no observations
  itself, not in `localStorage`, not in extension storage.
- **Daemon (Go)** — the product. The corpus, the suggestion walk, blocking,
  search, thumbnails, and the peer-to-peer layer all live here. Splitting it this
  way is deliberate: the extension can stay small and stop changing, while the
  part that does the work keeps improving.
- **Peer-to-peer, no server.** Discovery rides the public IPFS DHT as a
  directory; nodes serve each other directly. Requests go out in buckets of
  thousands rather than naming one video, and network identity is a fresh key
  each session, so a peer answering you learns very little.

## Status

The local product works: recording, search, suggestions, channel blocking,
analysis, export and wipe. The peer-to-peer layer is built and passes its tests,
but has only ever run against loopback.

**The next milestone is people, not code.** Not one more person — several, and
the reasons are specific:

- **NAT traversal only means something across different networks.** Two machines
  behind the same router prove nothing about the case that actually matters.
- **A gossip mesh needs a handful of nodes to behave like one.** With two, every
  message is a direct hop and none of the routing is exercised.
- **The measurement that gates aggregate publication needs several independent
  corpora.** How much do two people's recommendations overlap? Two people give
  one number, and one number could be a coincidence.
- **The installer fails on machines we do not have.** Windows, macOS, browsers
  and Go versions we have never seen are where new people actually get stuck.
- **The livestream index is only useful once enough people watch enough
  different things.** One person's view of "what is live" is one person's.

If you want to help, [INSTALL.md](INSTALL.md) is about ten minutes, and an
issue saying what broke is worth more than a patch right now.

Aggregate publication under threshold encryption is designed and not built,
gated on the overlap measurement above.

Not in the Chrome Web Store yet. The plan is to apply once, with the whole thing
finished.

## Research

A recommendation system is a map from *who you are and what you just watched* to
*what you see next*. Run it over a whole population and you get a shape — a
manifold in recommendation-space: dense where millions of people's paths
overlap, thin and hard to see where only a few people ever get sent. The
questions everyone actually wants answered — what is being pushed hardest, where
do people get funnelled, what shows up in the tail — are questions about the
shape of that manifold. Keel is a way to measure it directly, from the user's own
screen, without asking the platform's permission.

**Why existing audits do not settle it**

There is no shortage of studies. Each one does one slice, and none of them give
the person being measured anything back:

- **Bot / sockpuppet audits** (the TikTok and YouTube radicalisation studies) run
  scripted accounts and watch what the feed serves. They are centralised,
  one-shot, and — per Mosnar et al., SIGIR 2025 — their findings often fail to
  reproduce weeks later, because the algorithm and the content both move.
- **Data donations** (e.g. Zannettou et al., CHI 2024) collect real users' feeds
  into a researcher's dataset. The user gets a paper, not a tool, and the data
  sits with the researchers.
- **Platform enclaves** (the Christchurch Call / LinkedIn / Dailymotion audit,
  using PySyft + OpenDP) only work because the platform agrees to be studied. A
  platform that cooperates in its own audit can fake the data or throttle itself
  during the test. That is a compliance exercise, not evidence you can trust.
- **Crowdsourced tools** (Mozilla's YouTube Regrets, NYU's Ad Observer) install on
  real browsers but send raw observations to a central research server.

What Keel does differently is the combination, not any one part:

1. **Raw observation never leaves the device.** You get personal value first —
   search your own recommendations, see which video led to which. The recording
   is yours.
2. **Sharing is opt-in and threshold-encrypted (STAR).** When built, you can
   contribute to a global count without any server learning your trail, and
   without the platform ever being involved. Until then, what moves is graph
   data *other people* have published (which your node pulls and relays) and,
   for search, whatever the network has already been asked for — demand-driven,
   not published.
3. **No platform cooperation required.** The measurement is taken from the page
   you actually see, so it cannot be gamed from the server side the way an
   enclave audit can.
4. **The graph is decentralised.** What users actually exchange today is the
   recommendation graph itself — pulled neighbourhood by neighbourhood and
   re-served — not a central database and not your raw history.

The bucket system is what will make peer sharing private *when there is a graph
to share*. When a node asks the network it never requests one video's
neighbourhood — it asks for every neighbourhood whose key falls in a prefix
bucket (thousands of videos at once, hashed so the buckets are evenly
populated) and takes the whole bucket. There is no "real request hidden among
decoys" to statistically separate, because the node genuinely takes everything
in the bucket. The blocks fetched for cover are exactly the blocks that would
make the node a useful mirror for others, so Level 2's privacy mechanism and its
contribution are the *same act*, and the disk budget you set is the anonymity
parameter. Combined with a fresh network identity per session, a peer answering
you learns almost nothing about what you watched.

**What is already known, and what is not**

The phenomenon is real. Twitter's own 2021 study and Huszár et al. (PNAS 2022)
both found the algorithmic timeline amplifies political content. Haroon et al.
(2022) found YouTube's watch-next pushes right-leaning users toward increasingly
extreme content. The Christchurch Call audit measured that on Dailymotion the top
10% of videos receive 75% of all recommendations — a real concentration skew. The
Facebook Papers show the algorithm was explicitly tuned to reward outrage.

What is *not* settled: the universal "filter bubble" claim is contested, and
TikTok's radicalisation in particular is currently underproven — the SIGIR 2025
paper could not reproduce earlier TikTok audit findings at all. So the bulk of
the manifold is mapped; the tail is not.

**The honest limit of the method**

STAR only resolves the manifold where at least K people (≥50) report an identical
measurement — it sees the dense regions and is structurally blind to the rare
pathways, which are exactly the ones that matter most. Reaching the tail needs
Prio + differential privacy (OpenDP), which is designed-for but deferred until the
population is large enough that the noise does not drown the signal. So Keel
measures the bulk now and the tail later; it does not pretend the bulk describes
the tail.

**For researchers**

At v0.1.0 the motivated early adopters are people who want to measure this, not
casual users. If that is you: install it, run it against your own YouTube, and
the per-person record is the raw material — the one piece that already works
fully. The peer graph layer (seed bundle plus swarm relay) is built and passes
its tests, but there is no published seed and no automatic peer data yet, so the
suggestion walk reaches past your own history only once a seed or bundle is
imported. The aggregate that becomes the empirical density estimate of the
population manifold is designed-for but not yet populated; when independent
corpora exist, the pipeline can re-run continuously rather than once, which is
what resists the one-shot decay that sinks the sockpuppet studies. The design is
in `DESIGN_v2.md` (§6 for the aggregation, §6.3 for cohort rules); the
contribution levels are in `handoff/WO-051`. An install that breaks on your
machine is a bug report we want.

## Repository layout

| Path | Purpose |
|------|---------|
| `extension/` | The browser extension (ES modules, no build step). |
| `daemon/` | The local Go daemon (blocking, corpus, native-messaging host). |
| `test/` | Fixture-driven extraction tests (logged-out captures). |
| `handoff/` | Work orders and the index of what's done. |
| `DESIGN_v2.md` | Architecture and rationale (load-bearing sections marked). |
| `BUILD_P0.md` | P0 spec and acceptance record. |
| `ROADMAP.md` | Phase state and queue. |
| `INSTALL.md` | Build and install, for people who have never built software. |
| `PRIVACY.md` | What is recorded, what leaves, and what does not. |

## Licence

Apache-2.0. See `LICENSE` and `NOTICE`.

---

This repository is the public source tree. Internal strategy notes and
pre-decision drafts are kept outside this tree.
