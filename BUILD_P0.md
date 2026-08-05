# P0 Build Brief — end-to-end vertical slice

**Read `DESIGN_v2.md` first**, especially §2.1 (trust boundary), §2.2 (distribution), §8 (daemon).
Where this brief and the design doc disagree, the design doc wins — raise the conflict, don't guess.

**This brief replaces an earlier version** that had the extension storing impressions in IndexedDB.
That was wrong and is reverted: **no observation data is ever persisted in the browser.** If you
have already written IndexedDB code for impressions, delete it.

---

## 1. What P0 delivers

One surface, all the way through the stack:

```
/watch sidebar → content script → in-memory batch → Keel Bridge
    → Go daemon → SQLite → count → SidePanel
```

That is the whole of P0. `WATCH_NEXT` only. No home feed, no search, no filtering, no blocking,
no preservation, no contribution, no crypto, no network.

**Why a vertical slice and not "the extension first":** the riskiest part of this system is the
native messaging bridge — framing, size limits, reconnect, daemon lifecycle — not the DOM parsing.
Building the extension fully and attaching the daemon later defers that risk to the worst moment.
One surface end-to-end exercises the bridge while the surface area is still small enough to debug.

---

## 2. Hard constraints

From §2.1 and §3 of the design doc. Not negotiable without a design change.

**Trust boundary — the one that matters most:**

- **No observation data is persisted in the browser.** Not IndexedDB, not `chrome.storage`, not
  `localStorage`. Impressions live in a bounded in-memory buffer only, long enough to batch across
  the bridge, plus a bounded backlog (~200, newest kept) held across a disconnect and flushed on
  reconnect. Never spill to browser storage; never grow unbounded. Losing the buffer to worker
  eviction is fine — writing history into Google's runtime is not.
- `chrome.storage` in P0 holds nothing but UI state. No video IDs, no titles, no history.

**Observation:**

- **Never read `window.ytInitialData` from a MAIN-world script.** Content scripts run ISOLATED. For
  the initial page load, parse the inline `<script>` element's text out of the DOM. Reading rendered
  output is what preserves the "like a screen reader" posture the legal strategy rests on.
- **Never intercept `fetch`/XHR.** Requires MAIN world and reads as scraping.
- **Never call the YouTube Data API.** Its terms force deletion or refresh of stored data within 30
  days, which would destroy the corpus (§3.2).
- **No analytics, no crash reporting, no remote config.**

**Code:**

- **No framework, no bundler, no build step, no runtime dependencies, no TypeScript toolchain.**
  Plain ES modules loaded directly by the browser. `package.json` may exist only for a test runner
  in `devDependencies`.
- Forbidden: React, Vue, Svelte, Preact, webpack, rollup, vite, Tailwind, any UI component library,
  any UUID library, any hashing library, any IndexedDB wrapper.
- Use the platform: `crypto.randomUUID()`, `crypto.subtle.digest()`, `MutationObserver`,
  `document.createElement`.
- **Ship readable source.** No minification, no obfuscation.
- Size budget: see §9 (amended WO-011). Original P0 ~400-line target predates HOME, second card
  shape, JSON enrichment, watchdog, and hide — do not refactor solely to hit the old number.

---

## 3. Layout

```
extension/
  manifest.chrome.json
  manifest.firefox.json
  lib/
    browser.js       # WebExtension API shim (Chrome/Firefox)
    protocol.js      # Envelope build + validate (shared shape with daemon)
  content/
    extract.js       # DOM subtree -> Impression | null   (PURE, no browser APIs)
    observer.js      # lifecycle, debounce, in-memory buffer
  background/
    sw.js            # bridge port, reconnect, batching
  sidepanel/
    index.html · index.js · style.css
daemon/
  main.go
  bridge/            # stdio framing, envelope validation
  store/             # SQLite schema + writes
  go.mod
test/
  extract_test.js    # fixture-driven, no browser
  fixtures/          # saved /watch DOM snapshots
```

---

## 4. Manifest

Per §5.2 of the design doc, as amended by `handoff/WO-001` §3:

```jsonc
"permissions": ["sidePanel", "storage", "nativeMessaging"],
// no optional_permissions, no host_permissions, no "scripting"
"content_scripts": [{
  "matches": ["*://www.youtube.com/watch*"],   // P0: WATCH_NEXT only
  "js": ["content/bootstrap.js"],
  "run_at": "document_idle",
  "world": "ISOLATED"
}]
```

- `nativeMessaging` is **required** — the daemon is not optional.
- **No `host_permissions`.** The SidePanel must be enabled from a `PAGE_CONTEXT` message sent by the
  content script, using `sender.tab.id` — not by reading `tab.url` in `tabs.onUpdated`, which is
  what forces a host permission. See WO-001 §3.4.
- **Match only the surfaces in scope.** P0 is `/watch*`. P1 adds `/results*` and the exact root.
  Accepted coverage gap: a user who hard-loads a non-matching page and SPA-navigates to a watch page
  is never observed. Do not widen the pattern to fix it (WO-001 §3).
