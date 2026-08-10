# WO-064: Watch queue (persistent, daemon-owned, ordered)

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Depends on:** existing `BLOCK_CHANNEL` RPC path (WO-016) — mirror it.

## What to build

A user-controlled "watch queue": from any suggestion in the side panel, a button
adds that video to an ordered queue; a "Watch queue" section lists the queue with
play / remove / reorder controls. The queue persists across daemon restarts.

## Hard constraints (load-bearing — do not route around)

- **No browser storage of any data.** AGENTS.md §2.1: "No observation data in
  browser storage… In-memory only." A watch queue is user-intent, but the safe,
  consistent design is to store it in the daemon (same place the blocklist lives).
  Do NOT use `chrome.storage` / IndexedDB / localStorage for it.
- **No MAIN-world scripts, no `fetch`/XHR, no YouTube Data API** (AGENTS.md §4.1,
  §3.2). The extension only reads the rendered DOM and talks to the daemon.
- **No framework / bundler / build step** in the extension. Plain ES modules.
- **Play = normal navigation.** Opening a queued video means opening
  `watch?v=<id>` in a tab — the same link the panel already renders. This is a
  user navigation, not "automated means."
- **Do NOT use YouTube's native "Up next" / queue.** There is no extension
  interface for it. Driving it would require either the YouTube Data API
  (`playlistItems.insert` — forbidden by §3.2, and its terms force deletion within
  30 days) or a content script clicking the in-page control — which is "automated
  means… without YouTube's prior written permission" (YouTube ToS) and "mimic or
  replicate YouTube's core user experiences" (YouTube API Developer Policies §C).
  Keel owns the queue; play opens YouTube via a normal link.

## Implementation

### Extension (`extension/sidepanel/index.js`)

- In `makeSuggestionLi()` (~line 231), add a third button inside the existing
  `.row-actions` span, after the `why` and `block` buttons. Copy their pattern:
  `document.createElement("button")`, `type="button"`, `btn-icon` class,
  `title`/`aria-label` = "Add to queue", an SVG icon, and a click handler that
  calls `rpc("QUEUE_ADD", { video_id: s.video_id })` (swallow/log errors like the
  block handler does).
- Add a "Watch queue" section to the side panel (markup in
  `extension/sidepanel/index.html`, render logic in `index.js`). On load and after
  any mutation, call `rpc("QUEUE_LIST", {})` and render the ordered list:
  - each row: thumbnail (reuse `fillThumb` + `thumbHtml`), title, a **Play** link
    (`watchUrl(video_id, platform)` — already exists), a **Remove** button
    (`QUEUE_REMOVE {index}`), and **Up / Down** reorder buttons
    (`QUEUE_REORDER {from, to}`). No drag-and-drop (no framework).
  - empty state: a `.meta` line "Queue is empty."
- Persist nothing in the extension; the daemon is the source of truth.

### Daemon

- **Storage** (`daemon/store/`): a `watch_queue` table, ordered (e.g. an
  `position` integer or an `added_at` timestamp; order = queue order). Mirror how
  the blocklist is stored/queried. Methods: `AddToQueue(videoID)`,
  `ListQueue() []QueueItem`, `RemoveFromQueue(index)`, `ReorderQueue(from, to)`.
- **Dispatch** (`daemon/main.go`): add cases `QUEUE_ADD`, `QUEUE_LIST`,
  `QUEUE_REMOVE`, `QUEUE_REORDER` — mirror `BLOCK_CHANNEL` / `UNBLOCK_CHANNEL`
  (~line 262).
- **Protocol** (`daemon/bridge/protocol.go`): add `QueuePayload`
  (`{video_id}` / `{items:[{video_id,added_at}]}`) and `QueueIndexPayload`
  (`{index}` / `{from,to}`) — mirror `BlocklistPayload` / `ChannelBlockPayload`
  (~line 179).

### Tests

- Daemon unit tests in `daemon/store/` mirroring `store/blocks_test.go`:
  add → list order correct; remove shifts order; reorder moves item; persistence
  across a re-opened store.
- Extension: a test asserting the add-to-queue button exists per suggestion row
  and that `QUEUE_LIST` rendering handles empty + populated.

## Acceptance

- [ ] "Add to queue" appears on every suggestion in the side panel and adds the
      video to the daemon queue.
- [ ] Watch-queue section lists items in order, with working Play (opens
      watch?v=id), Remove, and Up/Down reorder.
- [ ] Queue survives a daemon restart (persisted in SQLite).
- [ ] Nothing is written to browser storage for the queue.
- [ ] Play opens YouTube via a normal link — no YouTube API, no DOM-driving.
- [ ] Daemon tests pass; each claimed bug has a regression test.

## Pushback invited

- Ordered queue vs simple set: we chose ordered (play-through semantics). If you
  think a set is enough, say why.
- Reorder via up/down vs drag: we chose up/down (no framework). Flag if drag is
  worth the code.
- Persistence: we chose daemon (matches blocklist, satisfies §2.1). Session-only
  was rejected because the extension must not store it.
