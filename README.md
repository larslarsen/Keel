# Keel

Browser extension + local daemon that give you **transparency and control over
the video recommendations you're shown** — starting with YouTube.

Keel shows you what the recommendation engine is actually serving, and lets you
act on it (hide, downrank, surface alternatives) without sending your viewing
history anywhere it shouldn't go.

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

## What it is

- **Extension** — reads the *rendered* page DOM only. No MAIN-world scripts, no
  `fetch`/XHR interception, no YouTube Data API calls. It observes the rail you
  already see and reports structured records to the daemon.
- **Daemon (Go)** — the product. Owns blocking/downranking decisions and the
  local contribution corpus. Observation data is held in memory and bounded; it
  is never persisted in browser storage.
- **Publication is metadata-only.** The release path aggregates signals (STAR)
  and publishes over free channels, so the project stays clear of video-archiving
  exposure.

## Why

Recommendation feeds shape what a large share of people watch, with little
visibility into *why* a given video appeared or *how* to push back on it. Keel
makes the feed legible and gives the viewer a local, auditable control surface.

## Privacy posture

- No observation data in browser storage (in-memory, bounded, flushed on reconnect).
- No raw search queries stored — queries are hashed.
- No runtime dependencies, no framework, no bundler, no build step in the
  extension. Plain ES modules; the daemon is Go.
- Minimum permissions: `sidePanel`, `storage`, `nativeMessaging`, `alarms`,
  `scripting`, plus host access to `youtube.com` only.

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

## Status

P0 is implemented and the fixture-driven test suite passes. This is an early,
serving-phase project: the local control surface works; cross-user aggregation is
the next stage.

## Licence

Apache-2.0. See `LICENSE` and `NOTICE`.

---

This repository is the public source tree. Internal strategy notes and
pre-decision drafts are kept outside this tree.
