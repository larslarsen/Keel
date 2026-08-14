# WO-108 — Separate WO-106 from unrelated live-QA changes

| | |
|---|---|
| **Addressee** | Sr Dev (GPT-5.6 Luna High) |
| **Status** | **Accepted** 2026-08-14 |
| **Date** | 2026-08-14 |
| **Source** | Reviewer diff after WO-106 implementation |
| **Depends on** | WO-106 implementation present in the worktree |

## Outcome

Keep the correct WO-106 router patch. Keep the two truthful stats corrections
that live QA exposed, and put focused tests around them. Remove the unrelated,
untested service-worker startup behavior that was added without an order.

This is scope cleanup, not a new feature order.

## Reviewer findings

The required WO-106 product change is confined to
`extension/background/rpc.js` plus router and daemon tests. The implementation
also changed the selector diagnostic, both Counts views, optimistic count
attribution, and service-worker startup.

Two of those changes close an already recorded correctness defect:

- TikTok Explore is an existing observed surface, but Counts omitted it; and
- `bumpCounts()` attributed every live insert to `WATCH_NEXT`, a defect already
  recorded in WO-009 and `ROADMAP.md`.

They are sensible to retain. They currently lack focused DOM assertions.

The unconditional top-level `rearmYoutubeTabs()` call in
`extension/background/sw.js` is different. WO-008 deliberately owns recovery
through the standing 30-second watchdog. WO-106 neither found a watchdog defect
nor authorized a second trigger. The new startup call changes every service
worker evaluation and has no test proving call count, failure isolation or
idempotence. Remove that call and its new comment; leave the WO-008 function,
alarm and watchdog path unchanged.

The selector diagnostic belongs to WO-107, which corrects the failure mode it
currently only describes. Do not remove it while preparing that order.

## Required implementation

1. Preserve the sender-derived WO-106 `GET_SELECTORS {platform}` routing and all
   of its tests unchanged in behavior.
2. Remove only the new unconditional top-level `rearmYoutubeTabs()` invocation
   and its accompanying startup comment. Do not alter the watchdog.
3. Keep the EXPLORE tiles in the full page and side panel.
4. Keep `bumpCounts()` limited to the combined total. Per-surface counts wait
   for authoritative `STATS`; no insert lacking a surface may be guessed into
   `WATCH_NEXT`, `HOME` or `EXPLORE`.
5. Add DOM-level regression coverage that proves both views render an EXPLORE
   value from `by_surface.EXPLORE`, and that a side-panel `STORE_UPDATED` insert
   without a stats payload increments only the total. Also prove a wipe clears
   EXPLORE with the other visible counts.
6. Keep the WO-009 and `ROADMAP.md` debt record, but describe this work as the
   WO-009 correctness closure rather than part of WO-106 acceptance.

Do not rewrite unrelated worktree files, regenerate manifests, or change
selector fallback behavior here. WO-107 owns the latter.

## Acceptance

- WO-106 router tests continue to inspect exact `tt` and `yt` bridge payloads.
- The service worker has one rearm owner: the WO-008 watchdog path.
- Focused tests fail if EXPLORE disappears, if an unscoped insert changes a
  per-surface count, or if wipe leaves the EXPLORE count stale.
- `npm test`, daemon Go tests, race, vet and `git diff --check` pass.

## Challenge

If immediate startup rearm fixes a reproduced gap that the 30-second WO-008
contract does not cover, stop and document that concrete lifecycle before
retaining it. Convenience during extension reload is not enough to silently
change the standing recovery architecture.

## Implementation and review record — 2026-08-14

The unconditional service-worker startup rearm and its comment were removed.
`rearmYoutubeTabs()` remains owned by WO-008's standing 30-second watchdog; its
function and alarm path are unchanged.

The existing EXPLORE tiles and total-only optimistic count update were retained.
`test/counts-dom.test.js` imports the real full-page and side-panel modules. It
proves both views render `by_surface.EXPLORE`, drives the real
`STORE_UPDATED` listener to prove an unscoped insert changes only the combined
total, and drives the two-button wipe flow to prove all four visible counts are
cleared.

Reviewer verification: 22 extension suites pass; daemon Go tests, race and vet
pass; `git diff --check` is clean. Accepted without further product-code
changes.
