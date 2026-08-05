# WO-009 — Stop showing recommendations, without changing or blocking them

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Done** — live-verified by Lars 2026-08-03. Hiding, resize and live toggle all work. |
| **Date** | 2026-08-02 |
| **Origin** | Lars, 2026-08-02: *"can't we make it so the youtube suggestions don't load… we can't CHANGE them but we can prevent them from loading, right?"* |

Suppress recommendations in the UI while continuing to record them.

---

## DO THIS — four fixes, all small. Detail in "LIVE QA FAILED" at the bottom.

**1. `HIDE_STATE` never reaches content scripts.** `sw.js` `broadcast()` uses
`runtime.sendMessage`, which delivers only to extension pages. Content scripts need
`tabs.sendMessage(tabId, msg)`. The SW already holds `tabs.query` and site-wide host permission:

```js
const tabs = await browser.tabs.query({ url: YT_URL });
for (const t of tabs) if (t.id != null) browser.tabs.sendMessage(t.id, msg).catch(() => {});
```

Keep `runtime.sendMessage` for the panel. This is why hiding only updates on page refresh —
`startHide()`'s one-time `GET_HIDE_STATE` is currently the only working path.

**2. Hiding is WATCH PAGE ONLY.** Delete the `ytd-browse[page-subtype="home"]
ytd-rich-grid-renderer` rule from `CSS_TEXT` in `hide.js`. The feature exists to reclaim width for
the player; the homepage has no player. The earlier spec said to hide the home grid — **that spec was
wrong, not your implementation.**

**3. Add a HOME tile to the SidePanel.** `renderStats` writes only `total` and
`by_surface.WATCH_NEXT`, so homepage collection is invisible and the panel looks frozen. It is not —
panel and corpus agree exactly. `stats.by_surface.HOME` already exists; the `.stats` grid is
`auto-fit`, so a third tile needs no layout work.

**4. REVERSED 2026-08-03 — hide `#secondary` itself again.** The previous instruction (leave the
column in place, hide only its contents) made layout deterministic by giving up the width
permanently. Lars: *"before it was making the video player wider most of the time, now it's not."*
Widening most of the time is better than never. Go back to `display: none` on
`ytd-watch-flexy #secondary`.

To make it widen *reliably*: `ytd-watch-flexy` recomputes player size from window width on resize, so
after adding or removing the style, dispatch a resize on the next frame:

```js
requestAnimationFrame(() => window.dispatchEvent(new Event("resize")));
```

This is allowed — a content script dispatching a DOM event is not a MAIN-world script and does not
touch page JS directly (§4.1 stands). Try it; if the player still only widens sometimes, report what
you observe rather than adding more CSS. Inconsistent-but-usually-wider is the accepted fallback.

**Then verify each mode — `never` / `with-panel` / `always` — takes effect without a page reload.**
If `never` still leaves the rail hidden once tab messaging works, that is a separate bug in the
`storage.onChanged` listener in `hide.js`. Report it; do not paper over it.


## Why — the real driver is layout, not bandwidth

Lars, 2026-08-02: *"We kind of have to remove the side recommendations because it doesn't leave room
for ours AND the player."*

The SidePanel is browser chrome: opening it narrows the page viewport. On a watch page the remaining
width is then split between the player and YouTube's `#secondary` rail, and neither gets enough. This
is not an optional cleanup — **the watch-page UI does not work with both rails present**, which
changes two things below: the default (§3) and the risk that matters most (§4a).

The bandwidth saving is a side effect, not the point. Do not optimise for it.

---

## 0. What this does and does not change

**It does not change the recommendations.** Nothing here reorders, substitutes, filters or edits what
YouTube's algorithm produced. The corpus records exactly the same rows, in the same order, with the
same `slot_index`, whether the toggle is on or off — that is the §Acceptance criterion. Keel remains
a faithful witness to what YouTube served.

**It does not stop them loading, either.** Be precise about this, because it is easy to assume
otherwise: the card markup and the `ytInitialData` blob arrive over the network regardless. CSS
suppresses *painting*, not delivery. The measurable saving is thumbnail images, which browsers skip
inside a `display: none` subtree — real, but it is a bandwidth saving, not non-delivery.

Actual non-delivery would require network interception, which §1 rejects.

**What it does change is rendering**, and that is worth one paragraph in `DESIGN_v2.md` for two
practical reasons, not philosophical ones:

