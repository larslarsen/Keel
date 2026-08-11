# WO-071 — Panel not context-aware per platform; TikTok shows YouTube counts

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Date:** 2026-08-11
**Source:** Lars, 2026-08-11 — the side panel used to only open on a YouTube page
and stay context-aware. Now it opens on any page (intentional — see sw.js:196-209:
the old per-tab gating made the toolbar button silently dead, which "reads as
broken"). But two real defects remain:

1. The panel shows STALE YouTube data when you switch tabs to a non-YouTube,
   non-TikTok page (it stays open and does not re-scope / clear).
2. On a TikTok page it shows YouTube counts, not TikTok-scoped counts.

## What exists (read first)

- `extension/lib/native.js:125` — bridge `request(type, payload, timeoutMs = 8000)`.
- `extension/background/sw.js:31` `SITE_URLS = ["*://www.youtube.com/*",
  "*://www.tiktok.com/*"]`; `platformForUrl` maps a tab URL → "yt" / "tt" / null.
- `sw.js:624` `setPanelBehavior({ openPanelOnActionClick: true })` — panel opens on
  icon click on ANY page (intentional, per the comment at 196-209).
- Panel SUGGEST uses `lastPageCache?.platform || "yt"` (sidepanel/index.js:614).
  `lastPage.platform` is set from PAGE_CONTEXT `platform` (sw.js:225, 228).
- Daemon `handleSuggest` → `st.SuggestOn(p.Platform, ...)` (main.go:724) — platform
  is a parameter, so per-platform scoping is supported IF the platform value is
  correct end-to-end.

## Defect 1 — stale data on tab switch

When the active tab is not a YouTube/TikTok page, the panel should either close or
show "no active surface" rather than the last YouTube suggestions. Currently it
holds the last page's data. This is the WO-021 concern the 196-209 comment
deliberately traded away (open-everywhere > dead-button), but the *render* side was
never solved: the panel must re-scope or blank when the active tab leaves a Keel
surface, without making the button dead.

## Defect 2 — TikTok shows YouTube counts

On a TikTok tab the panel should pass `platform: "tt"` so SUGGEST returns
TikTok-scoped suggestions. If it shows YouTube counts, either:
- The TikTok content script's PAGE_CONTEXT does not send `platform: "tt"` (so
  `lastPage.platform` stays "yt"), or
- `st.SuggestOn` is not actually partitioning the graph by platform (the store must
  keep per-platform neighbourhoods; if it mixes, TikTok queries return YouTube
  edges).

Verify which before fixing. The manifest already grants TikTok host permission
(manifest.*.json), so it is not a permissions gap.

## What to fix (Opus decides)

- Defect 1: on tab switch away from a Keel surface, the panel should re-scope to the
  new surface's platform or show an explicit "not on a supported page" state — NOT
  retain stale YouTube data. Keep the icon clickable everywhere (don't regress to the
  dead-button behaviour the 196-209 comment rejected).
- Defect 2: ensure the TikTok content script sends `platform: "tt"` in PAGE_CONTEXT,
  and that `SuggestOn` partitions by platform. Add a regression: a TikTok-page
  SUGGEST returns only TikTok-scoped hits (or empty), never YouTube edges.

## Verification

- Open panel on YouTube → suggestions. Switch to a blank/neutral tab → panel must not
  show the stale YouTube list (re-scope or blank). Switch to TikTok → suggestions
  scoped to tt, not yt.
- Regression: `handleSuggest` with `Platform: "tt"` returns TikTok neighbourhood
  only; a test asserts no "yt"-only video_id appears.
