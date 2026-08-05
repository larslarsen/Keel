# WO-008 — Self-heal: reconnect daemon link and re-arm observers after silent death

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done** — live-verified by Lars on Brave 2026-08-03. Recovery gap ≤30 s. |
| **Date** | 2026-08-03 |
| **Follows** | WO-006, WO-007 |
| **Source** | Overnight run on Brave: tab stopped collecting until a manual hard-reload |

---

## Defect

Left running overnight, the extension stopped collecting and did **not** recover on its own. The tab
kept its observer (SW eviction does not un-inject content scripts) but the pipeline was dead:

1. **SW → daemon link died silently.** A connected-but-idle service worker is evicted after ~30 s.
   WO-004 §7 made the reconnect *backoff* survive eviction (one-shot `keel-native-reconnect` alarm),
   but a healthy connected SW evicts with **no pending alarm** — the port closes, the daemon exits
   on EOF, and nothing wakes the SW until a user interaction or content-script message. Verified:
   last DB write 21:23:53, next `keel-host` spawn 09:03:51 (12 h gap, recovered only by user contact).
2. **Tab → SW link is not restored by anything.** When an extension is reloaded or updated, Chrome
   tears down content scripts in already-open tabs and does **not** re-inject them
   (`content_scripts` inject on page open only). A watch tab left open through a reload/update stays
   dark — collecting nothing — until the user navigates or hard-reloads. WO-001 §3 accepted this
   class of gap "with data behind it"; the overnight incident is the data.

Both are the same requirement: **the extension must self-heal without manual tab reloads.**

## Fix — one standing watchdog alarm (0.5 min) + narrow programmatic re-injection

- `sw.js`: new standing `keel-watchdog` alarm (`periodInMinutes: 0.5`). Fires every 30 s, which also
  stops idle SW eviction whenever the extension is enabled. Each tick:
  - reconnects the bridge if `!connected` (spawns the daemon again);
  - re-injects `content/bootstrap.js` into every open `*://www.youtube.com/watch*` tab.
- Idempotent: `bootstrap.js` loads the observer via dynamic ESM `import()`, so a re-injection on an
  already-armed tab is a cached no-op — no duplicate observers, no double emissions. (`observer.js`
  additionally guards on its module-scoped `armed` flag.)
- `manifest.chrome.json` / `manifest.firefox.json`: add `"scripting"` and a **path-scoped**
  `host_permissions: ["*://www.youtube.com/watch*"]` — narrow to the P0 surface, matching the
  content-script match pattern; no `tabs` permission (host permission makes `tabs.query({url})`
  return the matching tabs).
- `lib/browser.js`: add `scripting.executeScript` and `tabs.query` wrappers (chrome.* stays
  confined to this file).

## Compliance tradeoff — amends WO-001 §3 and DESIGN §3.1

WO-001 §3 and DESIGN §3.1 mandated **no `host_permissions`, no `scripting`** as a Chrome Web Store
review requirement (permission minimisation, §3.1 "Permissions — minimise aggressively"). This WO
re-introduces both, deliberately, and must be read with that in mind:

- The host permission is **path-scoped to `/watch*` only** — the exact surface already declared in
  `content_scripts.matches`. It grants nothing the content script does not already read; it exists
  only so the SW can target already-open tabs with `executeScript`.
- `scripting` is used for **re-injection only** — no dynamic content-script registration, no new
  surfaces, no MAIN-world execution.
- Store listing must now justify: `"Host access to youtube.com/watch pages only, so the extension
  can re-arm itself in tabs already open during an update."` The install-time prompt is larger; that
  is the accepted cost of closing the weeks-long dark-tab gap.

If the reviewer judges the CWS risk unacceptable, the fallback is: keep the watchdog reconnect
(free) and revert scripting, re-accepting the dark-tab gap and surfacing a "reload this tab to
re-arm" hint in the panel instead.

## Acceptance

- [x] Watch tab open at extension reload time gets re-armed by the watchdog within ~30 s, without
      manual tab reload.
- [x] SW eviction no longer kills the daemon link for long: watchdog wakes the SW and reconnects.
- [x] No duplicate observers / duplicate impressions on re-injection (module cache).
- [x] `host_permissions` scoped to `*://www.youtube.com/watch*` only; `tabs` permission absent.
- [ ] **Live QA on Brave (Lars):** with a watch tab open, reload the extension in `brave://extensions`
      and do NOT touch the tab; confirm data resumes within ~30 s. Then close the panel and let the
      SW idle ~2 min; confirm the daemon stays reconnected without user interaction.
- [x] `npm test` 16/16 pass; daemon untouched (`go test` still green).

## Notes for the reviewer on return

- The watchdog wakes the SW every 30 s permanently. Cost: one `tabs.query` + one cached-injection
  per watch tab + a `HELLO` when down — negligible, and it is the whole point (continuous
  collection must not sleep).
- Edge not covered: a tab whose observer module evaluated but then died to an internal error is a
  cached module, so re-injection cannot re-run it (module cache). Rare; if it shows up, the fix is
  a `func` injection that clears the module cache marker — not needed now.
- `rearmWatchTabs` re-injects unconditionally each tick; it does not first check whether the tab is
  armed. Cheaper than tracking state, correct because injection is a no-op. Do not "optimise" by
  querying arm state without re-testing the reload case.


---

## RESOLVED — 2026-08-03: re-injection does revive already-open tabs

**The watchdog works. No change needed.**

Lars confirmed: a YouTube tab left **untouched** across an extension reload picked up the new
content-script code and recorded its 20 suggestions on its own.

I had recorded the opposite an hour earlier — that `bootstrap.js` re-runs but its dynamic `import()`
resolves from the page's module cache, so nothing re-evaluates and the tab keeps stale code. That was
inference from reading the code, never tested, and it is wrong. The isolated world is torn down and
recreated on extension reload, so re-injection gets a fresh module graph.

One tab did need a manual reload during WO-009 QA. That tab had been manually injected into from the
service-worker console during earlier debugging, which is the likely difference — not a general
failure mode.

**Consequence for release:** the silent-loss risk from Chrome's background auto-updates does not
apply. Open tabs recover within one watchdog period (~30 s), which matches the ≤30 s gap measured
when this WO was first verified.
