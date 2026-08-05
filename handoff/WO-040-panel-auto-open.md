# WO-040 — Side panel auto-opens when a video link is clicked on the full page; panel links navigate in place

| | |
|---|---|
| **Addressee** | Jr Dev (opencode) |
| **Status** | Done |
| **Date** | 2026-08-03 |
| **Source** | Lars, 2026-08-03 |

---

## Problem

Clicking a video link on the full-page surface (search results, suggestions) opens the video in a
new tab, but the side panel stays closed there. The panel is the place Keel's context is read; on a
new watch tab it is empty chrome until the user reaches for the toolbar icon.

Panel link clicks were the opposite failure: the panel's own video links used `target="_blank"`,
opening yet another tab. The panel is meant to be a companion while a video plays — "thats the whole
point of the side panel" — so a panel link must load in the active tab, not spawn a new one.

## Behaviour decided

**Always open.** Clicking any video link from the full-page surface opens the side panel in that
window, even if the user had explicitly closed it. Lars's rationale, which is the decision: to reach
the full page you must have clicked a button in the panel first, so the panel is effectively always
meant to be open; the one place you act on a recommendation is the one place the panel should be
showing. Option B (remember last per-window state) was considered and rejected — the state is almost
always "open" anyway, and a remembered-close would silently break the flow it exists to serve.

**Panel links navigate the active tab** (see Mechanism).

## Mechanism

### Full page → new watch tab with panel (auto-open)

`page/index.js` delegates a `click` listener on `#results` and `#suggestions`. On a plain left click
of `a[href^="https://www.youtube.com/watch?v="]` it intercepts the default new-tab navigation and,
**while the user gesture is still alive**, runs three calls in sequence:

1. `tabs.create({ url, active: true })` — the new watch tab (no `tabs` permission needed).
2. `sidePanel.setOptions({ tabId, path, enabled: true })` — the panel is per-tab opted in by
   content-script arming (WO-021) and disabled by default; it must be enabled before `open` succeeds.
3. `sidePanel.open({ tabId })` — targets the tab-specific panel on the new tab.

Modified clicks (meta/ctrl/shift/alt, middle button) keep their native behaviour.

### Panel → active tab (same-tab navigation)

Side-panel frames cannot navigate to a web URL, so `sidepanel/index.js` delegates a `click` listener
on `#list`; on a plain left click of a watch link it prevents the default and calls
`tabs.update(activeTab, { url })` where `activeTab` is the window's active tab
(`tabs.query({ active: true, currentWindow: true })`, guarded to a `youtube.com` URL). Host
permission covers the URL; no `tabs` permission, no manifest change.

### Why not the earlier design (SW-deferred open)

The first implementation sent `{ type: "OPEN_PANEL" }` to the SW, which remembered the window and
called `sidePanel.open({ windowId })` when the arriving tab armed. Two facts kill it:

- `sidePanel.open` "may only be called in response to a user action" — user activation does **not**
  survive a `runtime.sendMessage` round-trip, so the SW call was rejected.
- `open({ windowId })` only applies to a **global** panel; ours is tab-specific (WO-021), so the
  open must name the tab.

Hence the open must be called from an extension page or content script inside the click, targeting
`tabId`. That is what the mechanism above does. Chrome ≥ 116 (the manifest floor) has
`sidePanel.open` and requires no `tabs` permission.

## Not in this ticket

- Remembering per-window open/closed state (rejected above).
- Firefox: `sidebar_action` has no `sidePanel.open`; the guards
  (`browser.tabs?.create`, `browser.sidePanel?.setOptions?.open`) no-op there. A Firefox port is not
  blocked on this.

## Acceptance

- [x] Clicking a search or suggestion video link on the full page opens the side panel on the new
      watch tab.
- [x] Clicking a video title in the panel loads that video in the active tab — no new tab opens, the
      panel stays open on the same tab.
- [x] Modified clicks (ctrl/cmd/shift/middle) keep native new-tab behaviour.
- [x] Panel stays closed when navigating to a watch page without a full-page link click (typed URL,
      YouTube's own links).
- [x] No new permissions, no manifest change.
- [x] 26 JS tests still pass.
- [x] Live QA on Brave after extension reload.

## Pushback invited

If `sidePanel.open({ tabId })` proves flaky after the `tabs.create` await (user-activation edge on
Brave), the fallback is to call `open` in the click first with `tabs.create`'s promise, or to open
the panel on the current window synchronously and accept the brief disabled state — raise it, don't
ship it silently.
