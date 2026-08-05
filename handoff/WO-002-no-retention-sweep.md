# WO-002 — Remove the retention sweep

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Open |
| **Date** | 2026-08-02 |
| **Amends** | `BUILD_P0.md` §7, §9 · `DESIGN_v2.md` §4.3, §8 |

## Change

**Do not implement the 90-day retention sweep.** If it exists, remove it. The daemon keeps the
corpus indefinitely.

## Rationale

The sweep was wrong and contradicted the design:

1. **It defeats G2 (preservation).** The stated success criterion is that a video removed in month
   *N* still has a complete record in month *N+12*. A 90-day sweep deletes the record before the
   removal it is supposed to witness.
2. **It degrades the product.** The Local Interest Vector and "recommended because you watched X"
   both get better with history depth. Deleting history makes recommendations worse — the opposite
   of the point.
3. **It bought no privacy.** This is the user's own data, about themselves, on their own machine.
   We never receive it, so no minimisation obligation attaches to us. Deleting it is a product
   defect dressed as a privacy feature.
4. **No storage pressure.** Metadata rows are small; a year of heavy use is tens of MB in SQLite.

## What replaces it

- **Default: keep everything, forever.**
- **P1:** a user-settable retention limit, defaulting to *off*. The user's choice, never ours.
- **Delete-all** stays available at any time (already specified).

Nothing in the browser changes — the extension still persists nothing (WO-001 §2).

## Acceptance

- [ ] No time-based deletion anywhere in `daemon/`.
- [ ] Records written a year ago are still queryable.
- [ ] The retention acceptance criterion is gone from `BUILD_P0.md` §9.
