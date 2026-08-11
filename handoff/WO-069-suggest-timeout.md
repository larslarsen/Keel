# WO-069 — SUGGEST intermittently times out (8s native-bridge client cap vs synchronous graph walk)

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Date:** 2026-08-11
**Source:** Lars, 2026-08-11 — panel shows "walking the graph" then "suggestion
failed: timeout". Intermittent: reproduces on a cold/empty DB (first run, or after
WIPE), clears once the corpus is warm. Grok diagnosed the cause before running out
of tokens; this ticket records it so it is not re-derived.

## Root cause (confirmed in code)

- The native-messaging bridge is single-threaded and the client side has a hard
  **8-second timeout** on every request: `extension/lib/native.js:125`
  `function request(type, payload, timeoutMs = 8000)` — rejects with `new Error("timeout")`
  after 8000ms if no reply. This is the "suggestion failed: timeout" the user sees
  (sw.js:465 rethrows `env.payload?.message || "SUGGEST failed"`; the underlying
  rejection is the 8s timer).
- `handleSuggest` (`daemon/main.go:692`) calls `st.SuggestOn(...)` **synchronously**
  on the bridge handler thread (main.go:724). The graph walk can take >8s on a
  cold/empty DB or large corpus, so the client rejects before the daemon finishes.
  Once the corpus is warm the walk is fast → no timeout. Hence "intermittent, worse
  when cold." NOT a logic error in the walk — a handler-blocking-vs-client-cap
  mismatch.
- The swarm already budgets for slow fetches: `prewarmTimeout = 30s`
  (swarm_runtime.go:37) and `requestTimeout = 20s` (swarm.go:83) for block/shard
  fetches. SUGGEST has no equivalent — it blocks the single bridge thread with no
  cap or offload.

## What to fix (Opus decides; do not guess)

The bridge handler must not block on a multi-second graph walk. Options, in
preferred order:
1. **Offload the walk off the bridge thread.** Run `SuggestOn` in a goroutine; the
   handler returns promptly (or the walk streams). The 8s client cap is for *all*
   RPCs, so a genuinely slow walk still needs the daemon to respond within it — but
   a warm-cache / incremental walk should land under 8s. Pair with option 2.
2. **Prewarm / cache the walk** so first SUGGEST is fast (mirror `prewarmTimeout`
   intent: the swarm prewarms fetches ahead of the user; do the same for the
   suggestion walk, or memoize the last walk and serve stale-while-revalidate).
3. **Raise the client cap for SUGGEST only** as a stopgap (e.g. `bridge.request("SUGGEST", …, 30000)` in sw.js) — hides the symptom, does not fix the
   blocking; acceptable short-term but the handler should still not monopolize the
   single bridge thread.

Do NOT just bump the 8s global default — that masks slowness for every RPC.

## Verification

- Reproduce on a COLD DB: close Keel, delete/rename `~/.config/keel/keel.sqlite`,
  reopen, open the sidepanel, trigger a SUGGEST. Time `st.SuggestOn` on the REAL
  populated DB (a bench DB opened empty gives a meaningless profile — Grok hit
  this). Assert SUGGEST returns before the 8s client cap on a cold DB, or the
  handler yields the bridge thread within the cap regardless of walk duration.
- Regression test: a timed `bridge.request("SUGGEST", …)` harness (or a unit test
  on `handleSuggest` with an injected slow `SuggestOn`) asserting no 8s timeout on
  first/cold call. Lock the fix with a failing-then-passing test.

## Pushback invited

- If the walk is inherently >8s on a cold DB, option 3 alone is wrong — the bridge
  thread is the bottleneck, not the cap. Fix the blocking (1/2), not the timer.
- The 8s cap protects every RPC from a hung daemon; raising it globally weakens
  that. Keep SUGGEST-specific if you go the cap route.
