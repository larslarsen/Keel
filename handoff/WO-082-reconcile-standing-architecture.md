# WO-082 — Reconcile the standing architecture and current work-order authority

| | |
|---|---|
| **Addressee** | Architect |
| **Status** | **Done — final architecture audit closed 2026-08-12** |
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
  current architecture-review implementation order and, at final close, the
  newly discovered WO-089 consent blocker.
- Decided the four-level contribution matrix, including Level-1 full non-Live
  consumption, fetch-only word HLL/CMS, no block service and no three-gram
  telemetry. The final compliance decision moved the complete Live system and
  outbound word telemetry to Level 2+ in WO-089.
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
- WO-083, WO-085 and WO-088 subsequently landed. Graphify confirms the service
  worker is now a composition root, Level-1 peer search is refused before peer
  contact, serving paths are bounded, and unavailable contribution controls
  remain visible and disabled.
- The final Chrome Web Store policy pass found a different boundary problem.
  Level 1 is the default while the shipped daemon runs the full Live system, and
  the first-run network disclosure appears below an action labelled only
  `Start recording`. The independently starting daemon also lacks the current
  initial-consent record. WO-089 moves Live wholly to Level 2+ and makes the
  corrected Level-1 recording/consumer-network consent enforceable by the
  daemon. Level 1 may fetch global word statistics but does not serve or add its
  local corpus to them.
- WO-089's runtime is implemented and its automated suites pass. The review
  found two stale strings in `extension/page/index.js` that still tell Level-1
  users Live works. WO-090 owns only those copy corrections and their DOM
  regression tests; no architecture or network-policy change remains.

## Closure record

1. WO-085, WO-088 and WO-083 were checked against their implementation records,
   code graph and regression tests; the standing architecture now describes the
   landed boundaries.
2. Outbound-data copy was reconciled so Level 1 is never described as
   network-silent and Level 2 is never described as mirror-only.
3. Current Chrome Web Store rules were checked against the actual process
   boundary. The selected conservative correction is not a partial Live mode:
   Level 1 has no Live topic, gossip, snapshot, local seed or publish path;
   Level 2+ retains the complete feature.
4. WO-089 runtime and primary disclosures landed (Level 1 has no Live or
   outbound word pack; daemon-acknowledged consent is required before any
   swarm node). WO-090 corrected the last two contradictory UI strings and
   added DOM regression coverage.
5. The closing Graphify pass found no current Level-1 Live or outbound-word
   contradiction. `DESIGN_v2` retains the rejected Level-1 Live rationale as
   history, labels it superseded, and now uses Level-2+ wording for the live
   swarm and snapshot behavior.

Operational QA remains for WO-079, WO-080, WO-084, WO-085 and WO-089. Those are
deployment/wire checks, not unresolved architecture or documentation gaps.

## Do not

- Do not delete historical rationale that explains a rejected approach.
- Do not restore the superseded mirror-only Level-2 policy; WO-084 owns it.
- Do not claim a feature is shipped merely because a work order has code.

## Acceptance

- [x] A new engineer can identify the current architecture, current work order,
      and applicable hard constraints without resolving contradictions.
- [x] Every stated persistence and outbound-data rule agrees across standing,
      runtime and user-facing documents. Standing docs (ARCHITECTURE_CURRENT,
      DESIGN_v2, PRIVACY, README, policy handoffs) now describe Level-1
      fetch-only / no-Live. WO-090 corrected and regression-tested the final
      two stale full-page status strings.
- [x] The current phase map has one numbering scheme and one dependency story;
      historical phase ledgers are labelled or updated.

## Verification — 2026-08-12

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `npm test` (16 test files, 169 tests)
- `git diff --check`

## Challenge

If keeping a historical design statement in place is useful, label it explicitly
as superseded and link to the replacement rather than editing around it.