1. Every prior work order kept Keel strictly non-interacting with the page. An extension that injects
   a stylesheet can be blamed for YouTube layout breakage; a pure observer cannot. That is a support
   and diagnosis burden worth naming.
2. Store listings and the project's own claims describe what the extension does to pages. If the
   claim is "reads only," it stops being accurate here.

This is **within the stated purpose** — `AGENTS.md` opens with "Gives people control over the video
recommendations they see," and the extension description is "See and control how YouTube recommends
videos to you." Control was always the point.

**Amend `DESIGN_v2.md` in the same commit** (handoff rule 3) — a short section noting the extension
suppresses display under user control, and that suppression does not affect collection.

## 1. Hide, do not block

**Hiding is CSS injected from the existing ISOLATED content script.** No new permission, no new
manifest key, no MAIN-world code.

**Extraction is unaffected, and this was verified, not assumed:** `extension/content/*.js` contains
no `innerText`, no `getBoundingClientRect`, no `offsetParent`, no `checkVisibility`. Extraction reads
attributes and `textContent` only. A `display: none` element remains in the DOM and remains fully
extractable, so **the corpus is unchanged whether the rail is visible or hidden.** Do not switch any
extractor to a layout-dependent or visibility-dependent API, or this stops being true.

### Blocking the network is rejected

1. **It is banned.** `DESIGN_v2.md` §4.1: no MAIN-world scripts, no `fetch`/XHR interception.
   Network-level blocking would need `declarativeNetRequest`, outside the pinned permission set in
   `AGENTS.md`.
2. **It destroys the measurement.** A recommendation that never arrives cannot be recorded. The
   corpus is the product; trading it for bandwidth is the wrong trade.
3. **It is fragile and hostile.** Blocking YouTube's own endpoints tends to break unrelated page
   behaviour, and is the kind of thing that gets an extension delisted.

Note that hiding already recovers most of the loading cost: browsers skip image fetches inside a
`display: none` subtree, so thumbnails do not download.

## 2. Scope — WATCH PAGE ONLY

**Corrected 2026-08-03 by Lars:** *"it removes the main youtube.com page of suggestions too, which it
shouldn't — it's just trying to make space for the watch page's player."*

- **WATCH_NEXT rail only.** The purpose of this feature is reclaiming width for the player. The
  player only exists on `/watch`, so hiding is only ever justified there.
- **Do NOT hide the HOME grid.** The homepage has no player and no space contention; hiding there
  just blanks the page. Remove the `ytd-browse[page-subtype="home"] ytd-rich-grid-renderer` rule from
  `CSS_TEXT` in `hide.js`.
- Do **not** hide the player, search box, subscriptions, or navigation.

The earlier version of this section told the implementer to hide the home grid. That was written when
the stated goal was "stop showing recommendations" in general; the actual goal is layout on the watch
page. **The spec was wrong, not the implementation.**

HOME collection is unaffected either way — hiding never touched extraction.

## 3. Default: tied to the SidePanel, not a plain off switch

The original spec said "default off." That was written when hiding was a nicety. Now that the
watch-page UI does not work with both rails present, shipping it off ships it broken — but shipping
it always-on means a user who never opens Keel finds YouTube's rail silently gone, which is worse.

**Recommended: hide the rail only while the SidePanel is open.** That matches the actual reason for
the feature exactly — the rail is removed precisely when Keel needs the width, and YouTube looks
untouched whenever Keel is not in use. It also makes the behaviour self-explanatory: close the panel,
the rail comes back.

Implement as three states, defaulting to the middle:

| Setting | Behaviour |
|---|---|
| `never` | YouTube's rail always visible |
| `with-panel` *(default)* | Rail hidden while the SidePanel is open |
| `always` | Rail always hidden |

- Opt in / change from the SidePanel.
- Persist the setting in `chrome.storage` — configuration, not observation data, so it does not
  touch the "no observation data in browser storage" rule (§2.1). One string key, three values.
- Toggling must take effect without a reload: add or remove the injected `<style>`, do not require
  re-navigation.
- The SidePanel should state plainly that hidden recommendations are **still being recorded** — a
  user who thinks hiding means not-collecting has been misled by our own UI.

## 4a. RISK — hiding the rail may not give the space back

**Verify this before building anything else in this WO.** `ytd-watch-flexy` computes player
dimensions in JavaScript from window width, not from whether `#secondary` is present.
`display: none` on the rail will plausibly leave a blank gutter with the player unchanged — in which
case the feature does not solve the problem it exists to solve.

