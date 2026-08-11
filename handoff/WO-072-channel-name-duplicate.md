# WO-072 — Same channel name appears twice in "Channels seen most" (two channel_ids)

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Date:** 2026-08-11
**Source:** Lars, 2026-08-11 — on the fullpage tab's "Channels seen most" list,
"Basic Logic" appears TWICE with DIFFERENT counts. That is a bug: one channel
should be one row.

## Diagnosis (high confidence from the query)

The list is built by `store.Analysis` (daemon/store/analysis.go:61-69):

```sql
SELECT i.channel_id, MAX(c.name), COUNT(*) AS n, AVG(i.slot_index)
FROM impressions i
LEFT JOIN channels c ON c.channel_id = i.channel_id
WHERE i.channel_id IS NOT NULL AND i.channel_id != ''
GROUP BY i.channel_id ORDER BY n DESC, i.channel_id ASC LIMIT ?
```

The `GROUP BY i.channel_id` is correct — it does NOT merge two rows with the same
name. So two rows with the same display name must have **two distinct `channel_id`
values**. I.e. the same channel ("Basic Logic") is stored under two different
channel_ids in the `channels` / `impressions` tables, and each gets its own
aggregated count. The list then shows "Basic Logic" twice (with different
channel_ids, hence different counts).

## Root cause (to verify — two candidates)

The channel identity is not canonicalized to a single id. Likely one of:
1. **UC-id vs @handle form.** YouTube exposes a channel both as `UCxxxx` and as
   `@handle`; if the observer captures both forms at different times, they land as
   two channel_ids. Check `extension/content/extract*.js` / `extract.js`
   `channelIdFromHref` + `videoIdFromHref` — does it normalize `@handle` to the
   `UC` form, or store whichever it saw?
2. **channel_unknown backfill mismatch.** `TestChannelBackfillAndBlocklist`
   (sqlite_test.go:371) shows a video can be observed once WITH a channel and once
   WITHOUT (channel_unknown=true), then backfilled. If two observations of the same
   channel were stored with different ids (one real, one a variant/empty), they
   split.

Verify with:
```sql
SELECT channel_id, name FROM channels WHERE name = 'Basic Logic';
-- expect: one row. If two distinct channel_id -> confirms the split.
```

## What to fix (Opus decides)

- Canonicalize channel identity so one channel = one channel_id. Most likely: in the
  observer/extraction, always reduce `@handle` → `UCxxxx` before storing (YouTube
  resolves @handle to a UC id; the extractor should too, or the store should
  normalize on insert). Once ids are canonical, `GROUP BY channel_id` collapses the
  duplicate into one row with the summed count.
- Do NOT "fix" the SQL (it is correct). The bug is upstream in identity capture.

## Verification

- Repro: open fullpage tab → "Channels seen most" → find a name appearing twice.
- DB check above confirms two channel_ids for one name.
- Fix: after canonicalization, the same name yields exactly one row; its count =
  sum of the previously-split counts.
- Regression: a channel observed under both `@handle` and `UCxxxx` forms lands as
  ONE channel_id (add a test seeding both forms, asserting one row in
  `top_channels`).
