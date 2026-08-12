# WO-083 — Split the extension control plane at its existing responsibility boundaries

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Architecture decided — ready after WO-085/088 (Claude Opus); WO-080 prerequisite done** |
| **Date** | 2026-08-11 |
| **Source** | Architecture review, 2026-08-11 |

## Problem

The extension remains data-thin, but its service worker and two surfaces now
combine bridge transport, RPC routing, page-proof state, panel lifecycle,
preferences, consent, queue navigation and platform policy. This has already
produced fall-through and global-state defects. Plain ES modules are required;
the present shape does not require a framework to improve.

## Required change

Split without changing user-visible behavior or data flow:

- native bridge lifecycle and request transport;
- typed/validated extension RPC dispatcher;
- tab/window page-proof and panel-context state;
- browser preference/consent handling;
- surface-specific rendering and interaction helpers.

Define explicit module APIs and ownership for every mutable state object. Keep
the service worker as composition root only.

## Selected boundaries

| Module | Owns | Must not own |
|---|---|---|
| `lib/native.js` | native port lifecycle, HELLO/ACK negotiation, request ids, pending map, reconnect alarm/backoff | feature routing, proofs, DOM rendering |
| `background/page_proofs.js` | WO-080's pure tab-keyed proof map and bounded lifecycle | browser APIs, daemon RPC |
| `background/panel_context.js` | active-tab/window resolution, panel allow/close policy, context broadcasts | proof storage, native transport |
| `background/prefs.js` | `chrome.storage` adapter for hide and recording-consent preferences only | observations, proofs, daemon-owned settings |
| `background/rpc.js` | message schema validation, command dispatch, negotiated-capability gates | connection lifecycle, feature state |
| `background/sw.js` | instantiate modules, inject browser adapters, register listeners | mutable feature state or a feature command switch |
| `sidepanel/render.js`, `page/render.js` | escaped DOM creation/rendering helpers | transport, persistence, browser-global state |
| Existing surface controllers | user interactions and calls into render/RPC helpers | duplicated bridge implementation |

Module APIs use plain objects and explicit dependencies; no module reaches back
into `sw.js` globals. Keep browser-specific behavior behind the existing
compatibility adapter. Mutable state has exactly one owner, and tests construct
that owner without a real browser or native host.

## Landing order

1. Land WO-080 so the correct proof state is extracted, not the global defect.
2. Extract pure proof and message-validation modules with characterization tests.
3. Extract native lifecycle without changing alarm names, reconnect-on-
   `onDisconnect`, watchdog injection, framing or request timeouts.
4. Extract panel context and preference adapters.
5. Extract render helpers, then reduce `sw.js` to composition and listener
   registration. Keep commits behavior-preserving and independently testable.

## Do not

- Do not add a framework, bundler, TypeScript, runtime dependency or build step.
- Do not move observation persistence into the browser.
- Do not merge this with new product features or redesign panel UI.
- Do not implement before WO-080 determines the correct page-proof ownership.

## Acceptance

- [ ] `sw.js` contains composition/wiring rather than a monolithic command
      switch and mixed state ownership.
- [ ] The existing test suite still passes, with unit tests for the extracted
      state and dispatcher boundaries.
- [ ] Dependency tests prove no cycle and no module except `prefs.js` imports or
      receives browser storage; observation/proof fixtures never reach storage.
- [ ] Native tests cover concurrent requests, disconnect rejection, alarm-based
      reconnect and incompatible HELLO without importing a surface controller.
- [ ] Manifest permissions, isolated-world extraction and native bridge framing
      are unchanged.

## Challenge

If an extraction would create circular dependencies or duplicate browser
compatibility logic, record the boundary adjustment in this ticket rather than
keeping an implicit global.
