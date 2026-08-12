# WO-083 — Split the extension control plane at its existing responsibility boundaries

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Done 2026-08-12** — see the implementation record below |
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

- [x] `sw.js` contains composition/wiring rather than a monolithic command
      switch and mixed state ownership.
      1147 → 373 lines, with no `case` label and no feature state left in it.
      `test/background-structure.test.js` asserts both mechanically, so the
      switch cannot drift back in.
- [x] The existing test suite still passes, with unit tests for the extracted
      state and dispatcher boundaries.
      161/161 (was 130). `test/background-modules.test.js` adds 20, each
      constructing its subject from plain objects — no `globalThis.browser`, no
      imported service worker, no native host.
- [x] Dependency tests prove no cycle and no module except `prefs.js` imports or
      receives browser storage; observation/proof fixtures never reach storage.
      `test/background-structure.test.js`: a DFS over every `import` in
      `extension/` for cycles; the storage-owner assertion, including that
      `sw.js`'s single mention is the injection line itself; and a scan proving
      the *whole* extension writes only the two known preference keys. Verified
      falsifiable — adding a `browser.storage` reference to `rpc.js` turns the
      ownership assertion red, and removing it turns it green again.
- [x] Native tests cover concurrent requests, disconnect rejection, alarm-based
      reconnect and incompatible HELLO without importing a surface controller.
      The last three already existed; concurrent correlation did not, and is
      now `correlates concurrent requests by id, including out-of-order
      replies` plus `rejects every in-flight request when the port disconnects`.
      `test/native.test.js` imports only `lib/native.js` and `lib/protocol.js`.
- [x] Manifest permissions, isolated-world extraction and native bridge framing
      are unchanged.
      Untouched by this work order: both manifests, all of `content/`,
      `lib/native.js`, `lib/browser.js` and `lib/protocol.js`. The only
      production files modified are the three being split.

## Boundary adjustments

The challenge asks for these to be recorded rather than worked around.

1. **`background/prefs.js` is an adapter over the existing pure
   `lib/prefs.js`,** not a replacement. Validation and the legacy-mode coercion
   stay pure and shared with the content script; the new module owns only the
   async storage access, the missing-API paths and the migration write. Without
   this split, `content/hide.js` would have had to import a module that holds
   browser state.

2. **A third render module, `lib/render.js`.** The ticket named
   `sidepanel/render.js` and `page/render.js`. `escapeHtml` and `fmtDuration`
   turned out to be character-identical in both surfaces, and `liveUrl` /
   `watchUrl` were the same function under two names, so copying them into two
   new files would have re-created the duplication the split exists to remove.
   The identical helpers are shared; each surface's `render.js` holds what is
   genuinely its own.

3. **`fmtCount` was *not* shared,** despite looking like a fourth candidate.
   The two implementations differ: the panel blanks a zero view count, the full
   page renders "0 views". Both readings are defensible and unifying them would
   silently change one surface, which this ticket forbids. Each keeps its own,
   with the divergence documented in both files.

4. **The storage-ownership rule is enforced over the background control plane,**
   not the whole extension. `content/hide.js` reads the hide mode before first
   paint — routing it through the service worker would put a message round trip
   in front of a paint decision, which is the flicker WO-009 removed — and
   `sidepanel/index.js` reads the consent key for its nag banner. Both read a
   preference the user set, never an observation, and moving either would be a
   behaviour change. What *is* enforced extension-wide is the stronger property
   underneath: nothing anywhere writes anything to storage but those two keys.

5. **`openFullpageTab` lives in `panel_context.js`.** It is not panel state, but
   it is the other half of one decision — the toolbar button opens whichever of
   the two surfaces the current tab can have — and splitting them across modules
   would let them disagree about when a click does nothing.

## Defects and oddities found while splitting

- **No test loaded `sidepanel/index.js`.** 1,200 lines of a user-facing surface
  were verified only by reading. That is a bad position from which to move
  functions out of a file, since the failure mode is a `ReferenceError` at first
  use. `test/sidepanel-smoke.test.js` now evaluates the real module against the
  real markup and exercises the moved helpers, including that a hostile title is
  escaped rather than rendered.
- **`writeHideMode`'s validation is unreachable.** `isHideMode(coerceHideMode(v))`
  is true for every input, because coercion already answers a valid mode — so an
  unrecognised value is stored as the default rather than refused. Pre-existing,
  left as it is because this is a behaviour-preserving split, and pinned by a
  test that says so rather than left to look intentional.
- **Error-message fallbacks were not uniform** (`"export failed"` lowercase,
  bare `"THUMBNAIL"` for the relay group). The shared `relay` helper takes the
  fallback per call so every one of them is preserved exactly; a refactor is the
  wrong place to change a string a user might see.

## Implementation record — 2026-08-12

| Module | Lines | Owns |
|---|---:|---|
| `background/sw.js` | 373 (was 1147) | Construction, adapter injection, listener registration |
| `background/rpc.js` | 617 | Dispatch, validation, capability gates, the bounded impression buffer |
| `background/panel_context.js` | 336 | Panel gate, port bookkeeping, active-tab lookup, context broadcasts |
| `background/prefs.js` | 98 | The control plane's only storage access |
| `background/page_proofs.js` | 129 | Unchanged (WO-080) |
| `lib/render.js` | 52 | Escaping and formatting shared by both surfaces |
| `page/render.js` | 113 | The full page's pure helpers |
| `sidepanel/render.js` | 120 | The panel's pure helpers |

Each module is a factory taking explicit dependencies, and receives a *slice* of
the browser API rather than the whole adapter — which is what makes the storage
rule checkable rather than merely stated. The bridge is read through
`getBridge()` rather than captured, because the binding is replaced by
reconnection in production and by the test seam in the suite; a captured
reference would keep answering from a dead port.

No framework, bundler, TypeScript, runtime dependency or build step was added.
`sw.js` keeps its existing exports (`handle`, `flushBuffer`, `pageProofs`,
`__test_setBridge`) so every pre-existing test drives it unchanged — the
strongest available evidence that the split preserved behaviour.
