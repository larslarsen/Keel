# WO-090 — Remove stale copy claiming Live works at Level 1

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Sonnet) |
| **Status** | **Done** |
| **Date** | 2026-08-12 |
| **Source** | WO-089 implementation review |

## Problem

WO-089 correctly moved the complete Live system to Level 2, but two strings in
`extension/page/index.js` still describe the superseded Level-1 behavior:

1. `renderSwarm()` says that being disconnected is normal at the default
   setting “for everything except livestreams.” Live is also unavailable at
   the default setting.
2. `applyCapabilityUi()` says local search, suggestions, graph pre-walk,
   “Live and word statistics all work” at Level 1. Live does not.

The daemon policy and the dedicated Live-page explanation are correct. This is
a user-facing disclosure regression, not a network-policy defect.

## Required change

1. In `renderSwarm()`, remove the livestream exception. When Level 1 is
   disconnected, say simply that no peer connection is currently available;
   do not imply that Live is expected to work at Level 1.
2. In the Level-1 distributed-search explanation in `applyCapabilityUi()`, list
   only features Level 1 actually retains: local search, suggestions, graph
   pre-walk and fetched global word statistics. State separately that shared
   Live and distributed peer search start at Broad sharing.
3. Add DOM regression tests that exercise both rendered paths and assert that
   neither string says or implies that Live works at Level 1. Use the existing
   page test harness; do not create a second UI test framework.

## Do not

- Do not change `PolicyForLevel`, Live gossip, word telemetry, consent, or any
  other runtime behavior.
- Do not make the Live tab disappear. At Level 1 it remains visible and
  unavailable with the existing direct Broad-sharing explanation.
- Do not rename the contribution levels or introduce a new network mode.

## Acceptance

- [x] Every Level-1 message on the full page agrees that shared Live starts at
      Level 2.
- [x] The network-search explanation still says which Level-1 features work.
- [x] The disconnected-swarm message does not claim Live is an exception at
      the default setting.
- [x] Regression tests cover both corrected render paths.
- [x] `npm test` and `git diff --check` pass.

## Implementation note — 2026-08-12

`renderSwarm()`'s disconnected copy no longer carves out a livestream
exception. `applyCapabilityUi()`'s Level-1 network-search reason now lists
only what Level 1 retains (local search, suggestions, graph pre-walk,
downloaded global word statistics) and states separately that shared Live and
distributed peer search both start at Broad sharing. Both paths are covered
by new regression tests in `test/search-entitlement.test.js`, reached through
the real render paths (module-load `refreshStats()` and the existing
Level-1 `CONTRIBUTION_STATUS` gate test) rather than a source-text search.
`npm test` (169/169) and `git diff --check` pass.

## Challenge

If a test cannot reach one of these paths through the public page behavior,
improve the existing harness or export the smallest pure renderer. Do not
weaken the assertion to a source-text search.
