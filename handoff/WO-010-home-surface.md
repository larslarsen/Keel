# WO-010 — Collect suggestions everywhere, not just watch pages

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Nested-shelf root fixed — needs live QA (homepage + SPA click-through) |
| **Date** | 2026-08-03 |
| **Absorbs** | WO-004 §10 (widen injection). Do not go read WO-004 for it; everything is below. |
| **Origin** | Lars: "we need to collect all the suggestions the user gets." |

Today the extension records the watch-page rail, and only when the user hard-loads a `/watch` URL.
Clicking a video from the homepage records nothing, and the homepage's own recommendations are never
recorded at all. Both are the majority of real use.

---

## 1. Widen injection

`extension/manifest.chrome.json` and `manifest.firefox.json`:

- `content_scripts.matches`: `"*://www.youtube.com/watch*"` → `"*://www.youtube.com/*"`
- `host_permissions`: widen to match.
- Re-run `npm run manifest:chrome`.

This is what makes homepage→video clicks record: a soft SPA navigation never injects, so today the
script simply is not running unless the tab started on `/watch`.

## 2. HOME surface

`extract.js` `surfaceFromUrl()`: `/` returns `{ surface: "HOME", context_video_id: null }`.

Check `validateImpression` in `lib/protocol.js` does not demand `context_video_id` for `HOME` — the
existing check is `WATCH_NEXT`-specific and should stay that way. `HOME` is already in `SURFACES` and
`daemon/bridge/protocol.go` already accepts it. No schema or protocol change needed.

The homepage is a **grid** (`ytd-rich-grid-renderer` / `ytd-rich-item-renderer`), not the watch rail.
`container()` in `observer.js` is watch-specific — make it resolve a container **per surface**, not
by bolting more fallbacks onto the existing chain.

**`slot_index` on the grid is row-major** — left to right, then down. Decided; do not re-litigate.
Put the rule in a comment where the index is assigned.

**Non-video items must still consume a slot** — shelves, ads, Shorts rows, playlist and channel
cards. If they do not, every index below them is wrong, in the way WO-004 §4 describes.

## 3. Stay idle off-surface

Widening the match means the script loads on every YouTube page. On anything that is neither `/` nor
`/watch` it must do **nothing**: no observer armed, no `ytInitialData` parse, no retry loop, no timers
left running. Verify on `/feed/subscriptions` and a channel page.

## 4. Fixtures — captured, not written

Capture the real homepage `#contents` per `test/README.md`. Do not hand-author one: a synthetic
fixture has now caused three production defects on the watch path, most recently `view_count` null on
every card because the real markup omits the word "views" entirely.

Extend the fixture-authenticity test to glob `home_*` and delete the P1-placeholder exemption in
`extract.test.js`. Replace the existing `home_*.html` placeholders rather than building on them.

## 5. Out of scope

**No SEARCH.** Search results are things the user asked for; homepage cards are things YouTube chose
to show them unprompted. Only the second is a recommendation. Do not add it because the enum has a
slot for it. Leave `CHANNEL` and `SHORTS` alone for the same reason.

---

## Acceptance

- [ ] Load `youtube.com`, click a video → impressions recorded for that watch page.
- [ ] Load the homepage → impressions recorded, `slot_index` contiguous and row-major.
- [ ] Non-video grid items consume a slot rather than shifting the index.
- [ ] Nothing runs on `/feed/subscriptions` or a channel page — no timers, no parse.
- [ ] Watch-page collection still works exactly as before.
- [ ] Homepage fixture is a real capture; authenticity test covers `home_*`.
- [ ] 60 s of homepage scrolling, no jank on Brave. The homepage paginates indefinitely and the
      throttle was tuned for a ~20-card bounded rail — this is the harshest case it has faced.

## Pushback invited

If per-surface containers want a bigger refactor of `observer.js` than this implies, say so before
doing it rather than after.

---

## LIVE QA FAILED — 2026-08-03. Root cause found, one function.

Homepage records **nothing**. `PAGE_CONTEXT HOME` reaches the SW exactly once; `IMPRESSIONS` never
does. Watch pages are unaffected.

**Not** the container chain, injection, permissions, validation or the daemon — all verified working.
`surfaceFromUrl` returns HOME, the observer arms, `armMo()` schedules a scan, and both validators
accept HOME records.

### The defect: `extractFromHomeContainer` searches descendants before itself

Measured on a live homepage, calling the real extractor against the real container:

```
container direct children: 25  (ytd-rich-item-renderer, ytd-rich-section-renderer,
                                ytd-continuation-item-renderer)
extractFromContainer(...)  →  candidates: 3   impressions: 0   failures: 0
```

25 children in, 3 candidates out — it is iterating a different node.

The cause is the resolution order:

```js
contents =
  el.querySelector?.("ytd-rich-grid-renderer > #contents") ||   // ← descends first
  el.querySelector?.("ytd-rich-grid-renderer #contents") ||
  (el.id === "contents" && parent is ytd-rich-grid-renderer ? el : null) ||
  ...
```

`observer.js` already hands this function the correct node (`ytd-rich-grid-renderer #contents`). But
**live shelves contain nested `ytd-rich-grid-renderer` elements**, so the first `querySelector` finds
a shelf's inner grid and iterates that — 3 items, none of which parse as cards. Zero impressions,
zero failures, nothing emitted, no error anywhere.

### Fix (landed 2026-08-03)

In `extractFromHomeContainer`: treat `root` as the grid `#contents` **before** any descendant
search. Only when it is not already that node, resolve a grid via direct-child combinators
(`:scope > #contents` on a grid; `ytd-rich-grid-renderer > #contents` from a shell). Never prefer
a shelf-nested grid over the outer contents observer already handed in.

### Why the tests did not catch it

`home_grid.html` previously contained exactly **one** `ytd-rich-grid-renderer`, so the descendant
search found nothing and fell through to the correct node. The live homepage has several — one per
shelf. The fixture was a real capture trimmed to a few cards; trimming removed the nested grids.

**Regression coverage now in place:** `home_grid.html` nests a 3-item shelf grid inside the section;
`extract.test.js` asserts `candidates === outer #contents.directChildren` when root is that node
(the observer path). `candidates === 3` is an explicit failure (the live-QA mode).

### Remaining

Live QA on Brave: homepage impressions + SPA click-through + 60 s scroll. Unit tests alone cannot
close the WO.