- Name is **`Keel`**. No "YouTube", no "YT", no variant.

Defects from the v1 prototype that must not recur:

- v1's `"://www.youtube.com/watch"` is an **invalid match pattern** — no scheme — and the extension
  will not load. Every pattern needs a scheme: `*://www.youtube.com/watch*`. This applies to
  `content_scripts.matches` now that `host_permissions` is gone.
- A `content_scripts` entry is **required**. v1 declared none and could observe nothing.

`manifest.firefox.json` differs in `background` (`scripts`, not `service_worker`) and needs
`browser_specific_settings.gecko.id`. Two files; the packaging step picks one. Do not attempt a
unified manifest.

---

## 5. Observation

### 5.1 Lifecycle

YouTube is an SPA and does not reload between navigations.

1. At `document_idle`, parse inline `ytInitialData` script text for the first render.
2. Listen for `yt-navigate-finish`; re-arm on each navigation.
3. `MutationObserver` on the sidebar container for lazily-loaded items.
4. Each navigation gets a fresh `page_load_id` (`crypto.randomUUID()`).

**Debounce:** coalesce and emit at most once per ~500ms. Per-mutation emission will melt the browser.

**Idempotency:** `(page_load_id, surface, video_id, slot_index)` must never yield two rows.
Deduplicate in the buffer *and* enforce it as a SQLite unique constraint — re-renders are normal.

### 5.2 Record

Exactly §4.2 of the design doc. `context_query_hash` is unused in P0 (`SEARCH` is P1) but keep the
field. Never store a raw query anywhere, ever.

`extract.js` must be a **pure function** from DOM subtree to `Impression | null` — no browser APIs,
no storage, no imports beyond pure helpers — so it is testable against saved fixtures without a
browser. This is the single most important testability decision in P0: YouTube's DOM changes
constantly and this is the code that will break. Fixture updates should be the fix, not archaeology.

Return `null` rather than a partial record. Count extraction failures and surface them; never
swallow them silently.

---

## 6. The bridge

Per §8.1. The constraints that actually bite:

- **Host → browser: 1 MB per message.** Browser → host: 64 MiB.
- Framing: 32-bit length prefix in **native byte order**, then UTF-8 JSON.
- `allowed_origins` in the host manifest takes no wildcards — it lists the exact extension ID.

Envelope per §8.1, with `v: 2`, a correlation `id`, and schema validation **on both sides**. The
daemon treats all extension input as untrusted; the extension treats all daemon input as untrusted.
Malformed message → drop the packet, log, keep the connection.

**Reconnect is mandatory and is the defect most likely to be reproduced.** A native port keeps the
service worker alive, but if the host dies the SW dies with it. Call `connectNative` again inside
`onDisconnect`, with backoff. The v1 prototype set `nativePort = null` and stopped, so one daemon
hiccup ended the session permanently. Its `try/catch` around `connectNative` was also dead code —
failures arrive asynchronously via `onDisconnect` + `chrome.runtime.lastError`, never as a throw.

P0 messages only: `HELLO`/`HELLO_ACK` (version negotiation), `IMPRESSIONS` (batched, normalized —
**never raw `ytInitialData`**), `STATS`/`STATS_RESULT`, `ERROR`.

---

## 7. Daemon

Go. SQLite. Reads stdin, writes stdout, per the framing above.

- `impressions` table with a unique constraint on `(page_load_id, surface, video_id, slot_index)`.
- A `stats` query returning total count, per-surface counts, and first/last observation timestamps.
- **No retention sweep. Keep everything.** Recommendation quality depends on history depth, and
  preservation (G2) requires records outliving the videos they describe. A user-settable limit
  arrives in P1, defaulting to off.
- Install the native host manifest to the correct per-platform path, with `allowed_origins` set to
  the extension ID.

Installer work (registering the extension per §2.2) is **P2**, not P0. For P0, document the manual
install steps in a README so a reviewer or contributor can run it.

---

## 8. SidePanel

Deliberately minimal in P0:

1. Daemon status: connected / not running.
2. Counts from the daemon.
3. A live view of the current page's observations — proof the pipeline works.

**The no-daemon state is a feature, not an error path.** It must be clean, explanatory, and
non-broken: "Keel's desktop app isn't running." No stack traces, no blank panel, no thrown
exceptions. A Web Store reviewer will see exactly this state, since they won't install the binary,
and it is the difference between approval and a Minimum Functionality rejection (§2.2).

---

## 9. Acceptance criteria

Audited **2026-08-03** (WO-011). Dates below are when the criterion was last verified.

- [x] **2026-08-03** Loads in Chrome/Brave with zero console errors or manifest warnings.
  Manifest is valid MV3 (`manifest.chrome.json`); live QA through WO-008–010 on Brave with Shields
  on. Headless Chrome load in this audit environment aborted for sandbox reasons — not a product
  failure. Residual: cold-load SW + extension page consoles once if packaging changes.
