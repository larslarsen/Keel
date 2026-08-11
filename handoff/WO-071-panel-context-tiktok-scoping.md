# WO-071 — Panel context-awareness: restore close-on-leave via fullpage surface; TikTok shows YouTube counts

**Addressee:** Sr Dev (Opus)
**Status:** **Done 2026-08-11** — both defects fixed and tested; see "What was found and built" below.
**Date:** 2026-08-11
**Source:** Lars, 2026-08-11.

## History (do not repeat the mistake)

The panel USED to close when you left a YouTube (or now TikTok) tab, and Lars
liked that. That behaviour was NOT lost in a merge — it was **deliberately removed**
in commit `7d60797` ("Side panel is always available; narrow the orphan check").
That commit removed the per-tab enable/disable (`setSidePanelForTab`) because a
*disabled* button "reads as broken": it did nothing on /feed, /results, channel
pages, and before consent, with no error shown (setOptions rejects silently). The
commit chose open-everywhere over close-on-leave. So: the old behaviour worked;
we removed it for UX reasons, not because it failed. Lars confirms he remembers it
working.

## The fix Lars wants

The side panel is ONLY open when the active tab is a YouTube or TikTok **watch page**.
It is NEVER open on the fullpage tab, or on any other tab. The toolbar button stays
clickable everywhere; when clicked while NOT on a YT/TikTok watch page, it opens the
**fullpage tab** (which is itself a surface with the functionality — stats / network /
your data). That is the "close on leave" behaviour Lars liked, expressed as a hard
gate: panel open ⇔ active tab is a YT/TikTok watch page; otherwise the button click
lands on the fullpage tab (never the panel).

This restores the context-awareness `7d60797` removed, WITHOUT the dead-button
problem (the button is always clickable and always does something — opens the
fullpage tab off-platform). It also means the fullpage surface is discoverable: a
user clicking the Keel button off a watch page lands on it.

The fullpage tab is a SEPARATE surface — do NOT render it inside the panel, and do
NOT leave the panel open on it. The panel and the fullpage tab are different views.

## Defect 2 — TikTok shows YouTube counts

On a TikTok watch page the panel must pass `platform: "tt"` so SUGGEST returns
TikTok-scoped suggestions. Currently it shows YouTube counts. Either:
- The TikTok content script's PAGE_CONTEXT does not send `platform: "tt"` (so
  `lastPage.platform` stays "yt" → sidepanel/index.js:614 defaults to "yt"), or
- `st.SuggestOn` is not actually partitioning the graph by platform (store must keep
  per-platform neighbourhoods; if it mixes, TikTok queries return YouTube edges).
Verify which before fixing. Manifest already grants TikTok host permission
(manifest.*.json) — not a permissions gap.

## What exists (read first)

- `extension/lib/native.js:125` — bridge `request(type, payload, timeoutMs = 8000)`.
- `extension/background/sw.js:31` `SITE_URLS = ["*://www.youtube.com/*",
  "*://www.tiktok.com/*"]`; `platformForUrl` maps a tab URL → "yt" / "tt" / null.
- `sw.js:624` `setPanelBehavior({ openPanelOnActionClick: true })` — panel opens on
  icon click on ANY page (this is the part to change: gate it to YT/TikTok watch
  pages; off-watch-page clicks open the fullpage tab instead).
- `sw.js:543` `setOptions({ tabId, enabled: false })` still exists but is only used
  by the fullpage-tab "hide panel on this tab" path — reuse the enable/disable
  mechanism to close the panel when leaving a watch page.
- Panel SUGGEST uses `lastPageCache?.platform || "yt"` (sidepanel/index.js:614);
  `lastPage.platform` set from PAGE_CONTEXT `platform` (sw.js:225, 228).
- Daemon `handleSuggest` → `st.SuggestOn(p.Platform, ...)` (main.go:724) — platform
  is a parameter; per-platform scoping supported IF the value is correct end-to-end.

## What to fix (Opus decides)

1. **Gate the panel to YT/TikTok watch pages only.** Track active tab via
   `browser.tabs.onActivated` / `onUpdated` + `platformForUrl`. When the active tab is
   a YT/TikTok watch page → panel open, scoped to that platform. When it is NOT (fullpage
   tab, blank tab, any other site) → panel closed (reuse `setOptions enabled:false`, the
   `7d60797` mechanism, now correctly scoped). The toolbar button stays clickable
   everywhere; an off-watch-page click opens the fullpage tab, not the panel. This is
   the close-on-leave behaviour Lars liked, with no dead-button (button always opens
   the fullpage tab off-platform).
