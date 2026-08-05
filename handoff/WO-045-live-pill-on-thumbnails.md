# WO-045 — LIVE pill on panel thumbnails

| | |
|---|---|
| **Addressee** | Jr Dev (opencode) |
| **Status** | **Done** |
| **Date** | 2026-08-04 |
| **Source** | Lars, 2026-08-04 |

---

## Problem

Live streams are extracted (a `LIVE` badge is captured in `impressions[].badges`
and a live card has `duration_s: null`) but nothing in the panel distinguishes
them. A stream and a normal video look identical apart from a missing duration, so
the one card type with real time-pressure is the easiest to overlook.

## Behaviour decided

**YouTube-style red pill on the thumbnail.** A live stream shows a red `LIVE`
badge over the lower-left of its thumbnail — the same cue YouTube uses — so it
jumps out at a glance. A red outline was considered and rejected as the weaker
signal; the pill is what users already read as "live".

## The change

`extension/sidepanel/index.js` `makeLi` + `style.css`:

- `isLive = badges.includes("LIVE")` → wrap the image in `.thumb-box` and overlay
  `<span class="live">LIVE</span>` (absolute, lower-left, `#ff0000` background).
- Works with and without the channel name: `.thumb-box` nests inside
  `.thumb-wrap` when a `.chan` label is present, and stands alone otherwise.

No extraction, protocol, or daemon change: the `LIVE` badge was already flowing
through `badges` (`extractBadges`, `fieldsFromCompact`'s `metadataBadgeRenderer`
labels, lockup `badge-shape` text) and is validated by `protocol.js`.

## Not in this ticket

- The full-page surface (`page/index.js`) renders search/suggestion rows from the
  daemon catalogue (`SearchHit`), which has no `badges` field; a LIVE cue there
  needs a catalogue-side `is_live` column and a separate work order.
- "Live" by heuristic (null duration) was deliberately not used — a card with no
  duration badge is not necessarily a stream, and a false LIVE cue is worse than
  none.

## Acceptance

- [x] Live cards (badge `LIVE`) show a red `LIVE` pill on the thumbnail.
- [x] Non-live cards are unchanged.
- [x] Pill renders both with and without the channel-name label.
- [x] No new permissions, no manifest change.
- [x] 27 JS tests pass (badge extraction already covered by lockup fixture).

## Pushback invited

If the pill reads too big at 96 px wide, shrink `#list .live` padding/font. If
live streams should also be flagged on the full-page surface, that needs the
catalogue `is_live` column above — raise it rather than papering over it in the
renderer.
