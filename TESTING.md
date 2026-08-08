# Testing strategy

**Why this document exists.** For other projects Lars ran an engineering process with
separate bug-testing; on Keel he did the bug-hunting himself. Solo testing has a known
failure mode: you test what you *expect* to break, and your mental model of the code
blinds you to the paths you didn't imagine. That is how bugs slip. This doc is the
standing antidote — cheap, automatic, non-human-dependent testing that does not rely on
an expensive model doing engineering reviews.

Reference point: SQLite's testing page (https://www.sqlite.org/testing.html) reports
~590× more test code than product code, targeting 100% branch/MC/DC coverage. Keel is a
smaller, younger codebase and does not need that ratio, but the *techniques* transfer.
We already ported several to the daemon (see `prefix_sqlite_techniques_test.go` and
`frame_sqlite_techniques_test.go`): fuzzing, I/O-error injection, boundary-value tests,
malformed-input tests, round-trip/equivalence tests. This doc generalizes that.

## Techniques, in priority order

1. **Fuzzing on every parser and untrusted-input boundary.** Highest leverage. Fuzz
   what we already fuzz (`BlockPrefix`, `ReadMessage`) and extend to: the swarm wire
   path (block encode/decode, `BlocksInPrefix` inputs, catalogue import), and the WO-059
   tokenizer the moment it is built. Fuzzing is the direct antidote to "you never
   imagined this input" — it found the `PrefixOf("12:")` hole. Run in CI; no human.

2. **Property-based tests for the invariants that matter.** Assert laws, not specific
   outputs:
   - Determinism: same input → same key, every path (WO-060).
   - Monotonicity: larger `limit` never drops blocks a smaller `limit` returned.
   - k-anonymity floor: no bucket smaller than K.
   - Stringless: no title/query ever appears in a block key or advertised prefix — the
     privacy invariant, asserted by checking watched titles are absent from every key
     the node emits.
   Property tests encode intent as a machine-checkable law, catching regressions nobody
   wrote a specific test for.

3. **Error-injection at real failure boundaries** (SQLite "anomaly testing"). Simulate
   failure, assert graceful degradation: disk-full on SQLite write, libp2p peer disconnect
   mid-fetch, DHT timeout, I/O error on the frame reader (done). Especially the swarm:
   spawn N in-process libp2p nodes, kill one mid-transfer, assert the others converge.

4. **Multi-node integration tests.** The ROADMAP's #1 blocker is "testers, plural"
   (NAT traversal, relay, hole punching only behave in the wild). One machine cannot
   fully replace that, but in-process multi-node tests cover the *logic* (gossip, seed,
   catalogue sync, block fetch across 3+ nodes) so it is not untested until real testers
   arrive. Run N nodes, kill one, assert convergence (SQLite "compound failure" testing).

5. **Coverage gate in CI.** `go test -cover` threshold (e.g. fail under 80% on
   security-critical packages: `store`, `bridge`, `swarm`). Turns "I think it's tested"
   into "CI says it's tested." Cheap, automatic, no human review needed. 100% MC/DC is
   overkill for Keel; an 80% floor on the crypto/privacy paths is the right call.

6. **Regression test per bug.** SQLite keeps one test per bug. When a bug is found, the
   fix is not done until a test reproduces it. See `BUG_REGRESSIONS.md` / the
   `daemon/store/regressions_test.go` and `daemon/swarm/regressions_test.go` files, which
   lock in every bug from the WO-003/WO-004 review findings and WO-055 that has a
   daemon-testable surface. No bug fix without a failing-then-passing test.

## What we do NOT do

- **Expensive engineering-review models on every diff.** Wrong tool. The cheap automatic
  suite above catches most of what reviews catch, at a fraction of the cost, and runs
  whether or not a human is paying attention. Reserve human review for design decisions,
  not bug-hunting.
- **100% MC/DC coverage mandate.** SQLite-scale coverage is not warranted at Keel's size.
  Target the security-critical paths.

## CI shape (sketch)

```
go test ./... -race -coverprofile=cover.out
go tool cover -func=cover.out   # fail if store/bridge/swarm < 80%
# fuzz seeds are committed; CI runs `go test -fuzz` for a bounded time
```

## Relationship to work orders

- WO-062 — this strategy, tracked.
- WO-059 / WO-060 / WO-061 — the features this testing must protect (deterministic
  tokenizer, version negotiation). Their acceptance criteria already include
  determinism and fuzz-style checks.
- WO-003 / WO-004 — the bug list that seeds `BUG_REGRESSIONS.md`.
