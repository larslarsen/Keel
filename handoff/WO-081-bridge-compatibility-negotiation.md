# WO-081 — Keel Bridge needs real compatibility negotiation

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Architecture decided — ready for Sr Dev (Claude Sonnet/Opus)** |
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

- [ ] Both sides reject incompatible peers with clear, actionable UI copy.
- [ ] Compatible partial pairs expose only mutually-supported features.
- [ ] Existing framed-message validation and reconnect behavior remain intact.
- [ ] A non-HELLO first frame, duplicate HELLO, invalid capability revision,
      missing `core`, non-overlapping API and incompatible `owner_ipc` all fail
      deterministically with stable error codes.

## Challenge

If a single semantic version is proposed instead of capabilities, show how it
handles optional swarm, selector, and platform features without blocking the
local recorder.
