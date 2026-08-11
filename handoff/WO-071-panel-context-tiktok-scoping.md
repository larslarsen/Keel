# WO-071 — Panel context-awareness: restore close-on-leave via fullpage surface; TikTok shows YouTube counts

**Addressee:** Sr Dev (Opus)
**Status:** Open
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

## The fix Lars wants (resolves both the old UX complaint AND the current stale-data bug)

Keep the toolbar button CLICKABLE everywhere (don't regress to the dead-button
problem `7d60797` fixed). But restore the close-on-leave *feel* WITHOUT disabling
the button: **when the active tab leaves a Keel surface (YouTube/TikTok), route the
panel to the fullpage tab** — the one no-context surface (stats / network / your
data) — instead of retaining stale YouTube suggestions. So:
- On YouTube/TikTok tab → panel shows that surface's suggestions (scoped per platform).
- Leave to a non-Keel tab → panel shows the fullpage surface (no video suggestions,
  no stale data). Button stays live.
- This is the "close on leave" behaviour Lars liked, re-expressed as "switch to the
  context-free surface" rather than "disable the button."

The fullpage surface already exists and is our only no-context surface — use it as
the landing surface when no Keel tab is active.

## Defect 2 — TikTok shows YouTube counts

On a TikTok tab the panel must pass `platform: "tt"` so SUGGEST returns
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
  icon click on ANY page (intentional, per `7d60797`).
- `sw.js:543` `setOptions({ tabId, enabled: false })` still exists but is only used
  by the fullpage-tab "hide panel on this tab" path — NOT the old per-tab gate.
- Panel SUGGEST uses `lastPageCache?.platform || "yt"` (sidepanel/index.js:614);
  `lastPage.platform` set from PAGE_CONTEXT `platform` (sw.js:225, 228).
- Daemon `handleSuggest` → `st.SuggestOn(p.Platform, ...)` (main.go:724) — platform
  is a parameter; per-platform scoping supported IF the value is correct end-to-end.

## What to fix (Opus decides)

1. **Restore context-awareness without disabling the button.** Track the active tab's
   platform (via `browser.tabs.onActivated` / `onUpdated` + `platformForUrl`); when
   the active tab is not a Keel surface, point the panel at the **fullpage surface**
   (reuse the existing fullpage route — our only no-context surface). When it IS a
   Keel surface, scope suggestions to that platform. Button remains clickable
   everywhere (no `enabled:false` gating — that is the `7d60797` regression to avoid).
   This gives Lars the "panel closes when I leave YouTube/TikTok" behaviour he liked,
   expressed as a surface switch, not a button kill.
2. **TikTok scoping.** Ensure TikTok PAGE_CONTEXT sends `platform: "tt"` and
   `SuggestOn` partitions by platform. Regression: TikTok-page SUGGEST returns only
   TikTok-scoped hits, never YouTube edges.

## Verification

- Open panel on YouTube → suggestions. Switch to a blank/neutral tab → panel must
  show the fullpage (no-context) surface, NOT the stale YouTube list, and the button
  must still be clickable. Switch to TikTok → suggestions scoped to tt, not yt.
- Regression: `handleSuggest` with `Platform: "tt"` returns TikTok neighbourhood
  only; assert no "yt"-only video_id appears.
- Confirm the toolbar button is clickable on every page (no `enabled:false` path
  re-introduced for the dead-button case `7d60797` removed).
