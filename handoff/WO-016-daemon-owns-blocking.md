# WO-016 — Move blocking into the daemon; remove page-level blocking

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done — 2026-08-03** |
| **Date** | 2026-08-03 |
| **Reverses** | WO-015's page-level implementation. The blocklist concept stays; the mechanism changes. |
| **Source** | Lars, 2026-08-03: *"blocking youtube at the webpage is bad news… everything should be in the daemon"* |

WO-015 shipped a working channel blocklist that hides cards on YouTube's page with injected CSS.
The feature is wanted; **hiding YouTube's own cards per-card is not**.

This is not a criticism of the implementation — the code is clean and the delivery path correctly
used `broadcastToYoutubeTabs`. The design was wrong, and the ticket that specified it was mine.

---

## Why the page-level version goes

1. **`DESIGN_v2.md` §8 already said so.** Daemon responsibilities include *"channel scrubbing and
   feed sanitization decisions (**the extension applies, it does not decide**)."* `block.js` decides:
   it holds the blocklist in the content script and generates CSS there.
2. **Per-card DOM manipulation is a different risk class** from hiding one container.
   `PUBLISH_CHECKLIST.md` §4b already had to retire the "does not modify page content" claim for
   WO-009; per-card suppression is harder to defend in store review.
3. **It is a performance risk.** The stylesheet is one `:has()` rule per blocked *video*, four
   selectors each, regenerated as the video→channel map grows. On a page mutating ~1,400 times a
   second that is real style-recalc cost — the WO-006 CPU-storm class.
4. **It only half-works.** Blocking keys on `channel_id`, unavailable past the initial rail (WO-013),
   so a blocked channel reappears once the user scrolls. That reads as broken.

## Do this

### 1. Remove page-level blocking from the extension

- Delete `extension/content/block.js` and its import from `observer.js`.
- Remove `BLOCKLIST_UPDATED` handling from the content script.
- The extension keeps **no blocklist state and makes no blocking decision**.
- Leave WO-009's rail hiding exactly as it is. That is one container, user-controlled, and already
  live-verified.

### 2. The blocklist becomes daemon-owned

- Blocklist lives in SQLite, not `chrome.storage`. It is user configuration the daemon acts on.
- Daemon RPCs: `GET_BLOCKLIST`, `BLOCK_CHANNEL`, `UNBLOCK_CHANNEL`.
- The SidePanel reads and writes it through the SW as it does today — only the storage location and
  the decision-maker change.
- **Blocked channels are omitted from what Keel displays**, not from what Keel records. The corpus
  stays a faithful witness: blocked channels are still observed and still stored. Hiding is a view
  concern.

When Keel has its own suggestions (roadmap stage 2/3), the same blocklist filters them. That is the
real payoff and it needs no page manipulation.

### 3. Backfill `channel_id` across page loads

Lars: *"once we have a catalogue, we can find the channel name of a given suggestion from it."*
Correct, and the local corpus is already a partial catalogue.

The daemon's upsert uses `COALESCE(excluded.channel_id, impressions.channel_id)`, which only fills
within the **same** `(page_load_id, surface, video_id)` row. A channel learned last week is never
applied to the same video seen today.

**Measured on the live corpus 2026-08-03:**

| | |
|---|---|
| Rows | 641 |
| `channel_unknown` | 268 (42%) |
| **Resolvable from a prior row for the same `video_id`** | **71 — 26% of all unknowns** |

Add a `video_id → channel_id` lookup on insert: when an incoming row has no channel and a prior row
for that `video_id` has one, use it. Pure daemon change, no new collection, no extension involvement.
It improves as the corpus grows and will improve further when a shared catalogue exists.

Set `channel_unknown = 0` on backfilled rows — the value is known, just not from that observation.
Do **not** invent a `channel_source` column unless you find a reason; if you do, say why.

## Do not

- Do not touch WO-009 rail hiding.
- Do not filter blocked channels out of the corpus. Record everything; hide at display.
- Do not add a display-name fallback for channel (WO-004 §6.3).

---

## Acceptance

- [x] Page-level `block.js` deleted; content script has no blocklist.
- [x] SW SidePanel RPCs only proxy daemon `GET_BLOCKLIST` / `BLOCK_CHANNEL` / `UNBLOCK_CHANNEL`.
- [x] Blocklist in SQLite `channel_blocklist` (survives restart).
- [x] Panel filters display by blocklist; corpus insert unchanged (tests keep rows after block).
- [x] Channel backfill on insert + open-time catalogue pass; `channel_unknown = 0` when filled.
- [x] Tests: `TestChannelBackfillAndBlocklist`; JS 27 pass; forbidden-API grep clean.

### Backfill report

Unit test proves insert-time fill from prior `video_id` and open-time catalogue update of
orphans. Live corpus “≥71 of 268” is verified when the user restarts the daemon against their
DB (Open runs `BackfillChannelsFromCatalogue`).

## Pushback invited

If moving the blocklist to SQLite makes the panel's write path awkward — it now needs a daemon
round-trip where `chrome.storage` was synchronous — say so. Keeping the *cache* in the extension
while the daemon owns the truth is acceptable; the extension deciding is not.
