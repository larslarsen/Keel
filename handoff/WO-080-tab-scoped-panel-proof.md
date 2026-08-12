# WO-080 — Side-panel page proof must be tab-scoped, not extension-global

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Architecture decided — ready for Sr Dev (Claude Sonnet/Opus)** |
| **Date** | 2026-08-11 |
| **Source** | Architecture review; follow-on to WO-073 |

## Problem

`sw.js` stores one `lastPage` for all tabs. WO-073 prevents a YouTube proof
from seeding a TikTok panel, but two YouTube tabs (or two TikTok tabs) can still
overwrite one another. The panel is per window and the observation proof is per
tab; platform filtering cannot repair the lost tab identity.

## Required change

- Introduce one in-memory proof store with the shape
  `Map<tabId, {windowId, pageLoadId, platform, surface, focus, impressions,
  failures, railGeneration}>`. Put it in a pure module with no browser APIs.
- Accept proof writes only from a content-script message whose `sender.tab.id`
  and `sender.tab.windowId` exist. Ignore any tab/window identifiers supplied by
  the payload. A write replaces only that tab's entry.
- Derive the panel's proof from its window's active tab, not from whichever tab
  last emitted an observation.
- Keep the bounded disconnected buffer separate from page-proof ownership.
- Remove proofs on `tabs.onRemoved`. On navigation to a new document/surface,
  invalidate the old `pageLoadId` before accepting the replacement; late
  messages from the previous document cannot restore it. Bound the map to the
  set of open observed tabs and clear it on service-worker restart—never persist.
- `PANEL_CONTEXT_QUERY` resolves the requesting panel's `windowId`, queries the
  active tab in that window, then returns
  `{window_id, tab_id, platform, surface, focus, proof|null}`. The proof must
  come from that exact `tab_id` and current `pageLoadId`.
- Context/store broadcasts carry source `window_id` and `tab_id`. A panel may
  refresh counts for a generic corpus change but ignores any proof/seed whose
  ids do not match its active context.

## Do not

- Do not use platform equality as a substitute for tab identity.
- Do not introduce the `tabs` permission; sender tab IDs and existing APIs are
  sufficient.
- Do not persist proofs in browser storage.

## Acceptance

- [ ] Two same-platform watch tabs with different seeds never cross-seed the
      panel when switching tabs or windows.
- [ ] Background-tab observations cannot replace the active panel's proof.
- [ ] Existing cross-platform behavior from WO-073 remains covered.
- [ ] Tests cover same-platform, cross-platform, multi-window, navigation and
      tab-removal cases.
- [ ] Tests cover a delayed old-page message after SPA/full navigation, unknown
      sender tab, panel query with no active proof, and service-worker restart.

## Challenge

If a browser cannot identify the panel's window/tab relationship, document the
specific API limitation and present a fail-closed UI state rather than guessing.
