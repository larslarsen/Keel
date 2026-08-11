# WO-065: Refresh suggestions button

**Addressee:** Sr Dev (Opus)
**Status:** Done — re-suggest button in the entropy row (2026-08-10)
**Depends on:** WO-064 (same side panel); the suggestion-render path in
`extension/sidepanel/index.js`.

## What to build

A "Refresh" control in the side panel's suggestions area that re-fetches a fresh
set of suggestions when the user does not like the current ones.

## Rationale

The panel shows the daemon's graph walk (`SUGGEST` RPC), not YouTube's rail
(WO-046). The walk has inherent randomness, so re-running it produces a different
draw. Users currently have no way to ask for "a different set" without re-watching
something. A refresh button closes that gap with near-zero cost — it is the same
RPC the panel already calls on load.

## Implementation

- In `extension/sidepanel/index.js`, add a "Refresh" button near the suggestions
  list (reuse the existing `rpc` helper and the `SUGGEST` call shape already used
  to populate suggestions on load).
- On click: call `rpc("SUGGEST", { entropy: <current entropy>, limit: <current
  limit> })` and re-render the suggestion list with the result. Use the same
  render path the initial load uses.
- **Plain re-draw, same entropy.** Do NOT bump entropy per press. The graph walk is
  already randomized, so a re-draw yields a different set; an entropy bump would
  change exploration behavior the user did not ask for. (If you think entropy
  should drift upward per refresh to avoid repeats, raise it as pushback — we
  deliberately did not.)
- Disable/ignore the button while a request is in flight (avoid double-fires).

## Acceptance

- [ ] A Refresh control exists in the suggestions area.
- [ ] Clicking it re-renders the list with a fresh `SUGGEST` result (different
      draw, same entropy).
- [ ] Rapid double-clicks do not fire concurrent requests (in-flight guard).
- [ ] No new daemon RPC needed — reuses `SUGGEST`.

## Pushback invited

- Entropy behavior: we kept it constant. If repeats are a problem in practice,
  propose a concrete drift rule rather than "bump it."
- Placement: button near the suggestions header. Move it if a better spot exists.
