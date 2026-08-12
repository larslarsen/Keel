# Keel

Browser extension + local Go daemon. Gives people control over the video recommendations they see.

**Target browsers: Brave (primary), Chrome, Chromium, Edge, Firefox.** Brave is Chromium, so
`manifest.chrome.json` applies unchanged; only the native-messaging host path differs per browser
(see `daemon/README.md`). QA runs on Brave with Shields on.

## Read before coding

1. `handoff/WO-088-capability-controls-stay-visible.md` — **current implementation work order. Start here.**
   `handoff/README.md` indexes every order and its status. WO-085 (reciprocal
   distributed peer search + per-node serving limits) closed 2026-08-12.
2. `ARCHITECTURE_CURRENT.md` — normative current architecture and implementation order.
3. `ROADMAP.md` — product stages and current stabilization queue.
4. `BUILD_P0.md` — P0 spec; §9 is the closed acceptance record.
5. `DESIGN_v2.md` — rationale and history. §2.1, §3, §6.0 remain load-bearing
   where `ARCHITECTURE_CURRENT.md` does not explicitly amend them.

`handoff/README.md` indexes every work order.

`RENAME_NOTE.md` and `HANDOFF.md` are superseded. Ignore them.

## Hard rules

Each traces to a legal or threat-model requirement in `DESIGN_v2.md`. Breaking one silently breaks
the project, not just the code. If one seems wrong, say so and cite the section — don't route around it.

- **No observation data in browser storage.** Not IndexedDB, not `chrome.storage`, not
  `localStorage`. In-memory only, bounded (~200), flushed on reconnect. (§2.1)
- **Level 1 is a full consumer and a live-gossip participant.** It may receive
  the seed, fetch graph/catalogue/search buckets, pre-walk the graph,
  receive/relay/originate authorless livestream
  notices, and exchange the whole fixed-shape word-level HLL/CMS telemetry pack.
  It must not serve cached or own blocks, announce block providers, originate
  or join three-gram yield/token-sketch topics, publish recommendation edges,
  or initiate user-triggered distributed peer search. Local search and shared
  suggestions/pre-walk remain on. Distributed peer search is reciprocal at
  Level 2+ because Level 2 also hosts the broad corpus that answers it.
  Broad block service begins at explicit Level 2 and includes locally derived
  plus cached graph blocks in complete hashed-prefix buckets; it is never a
  selected-video response. Level 3 adds STAR cohort measurement rather than the
  first local edge. (`ARCHITECTURE_CURRENT.md` §3, WO-084)
- **One per-user daemon owns SQLite and libp2p.** Browser-launched native-host
  processes are local IPC proxies only; never open the corpus or start a swarm.
  (`ARCHITECTURE_CURRENT.md` §5)
- **No MAIN-world scripts. No `fetch`/XHR interception.** Read the rendered DOM. (§4.1)
- **Never call the YouTube Data API.** Its terms force deletion within 30 days. (§3.2)
- **Never store a raw search query.** Hash it. (§4.2)
- **No runtime dependencies, no framework, no bundler, no build step.** Plain ES modules.
  `devDependencies` for tests only. Use `crypto.randomUUID()`, `crypto.subtle.digest()`,
  `MutationObserver`. (`BUILD_P0.md` §2)
- **Minimum permissions.** `["sidePanel", "storage", "nativeMessaging", "alarms", "scripting"]`
  plus the two named host permissions `*://www.youtube.com/*` and
  `*://www.tiktok.com/*`, matching `content_scripts` (WO-010/057). **No `tabs`,
  no optional permissions, no third-platform pattern and no wildcard web
  permission.** `scripting` plus those host permissions lets the SW watchdog
  (`keel-watchdog` in `sw.js`) re-inject `bootstrap.js` into already-open
  supported-site tabs after a reload/update (WO-008). Do not remove it as
  "unused" and do not use it to run in MAIN world. Pages that do not classify
  as a supported surface must stay fully idle. `alarms` is load
  bearing twice: `keel-native-reconnect` backoff survives SW eviction (WO-004 §7), and the standing
  `keel-watchdog` keeps the SW from silent idle eviction (WO-008).
- **Nothing named "YouTube", "YT", or any variant** in the extension name. Branding-guideline
  violation. (§3.1)
- **`extract.js` stays a pure function** — DOM subtree in, record out, no browser APIs — so it is
  testable against fixtures. It's the code that rots when YouTube changes their DOM.
- **Reconnect in `onDisconnect`.** Native messaging failures arrive asynchronously via
  `onDisconnect` + `runtime.lastError`, never as a throw. A `try/catch` around `connectNative` is
  dead code. (§8.1)

## Conventions

- Licence Apache-2.0. SPDX header on new source files: `// SPDX-License-Identifier: Apache-2.0`
- `v1_prototype/` is **not a reference** — ten documented defects in `DESIGN_v2.md` §5.1.
- Decisions live in documents, not chat. New decisions get a work order in `handoff/`.
- Don't widen scope. If something seems to need a later phase, that's a scope error — raise it.
- `DESIGN_INCENTIVES.md` — what each contribution level earns, and why Level 4 earns nothing.

## Host permissions — the TikTok exception (WO-057, 2026-08-08)

The hard rule was "no match patterns outside `youtube.com`". `*://www.tiktok.com/*`
is now in `host_permissions` and `content_scripts.matches`, deliberately.

Keel's claim is about recommender systems, not about one company. A second
platform is what turns "here is what YouTube shows you" into a comparison, and
the whole distribution argument — that the corpus is worth something to
researchers — is weaker with a single source.

What has **not** changed, and must not:

- No `tabs` permission, no optional permissions, no MAIN-world script.
- No new capability on YouTube. TikTok gets exactly what YouTube gets: read the
  rendered page, report structured records to the daemon.
- Adding a third platform is the same deliberate edit, not a wildcard. There is
  no `*://*/*` and there never should be — the permission a user grants should
  name the sites it covers.

The rule this replaces was right about the risk and wrong to be absolute: the
danger is broad or growing permissions, not a second named site.
