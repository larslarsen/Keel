# Keel

Browser extension + local Go daemon. Gives people control over the video recommendations they see.

**Target browsers: Brave (primary), Chrome, Chromium, Edge, Firefox.** Brave is Chromium, so
`manifest.chrome.json` applies unchanged; only the native-messaging host path differs per browser
(see `daemon/README.md`). QA runs on Brave with Shields on.

## Read before coding

1. `handoff/WO-047-remove-p2p-sharing-ux.md` — **current work order. Start here.**
   `handoff/README.md` indexes every order and its status.
2. `ROADMAP.md` — phase state and queue.
3. `BUILD_P0.md` — P0 spec; §9 is the closed acceptance record.
4. `DESIGN_v2.md` — architecture and rationale. §2.1, §3, §6.0 are load-bearing.

`handoff/README.md` indexes every work order.

`RENAME_NOTE.md` and `HANDOFF.md` are superseded. Ignore them.

## Hard rules

Each traces to a legal or threat-model requirement in `DESIGN_v2.md`. Breaking one silently breaks
the project, not just the code. If one seems wrong, say so and cite the section — don't route around it.

- **No observation data in browser storage.** Not IndexedDB, not `chrome.storage`, not
  `localStorage`. In-memory only, bounded (~200), flushed on reconnect. (§2.1)
- **No MAIN-world scripts. No `fetch`/XHR interception.** Read the rendered DOM. (§4.1)
- **Never call the YouTube Data API.** Its terms force deletion within 30 days. (§3.2)
- **Never store a raw search query.** Hash it. (§4.2)
- **No runtime dependencies, no framework, no bundler, no build step.** Plain ES modules.
  `devDependencies` for tests only. Use `crypto.randomUUID()`, `crypto.subtle.digest()`,
  `MutationObserver`. (`BUILD_P0.md` §2)
- **Minimum permissions.** `["sidePanel", "storage", "nativeMessaging", "alarms", "scripting"]`
  plus `host_permissions: ["*://www.youtube.com/*"]` — matches `content_scripts` (WO-010 widened
  from `/watch*` so homepage→video SPA navigations are observed). **No `tabs`, no optional
  permissions, no patterns outside `youtube.com`.** `scripting` + the host permission also let the
  SW watchdog (`keel-watchdog` in `sw.js`) re-inject `bootstrap.js` into already-open YouTube tabs
  after a reload/update (WO-008). Do not remove them as "unused" and do not use `scripting` to run
  in MAIN world. Off-surface pages (not `/` or `/watch`) must stay fully idle. `alarms` is load
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