2. **TikTok scoping.** Ensure TikTok PAGE_CONTEXT sends `platform: "tt"` and
   `SuggestOn` partitions by platform. Regression: TikTok-page SUGGEST returns only
   TikTok-scoped hits, never YouTube edges.

## Verification

- On a YouTube/TikTok watch page → panel open, suggestions scoped to that platform.
- Switch to fullpage tab, blank tab, or any other site → panel CLOSED (not open on
  fullpage; fullpage is a tab, not panel content).
- Click Keel button while off a watch page → fullpage tab opens; panel does not.
- On TikTok watch page → suggestions scoped to tt, not yt.
- Regression: `handleSuggest` with `Platform: "tt"` returns TikTok neighbourhood
  only; assert no "yt"-only video_id appears.
- Toolbar button clickable on every page (no `enabled:false` dead state for the
  button itself — only the panel visibility is gated).

## What was found and built (2026-08-11)

Despite three prior commits with "WO-071" in their message (`e4b683d`,
`43c0217`, `5be9cdb`), none of them touched any code — `git show --stat` on
all three shows only edits to this ticket file and `handoff/README.md`. A
direct read of `extension/background/sw.js` confirmed no `onActivated`/
`onUpdated` gating, no `action.onClicked` handler, and `openPanelOnActionClick:
true` still unconditional — defect 1 was entirely unimplemented, matching
this session's WO-069 lesson that ticket-titled commits from the concurrent
session are not evidence of landed code.

**Defect 1 — panel gating.** Implemented as specified:
- `browser.sidePanel.setPanelBehavior({ openPanelOnActionClick: false })`
  (was `true`).
- `syncPanelForTab(tabId, url)` computes watch-page-ness via
  `surfaceFromUrl` (`content/extract.js`, already shared with the content
  script — no second definition of "watch page" to drift out of sync) and
  calls `sidePanel.setOptions({ tabId, enabled })`. Wired to
  `tabs.onUpdated` (url changes / SPA nav) and `tabs.onCreated`, plus a
  full-tab sweep at SW startup and on every watchdog tick (mirrors
  `rearmYoutubeTabs`' own SW-restart-recovery reasoning — a tab opened while
  the SW was evicted must still end up correctly gated).
- `action.onClicked`: on a watch tab, `sidePanel.open({tabId})`; otherwise
  `openFullpageTab()`, which focuses an already-open full-page tab rather
  than stacking a duplicate.

**Defect 2 — TikTok showing YouTube counts.** Both hypotheses in the
original ticket turned out to be wrong on direct inspection:
- The TikTok content script *does* send `platform: "tt"`
  (`observer.js` → `platformFromUrl(location.href)`).
- `SuggestOn`/`loadGraph` *does* partition every query by
  `WHERE platform = ?`, and `platformOf` correctly passes `"tt"` through
  (`bridge.KnownPlatforms["tt"] == true`).

The real bug: `sw.js`'s `rememberPage` resets `lastPage` on a rail-generation
change (YouTube swaps the watch-next rail ~2s after every load — a normal
part of the page lifecycle, not an edge case) and the reset object literal
never included `platform`, silently setting it to `undefined`. The panel
(`sidepanel/index.js:614`, `lastPageCache?.platform || "yt"`) then fell back
to `"yt"` — which reads as correct on a YouTube page (coincidence) and wrong
on a TikTok page (the reported bug), exactly matching "TikTok shows YouTube
counts" rather than "TikTok shows random/missing counts." Fixed with a
one-line addition: `platform: lastPage.platform || "yt"` in the reset.

Tests (`test/sw-panel-gating.test.js`): `setPanelBehavior` called with
`openPanelOnActionClick: false`; `syncPanelForTab` enables on YT/TikTok
watch URLs and disables on YT home / an unrelated site; `action.onClicked`
opens the panel on a watch tab and never calls `tabs.create`, opens (or
focuses, not duplicates) the full-page tab everywhere else and never calls
`sidePanel.open`; the defect-2 regression constructs the exact rail-swap
sequence (PAGE_CONTEXT platform:"tt" → IMPRESSIONS generation 1 → IMPRESSIONS
generation 2, same page) and asserts `platform` is still `"tt"` after GET_STATUS
— verified red (fails with `undefined !== 'tt'`) against the pre-fix code,
green after. Full suite: `npm test` 74/74 (10 new); `go build ./... && go test
./... -race` and `scripts/check-coverage.sh` unaffected (no Go changes in this
ticket).
