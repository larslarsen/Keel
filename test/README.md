# Extract fixtures

Fixtures under `fixtures/` are **saved YouTube DOM / JSON snapshots** used by
`extract.test.js`. They are the canary for YouTube DOM drift.

If live `/watch` extraction breaks while tests still pass, the fixtures are stale —
update them before changing production selectors.

## Refresh procedure

### 0. Capture DOM cards (HOME)

1. Open Brave/Chrome with Keel **disabled** (or a clean profile that has accepted
   YouTube consent — headless dumps return a consent shell with no feed).
2. Hard-load `https://www.youtube.com/`.
3. Wait until the home grid shows a full first page of cards (and ideally one
   Shorts / shelf row).
4. DevTools → Elements → find `ytd-rich-grid-renderer` → its `#contents` div
   (direct children are `ytd-rich-item-renderer` / `ytd-rich-section-renderer`).
5. Right-click that `#contents` → **Copy** → **Copy outerHTML**.
6. Scrub (below) and save as `fixtures/home_grid.html` (or `home_*.html`).

Confirm live component types:

```js
document.querySelectorAll('ytd-rich-item-renderer').length
document.querySelectorAll('yt-lockup-view-model').length
document.querySelectorAll('ytd-rich-grid-media').length
document.querySelectorAll('ytd-rich-section-renderer').length
```

`slot_index` is **row-major** over those grid children; non-video sections must
remain in the fixture so they consume an index.

### 1. Capture DOM cards (WATCH_NEXT)

1. Open Brave/Chrome with Keel **disabled** (or a clean profile).
2. Hard-load a watch URL, e.g. `https://www.youtube.com/watch?v=dQw4w9WgXcQ`.
3. Wait until the right-hand related rail shows ~20 cards.
4. DevTools → Elements → find `#related` (or `#items` inside
   `ytd-watch-next-secondary-results-renderer`).
5. Right-click the results container → **Copy** → **Copy outerHTML**.
6. Scrub before commit (see below) and save as e.g.
   `fixtures/watch_next_lockup.html` (or `_compact` if the page still uses
   `ytd-compact-video-renderer`).

Confirm live component type:

```js
document.querySelectorAll('ytd-compact-video-renderer').length
document.querySelectorAll('yt-lockup-view-model').length
```

Capture **both** shapes if both appear in the wild.

### 2. Capture ytInitialData

In the same page console (ISOLATED world is fine — read script text, not
`window.ytInitialData` from an extension MAIN world):

```js
[...document.scripts]
  .map(s => s.textContent)
  .find(t => t.includes('var ytInitialData') || t.includes('ytInitialData ='));
```

Or copy the matching `<script>` from View Source. Extract the JSON object only
(the balanced `{...}` after `ytInitialData =`).

Trim the blob to secondary-results if huge: keep
`contents.twoColumnWatchNextResults.secondaryResults` plus enough parent keys
for the fixture to parse. Prefer a sample that includes `lockupViewModel`
and/or `compactVideoRenderer`.

Save as `fixtures/yt_initial_watch.json`.

### 3. Scrub PII / noise

Before commit, remove or replace:

- Personalised titles that identify the capturing account (optional; public
  video titles are fine)
- `continuation` tokens, tracking params, long thumbnail URL query strings
- Any cookie/session identifiers if present (should not appear in these blobs)

Keep structure, `videoId` / `contentId`, channel `browseId`, titles, duration
text, and renderer type keys.

### 4. Verify

```bash
npm test
```

Requirements:

- Every `watch_next_*.html` fixture must contain at least one `CARD_SEL` node.
- Every `home_*.html` fixture must contain grid children
  (`ytd-rich-item-renderer` and/or sections) with real video/channel IDs.
- Extraction must return **≥1 impression** for each active fixture; **0
  impressions is a failing test**, not a pass.
- After a fixture update that reflects live markup, also smoke-test the
  extension on hard-loaded `/` and `/watch` (daemon optional for DOM→SW path).

### 5. Commit

Commit fixtures + any selector changes together. Note the capture date and
browser in the commit message (e.g. `fixtures: lockup rail 2026-08-02 Brave`).

## Component registry

Keep these two lists in sync when YouTube renames components:

| Surface role | DOM (`extract.js` `CARD_SEL`) | JSON (`extract_yt.js` `RENDERER_KEYS`) |
|---|---|---|
| Compact card | `ytd-compact-video-renderer` | `compactVideoRenderer`, `videoRenderer` |
| Lockup card | `yt-lockup-view-model` | `lockupViewModel` |
| Home grid media | `ytd-rich-grid-media` | (DOM path; JSON enrichment is WATCH_NEXT-only) |
| Home grid unit | `HOME_ITEM_SEL` (`ytd-rich-item-renderer`, section, shelf) | — |

`ytd-compact-radio-renderer` is intentionally **not** selected (playlist shelves
are not video impressions; on HOME a section still consumes a `slot_index`).

## Historical fixtures

`search_*.html` are retained for a future SEARCH surface (explicitly out of scope
in WO-010); they are not run in tests and stay outside the authenticity guard.

`home_grid.html` is active (WO-010). Card markup is from the live lockup capture
re-parented into a home grid shell (headless YouTube returns a consent shell with
no feed). Prefer a full live `#contents` capture when a consented session is
available — follow §0 above.

`watch_next_compact.html` is a **historical component shape** (compact renderers
are extinct on current watch-next). It uses real public video/channel IDs and an
intentional title-less card for `slot_index` gap coverage. Prefer
`watch_next_lockup.html` + `yt_initial_watch.json` as the live-shape canaries.

## Decisions (WO-005 §3)

- **compact-radio:** Not in `CARD_SEL`. Current watch-next serves lockups only
  (`compactVideoRenderer` count 0 in the 2026-08-02 capture). Playlist/mix
  shelves are not video impressions for P0; if they reappear as a distinct
  component they must consume a slot without requiring a `video_id`.
- **first-observation-wins on `slot_index`:** Keep. Insertions above a recorded
  card are unobserved; rewriting slots on every scan would thrash the corpus.
  Revisit if live QA shows systematic drift.
