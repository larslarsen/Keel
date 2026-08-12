# WO-074 — TikTok's For-You feed must open the panel (platform-aware WO-071 gate)

**Addressee:** Sr Dev (Opus)
**Status:** **Done 2026-08-11** — see "What was found and built". Live-QA passed
(Lars): YT→TT switch keeps the panel open with TT suggestions; no further action.
**Date:** 2026-08-11
**Source:** Lars, 2026-08-11, live QA immediately after WO-073: *"the panel just
closes when I switch from youtube to tiktok. It won't open on tiktok it just
goes to a fullpage tab if I click on the keel button."*

## The bug

WO-071's hard gate opens the panel only when the active tab is a
**WATCH_NEXT** surface. That definition is YouTube-shaped: on YouTube a
`/watch?v=` URL exists and HOME is correctly excluded. But TikTok desktop
**never navigates to `/@author/video/…`** — videos play inline in the For-You
feed and the URL stays on `https://www.tiktok.com/` (the WO-063 probe
confirmed it: all 399 live captures were `https://www.tiktok.com/`). So with
the YT-shaped gate, the TikTok panel was unreachable in normal use: switching
to a TikTok tab closed the panel (correctly per the gate, wrongly per intent)
and the toolbar button opened the full-page tab instead (WO-063's mirror
features were stranded).

## The fix

The gate is now platform-aware (`panelAllowedFor` in
`extension/background/sw.js`, replacing `isWatchUrl`):

- **YouTube: WATCH_NEXT only.** YT HOME stays excluded — nothing about the
  close-on-leave behaviour Lars liked changes.
- **TikTok: WATCH_NEXT (video page, live room) OR HOME (the FYP).** The FYP
  *is* the TikTok watch page. The panel closes when the active tab leaves
  TikTok, and only then.

All gate call sites switched to `panelAllowedFor`:
`syncPanelForTab` (per-tab enable/disable), `panelContextPayload` (the
`focus` flag the panel seeds from — a FYP tab now reports `focus:true`), and
the `action.onClicked` watch-check (button on the FYP opens the panel, never
the full-page tab).

Panel side needed no change: `panelPlatform()` already answers "tt" for a
FYP tab (PAGE_CONTEXT carries platform), the FYP has no impressions so the
walk seeds from recent watching, and `absorbLastPage`'s cross-platform guard
stops a YouTube tab's proof leaking in.

## Verification

- `test/sw-panel-gating.test.js`: enables the panel on `https://www.tiktok.com/`;
  the toolbar button opens the panel there (never the full-page tab);
  PANEL_CONTEXT_QUERY returns `focus:true, platform:"tt"` for the FYP;
  `onActivated` on the FYP enables + broadcasts `{windowId, platform:"tt",
  focus:true}` and does not close. YouTube HOME and OTHER_SITE still disable.
- Full suite: `npm test` 92/92 (5 new). No Go changes.
- Live QA (Lars): YT watch → TT FYP: panel stays open, showing tt-scoped
  suggestions ("From your recent watching"); Keel button on the FYP opens the
  panel; switching to a non-TikTok tab closes it; YT HOME still never opens it.

## Out of scope

The WO-063 mirror content itself (scroll history / hashtag & sound
clustering / engagement mirror). This ticket only un-strands the surface; the
mirror features remain their own work order.