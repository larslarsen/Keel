# WO-075 — Toolbar button is a toggle: closes an open panel

**Addressee:** Sr Dev (Opus)
**Status:** **Done 2026-08-11** — see "What was found and built".
**Date:** 2026-08-11
**Source:** Lars, live QA right after WO-074: *"I can't close the panel on either YT
or TT watch pages by clicking on the keel button."*

## The bug

`action.onClicked` only ever opened the panel on a watch page
(`sidePanel.open({windowId})`). `sidePanel.open()` on an already-open panel is
a no-op — so once open, there was no toolbar path to dismiss it; the panel
stayed open until the user left the watch surface. Lars expected the button to
close it again, like the browser's own toggle affordance.

## The fix

`extension/background/sw.js` — the click handler now toggles:

- If the panel is already open in the clicked tab's window
  (`panelOpen()`, the long-lived `keel-sidepanel` port counter — the same
  state the with-panel hide mode uses; Chrome's sidePanel API has no
  getState), call `closePanelInWindow(windowId, tabId)` and return.
  `sidePanel.close()` needs no user gesture, so this branch is safe anywhere
  in the handler.
- Otherwise the existing open path is unchanged (open() still the first
  awaited call — the WO-071 gesture constraint).
- Off-watch pages unchanged: full-page tab.

The engine guard in `closePanelInWindow` means on engines without
`sidePanel.close` (Firefox) the toggle-off silently no-ops — accepted
Chromium-first degradation; the panel's own close affordance still works
there.

## Verification

- `test/sw-panel-gating.test.js`: new stub helper connects a fake
  `keel-sidepanel` port so `panelOpen()` reads true; tests assert the button
  then closes (close called, open NOT called, no full-page tab) on a YT watch
  tab and on the TikTok FYP. Open-when-closed behaviour still covered by the
  existing tests.
- Full suite: `npm test` 95/95 (3 new). No Go changes.
- Live QA (Lars): on a YT watch page and on the TikTok FYP — click Keel to
  open, click again to close; both directions work; off-watch pages still open
  the full-page tab.
## Follow-up 2026-08-11 — per-window toggle (reported by Lars after WO-074 QA)

Lars: *"On TikTok I can only close the panel, not open it"* — clicking Keel on
the FYP with the panel closed did nothing (no panel, no full-page tab).
YouTube toggled fine in the same session.

Root cause: `panelOpen()` counted `keel-sidepanel` ports globally. Chrome's
side panel is per-window, so with the panel open in one window the toggle in
ANY other window saw "panel open", fired `closePanelInWindow` on the clicked
window's already-closed panel (a no-op), and returned — every click was the
close branch. YouTube's own window had an open panel, which is why it kept
working there.

Fix: the panel now handshakes its window id over the port
(`PANEL_HANDSHAKE`, `browser.windows.getCurrent()` from the panel document),
and `panelOpen(windowId)` answers for the clicked window only. Ports that
haven't handshaken (or handshake with null) stay conservative — "open
everywhere" — so a closed-but-unknown-window panel can never double-open.
Per-window state is in-memory only, like the rest of the port state; it dies
with the SW, and the panel re-handshakes on reconnect.

Verification: 4 new tests in `test/sw-panel-gating.test.js` (cross-window
open, same-window close, reopen after port drop, unknown-window
conservatism). Full suite 99/99. Needs reload + live re-QA on the TikTok FYP
for the final word.
