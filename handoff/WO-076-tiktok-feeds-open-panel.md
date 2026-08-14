# WO-076 — TikTok /live, /explore, /following must open the panel

**Addressee:** Sr Dev (Opus)
**Status:** **Superseded before implementation by WO-098** 2026-08-13
**Date:** 2026-08-11
**Source:** Lars, live QA on TikTok in sequence: *"I went to the live page,
and it won't open there. We probably have to do the same thing Sr Dev just
did for the tiktok.com/ page."* … *"explore doesn't work either"* … *"or
following"*.

> **Do not implement the `HOME` mapping proposed below.** Live review showed
> that `/explore` and `/following` are distinct ordered video feeds, while
> `/live` is a wall of livestream cards whose primary destination is Keel's
> Live index. Mapping all three to `HOME` would erase useful surface provenance
> and would not solve TikTok Live identity, extraction, linking, or gossip.
> WO-098 is the complete, authoritative replacement. The remainder of this
> file is retained as the original bug report.

## The bug

On `https://www.tiktok.com/live`, `/explore`, and `/following` the Keel button
is a dead click — no panel, and the observer never arms. Same root cause as
the pre-WO-074 FYP:

`surfaceFromUrl` (`extension/content/extract.js`) knows four TikTok surfaces:
`/@author/video/<id>` (WATCH_NEXT), `/@author/live` (WATCH_NEXT), `/` and
`/foryou` (HOME). Everything else — including TikTok's other three feed
pages — returns `surface: null`. From null flows the whole dead chain:
`panelAllowedFor` → false (button falls through to the full-page tab focus
no-op), `panelContextPayload.focus` → false, and the observer goes fully
idle (`buildContext` returns null → no PAGE_CONTEXT, no IMPRESSIONS).

## The fix

Same treatment as WO-074's `/` + `/foryou`: classify the three as HOME in
`surfaceFromUrl`'s tt branch:

```js
if (
  u.pathname === "/" || u.pathname === "" || u.pathname === "/foryou" ||
  u.pathname === "/live" || u.pathname === "/explore" || u.pathname === "/following"
) {
  return { platform, surface: "HOME", context_video_id: null };
}
```

HOME is the right bucket for all three — the WO-074 comment's own reasoning:
HOME means "an unprompted feed", and LIVE/Explore/Following are exactly
TikTok's other endless feeds (a feed is a feed; the daemon's surface
whitelist already accepts HOME, so no `daemon/bridge/protocol.go` change).
No new surface name, no daemon change.

Once surface is HOME everything downstream works unchanged: the button
opens the panel (WO-074 gate), `evalActivePanelContext` keeps it open while
such a tab is active, and the observer's HOME path arms (WO-063 mirror).

## What to verify, and where it can break

1. **Unit tests** (`test/extract.test.js`, `test/sw-panel-gating.test.js`):
   the three URLs classify HOME; `action.onClicked` on each opens the panel;
   `syncPanelForTab` enables the tab. Keep `test/fixtures` green.
2. **Live**: the WO-063 TikTok mirror only reads the FYP DOM through
   `selectors_tt.json` `containers.home`. The other three feeds use TikTok's
   card markup too, but confirm cards actually extract on each page — if the
   panel opens empty there, that is a selector gap to file under WO-063, not
   a band-aid here.
3. **Navigation**: `/explore` and `/following` are SPA targets from the nav
   bar — the existing onNavigate/SPA machinery must report PAGE_CONTEXT for
   them like it does for `/` (same code path, worth one live check).

## Scope

TikTok feeds only — that is what Lars reported. YouTube's `/feed/*`, `/live`
and similar stay deliberately idle (WO-010). If you think the same argument
applies to YouTube's feeds, raise it separately; do not fold it in here.

## Rationale

The panel gate and observer surface map exist to scope "what the recommender
serves you" (§3 REC_surface). TikTok serves these feeds just as it serves
the FYP — leaving them dead makes the panel behave as if half of TikTok
didn't exist, for the same reason WO-074 fixed the FYP.

## Challenge

If `/following` feels different to you (user-chosen subscriptions, not a
pure recommender feed) say so — candidates: keep it HOME anyway (its
ordering is still algorithmic), or map it to a distinct surface with
protocol support. Deciding that is part of this ticket.