Check first, in the console on a live watch page:

```js
document.querySelector('#secondary').style.display = 'none';
```

Does the player widen? If yes, this WO is straightforward. If no, stop and report before writing
code, because the options get materially worse:

- Forcing the flexy container wider with additional CSS means fighting YouTube's layout JS. It will
  break on their next layout change and it is the most likely source of "Keel broke YouTube" reports.
- Triggering YouTube's own theater mode is a page interaction, not a style — a different and larger
  step than this WO authorises.
- Accepting the gutter means the player stays small and the feature buys only the removal of a
  distraction, not room. That may still be worth shipping, but it is a different trade and Lars
  should decide it rather than have it emerge from implementation.

## 4. Implementation notes

- Inject one `<style>` with a single id into `document.documentElement`; remove that node to disable.
  Do not set inline styles on YouTube's elements — they get overwritten on re-render.
- Use `display: none`, not `visibility: hidden` or opacity: only `display: none` reliably suppresses
  the image fetches.
- YouTube re-renders the rail; the style element survives because it is attached to
  `documentElement`, not inside the container. Confirm it survives an SPA navigation.
- Hiding the homepage grid may stop YouTube's lazy pagination, since there is no visible scroll
  region. That is acceptable — the first page is the recommendation set that matters — but note it in
  the WO when you verify, because it changes how much HOME data is collected per visit.

---

## Acceptance

- [x] `DESIGN_v2.md` amended to describe suppression (§5.3), with the code.
- [ ] Rail hidden on the watch page with the toggle on; visible with it off. **(live QA)**
- [ ] **Corpus rows are identical with the toggle on and off** — same count, same `slot_index`, same
      fields. This is the criterion that matters; verify it against the live corpus, not fixtures.
- [x] No new permissions; no `declarativeNetRequest`; no MAIN-world script; no `fetch` interception.
- [x] Toggle takes effect without a page reload (storage + `HIDE_STATE`); style on `documentElement`.
- [x] SidePanel states that hidden recommendations are still recorded.
- [x] Default is `with-panel`; `never` and `always` both work (unit-tested `shouldHide`).
- [ ] Player actually reclaims the width, or the gutter limitation is reported and accepted (§4a). **(live QA)**

## Implementation (2026-08-03)

| Piece | Where |
|---|---|
| Pref key `hide_recommendations` | `extension/lib/prefs.js` |
| CSS inject / remove | `extension/content/hide.js` — `ytd-watch-flexy #secondary` + resize nudge |
| Panel open signal | SidePanel `runtime.connect({ name: "keel-sidepanel" })`; SW counts ports |
| Setting UI | SidePanel select + note that hide ≠ stop recording |
| Design note | `DESIGN_v2.md` §5.3 |

### Live-QA fixes (same day, second pass)

| # | Fix |
|---|---|
| 1 | `broadcastHideState` → `tabs.sendMessage` to YT tabs + `runtime.sendMessage` for panel |
| 2 | Removed home-grid hide rule; watch page only |
| 3 | SidePanel HOME count tile (`by_surface.HOME`) |
| 4 | **Reversed:** hide `#secondary` itself again; after toggle, `requestAnimationFrame` → `resize` so flexy remeasures |

**Re-verify on Brave without reload:** `never` / `with-panel` / `always`; open/close panel under default; player widens most of the time when rail hides.

## Pushback invited

If hiding the container turns out to break YouTube's own layout — a collapsed column, or the player
resizing oddly — say so rather than working around it with more CSS. A narrower target (hiding the
individual cards rather than the container) may be the better shape, at the cost of the image-fetch
saving.

---

## LIVE QA FAILED — 2026-08-03

Two defects. The first is certain and explains "always hidden no matter what".

### 1. `HIDE_STATE` never reaches content scripts

`sw.js` `broadcast()` uses `browser.runtime.sendMessage()`. That delivers to extension pages
(SidePanel, popup, options) and **not to content scripts**. Reaching a content script requires
`chrome.tabs.sendMessage(tabId, …)`.

Consequence: `hide.js` receives its state exactly once, from its own `GET_HIDE_STATE` request during
`startHide()`, and never again. `panelOpen` is frozen at page-load value. If the panel was open when
the tab loaded, the rail hides and **closing the panel cannot un-hide it** — the `with-panel` default
is therefore permanently stuck on for that tab.

