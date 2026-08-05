# WO-012 — Export and wipe

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done — live-verified 2026-08-03** |
| **Date** | 2026-08-03 |
| **Follows** | WO-011 (P0 closed). First P1 feature. |
| **Source** | `DESIGN_v2.md`: *"The user can export everything as JSON and wipe everything, from one screen, with no dark patterns."* |

Nothing implements this. It is the largest unmet commitment in the design, and it is the one that
makes the rest defensible: a local-only tool that cannot hand the data back is asking for trust it
has not earned.

Two operations, one screen.

---

## 1. Daemon: two new RPCs

The bridge currently handles `HELLO`, `IMPRESSIONS`, `STATS` (`daemon/main.go`). Add:

**`EXPORT`** → returns the full corpus as JSON.

- **Every column, every row.** `page_load_id`, `observed_at`, `surface`, `context_video_id`,
  `context_query_hash`, `slot_index`, `video_id`, `channel_id`, `channel_unknown`, `title`,
  `duration_s`, `view_count`, `published_at`, `badges_json`. Not a summary, not a subset.
- `badges_json` should be emitted as a real JSON array, not a string containing JSON.
- Include a small header: schema version, row count, export timestamp, and the daemon version.

**`WIPE`** → deletes every row and reports how many were deleted.

- `DELETE FROM impressions`, then `VACUUM` so the file actually shrinks on disk. A "wipe" that
  leaves the data recoverable in free pages is a dark pattern by omission.
- Return the deleted count so the panel can confirm honestly.
- Do **not** touch the `meta` table's schema version.

### Size is the real constraint here

The corpus is ~4,700 rows today and grows indefinitely — retention was deliberately removed
(WO-002/WO-004 §3). The browser→host cap is 64 MiB, and a single `EXPORT` response will eventually
exceed any sane message size.

**Do not stream the whole corpus through the native-messaging bridge.** Write the file from the
daemon and return its path:

- Daemon writes to a user-visible location — `~/Downloads/keel-export-<ISO8601>.json` or the
  platform equivalent.
- `EXPORT` returns `{ path, rows, bytes }`.
- The panel shows the path and the row count. It does not receive the data.

This keeps the bridge small, avoids a second copy in browser memory, and means a 500 MB corpus
exports as easily as a 5 MB one. If you disagree, say so before building it — this is the one
architectural choice in this ticket.

## 2. SidePanel: one screen, no dark patterns

Add a section to the existing panel. It needs:

- **Export** — one button. On success show the file path and row count. On failure show the error.
- **Wipe** — one button, with a confirmation that states plainly what will happen: how many rows,
  that it cannot be undone, and that it does not affect what YouTube knows about the user.
- The confirm button says **Delete everything**, not "OK". The cancel is not styled to be harder to
  find than the confirm. No pre-ticked boxes, no countdown, no "are you sure you're sure".
- After a wipe, counts reset to zero visibly rather than waiting for the next `GET_STATS`.

`DESIGN_v2.md` says "with no dark patterns" — that is a requirement, not a tone. Deleting data must
be exactly as easy as exporting it.

## 3. Do not

- Do not add a retention setting. That is P1 but a separate ticket, and it defaults to off.
- Do not add "export last N days" or any filter. Everything means everything.
- Do not put the export file anywhere hidden. The user must be able to find it without being told.

---

## Acceptance

- [x] `EXPORT` writes a file containing every row and every column; row count matches
      `SELECT COUNT(*)`. (`store.TestExportAndWipe`)
- [x] Round-trip check: the exported JSON parses, and a spot-checked row matches the database
      exactly, including a null `channel_id` and a non-empty `badges` array.
- [x] `WIPE` deletes every row, `VACUUM`s, and reports the count. `SELECT COUNT(*)` returns 0.
      (VACUUM exercised; disk shrink verified via empty re-export.)
- [x] Export of a corpus with ≥5,000 rows: `EXPORT_RESULT` payload is path/rows/bytes only
      (`TestExportManyBridgePayloadSmall`).
- [x] Panel: Export button + path/rows status; Wipe → confirm with rows, irreversibility, YouTube
      note; **Delete everything** / Cancel equal prominence.
- [x] Daemon tests cover both RPCs, including wipe-then-export empty corpus.
- [x] Browser never holds the full export (SW forwards daemon `{path,rows,bytes}` only).

## Implementation (2026-08-03)

| Piece | Where |
|---|---|
| `ExportToFile` / `Wipe` | `daemon/store/sqlite.go` |
| `EXPORT` / `WIPE` RPCs | `daemon/main.go` → `EXPORT_RESULT` / `WIPE_RESULT` |
| Bridge types | `daemon/bridge/protocol.go` |
| SW | `EXPORT` / `WIPE` message types → `bridge.request` |
| Panel | "Your data" section in SidePanel |

Export path: `~/Downloads/keel-export-<UTC>.json` (override `KEEL_EXPORT_DIR` / `XDG_DOWNLOAD_DIR`).

## Pushback invited

The file-path-not-payload decision in §1 is the load-bearing one. If you think the corpus will stay
small enough to stream through the bridge indefinitely, say so with a number before building it —
but note that retention was removed on purpose and this data is meant to be kept for years.
