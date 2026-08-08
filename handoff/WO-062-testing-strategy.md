# WO-062 — Testing strategy: fuzz + property + error-injection + regression, not review models

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Open** |
| **Date** | 2026-08-08 |
| **Source** | Lars, 2026-08-08 — "I did the bug testing myself instead of an engineer agent. We let more bugs slip. Testing is the way to clean up, not expensive review models." |

## Problem

On Keel, Lars did bug-hunting solo rather than delegating it. Solo testing has a known
failure mode: you test what you expect to break, and your mental model blinds you to the
paths you didn't imagine. That is how bugs slip. Expensive engineering-review models on
every diff are the wrong fix — they are costly and still human-bounded. The cheap,
automatic, non-human-dependent testing does the bug-catching work.

SQLite's testing page reports ~590× more test code than product, 100% branch/MC/DC. Keel
does not need that ratio, but the *techniques* transfer. We already ported several to the
daemon (`prefix_sqlite_techniques_test.go`, `frame_sqlite_techniques_test.go`): fuzzing,
I/O-error injection, boundary and malformed-input tests, round-trip equivalence. This WO
generalizes that into a standing strategy (see `TESTING.md`) and a regression suite seeded
from the WO-003/WO-004 review findings and WO-055.

## Requirements (from TESTING.md)

1. **Fuzz every parser / untrusted boundary.** Extend beyond `BlockPrefix`/`ReadMessage`
   to the swarm wire path (block encode/decode, `BlocksInPrefix`, catalogue import) and
   the WO-059 tokenizer when built. CI runs `go test -fuzz` bounded.
2. **Property-based tests** for: key determinism (WO-060), limit monotonicity,
   k-anonymity floor, stringless invariant (no title/query in any key/prefix).
3. **Error-injection** at failure boundaries: disk-full SQLite write, libp2p disconnect
   mid-fetch, DHT timeout, frame I/O error (done).
4. **Multi-node integration**: spawn 3+ in-process libp2p nodes, exercise gossip/seed/
   catalogue/block-fetch, kill one, assert convergence.
5. **CI coverage gate**: fail under 80% on `store`/`bridge`/`swarm`.
6. **Regression test per bug** from WO-003/WO-004/WO-055 (daemon-testable surfaces) —
   see `daemon/store/regressions_test.go`, `daemon/swarm/regressions_test.go`.

## Already-done (this session)

- Fuzz targets `FuzzBlockPrefix`, `FuzzReadMessage` (Go native `testing.F`).
- I/O-error injection on frame reader; boundary + malformed-input tests.
- Found + fixed real bug: `PrefixOf("12:")` accepted empty payload (hardened
  `prefix.go`).
- `TESTING.md` written.

## Acceptance

- [ ] Swarm wire path has fuzz targets (block encode/decode, BlocksInPrefix, catalogue).
- [ ] Property tests cover determinism, monotonicity, k-anonymity floor, stringless.
- [ ] Multi-node test spawns ≥3 nodes, kills one mid-transfer, asserts convergence.
- [ ] CI runs `go test -race -cover` with an 80% floor on store/bridge/swarm.
- [ ] `regressions_test.go` locks in each daemon-testable bug from WO-003/WO-004/WO-055.
- [ ] No bug fix merged without a failing-then-passing regression test.
