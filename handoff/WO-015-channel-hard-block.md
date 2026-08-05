# WO-015 — Channel hard block

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done — 2026-08-03** (needs live QA: block/unblock first-paint cards) |
| **Date** | 2026-08-03 |
| **Source** | `ROADMAP.md` stage 2; `User Utility Architecture.md` §2; DESIGN §2.1 / §5.3 |

Persistent client-side channel blocklist. Blocked channels’ cards are not shown in the YouTube
feed UI. The corpus still records them (faithful witness — same posture as WO-009 hide).

---

## Constraints (do not violate)

1. **No MAIN world. No `fetch`/XHR interception. No Data API.** (`DESIGN_v2` §4.1, §3.2)
2. **Do not rewrite or delete YouTube DOM nodes.** Hide with CSS only (attribute/`style` element
   on `documentElement`, same family as WO-009). Do not “scrub `ytInitialData` before paint” —
   that requires MAIN-world injection and is rejected.
3. **Blocklist is preference data in `chrome.storage`**, not observation data (§2.1). Keys are
   channel IDs (`UC…`), never raw search queries.
4. **Do not synthesise `channel_id` from display names** (WO-004 §6.3).
5. **Do not drop corpus rows** that lack a channel or are blocked. Still emit impressions.

## WO-013 gap (resolve in this ticket, not in QA)

`channel_id` is reliable for **first-paint** WATCH_NEXT cards (ytInitialData `browseId` map) and
for some HOME cards that expose channel links in the DOM. Scrolled lockups usually have
`channel_unknown: true`.

**Behaviour:**

- Block applies only when Keel knows `channel_id` for that card (map or DOM).
- Cards with unknown channel are **not** blocked by this feature (cannot match).
- SidePanel must state that plainly next to the blocklist UI.
- Blocking a channel hides every **known** video of that channel currently mapped, and future
  cards once their channel is resolved.

## Implementation shape

1. **Storage:** `channel_blocklist: string[]` of `UC…` ids (deduped, validated).
2. **Content script:** after extract + channel enrichment, build the set of `video_id`s whose
   `channel_id` is blocked; inject one `<style id="keel-channel-block">` that hides matching
   cards via `:has(a[href*="watch?v=…"])` (or equivalent) on known card hosts. Re-apply on
   storage change and each scan. Prefer not setting inline styles on YouTube nodes.
3. **SidePanel:** collapsed section **Blocked channels** — list + remove; add by UC id; **Block**
   on a “This page” row when `channel_id` is known.
4. **SW:** get/set blocklist helpers; broadcast to YT tabs like `HIDE_STATE` if needed for live
   updates (storage `onChanged` in content script is enough if both sides write storage).

## Acceptance

- [x] Implemented: CSS hide via `keel-channel-block` + video ids mapped to blocked UC ids.
- [x] Collection still emits blocked rows (hide after enrich, no filter on emit).
- [x] Unknown channel not blocked (no id → no match).
- [x] UI: Blocked channels fold + Block/Unblock on list rows + WO-013 note.
- [x] No MAIN world / network intercept / Data API.
- [x] Prefs unit tests (29 total green).
- [ ] Live QA: block a first-paint channel → card gone; unblock → back; corpus still has rows.

## Implementation

| Piece | Where |
|---|---|
| Prefs | `lib/prefs.js` — `channel_blocklist`, helpers |
| Content | `content/block.js` + observer note after enrich |
| SW | `GET/SET_BLOCKLIST`, `BLOCK_CHANNEL`, `UNBLOCK_CHANNEL` |
| Panel | Fold + row buttons |
| Design | `DESIGN_v2.md` §4.2 |

## Pushback invited

If CSS `:has()` is too weak on a live surface, report which card host fails before inventing
DOM surgery.
