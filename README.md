# Keel

Browser extension + local daemon that give you **transparency and control over
the video recommendations you're shown** — starting with YouTube.

Keel shows you what the recommendation engine is actually serving, and lets you
act on it (hide, downrank, surface alternatives) without sending your viewing
history anywhere it shouldn't go.

## Install

See **[INSTALL.md](INSTALL.md)** — build it yourself in about ten minutes. Not
in the Chrome Web Store yet.

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
