# WO-066 — Live detection false-positives: non-live video flagged LIVE

**Addressee:** Sr Dev (Opus)
**Status:** **Resolved** (fix landed + regression tests green, 2026-08-10)
**Date:** 2026-08-10
**Source:** Lars, 2026-08-10 — observed a non-live stream ("Master Day Trading…
@AboveTheGreenLine") sitting in the Live tab with stale "2+ hours / 1 hour ago"
metadata. The live badge is the only thing that should put a stream in the Live
index; this one was NOT live when captured. The bug is in the extension's live
detection, not the daemon (the daemon trusts whatever the extension publishes).

## Root cause (code-verified, fix landed)

The extension flagged LIVE via loose substring matches, not the genuine YouTube
LIVE *broadcast* badge. Two real false-positive sources:

1. `extension/content/extract.js` `extractBadges()` line 293 — `mt.liveLoose.test(overlay?.textContent)` ran a **substring, no-word-boundary** matcher against the card's entire overlay/description text. A non-live VOD whose title/overlay contained "live" / "livestream" / "live chat" was falsely flagged LIVE. **This was the path behind the observed bug.** `liveLoose` (extract.js:48) has no `\b` boundary; `mt.live` (extract.js:47, word-bounded) on badge containers is the correct signal and was kept.

2. `extension/content/extract_yt.js` — two loose LIVE matchers:
   - `fieldsFromFound` badge label (was line 152): `label.toUpperCase().includes("LIVE")` on `r.badges`/`ownerBadges` metadata labels — matched "LIVESTREAM" etc.
   - `fieldsFromLockup` thumbnail overlay loop (was line 302): `/LIVE/i.test(badge)` on `thumbnailOverlayBadgeViewModel` / `animatedThumbnailOverlayViewModel` text — matched "LIVE replay" / "Live chat replay".

## Fix (committed 6a57169)

- `extract.js`: removed the `liveLoose` overlay match (lines 292-293). LIVE now comes only from the word-bounded `mt.live` on badge containers.
- `extract_yt.js`: both matchers now require the **standalone** `LIVE` word (`\bLIVE\b`) and **reject** replay/chat/stream variants (`!/replay|chat|stream/i` for labels; `!/replay|chat/i` for overlay text).

## Regression tests (test/live-badge.test.js, 6 cases, green)

- DOM card: genuine "LIVE" badge → flagged; "livestream…" overlay → NOT flagged; "Live chat replay" overlay → NOT flagged; "LIVE replay" overlay → NOT flagged; "● LIVE" badge → flagged.
- ytInitialData lockup: one lockup with LIVE label + one "LIVE replay" + one "LIVESTREAM" → exactly one genuine LIVE.
- Full suite: 57/57 pass.

## Follow-up gap (NOT fixed — separate ticket territory)

`fieldsFromLockup`'s LIVE detection reads ONLY `thumbnailOverlayBadgeViewModel` /
`animatedThumbnailOverlayViewModel` overlays. The real YouTube fixture
(`test/fixtures/yt_initial_watch.json`) carries the thumbnail duration/state badge
in **`thumbnailBottomOverlayViewModel.badges[].thumbnailBadgeViewModel.text`** —
which the loop does NOT read. So a genuine LIVE badge rendered by YouTube in
`thumbnailBottomOverlayViewModel` would be MISSED by current detection (silent
under-detection, opposite of this bug). Likely belongs to WO-050's live-verify
pass. Flagged here so it is not lost; out of scope for WO-066 (which is about
false positives, not missed true positives).

## Acceptance (all met)

- [x] `liveLoose` no longer matches "live" as a substring in overlay/description text; non-live VOD with "livestream" in title/overlay is NOT flagged LIVE.
- [x] Fixture of the observed case (AboveTheGreenLine day-trading VOD, "live" in overlay text, no genuine LIVE broadcast badge) parses with `badges` NOT containing "LIVE". Locked as regression test.
- [x] Thumbnail-badge matcher rejects replay/VOD/chat "live" labels.
- [x] Genuinely-live stream (real LIVE broadcast badge) STILL flagged LIVE (no regression).
- [x] Every claimed bug has a failing-then-passing regression test.
- [ ] (Follow-up) `thumbnailBottomOverlayViewModel` LIVE badge path — file under WO-050 verify.

## Pushback invited

- If a loose matcher is needed for coverage on some YouTube surface, justify WHICH
  element it reads and why that element cannot contain a false "live". "It catches
  more" is not sufficient — precision is the requirement here.
- The daemon side has no live re-validation. Should it? (Out of scope unless a
  fixture proves the extension fix is insufficient — the extension is the
  authoritative gate per the user's rule.)
