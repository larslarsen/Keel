# WO-081 — Keel Bridge needs real compatibility negotiation

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Implemented** 2026-08-12 — protocol/session acceptance complete; WO-088 corrects one contribution-control presentation defect found by WO-082 |
| **Date** | 2026-08-11 |
| **Source** | Architecture review, 2026-08-11 |

## Problem

The extension sends a version in `HELLO`, but the daemon unconditionally
returns `HELLO_ACK { OK: true }`. Envelope `v: 2` protects only the outer shape;
the bridge has acquired many independently evolving RPC payloads. Browser-store
updates and desktop-host updates can therefore be mismatched without a defined
failure mode.

## Required change

Keep envelope `v: 2` as the stable bootstrap frame. The first application frame
must be `HELLO`; no other RPC is accepted until negotiation succeeds.

`HELLO.payload`:

```json
{
  "client_version": "0.1.0",
  "api": {"min": 1, "max": 1},
  "required": {"core": 1},
  "optional": {
    "selectors": 1, "tiktok": 1, "scroll_history": 1,
    "peer_search": 1, "word_stats": 1, "queue": 1,
    "contribution_runtime": 1
  }
}
```

`HELLO_ACK.payload` returns `daemon_version`, selected `api`, `compatible`, a
stable `code`, human-readable `reason`, and the exact negotiated capability map.
Each capability value is a positive integer schema revision; select the highest
mutually supported revision. Absence means unavailable. `core:1` covers
impression ingestion, stats, export/wipe and clean disconnected behavior.

- A missing required capability or non-overlapping API range fails closed. The
  extension sends no application RPC and renders “desktop app update required.”
- Optional controls are hidden/disabled from the negotiated map. In particular,
  contribution controls require `contribution_runtime:1`; never pretend a
  stored-only older implementation changed effective networking.
- Semantic build versions are diagnostic/update metadata, not the compatibility
  algorithm.
- The proxy/owner link from WO-079 performs an analogous required
  `owner_ipc:1` handshake before forwarding browser envelopes.
- `HELLO_ACK` must report compatibility and the disabled reason; an incompatible
  pair must not set the bridge connected/ready state.
- Gate optional UI controls on negotiated capabilities, not on unknown-message
  failures.
- Add compatibility tests for an old extension/new daemon, new extension/old
  daemon, compatible optional capability absence, and incompatible framing.
- Record release/update UX in `DESIGN_v2.md` §8.1.

## Do not

- Do not bump the envelope version for every additive payload field.
- Do not silently fall back from a security/privacy capability to an older
  behavior.
- Do not use remote configuration to solve version compatibility.

## Acceptance

- [x] Both sides reject incompatible peers with clear, actionable UI copy.
- [x] Compatible partial pairs expose only mutually-supported features.
- [x] Existing framed-message validation and reconnect behavior remain intact.
- [x] A non-HELLO first frame, duplicate HELLO, invalid capability revision,
      missing `core`, non-overlapping API and incompatible `owner_ipc` all fail
      deterministically with stable error codes.
- [x] An un-negotiated or incompatible owner session receives no owner-wide
      broadcast. Found during review: `serveOwner` joined the hub at accept, so
      a session that had not agreed a schema — including one just told it was
      incompatible — still got unsolicited `CONTRIBUTION_STATUS` frames. The
      hub is now joined from the negotiation hook.
      (`TestUnnegotiatedSessionReceivesNoOwnerBroadcast`.)
- [x] Release/update UX recorded in `DESIGN_v2.md` §8.1.

## Notes for the WO-082 audit

- **"Highest mutually supported revision" is the lower of the two integers.**
  Each side advertises a maximum and is assumed to speak everything below it.
  Pinned by `TestNegotiateSelectsLowerOfTheTwoRevisions`, including for a
  *required* capability — an exact-match rule there would make every `core`
  bump a flag day, which is the lockstep this ticket exists to avoid.
- **A pre-WO-081 extension fails closed at the daemon.** Its `HELLO` declares no
  capabilities, so negotiation returns `missing_core` and no application RPC is
  accepted. Nothing can be done for an already-shipped client from this side;
  the deterministic refusal is the contract.
- **A pre-WO-081 daemon fails closed at the extension.** Its `HELLO_ACK` carries
  no `compatible`/`capabilities`, which `applyHelloAck` treats as incompatible.
- **Optional controls are disabled with a reason, never hidden.** A vanished
  control reads as "removed"; a greyed-out one reads as "update the desktop
  app", which is the action available.
  WO-082 found that peer search obeys this rule but `applyCapabilityUi()`
  removes the contribution radio rows when `contribution_runtime` is absent.
  WO-088 owns that UI correction; it does not reopen negotiation or the session
  gate.
- **Not gated on a capability, deliberately:** `SEARCH`, `SUGGEST`,
  `LIVE_SEARCH`, `THUMBNAIL`, `ANALYSIS`, `EXPLAIN_VIDEO`, blocklist, bundle
  import/export, `PEERS`, disk budget and `SET_COHORT` all run under `core`.
  Adding gates there would let an out-of-date desktop app break the local
  recorder, which §8.1 now rules out.

## Implementation map

| Area | Files |
|---|---|
| Negotiate | `daemon/bridge/hello.go`, `hello_test.go` |
| Session gate | `daemon/main.go` (`bridgeSession`, `handleHello`), `hello_session_test.go` |
| owner_ipc | `daemon/owner_ipc.go` (WO-079; codes stable) |
| Extension HELLO | `extension/lib/protocol.js` constants, `extension/lib/native.js` |
| Cap gates + status | `extension/background/sw.js`, sidepanel/page banners |
| JS tests | `test/native.test.js`, `test/peer-search.test.js`, `test/word-stats.test.js` |
| Hub gate | `daemon/owner.go` (`runSession` + negotiation hook), `daemon/owner_unix_test.go` |
| Release/update UX | `DESIGN_v2.md` §8.1 |

## Challenge

If a single semantic version is proposed instead of capabilities, show how it
handles optional swarm, selector, and platform features without blocking the
local recorder.
