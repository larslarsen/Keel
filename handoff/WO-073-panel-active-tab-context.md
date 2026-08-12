# WO-073 — Panel must follow the ACTIVE tab's platform, not the last page any tab reported

**Addressee:** Sr Dev (Opus)
**Status:** **Done 2026-08-11** — see "What was found and built". Live-QA confirmed by
Lars on 2026-08-11 after WO-074 landed (the TT half of the gate needed WO-074 first).
**Date:** 2026-08-11
**Source:** Lars, 2026-08-11 (regression found in live QA right after WO-071 landed).

## The bug

The side panel is a per-window artifact, but its data source is window-global:
`sw.js`'s `lastPage` is set by whichever tab — in ANY window — last sent
`PAGE_CONTEXT`/`IMPRESSIONS`. On a tab switch within one window, or with a
background tab of another platform reporting newer, the panel re-scoped to the
LAST TAB TO REPORT, not the tab the user is looking at. Symptom, reported live:
switching YT→TT kept the YouTube suggestions; a stale YT page proof could also
seed a TikTok walk.

## The fix

Two halves, both required:

**SW half (extension/background/sw.js).** The SW derives the panel's context
from its window's ACTIVE tab and tells the panel about it:
- `panelContextPayload(windowId, url)` — `{windowId, platform, focus}` from
  `surfaceFromUrl`, so "watch page" is defined in exactly the one place
  (`content/extract.js`) used everywhere else.
- `PANEL_CONTEXT_QUERY` RPC — the panel asks on load (the SW may have been
  evicted and missed every broadcast). Queries `tabs.query({active:true,
  windowId})`, falling back to `lastFocusedWindow`.
- `evalActivePanelContext(tab, windowId)` — the full gate for the ACTIVE tab:
  `syncPanelForTab` (enable/disable + path), `closePanelInWindow` on
  non-watch pages (sidePanel.close, Chrome 141+ — setOptions({enabled:false})
  never closes an ALREADY-open panel), then `broadcast(PANEL_CONTEXT)`.
- Wired to `tabs.onActivated`, `tabs.onUpdated` (active-tab navigations only),
  and the startup/watchdog sweep (`syncAllTabsPanelState` — active tabs get the
  full gate, background tabs only per-tab enable/disable). A background tab's
  navigation must never close or re-scope the window's panel.
- Bug found while wiring: `PANEL_CONTEXT_QUERY` called `panelContextFor()` —
  a function that never existed — so every query threw. Fixed to
  `panelContextPayload(...)`.

**Panel half (extension/sidepanel/index.js).** The panel now consumes the
context:
- `applyPanelContext()` on the `PANEL_CONTEXT` broadcast (ignores other
  windows' `windowId`s) and on the on-load `PANEL_CONTEXT_QUERY`.
- `panelPlatform()` — the platform SUGGEST/QUEUE_ADD/watch links answer for.
- `currentSeed()` returns "" when focus is false (no watch page open) or when
  the cached proof is from a different platform than the active tab — a YT
  proof must never seed a TT walk.
- `absorbLastPage()` rejects proofs whose platform differs from the active
  tab's (STORE_UPDATED is broadcast from whichever tab reported, including
  background tabs of the other platform).
- On platform change it drops the stale proof (`lastPageCache = null`) and
  force-refreshes suggestions.

## Verification

- Deterministic: `test/sw-panel-gating.test.js` (WO-073 suite): QUERY returns
  the active tab's platform/focus; focus:false off a watch page; lastFocusedWindow
  fallback; onActivated on a watch tab enables + broadcasts context + does NOT
  close; onActivated off-watch closes (both window and tab forms) + broadcasts
  focus:false; active-tab onUpdated runs the full gate (close-on-leave);
  background-tab onUpdated only syncs enable/disable, never closes or broadcasts.
- Full suite: `npm test` 87/87 (10 new in this ticket's suite). No Go changes.
- Living QA (Lars): open two tabs — one YT watch, one TT watch — in the same
  window; switch between them; the panel's platform header must follow the
  active tab (content scoped to the site you're on). Off-watch-page tab (home,
  blank, other site): panel closes.

## Out of scope (raise if it bites)

Multi-panel windows on engines without `sidePanel.close` (Firefox): the panel
stays open with focus:false, which renders the honest "no watch page open"
state instead of closing — accepted degradation until Chromium-only at
minimum.