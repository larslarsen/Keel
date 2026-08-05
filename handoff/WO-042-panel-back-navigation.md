# WO-042 — Back button is broken after panel same-tab navigation (reviewer-owned design)

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Open |
| **Date** | 2026-08-03 |
| **Source** | Lars, 2026-08-03 (reported live on Brave) |

---

## Problem

After WO-040, clicking a video title in the panel navigates the active YouTube tab with
`tabs.update(tabId, { url })` (`extension/sidepanel/index.js`, `openVideoInActiveTab`). On the new
watch page the **browser Back button is effectively dead**: it does not return to the previous video.

Verified on Brave (Chromium, 2026): pressing Back skips the extension-made entries entirely.
Clicking anywhere on the page afterwards does **not** restore the behavior (an older heuristic that
no longer holds). Long-pressing the Back button shows the full entry list and works — but users do
not know that exists, so the panel's main navigation has no discoverable way back.

## Root cause (confirmed)

Chromium's **history manipulation intervention** makes the Back/Forward buttons skip entries that
were created without user activation. The docs are explicit:

> "the intervention only impacts the browser back/forward buttons and not the `history.back()`
> /`forward()` APIs." — `chromium/src` `docs/history_manipulation_intervention.md`

Extension-initiated navigations count as "no user activation"; this was hardened by the
CVE-2025-12431 fix (Chrome 142), so `tabs.update` entries are now always pruned from the Back
button, regardless of later page interaction. This is intentional Chromium behavior — not a Keel
bug. Keel's `pushState`/`replaceState` wrappers (`observer.js:346`) are benign; they delegate to
the originals.

## Direction (reviewer owns the design; mechanism has a documented carve-out)

The intervention does **not** affect `history.back()` called from inside the page. So a panel
Back control that asks the active tab's content script to run `history.back()` should traverse the
real history, including the extension-created entries.

Sketch (not binding):

- `extension/sidepanel/index.html` / `index.js`: a Back control (icon button in the header row,
  mirroring `link-full-page`/`btn-hide-rail`). On plain left click, resolve the active tab
  (`tabs.query({ active: true, currentWindow: true })`, guarded to a `youtube.com` URL — same
  logic as `openVideoInActiveTab`) and send it a message (e.g. `{ type: "GO_BACK" }` via
  `tabs.sendMessage`).
- Content script: a `runtime.onMessage` handler for `GO_BACK` that calls `history.back()`.
  `hide.js:105` already listens for runtime messages, so there is precedent/plumbing to follow.
  Note: guard so the handler only fires when the document is a YouTube page (it already will be,
  by manifest match scope).
- Design decision for the reviewer: how to handle the "nothing to go back to" case (can the panel
  know the tab has prior entries? `tabs.goBack()` also exists but behaves like the pruned Back
  button and is not usable). If `history.back()` proves inconsistent on Brave, raise it with a
  tested alternative — do not ship silently.

## Constraints

- No new permissions, no manifest change, no daemon changes.
- Plain ES modules; no framework/bundler.
- This is a small extension feature; Lars explicitly requested it after the earlier "no features"
  constraint (that constraint was about the panel *display* work, WO-041 — not this fix).
- `tabs.sendMessage` to the active YouTube tab is already used by the SW (`broadcastToYoutubeTabs`),
  so the permission story is unchanged.

## Acceptance

- [ ] From a watch page reached via a panel link, the panel Back control returns to the previous
      video in the same tab (including videos that the browser Back button skips).
- [ ] Modified clicks / keyboard accessibility handled (button is a real `<button>` with
      `title` + `aria-label`).
- [ ] No new permissions, no manifest change.
- [ ] 26 JS tests still pass.
- [ ] Live QA on Brave after extension reload: panel → video A → video B → panel Back → A.

## Out of scope

- Changing how the panel navigates (same-tab `tabs.update` stays — it is the WO-040 decision).
- Fixing Chromium's Back button itself.
