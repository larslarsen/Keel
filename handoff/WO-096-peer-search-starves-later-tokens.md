# WO-096 — Superseded by WO-097 and WO-095

| | |
|---|---|
| **Status** | **Superseded before implementation** |
| **Date** | 2026-08-13 |
| **Superseded by** | WO-097 — complete search foundation; WO-095 — responsive streaming peer search and UI |

This order correctly recorded the live defect: the first token consumed the
whole query's six-second context and later tokens started expired. Its proposed
repair was incomplete because parallel work alone still produces an atomic UI
without per-peer progress events and a live delivery path.

WO-097 now owns the corrected continuous tokenizer, complete title index,
pagination, and retained word targets. WO-095 owns immediate job
acknowledgement, session-scoped progress, bounded parallel responses,
candidate-set union, local full-query verification, and the streaming UI.

This order must not be implemented.