**Fix:** broadcast to tabs, not just the runtime. The SW already holds `tabs.query` and site-wide
host permission (WO-008/WO-010), so:

```js
const tabs = await browser.tabs.query({ url: YT_URL });
for (const t of tabs) if (t.id != null) browser.tabs.sendMessage(t.id, msg).catch(() => {});
```

Send `HIDE_STATE` that way (keep `runtime.sendMessage` for the panel). Wrap per-tab sends in a catch —
tabs without a live content script reject, and that is normal.

**Also verify the mode dropdown path.** `hide.js` has a `storage.onChanged` listener that should carry
mode changes independently of `HIDE_STATE`, but Lars reports the dropdown has no effect either. Once
tab messaging works, re-test `never` / `with-panel` / `always` separately and confirm each takes
effect without a page reload — if `never` still leaves the rail hidden, the storage listener is not
firing in the content script and that is a second, separate bug.

### 1b. Confirmed by Lars 2026-08-03: only updates on page refresh

*"it sort of works, but it doesn't update unless you refresh the page."* That is defect 1 exactly —
`startHide()`'s one-time `GET_HIDE_STATE` is the only path that works, so a reload is the only way to
pick up a change. Fixing the tab messaging fixes this.

### 1c. Remove the HOME rule from `CSS_TEXT`

See the corrected §2. Hiding is watch-page only. This is a one-line deletion in `hide.js`.

### 1d. SidePanel has no HOME tile — counts look frozen

Reported as "counts haven't been updating". They are updating correctly: panel showed 3671 total /
3055 WATCH_NEXT while the corpus held exactly 3671 / 3055 / 616 HOME. **No stats bug.**

The cause is display: `renderStats` writes only `total` and `by_surface.WATCH_NEXT`. Browsing the
homepage moves neither tile visibly — HOME climbs invisibly — so the panel looks stuck.

This was WO-010's acceptance item "SidePanel reports per-surface counts", which was not built. Add a
HOME tile beside WATCH_NEXT. The stats payload already carries `by_surface.HOME`; the daemon
populates all five surfaces. `.stats` is `grid-template-columns: repeat(auto-fit, minmax(110px, 1fr))`
so a third tile reflows without layout work.

### 2. Player width is inconsistent — the §4a risk, realised

Lars: *"It both shrinks the window and doesn't shrink the window depending on what I did."*

This is exactly what §4a predicted: `ytd-watch-flexy` sizes the player in JS from window width, so
hiding `#secondary` sometimes lets the layout reflow and sometimes leaves a gutter, depending on what
triggered a recalculation. Non-deterministic width is worse than a consistent gutter.

**Do not chase this with more CSS** — that is the fragile path §4a warned about. Options, in order:

1. Accept a consistent gutter: hide the rail's contents but leave `#secondary` occupying its column,
   so layout never changes. Loses the width, keeps the distraction removal, fully deterministic.
2. Report back with what actually triggers the reflow (resize? theater toggle? SPA nav?) and let Lars
   decide whether it is worth pursuing.

Lars has already said, before implementation: *"Nevermind then if it doesn't get wider."* Option 1 is
the default unless he says otherwise.

---

## REVIEW 2026-08-03 — fixes 1–4 accepted, one defect found

All four landed correctly: `broadcastToYoutubeTabs` via `tabs.sendMessage`, HOME rule removed from
`CSS_TEXT`, HOME tile rendering `by_surface.HOME`, and `#secondary` hidden again with a
`requestAnimationFrame` resize nudge fired on **both** inject and remove, only when the style
actually changed. 25 tests pass.

### Defect — `bumpCounts` attributes every insert to WATCH_NEXT

`sidepanel/index.js`:

```js
const w = Number(el.watch.textContent);
if (Number.isFinite(w)) el.watch.textContent = String(w + inserted);
```

`STORE_UPDATED` carries an insert count with no surface, so homepage collection inflates the
WATCH_NEXT tile until the next `GET_STATS` corrects it ~5 s later. Harmless before fix 3; now
visibly wrong, and it makes the new HOME tile look inconsistent with the one beside it.

**Fix (pick one):**

- Derive the surface from `payload.lastPage.impressions[0].surface` and bump that tile only; or
- Bump `total` alone and let the 5 s refresh settle the per-surface tiles. Simpler, and the tiles are
  never wrong — only briefly stale.

The second is preferable: an optimistic count that can be attributed to the wrong surface is worse
than one that lags by a few seconds.
