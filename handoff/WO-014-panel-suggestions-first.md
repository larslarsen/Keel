# WO-014 — SidePanel: suggestions first, everything else collapsed

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done — 2026-08-03** |
| **Date** | 2026-08-03 |
| **Source** | Lars, 2026-08-03: *"we're already filling the panel up with a bunch of stuff I don't care about at all"* |

Layout only. **No behaviour changes, no new features, no daemon or extraction work.**

---

## The problem

The panel has grown four sections and the one thing it exists for is last:

1. Counts — tiles, time range, channel-coverage note
2. Hide recommendations — dropdown + explanation
3. Your data — export, wipe, confirmation
4. **Live page — the actual list of suggestions**

Sections 1–3 accumulated because the panel was the only surface available, so every feature landed
there. The channel-coverage note in particular is a research caveat that does not belong in front of
someone who just wants to see what YouTube recommended.

## Target

```
┌──────────────────────────┐
│ Desktop app connected    │
├──────────────────────────┤
│ 0. Video title here      │
│ 1. Another video         │
│ 2. And another           │
├──────────────────────────┤
│ ▸ Counts                 │
│ ▸ Hide recommendations   │
│ ▸ Your data              │
└──────────────────────────┘
```

## Do this

1. **Connection banner stays at the top.** It is the one thing that explains an empty list.
2. **The suggestions list moves directly under it**, above everything else. The `live-meta` line
   (`page abc123… · 20 item(s)`) stays with it.
3. **The three remaining sections become collapsed `<details>` blocks**, in this order: Counts, Hide
   recommendations, Your data. Use native `<details>`/`<summary>` — no framework, no new dependency,
   keyboard accessible for free.
4. **Collapsed by default**, every time the panel opens. Do not persist open state; that is another
   storage key for no benefit.
5. **The channel-coverage line moves inside Counts.** It stays exactly as worded — WO-013 requires
   the gap be visible — but it belongs next to the numbers it qualifies, not above the list.
6. **The Refresh button moves inside Counts.** The list updates itself via `STORE_UPDATED`; the
   button only exists for the stats round-trip.
7. **Keep the privacy line**, but one short line and small. It is the first-run trust signal.

## Do not

- Do not change what any control does. The hide dropdown, export, wipe and their confirmations keep
  their current behaviour exactly.
- Do not remove the channel-coverage note or soften its wording (WO-013).
- Do not touch `renderPage`'s incremental `<li>` reuse — it exists so the list does not rebuild on
  every update.
- Do not add anything new to the panel. If something seems to want adding, it is a different ticket.

---

## Acceptance

- [x] Layout: banner + privacy → **This page** list → collapsed Counts / Hide / Your data.
- [x] Counts, Hide, Your data use native `<details>` closed by default (no open state persisted).
- [x] Refresh + channel-coverage note live inside Counts only.
- [x] Control IDs unchanged — hide/export/wipe behaviour untouched; `index.js` unchanged.
- [x] No daemon / extract / observer / protocol changes.
- [x] 25 JS tests pass.

## Implementation

| File | Change |
|---|---|
| `sidepanel/index.html` | Reorder; folds via `<details>` |
| `sidepanel/style.css` | `.panel-fold` / `.panel-primary`; list fills remaining height |

## Pushback invited

If `<details>` fights the existing CSS badly enough that it wants a restyle, say so — but restyling
the panel is not in scope here and would be its own ticket.
