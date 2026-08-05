# WO-017 — SidePanel: scroll, and reduce the hide control to an icon

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done — 2026-08-03** |
| **Date** | 2026-08-03 |
| **Source** | Lars, 2026-08-03: *"before the whole panel would scroll when it filled up, now it's scaling everything to fit on the screen… for now scrolling would be better"* |

Two changes, both to the panel. **No daemon, extraction or observer work.**

---

## Symptom

As content grows, the panel squeezes everything into the viewport instead of overflowing. The
suggestions list gets shorter rather than the page getting longer.

## Cause

WO-014 made the panel a fill-the-viewport flex column. Every part now competes for a fixed height:

| `style.css` | Effect |
|---|---|
| `html, body { height: 100% }` | Height is pinned to the viewport |
| `body { display: flex; flex-direction: column; min-height: 100% }` | Children share that fixed height |
| `.panel-primary { flex: 1 1 auto; min-height: 0 }` | Primary section absorbs *and gives up* space |
| `.panel-primary #list { flex: 1 1 auto; max-height: none; min-height: 8rem }` | List shrinks to `8rem` as siblings grow |

Before WO-014 the list had `max-height: 55vh` and the document scrolled normally.

## Do this

Make the **document** scroll. The panel should be as tall as its content.

- Stop pinning height to the viewport — `html, body { height: 100% }` and `min-height: 100%` are what
  force the fit.
- `.panel-primary` and `.panel-primary #list` should size to content rather than flexing to fill.
  Dropping `flex: 1 1 auto` from both is the direct fix.
- Keep the vertical order from WO-014 exactly: banner → privacy line → suggestions list → the three
  collapsed folds. **Do not reorder anything.**
- **Remove the nested scroll on `#list`.** `#list { overflow: auto }` already exists in the original
  CSS (line ~226) and is the thing Lars is objecting to: *"'this page' scrolls but I don't want that.
  I want it to be full page size and the whole panel to scroll."*

  The list must render at its **natural full height** — no `overflow`, no `max-height`, no
  `min-height` floor. One scrollbar, on the panel document, and nowhere else.

  An earlier revision of this ticket offered a nested scroll region as an acceptable fallback. **That
  option is withdrawn.** If the folds end up far down the page behind a long list, that is the
  intended behaviour, not a defect to design around.

## 2. The hide control becomes a small icon button

Lars, 2026-08-03: *"the only thing that really needs to be here is the 'hide youtube suggestions'
thing, which should be a tiny little button, maybe even an icon."*

Today it is a labelled `<select>` plus an explanatory paragraph, buried in a collapsed fold. It is
the one control that gets used while watching, so it should be visible and take almost no space.

- **A small icon button, always visible**, in the panel header next to the banner — not inside a
  fold.
- It toggles hiding on and off. Show state clearly: filled/active when hiding, outline when not.
- Give it a `title` and `aria-label` ("Hide YouTube's suggestions") so it is not a mystery glyph.
- Inline SVG or a text glyph. **No icon font, no image file, no dependency** — the no-runtime-deps
  rule stands.
- The explanatory paragraph goes. If anything remains, one short line in the Counts fold.

### Collapse the three modes to two

`never` / `with-panel` / `always` was invented in WO-009 §3 by the reviewer, not requested. A toggle
is the natural shape for this control, so:

- **On** = hide YouTube's suggestion rail. **Off** = leave the page alone.
- Keep the stored value a string, not a boolean, so a third mode can return without a migration.
- Migrate existing settings: `with-panel` and `always` → on, `never` → off.

If you think dropping `with-panel` loses something real, say so before removing it — but do not keep
a three-state `<select>` alongside a toggle. One control, one meaning.

## Do not

- Do not anticipate moving Counts or Your data to a full-page surface. Lars: *"I'll fix it later."*
  Leave both exactly where they are, in their folds.
- Do not restructure the markup beyond what the icon button needs. WO-014's `<details>` folds stay.
- Do not change export, wipe or refresh behaviour.
- Do not touch `renderPage`'s incremental `<li>` reuse.
- Do not start moving sections to a full-screen surface. That is planned but explicitly later.

---

## Acceptance

- [x] Document scroll: no `html/body` height pin; list natural height, `overflow: visible`.
- [x] Single scrollbar on the panel document; `#list` has no nested scroll.
- [x] Order: banner + hide icon → privacy → list → Counts / Blocked / Your data folds.
- [x] Hide is header icon toggle (`on`/`off`); legacy modes coerced; short note under Counts.
- [x] 26 JS tests pass (prefs updated for on/off + migration).

## Note for later, not now

Counts and Your data are candidates to move to a full-page surface once one exists. Lars is handling
that himself later — **do not anticipate it here.**