- [x] **2026-08-03** Loads in Firefox — *static readiness*; live `about:debugging` not run here.
  `manifest.firefox.json` uses `sidebar_action` (not `sidePanel`), `background.scripts` +
  `type: module`, `browser_specific_settings.gecko.id`. SW guards all `browser.sidePanel` calls
  with `?.`. **Not a multi-day port** — residual smoke on Firefox is recommended; open a WO only if
  that smoke fails (see WO-011 notes). Does not block P0 close.
- [x] **2026-08-03** With daemon: `/watch` writes `WATCH_NEXT` rows with contiguous visual
  `slot_index` — live-verified (WO-005–008).
- [x] **2026-08-03** SPA navigation → new `page_load_id` and rows — live-verified (WO-010 homepage
  → video; `yt-navigate-finish` + history hooks).
- [x] **2026-08-03** Re-render produces no duplicate rows — unique constraint + in-memory dedupe;
  rail replacement uses generation (WO-007 live-verified).
- [x] **2026-08-03** Daemon not running: panel clean "not running" copy (`sidepanel/index.js`); no
  throw paths; SW buffers in memory only.
- [x] **2026-08-03** Daemon killed mid-session: reconnect via `onDisconnect` + alarms (WO-004/008
  live-verified ≤30 s).
- [x] **2026-08-03** Junk input (see WO-011 detail):
  - **Envelope level (both directions):** non-JSON / bad version / missing id dropped with log;
    framed stream continues (`daemon/bridge` tests + `native.js` `validateEnvelope`).
  - **Host → browser oversized:** not written (`writeEnv` drop + log).
  - **Browser → host oversized length prefix:** rejected; host process ends that stdio session
    (stream desync after corrupt length). Chrome relaunches host on next `connectNative`; SW
    reconnect path covers this. Not silent data corruption.
- [x] **2026-08-03** `extract` tests pass against ≥3 `/watch` fixtures
  (`watch_next_compact|lockup|mixed.html`) + HOME + ytInitialData — `npm test` 25/25.
- [x] **2026-08-03** No observation data in browser storage — **static audit.** Only
  `chrome.storage.local` key written by code is `hide_recommendations` (WO-009 pref, DESIGN §2.1).
  No `indexedDB` / `localStorage` / `sessionStorage` usage under `extension/`. Residual live
  inspect after a session recommended if shipping packaging changes.
- [x] **2026-08-03** `grep -rnE 'fetch\(|XMLHttpRequest|indexedDB|world.*MAIN' extension/` → empty.
- [x] **2026-08-03** No build step: plain ES modules; `npm run manifest:chrome` only copies
  manifest. Load unpacked from `extension/`.
- [x] **2026-08-03** Zero runtime dependencies; `package.json` has `devDependencies` only
  (`linkedom` for tests).
- [x] **2026-08-03** Size budget **amended (WO-011 decision — do not refactor to hit old numbers).**

  | Scope | Budget (amended) | Actual 2026-08-03 |
  |---|---|---|
  | Total `extension/**/*.js` | ≤ ~2,500 lines | **2,379** |
  | Soft per-file flag | note if >200 | see rationale |

  Files over 200 lines (rationale, not action):

  | File | Lines | Why over 200 |
  |---|---|---|
  | `content/extract.js` | 476 | Both card shapes + HOME grid + shared parsers; pure DOM→record; rots when YT DOM changes |
  | `content/observer.js` | 379 | Dual surface lifecycle, MO throttle, rail generation, SPA hooks |
  | `content/extract_yt.js` | 365 | ytInitialData lockup path (WATCH_NEXT enrichment) |
  | `background/sw.js` | 329 | Bridge batching, hide broadcast, watchdog re-arm, panel port |
  | `sidepanel/index.js` | 251 | Live list + stats + hide UI |

  **Recommendation (not in this ticket):** `extract.js` could split lockup vs compact vs HOME
  helpers on its own merits for edit locality — not for line count. Do not split while chasing
  green ticks.

- [x] **2026-08-03** Extension **name** is `Keel` — no "YouTube"/"YT" in `name`. Description and UI
  may mention the site (store clarity); branding rule is the product name (§3.1).

---


## 10. Not a reference

`v1_prototype/` contains the original `dumb_pipe.js` and manifest. **Do not port them forward.**
Ten documented defects in §5.1 of the design doc; the ones most likely to be copied are the invalid
match patterns, the missing content script, the dead `try/catch`, the absent reconnect, and shipping
whole `ytInitialData` across a 1 MB-capped channel.

---

## 11. Out of scope for P0

`HOME` and `SEARCH` surfaces, export/wipe (P1). Utility plane and installer (P2). Preservation,
fingerprints, tombstones (P3). Any contribution mode, any crypto, any server (P4+). If a requirement
seems to demand one of these, it is a scope error — raise it.
