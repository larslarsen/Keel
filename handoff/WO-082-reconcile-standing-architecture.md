# WO-082 — Reconcile the standing architecture and current work-order authority

| | |
|---|---|
| **Addressee** | Architect |
| **Status** | **Architecture pass complete — final audit after WO-077–083 implementation** |
| **Date** | 2026-08-11 |
| **Source** | Architecture review, 2026-08-11 |

## Problem

The standing documents did not describe one coherent system. Examples found by
the review:

- `DESIGN_v2.md` §2.1 requires daemon-only observation persistence, while §4.3
  still permits extension IndexedDB.
- Contribution levels, their numbering, and their network behavior disagree
  between §6, §7.4, §7.5, `PRIVACY.md`, the roadmap and work orders.
- The phase table and its prose disagree about P4 and the OPRF helper.
- The current-work-order pointer names WO-046 although the handoff index has
  later work, including open orders.

This defeats the repository rule that decisions live in documents.

## Architecture work completed

- Added `ARCHITECTURE_CURRENT.md` as the normative current contract; retained
  `DESIGN_v2.md` as rationale/history where not explicitly amended.
- Updated `AGENTS.md`, `ROADMAP.md` and `handoff/README.md` to point at WO-077
  and the architecture-review implementation order.
- Decided the four-level contribution matrix, including Level-1 full
  consumption, live gossip, word HLL/CMS exchange, no block service and no
  three-gram telemetry origination.
- Decided single daemon ownership, bridge capability negotiation, tab-scoped
  proofs and extension module boundaries in WO-079–083.
- Corrected the daemon-only browser-persistence rule and the standing phase
  numbering contradictions found during review.

## Required final pass

- When WO-077/078 land, update `PRIVACY.md`, consent and level UI atomically to
  describe Level-1 prefix requests, live metadata residuals and the word HLL/CMS
  aggregate. Until then, user-facing policy must describe shipped behavior.
- After WO-079/081/080/083 land, remove the corresponding “not yet implemented”
  differences from `ARCHITECTURE_CURRENT.md` and audit operational READMEs.
- Run a final contradiction search and mark the work orders implemented; do not
  close this ticket merely because the decisions are documented.

## Do not

- Do not delete historical rationale that explains a rejected approach.
- Do not silently choose the unresolved Level-1 policy; WO-078 owns it.
- Do not claim a feature is shipped merely because a work order has code.

## Acceptance

- [x] A new engineer can identify the current architecture, current work order,
      and applicable hard constraints without resolving contradictions.
- [ ] Every stated persistence and outbound-data rule agrees across standing and
      user-facing documents.
- [x] The current phase map has one numbering scheme and one dependency story;
      historical phase ledgers are labelled or updated.

## Challenge

If keeping a historical design statement in place is useful, label it explicitly
as superseded and link to the replacement rather than editing around it.
