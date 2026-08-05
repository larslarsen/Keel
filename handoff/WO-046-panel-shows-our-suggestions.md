# WO-046 — The panel must show *our* suggestions, not YouTube's

| | |
|---|---|
| **Addressee** | Anyone |
| **Status** | **Done — pending live QA** |
| **Date** | 2026-08-04 |
| **Source** | Lars: *"you made the panel match YouTube's suggestions. That defeats the entire point of installing the extension. We're replacing the panel with itself."* |

## The problem

The panel's main list is `lastPage.impressions` — the rows just scraped from
YouTube's rail. So Keel hides YouTube's recommendations and shows the same
recommendations back. The user gains nothing from the trade.

This is the central feature of the product and it is currently a mirror.

## Wanted

While watching a video, the panel shows **Keel's own ranking** for that video,
produced by the graph walk, not by YouTube.

The engine already exists and is live-verified — `SUGGEST` in the daemon
(WO-023), random-walk-with-restart over the co-recommendation graph with an
entropy control. It is wired to the full page only. This ticket points it at the
panel.

## The change

**1. Panel's primary list becomes `SUGGEST` output.**

- Seed the walk with the video currently being watched. The content script
  already derives it — `surfaceFromUrl()` returns `context_video_id` for
  `/watch` — and it is on every impression the panel receives, so no new
  extraction is needed.
- Call `SUGGEST { seed_video_id, entropy, limit }` through the SW, exactly as
  `page/index.js` does today.
- Re-run it when the watched video changes (new `page_load_id`), not on every
  scan tick.

**2. The scraped rail is not a browsable list anywhere.**

Corrected by Lars, 2026-08-04: *"NO, that should be a full page of suggestions
too… we do NOT want to click on YouTube suggestions, we could just close the
panel for that."*

An earlier revision of this ticket moved the scraped list to the full page. That
was wrong for the same reason it was wrong in the panel — a clickable list of
YouTube's recommendations is a surface the user already has, one click away, by
closing the panel. Reproducing it in Keel spends a surface to offer nothing.

So:

- **Panel** — our suggestions.
- **Full page** — our suggestions, with room for more of them.
- **The scraped rows are analysis, not an offer.** Clarified by Lars: *"you CAN
  click on it, but that's not what it's for — it's the analysis of why it showed
  it to you."*

  Links are fine and should stay. What must not happen is presenting the rail as
  a menu to pick from, which is the framing that makes it a duplicate of the
  page behind it. Present it as a record of what YouTube did — with counts,
  slots, and the "why" — and clicking a row is an incidental convenience rather
  than the point.

"Why did YouTube suggest this?" is a legitimate question and may have its own
tab — it is analysis *about* the rail, not the rail. `EXPLAIN_VIDEO` already
answers it, and it reads as a record rather than a menu.

**3. Entropy control in the panel.**

The full page has a Focus↔Serendipity slider. The panel needs one too, or the
walk is stuck at a default the user cannot feel. Keep it to a single compact
row — WO-041 just cleared this panel out and it should not fill up again.

## Why the results currently look like YouTube's rail

Lars: *"the full page is the same as the rail on the current watch page."* That
is expected, and it is the thing this ticket has to solve — not a defect in the
walk.

**A random walk with high restart, seeded on the current video, returns that
video's direct neighbours.** Those neighbours *are* YouTube's rail for that
video, because that is where the edges came from. At entropy 0 Keel reproduces
YouTube by construction.

Divergence only comes from three things, all already implemented:

1. **Lower restart** (higher entropy) — the walk goes several hops out and
   reaches videos never shown alongside this one.
2. **Popularity damping** — at high entropy the score is divided by
   `log10(views)`, which is what surfaces the 11-view and 64-view results
   measured in WO-023.
3. **Graph depth** — more roots means more places a walk can go.

**Therefore: the default entropy must not be near zero.** A default in the
focus half ships a product that mirrors YouTube out of the box, which is exactly
the complaint. Pick a default in the serendipity half and let the user pull it
back toward focus if they want.

Verify it concretely: at the shipped default, the panel's list must not be a
reordering of the rail on screen. Compare them by eye on a live page — if the
same videos appear in both, the default is too low.

## Corpus size — corrected 2026-08-04

An earlier revision said 6 watched videos. That was a snapshot taken minutes
after the WO-012 wipe test and was repeated afterwards without rechecking.

Actual, measured today: **4,126 impressions across 44 watched videos.** There is
real graph depth; the mirroring above is a parameter problem, not a data
shortage.

## Do not

- Do not show YouTube's scraped rail as the panel's main content, in any
  fallback path.
- Do not add new extraction, network calls, or fields. The seed and the engine
  both already exist.
- Do not remove the funnel inspector — "why was this suggested" applies to our
  suggestions too, and `EXPLAIN_VIDEO` already answers it for any video id.

## Acceptance

- [x] On a watch page, the panel lists videos ranked by `SUGGEST`, seeded on the
      video being watched.
- [x] Changing video re-runs the walk.
- [x] Neither the panel nor the full page offers YouTube's scraped rail as a
      list to pick from. Both lead with our suggestions.
- [x] Scraped rows appear as analysis — what was shown, where, how often, and
      why — with links intact but not framed as recommendations.
- [x] An entropy control is present in the panel and changes the results.
- [x] With too little history, the panel says so rather than showing YouTube's
      rail.
- [ ] At the shipped default entropy, the panel's suggestions are **not** a
      reordering of the rail visible on the page.
- [x] 26 JS tests still pass.

## Implemented — 2026-08-04

Panel primary list is now `SUGGEST` output (`extension/sidepanel/index.js`):
seeded with `context_video_id` from the current page proof (empty on non-watch
pages, where the daemon falls back to last-watch context), re-run only when
`page_load_id` changes or the slider moves. The scraped rail is no longer
rendered as a list anywhere; the funnel inspector (`why` button →
`EXPLAIN_VIDEO`) is the analysis record for any video id.

Entropy default is **70** (serendipity half; `alpha = 0.36`), a compact
Focus↔Serendipity slider row sits above the list (`style.css` `.entropy-row`).
No new extraction, fields, or network calls — the seed and engine already
existed.

**Still needs live QA:** the by-eye divergence check on a real watch page, and
confirming the walk re-runs when the watched video changes.

**Challenge:** if 70 feels too far from the seed (or too close), adjust the
default — the acceptance is that the shipped value must not reproduce the rail.
