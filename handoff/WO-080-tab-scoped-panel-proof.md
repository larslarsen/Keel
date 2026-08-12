# WO-080 — Side-panel page proof must be tab-scoped, not extension-global

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Implemented 2026-08-12 — automated acceptance complete; live QA pending** |
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

- [x] Two same-platform watch tabs with different seeds never cross-seed the
      panel when switching tabs or windows.
- [x] Background-tab observations cannot replace the active panel's proof.
- [x] Existing cross-platform behavior from WO-073 remains covered.
- [x] Tests cover same-platform, cross-platform, multi-window, navigation and
      tab-removal cases.
- [x] Tests cover a delayed old-page message after SPA/full navigation, unknown
      sender tab, panel query with no active proof, and service-worker restart.

## Challenge

If a browser cannot identify the panel's window/tab relationship, document the
specific API limitation and present a fail-closed UI state rather than guessing.

## Implemented 2026-08-12 — Jr Dev (opencode)

Landed per the ticket, no deviations from the required change:

- New pure module `extension/background/page_proofs.js` (no browser APIs):
  `Map<tabId, {windowId, pageLoadId, platform, surface, focus, impressions,
  failures, railGeneration}>`; `observeContext` (replaces the proof wholesale
  on a new page_load_id, refuses missing/absent page ids), `observeImpressions`
  (drops any entry whose page_load_id does not match the tab's current proof —
  late old-document messages cannot restore; dedupes by video_id|slot_index,
  sorts by slot, accumulates failures, tracks railGeneration), `remove`,
  `clear`, `get` (defensive copy), bound eviction (32).
- `sw.js`: `lastPage` global and `rememberPage` deleted. PAGE_CONTEXT/
  IMPRESSIONS attribute proofs to `sender.tab.id`/`sender.tab.windowId`
  (payload ids ignored); platform/surface/focus derive from `sender.tab.url`.
  `PANEL_CONTEXT_QUERY` returns `{window_id, tab_id, platform, surface, focus,
  proof}` with the proof from the ACTIVE tab only (via `activeProofForWindow`);
  GET_STATS/GET_STATUS resolve the requester's window's active tab proof;
  PANEL_CONTEXT broadcasts carry `tab_id` + `surface`; STORE_UPDATED carries
  `window_id`/`tab_id`/`proof`; flushBuffer broadcasts counts-only (the
  disconnected buffer is explicitly not proof state); `tabs.onRemoved` drops
  the proof; store clears on wipe and is in-memory per SW instance.
- `sidepanel/index.js`: `panelCtx` gains `tabId`; panel records `myWindowId`
  from `windows.getCurrent` and passes it in PANEL_CONTEXT_QUERY/GET_STATS/
  GET_STATUS; `absorbLastPage` rejects proofs whose windowId/tabId mismatch
  the active context (and keeps the WO-073 platform check); context
  broadcasts changing tab clear the cached proof.

Verification: `test/page-proofs.test.js` (16 tests covering the ticket's
same-platform, cross-platform, multi-window, navigation, tab-removal,
sw-restart, stale-late-message, unknown-sender, bound, merge cases);
`test/sw-lastpage-race.test.js` rewritten — BUG S2 is gone by construction
(two interleaved IMPRESSIONS from two tabs broadcast each own proof);
`test/sw-panel-gating.test.js` updated for the new query/broadcast shapes.
Full suite 119/119. Needs reload + live QA (two same-platform tabs: switching
must not cross-seed).
