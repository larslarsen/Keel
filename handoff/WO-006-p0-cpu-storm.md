# WO-006 — CPU storm freezes the browser (tab-switch hard freeze)

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Implemented, live-verified by Lars on Brave 2026-08-02 |
| **Date** | 2026-08-02 |
| **Follows** | WO-005 note A (first freeze sighting, non-reproducible — now reproduced) |
| **Source** | Live QA on Brave, second sighting, captured with process evidence |

---

## BLOCKER

WO-005 note A recorded a tab-switch hard freeze, one sighting, not reproducible. It recurred and
was live-captured. **Kill-only freeze** — nothing in the browser responds; only killing the process
recovers it. A fix must not ship until this is resolved.

## Evidence (captured with `ps` during the freeze)

| Process | CPU | Notes |
|---|---|---|
| `brave --type=renderer` (watch tab) | **114% sustained**, `R` | 1.3 GB RSS |
| `brave --type=renderer` (second) | **53.5%**, `R` | also spinning |
| `keel-host` daemon | **42.9%, bursting** | threads sleeping when sampled (wchan `futex_do_wait`); ~180 s CPU in ~9 min — driven in pulses, not self-looping |

Killing the two renderers stopped the daemon's bursts immediately. The storm is in the extension
chain. (Machine also runs the user's ffmpeg camera encoder at ~290% — a load confound that makes
the freeze system-wide, but the renderer + daemon storm is the trigger and had to go.)

## Two mechanisms — both fixed

**1. `mutationsRelevant()` did unbounded per-batch work.** The MutationObserver callback ran
`el.querySelector(CARD_SEL)` — a full subtree scan — on every added node of every batch, at a
measured **~1,400 mutations/s** on a live watch page. The callback runs per batch regardless of the
throttle (the throttle only gates `observeDom`), so this alone pinned a renderer at 100%+.

Fix (`observer.js`): `matches()` only (O(1)); never `querySelector` a subtree in the callback. Cap
examined nodes per batch (`MAX_NODES_PER_BATCH = 32`); if the cap is hit, schedule anyway — a no-op
`observeDom` pass is cheap, unbounded callback work is not.

**2. Emit → daemon → panel feedback round-trip amplified every emission.** Each emission ran
`IMPRESSIONS` → daemon insert → SW broadcast `STORE_UPDATED` → side panel `refresh()` → `GET_STATS`
→ daemon `STATS` (full-table `COUNT` + `GROUP BY surface` + `MIN/MAX(observed_at)`). The live rail
serves new video IDs continuously, so this chain fired repeatedly — the daemon's burst profile.

Fix:
- `sw.js`: stop re-broadcasting `STATS_RESULT`/`IMPRESSIONS_ACK` via the bridge hook (it made the
  panel re-enter `GET_STATS` on every reply). `STORE_UPDATED` is now emitted only from the
  `IMPRESSIONS` handler and `flushBuffer`, carrying `lastPage`.
- `sidepanel/index.js`: `STORE_UPDATED` applies `lastPage` from the payload directly and bumps the
  visible counts — **no `GET_STATS` round-trip per emission**. `STATS` is reserved for panel open /
  manual Refresh / daemon reconnect / a 5 s periodic catch-up. List rendering is now incremental:
  `<li>` nodes are keyed by `video_id|slot_index` and reused/moved/removed, never wiped+rebuilt per
  update (addresses the 1.3 GB renderer RSS concern).

## Acceptance

- [x] Hard-load `/watch`, browse + scroll 60 s, switch tabs repeatedly → no freeze, no kill needed.
      (Verified live by Lars on Brave.)
- [x] Page idle: renderer back to low single-digit CPU, `keel-host` ~0%. (Verified live.)
- [x] SidePanel still shows the numbered list and live updates via the `STORE_UPDATED` payload.
- [x] No schema/protocol change; standing rules respected (no build step, bounded work, unchanged
      permission set).
- [x] `npm test` and `go test -count=1 ./...` pass (daemon untouched — no daemon re-verify needed
      beyond the existing suite).

## Notes for the reviewer on return

- The load confound (user's ffmpeg encoder) remains — worth a note in `daemon/README.md` or the QA
  checklist that a busy machine changes the threshold at which an extension CPU cost becomes a
  system-wide freeze.
- WO-004 §2's scrolling-jank fix and WO-005's extractor work are untouched by this change. Only
  scheduler cost and panel feedback were modified.
