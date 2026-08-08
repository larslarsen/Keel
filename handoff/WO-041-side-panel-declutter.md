# WO-041 — Side panel declutter (design owned by reviewer)

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Mostly resolved — visual judgement outstanding** |
| **Date** | 2026-08-03 |
| **Source** | Lars, 2026-08-03 |

---

## Problem

The side panel is cluttered. Three concrete complaints from Lars:

1. **General clutter** — the panel reads busy and eats vertical space per row.
2. **Buttons take too much vertical room.** Every suggestion row reserves a whole
   second line below the title for the "Why" (analysis) button and the
   "Block"/"Unblock" button (`.row-actions` in `extension/sidepanel/index.js`
   `makeLi`, styled in `extension/sidepanel/style.css`).
3. **Meaningless hashes.** Each row's sub-line shows the raw `video_id` and
   `channel_id` (`UC…`) in monospace, and the list meta line shows a truncated
   `pageLoadId` hash. These are opaque and take up space. Either remove them or
   turn them into actual info.

## Constraint (from Lars)

**No new features in the extension.** This is a display/layout ticket only.
Capturing new fields (e.g. scraping a channel *name* into the record) would be a
feature addition — off the table unless the reviewer argues otherwise *and* the
user approves. Work with the data the records already carry (`title`,
`duration_s`, `view_count`, `published_at`, `badges`, `channel_id`, `slot_index`)
and what the daemon already returns. Note: the extension may not call the
YouTube Data API and must not add network fetches (§3.2, §4.1).

## Suggested direction (not binding — reviewer owns the design)

- Collapse the action buttons into compact icon-only controls aligned with the
  title row so a suggestion takes one line of controls, not two.
- Replace the `video_id · channel_id` sub-line with information already in the
  record (duration, view count) or drop it.
- Drop the truncated page-hash from the meta line.
- Keep the WO-040 behavior intact: title link navigates the active tab
  (`tabs.update`), plain left-click only; modified clicks keep native behavior.
- Keep the row keyed on `video_id|slot_index` reuse in `renderPage` — that is
  load-bearing for the incremental list.
- Keep accessibility: icon buttons still need `title` + `aria-label`; the
  block/unblock and why actions must remain reachable.

## Acceptance

- [ ] Suggestion rows no longer show raw `video_id` / `channel_id` hashes (or
      they become genuine information, approved by Lars).
- [ ] Block and analysis controls no longer reserve a full second line per row.
- [ ] Meta line no longer shows the truncated page hash.
- [ ] WO-040 panel behavior unchanged (same-tab navigation, auto-open).
- [ ] 26 JS tests still pass.
- [ ] Live QA on Brave after extension reload.

## Out of scope

- Adding new fields to extraction / protocol / daemon storage.
- The full page (`extension/page/`) — it has the same hash display; revisit in a
  separate order if Lars wants it.

---

## Engineer response — 2026-08-07

Complaint 3 is resolved: the panel no longer shows the raw `video_id`, the `UC…`
channel key, or the truncated page-load hash. The page-load id survives only as
an internal cache key for deciding when to re-run the walk, and is never
rendered.

Complaints 1 and 2 — general clutter, and the vertical space taken by per-row
action buttons — are visual judgements that need Lars looking at the panel rather
than an engineer guessing at a layout. The panel has changed substantially since
this was filed (suggestions replaced the impressions list, thumbnails and ages
were added), so the right next step is a fresh look rather than acting on the
2026-08-03 description.
