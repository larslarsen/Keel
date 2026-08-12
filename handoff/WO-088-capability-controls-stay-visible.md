# WO-088 — Capability-gated controls stay visible and disabled

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Sonnet) |
| **Status** | **Done 2026-08-12** — see "What was built" at the end. |
| **Date** | 2026-08-12 |
| **Source** | WO-082 final audit of WO-081 acceptance |

## Problem

WO-081 decided that an optional control whose bridge capability is unavailable
stays visible, disabled, and accompanied by an actionable desktop-update
reason. A hidden control looks removed and gives the user no way to understand
what an updated desktop app would restore.

Peer search follows that rule, but contribution controls do not.
`applyCapabilityUi()` calls `contrib.replaceChildren()` when
`contribution_runtime` is absent. The heading and explanation remain while the
actual Level 1–4 choices disappear. WO-081's ticket marks the opposite behavior
accepted, so this is a shipped-contract defect found by WO-082.

## Required change

1. When `contribution_runtime` is absent, render the same four contribution
   rows in their normal location with every radio disabled and a visible
   desktop-update explanation.
2. Do not select a radio or claim an effective level when the incompatible
   daemon did not negotiate the state schema. “Current level unavailable until
   the desktop app is updated” is honest; inventing Level 1 is not.
3. Do not send `GET_CONTRIBUTION` or `SET_CONTRIBUTION` without the capability.
4. Preserve the existing behavior when the capability is present: render the
   daemon's effective level, enforce `max_implemented`, and report stored/
   effective transition disagreement.
5. Add a DOM-level regression test for both capability states. The test must
   fail if the rows are removed, enabled without the capability, or if an RPC
   is attempted while unavailable.

## Ordering

Land after WO-085 because that order changes the contribution-dependent search
copy and negotiated UI state. Land before WO-083 so the control-plane split can
characterize and preserve the corrected behavior rather than moving the bug
into a render module.

## Acceptance

- [x] Missing `contribution_runtime` leaves all four labelled rows visible and
      disabled with a desktop-update reason.
- [x] No level appears selected and no contribution RPC is sent in that state.
- [x] Negotiated `contribution_runtime:1` still renders effective state and
      `max_implemented` exactly as returned by the daemon.
- [x] Peer-search capability behavior remains visible/disabled and unchanged.
- [x] Extension tests pass without adding a runtime dependency or build step.

## Do not

Do not weaken WO-081 to permit hiding controls merely because the present code
does so. The implementation is the part that is wrong.

## What was built (2026-08-12)

`applyCapabilityUi()` (`extension/page/index.js`) called
`contrib.replaceChildren()` when `contribution_runtime` was absent, wiping the
four Level 1–4 rows while the heading/note stayed — exactly the defect WO-082
found. Fixed by extracting the row-building logic `refreshContribution()`
already had into a shared `renderContributionRows(wrap, { level, maxImpl,
interactive })`:

- `interactive: true` (capability present) is byte-for-byte the prior
  behavior: real effective level checked, `max_implemented` enforced,
  transition/disagreement copy unchanged, change listener wired to
  `SET_CONTRIBUTION`.
- `interactive: false` (capability absent) renders the same four labelled
  rows, all disabled, none checked, with "Unavailable until the desktop app
  is updated" per row and no change listener attached at all — so there is no
  path by which an incompatible daemon could receive a `SET_CONTRIBUTION` it
  never negotiated, and no risk of inventing a selected level from stale
  state. `refreshContribution()` already declined to call `GET_CONTRIBUTION`
  without the capability (pre-existing guard); this only fixes what renders
  when it takes that early return.

Also switched the row's `checked`/`disabled` state from HTML-string
attributes to explicit property assignment after element creation — harmless
in a browser either way, but needed for `linkedom` (the test DOM) to read
`.checked` back correctly, and unambiguous regardless of environment.

New test `test/contribution-controls-visibility.test.js` (4 cases, DOM-level
via `linkedom`, same harness as WO-085's `search-entitlement.test.js`):
capability absent → 4 visible+disabled+unchecked rows, no
`GET_CONTRIBUTION`/`SET_CONTRIBUTION` sent; stale daemon state never renders
as checked; capability present → real state renders and RPC fires; peer-search
and contribution controls gate independently off their own capabilities.
Verified red-then-green by reintroducing `contrib.replaceChildren()` and
confirming test 1 fails, then restoring the fix.

`npm test`: 130/130 (full suite, 29 suites). `go build`/`go vet` unaffected —
no Go changes in this ticket.
