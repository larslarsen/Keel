# WO-066 — Live detection false-positives: non-live video flagged LIVE

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Date:** 2026-08-10
**Source:** Lars, 2026-08-10 — observed a non-live stream ("Master Day Trading…
@AboveTheGreenLine") sitting in the Live tab with stale "2+ hours / 1 hour ago"
metadata. The live badge is the only thing that should put a stream in the Live
index; this one was NOT live when captured. The bug is in the extension's live
detection, not the daemon (the daemon trusts whatever the extension publishes).

## Root cause (code-verified, fix must be validated by fixture)

The extension has THREE independent LIVE-detection paths; two are too loose:

1. `extension/content/extract_yt.js:152` — `label.toUpperCase().includes("LIVE")`
   on a ytInitialData badge label. Moderately specific.
2. `extension/content/extract_yt.js:302` — `/LIVE/i.test(badge)` on **thumbnail**
   badges. Matches ANY badge text containing "live" case-insensitively, including
   "LIVE replay", "Live chat replay", VOD labels, channel names with "live".
   **False-positive source.**
3. `extension/content/extract.js:287` (`mt.live.test`) and `:293`
   (`mt.liveLoose.test(overlay?.textContent)`) on a card's badge containers and
   **overlay textContent**.

   - `mt.live` (extract.js:47) is word-bounded (`\b(?:…)\b`) — specific.
   - `mt.liveLoose` (extract.js:48) has **NO word boundary**: `(?:alt…)`. It
     matches "live" as a substring ANYWHERE. Applied to `overlay.textContent`
     (line 293) — the broadest surface (title, description, everything on the
     card) — a non-live VOD whose text contains "live" / "livestream" /
     "live chat" / "relive" gets a `LIVE` badge. **Prime false-positive source.**

`mt.liveLoose` (extract.js:48) + its overlay use (extract.js:293) is the most
likely culprit for the observed bug, but the fix MUST be confirmed by a fixture
(see Acceptance), not by this analysis alone.

## Why it matters

The daemon (`daemon/swarm/live.go`) merges any `LiveRecord` the extension
publishes (no re-validation; `merge` at live.go:473, `PublishLive` at :779).
A false LIVE badge → false live record → polluted Live index → user sees streams
that were never broadcasting. This directly undermines the Live feature's one
job ("what is live right now").

## What to fix

- Tighten LIVE detection so ONLY a genuine YouTube LIVE *broadcast* badge sets
  `LIVE`. Concretely:
  - Remove or sharply restrict `liveLoose` (extract.js:48) — it must not match
    "live" as a bare substring in overlay/description text. If a loose matcher is
    kept, it must require the actual badge element (not arbitrary overlay text).
  - `/LIVE/i` on thumbnail badges (extract_yt.js:302) must require the badge to be
    a LIVE broadcast indicator, not a replay/VOD/chat label. Prefer the
    word-bounded `mt.live` (extract.js:47) and/or the ytInitialData
    `metadataBadgeRenderer` label (extract_yt.js:152), which are the specific
    signals.
  - Consider: a stream is "live" only if the page actually presents a LIVE badge
    on the WATCH page (the user's rule: "the live badge is what matters"). The
    watch-page detection must be the authoritative gate; shelf/card loose matches
    are supplementary at most.
- Do NOT widen matching "to catch more streams" — breadth here is exactly the
  bug. Precision over recall for the LIVE flag.

## Acceptance

- [ ] `liveLoose` (extract.js:48) no longer matches "live" as a substring in
      arbitrary overlay/description text; a non-live VOD containing "livestream"
      in its title/description is NOT flagged LIVE.
- [ ] A fixture of the observed case (AboveTheGreenLine day-trading VOD, "live"
      in overlay text, no genuine LIVE broadcast badge) parses with `badges`
      NOT containing "LIVE". LOCKED as a regression test (test/fixtures or
      extract unit test).
- [ ] Thumbnail-badge `/LIVE/i` matcher (extract_yt.js:302) rejects
      replay/VOD/chat "live" labels.
- [ ] A genuinely-live stream (real LIVE broadcast badge) is STILL flagged LIVE
      (no regression on true positives — add a fixture for this too).
- [ ] Every claimed bug in this ticket has a failing-then-passing regression
      test.

## Pushback invited

- If a loose matcher is needed for coverage on some YouTube surface, justify
  WHICH element it reads and why that element cannot contain a false "live".
  "It catches more" is not sufficient — precision is the requirement here.
- The daemon side has no live re-validation. Should it? (Out of scope unless a
  fixture proves the extension fix is insufficient — the extension is the
  authoritative gate per the user's rule.)
