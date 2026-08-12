# WO-082 — Reconcile the standing architecture and current work-order authority

| | |
|---|---|
| **Addressee** | Architect |
| **Status** | **Final audit performed 2026-08-12 — completion waits on WO-085, WO-088 and WO-083** |
| **Date** | 2026-08-12 |
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
- Updated `AGENTS.md`, `ROADMAP.md` and `handoff/README.md` to point at the
  current architecture-review implementation order. WO-085 is now the current
  implementation order; WO-088 and WO-083 follow it.
- Decided the four-level contribution matrix, including Level-1 full
  consumption, live gossip, word HLL/CMS exchange, no block service and no
  three-gram telemetry origination.
- Decided single daemon ownership, bridge capability negotiation, tab-scoped
  proofs and extension module boundaries in WO-079–083.
- Corrected the daemon-only browser-persistence rule and the standing phase
  numbering contradictions found during review.

## Audit result — 2026-08-12

Graphify and direct inspection confirm:

- WO-079, WO-080, WO-081 and WO-084 are implemented. Their standing
  architecture descriptions now match the code. Remaining verification is
  operational: WO-079 Windows live QA, WO-080 same-platform multi-tab live QA,
  and WO-084 two-machine network inspection.
- WO-084's runtime and `PRIVACY.md` had landed, but the full-page contribution
  explanation and public README still described mirror-only Level 2. The
  consent page also claimed TikTok was out of scope and overstated claim-key
  unlinkability as protection against network-metadata linkage. This pass
  corrected those shipped disclosures.
- WO-083 has **not** landed. `sw.js` remains a 1,100+ line stateful dispatcher;
  only `lib/native.js` and WO-080's pure `page_proofs.js` match the selected
  module table. There is no `background/panel_context.js`, `background/rpc.js`,
  background storage adapter or surface render-module split.
- WO-085 has **not** landed. `PolicyForLevel` has no distributed-search
  entitlement, `handlePeerSearchContext` contacts the current node without a
  level check, and the Level-1 test still permits the feature. User-facing copy
  therefore continues to describe peer search as currently available at Level
  1; WO-085 must change daemon behavior and copy together.
- WO-081's protocol and session gate are implemented, but its contribution UI
  violates the accepted “visible and disabled” rule by deleting the radio rows
  when `contribution_runtime` is absent. WO-088 records the narrow UI/test fix.

## Remaining final pass

1. Implement WO-085, then update `PRIVACY.md`, consent and level UI atomically
   from “currently available at Level 1” to reciprocal Level-2 peer search.
2. Implement WO-088, preserving visible disabled controls without inventing an
   effective level, then implement WO-083 and reduce `sw.js` to the recorded
   composition boundary.
3. Re-run Graphify's standing-document/implementation audit, remove the
   remaining implementation-gap notes from `ARCHITECTURE_CURRENT.md`, and then
   close this order. Do not treat the three outstanding live/network QA items
   as implemented code gaps, but keep them visible in the handoff index.

## Do not

- Do not delete historical rationale that explains a rejected approach.
- Do not restore the superseded mirror-only Level-2 policy; WO-084 owns it.
- Do not claim a feature is shipped merely because a work order has code.

## Acceptance

- [x] A new engineer can identify the current architecture, current work order,
      and applicable hard constraints without resolving contradictions.
- [ ] Every stated persistence and outbound-data rule agrees across standing and
      user-facing documents. Shipped Level-2 disclosure is reconciled; the
      selected Level-1 peer-search boundary still awaits WO-085 implementation.
- [x] The current phase map has one numbering scheme and one dependency story;
      historical phase ledgers are labelled or updated.

## Verification — 2026-08-12

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `npm test` (11 suites)
- `git diff --check`

## Challenge

If keeping a historical design statement in place is useful, label it explicitly
as superseded and link to the replacement rather than editing around it.
