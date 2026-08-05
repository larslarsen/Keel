# WO-007 — Rail replacement reuses slot numbers; panel shows repeated numbers

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Implemented, live-verified by Lars on Brave 2026-08-02 |
| **Date** | 2026-08-02 |
| **Follows** | WO-006 |
| **Source** | SidePanel QA after WO-006 |

---

## Defect

The SidePanel numbered every suggestion, but numbers repeated: e.g. `0.`, `0.`, `1.`, `2.`,
`2.`, … — only sometimes sequential. Reproduced from live daemon data, not guessed.

## Root cause

YouTube renders the watch-next rail, then **swaps it ~2 s after page load**: generation A at
`t=0` (slots 0–19), generation B with *different videos reusing the same slot numbers*.

The extension stored both generations under the same `page_load_id`, and `slot_index` is the DOM
position at extraction time — so generation B's videos legitimately collided on slots. The panel's
`lastPage` was append-only and sorted by `slot_index`, so it displayed both sets interleaved:

```
0.  Bodycam Released in Minnesota Triple Mur…
0.  Savannah knows who took her mother?? …
1.  TikTok Invasion! (8/02/26)
2.  Drunk Nurse Doesn't Realize…
2.  Crews respond after roof collapses…
```

Verified in `keel.sqlite`: a single page_load held 34 rows for a ~20-slot rail, with 14 slot
collisions. All collisions shared one `observed_at` ~2.2 s after the first batch.

## Fix (extension only — no daemon, no schema, no protocol version change)

A **rail generation** counter on the watch page.

- `observer.js`: track `generation` (starts 1, reset on navigation). After each extraction, compare
  `slot → video_id` against the previous rail; if any occupied slot now holds a different video,
  YouTube replaced the set → `generation++` and re-emit the whole rail (clear `emittedIds`).
- `sw.js`: `rememberPage` resets `lastPage.impressions` when `generation` changes (or on
  navigation, as before). `lastPage` gains `generation`; `STORE_UPDATED`/`GET_STATUS`/`GET_STATS`
  carry it unchanged. `generation` rides the IMPRESSIONS envelope payload only — per-impression
  validation untouched.
- `sidepanel/index.js`: the list-reset key is now `pageLoadId|generation`, so a rail swap replaces
  the list instead of appending colliding rows.

DB stays as-is: a video appears once per page (first-observed `slot_index`); multi-generation
offers remain visible to funnel analysis via `observed_at`.

## Acceptance

- [x] Watch page: numbers run 0–19 sequential, no repeats. (Verified live by Lars.)
- [x] When the ~2 s rail swap happens, the list *replaces* itself with the new set — no duplicates.
- [x] SPA navigation to a new video still starts a fresh sequential list.
- [x] No schema/protocol change, no daemon change, standing rules respected.
- [x] `npm test` 16/16 pass; `go test -count=1 ./...` pass (daemon untouched).

## Notes for the reviewer on return

- The panel shows raw `slot_index`; gaps occur when a card fails extraction (slot skipped, by
  design — "gaps for skips" in `extractFromContainer`). Cosmetic only.
- Rail-change detection keys on "an occupied slot changed videos." YouTube single-slot swaps will
  count as a replacement — correct, since the offered set changed.
- Re-check the `MAX_NODES_PER_BATCH` bound (WO-006) still holds with the re-emit path: a swap
  clears `emittedIds` and emits ~20 rows once per generation, well within the buffer and throttle
  budget.
