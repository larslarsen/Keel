# WO-018 — Funnel inspector: why was this recommended?

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done — 2026-08-03** |
| **Date** | 2026-08-03 |
| **Source** | `User Utility Architecture.md` §6; `ROADMAP.md` stage 2 |

First feature built *on* the corpus rather than to fill it. No new collection, no network, no
extraction changes — a daemon query and a small panel affordance.

---

## What it answers

`User Utility Architecture.md` §6 asks for an "Algorithm Info" affordance that demystifies the
sidebar. With the corpus as it stands, the honest answer is observational:

> **Seen 7 times.** After *Video X* (4×, usually slot 2), *Video Y* (2×, slot 5), *Video Z* (1×,
> slot 11). First seen 12 days ago.

### Say what we observed, never what YouTube intended

**Do not phrase this as "recommended because you watched X."** We do not know YouTube's reason and
cannot. We know only that B appeared in A's rail, at these positions, this many times.

The wording matters — this project's credibility rests on not overclaiming, and `DESIGN_v2.md` §6.4
already says plainly that this is a survey instrument, not a proof system. "Appeared after" is true.
"Because you watched" is a claim about an algorithm we cannot see.

## Do this

### 1. Daemon RPC

`EXPLAIN_VIDEO { video_id }` → `EXPLAIN_RESULT`:

- `total_impressions` — how many times this video has been observed at all
- `first_observed_at`, `last_observed_at`
- `contexts[]` — for `WATCH_NEXT`, the `context_video_id`s it appeared under, each with a count,
  median `slot_index`, and the context video's own `title` where the corpus has it
- `home_impressions` — count on HOME, where there is no context video
- `slot_histogram` — counts per slot bucket, so "usually near the top" is visible rather than asserted

Cap `contexts[]` at a sensible number ordered by count. Do not return the whole edge list.

### 2. Panel affordance

- Each row in the suggestions list gets a small "why" control. Keep it as light as the WO-017 hide
  icon — this list is the primary surface and must not become dense.
- Clicking it shows the explanation inline or in a small expanding region. Not a modal.
- Handle the empty case honestly: a video seen once, in this page load only, should say so rather
  than rendering an empty table.

### 3. Titles for context videos

`contexts[]` is far more useful with titles than bare IDs. The corpus has a title for any video it
has recorded as an impression — but a *context* video is one the user watched, which may never have
been recorded as an impression itself. Return `null` and let the panel show the ID when that happens;
**do not fabricate or fetch it.**

## Do not

- Do not add collection. This ticket reads the corpus, it does not grow it.
- Do not call any YouTube endpoint for titles or metadata (hard rule, `DESIGN_v2.md` §3.2).
- Do not phrase output as causal. Observed co-occurrence only.
- Do not touch the extraction path, the observer, or the hide/blocklist code.

---

## Acceptance

- [x] `EXPLAIN_VIDEO` / `EXPLAIN_RESULT` in daemon; store tests match counts (incl. single + HOME-only).
- [x] Context titles from corpus or null → panel shows ID.
- [x] UI: "Appeared after", "Seen N times", disclaimer — not causal.
- [x] Per-row light icon (`btn-why`); inline expand, not modal.
- [x] JS tests pass; no extract/observer/hide changes.

### Implementation

| Piece | Where |
|---|---|
| Query | `store.ExplainVideo` |
| RPC | `EXPLAIN_VIDEO` → `EXPLAIN_RESULT` |
| SW | `EXPLAIN_VIDEO` message |
| Panel | `formatExplain` + why button |

## Pushback invited

If the per-row control makes the list feel cluttered, propose an alternative — a single "why" on tap
of the row itself, for instance. The constraint is that the list stays the clean primary surface
WO-014 and WO-017 made it; how the affordance achieves that is yours.
