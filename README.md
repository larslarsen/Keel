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

Above the default you can lend disk space to mirror what *other* people have
published, which is what lets suggestions reach past your own history. Even
then, nothing you recorded is published: that needs threshold encryption, which
is not built.

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
