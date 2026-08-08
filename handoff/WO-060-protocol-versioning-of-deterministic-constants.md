# WO-060 — Protocol versioning for deterministic, node-agreeing constants

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Open** |
| **Date** | 2026-08-08 |
| **Source** | Lars, 2026-08-08 (P2P determinism discussion, WO-059 tokenizer) |

## The problem

A peer-to-peer network breaks if nodes disagree on any parameter that affects
what key a piece of data is stored or fetched under. The token-based distributed
search (WO-059) is the sharpest example: a node tokenizes "recommendation" into
buckets keyed by its tokens; a peer serves buckets under the SAME keys. If the
two disagree on the tokenizer — different k, different normalization, different
punctuation handling — the bucket keys never match and the fetch returns
nothing. The scheme requires **deterministic, identical behaviour on every
node, with no node-level freedom to adapt.**

This is not specific to the tokenizer. Any constant that determines a STORAGE or
FETCH KEY must be versioned and agreed:
- Tokenizer k, normalization, letters-only flag (WO-059).
- Bucket-hash parameters (prefix bits, hash domain string) — already versioned
  per-block via `blockSchemaVersion` but the parameter itself is a constant that
  must not drift between nodes.
- Hash algorithm / domain separators used to derive keys.
- Any future bucketing or sharding scheme.

A node that computes a different key than its peers is silently partitioned from
them — it fetches and finds nothing, serves and is queried for nothing. Worse
than an error: it looks like "the network is empty" (see WO-058).

## What exists already (don't duplicate)

- `daemon/main.go:20` `const version = "0.1.0"` — the daemon version, carried in
  HELLO / HELLO_ACK (`bridge/protocol.go:144-155`). `TestDropBadVersion`
  (`bridge/frame_test.go:40`) rejects mismatched frames.
- Per-payload schema versions: `blockSchemaVersion = 2` (blocks.go:30-32,
  bumped when wire shape changed), `BundleSchemaVersion = 1` (bundle.go:34),
  `catalogueSchemaVersion = 1` (catalogue.go:40), `seedSchemaVersion = 1`
  (seed.go:33). These version the wire/envelope shape, not the key-deriving
  constants.
- Gap: the tokenizer k and normalization are NOT versioned constants yet. They
  don't exist in code (WO-059 is unbuilt). When built, they must be hard-coded
  constants under version control, not runtime choices.

## Requirements

1. **Deterministic constants, hard-coded.** The tokenizer (k, normalization,
   letters-only) is a `const` compiled into the binary, identical on every node.
   No per-node configuration, no adaptive behaviour, no "pick k from what I see."
2. **Version the constant, not just the wire.** Bumping k from 2 to 3 (the
   WO-059 scale-step) is a PROTOCOL VERSION BUMP decided by the developers and
   shipped in a release — like any breaking wire change. It is NOT driven by
   network state. (See "why not network-driven" below.)
3. **No blockchain / no live consensus.** The k-step must NOT be triggered by
   global bucket-population estimates (the WO-059 count-reporting subsystem).
   Those estimates are eventually-consistent and can differ between nodes; using
   them to flip a hard deterministic parameter would let node A step to k=3 while
   node B (behind on gossip) stays at k=2 → they disagree on keys → partition.
   Global counts are for WARNINGS and COMPLETION (WO-059), never for protocol
   parameters.
4. **Handshake must surface the relevant version.** HELLO/HELLO_ACK already
   carry `version`. When the tokenizer/k is added, a node on k=2 must refuse to
   serve or fetch from a node on k=3 (or negotiate down to the lower common
   version and operate at that level). Define the compatibility rule explicitly:
   - Same major protocol version → compatible; differing key-deriving constants
     within a version is a bug, not a negotiation.
   - Differing major version → drop, as `TestDropBadVersion` already does for
     frames.
5. **Every key-deriving change is a migration, documented.** When k changes,
   old buckets are keyed by old tokens; nodes on the new version must either
   re-tokenize their local index under the new scheme or serve under both keys
   during a transition window. Specify the transition, don't let it be implicit.

## Why not network-driven versioning

Lars's point (2026-08-08): driving a protocol parameter from network state would
require every node to agree on the state simultaneously — i.e. consensus, which
means a blockchain or equivalent. Keel explicitly does not build that. Therefore
ALL protocol-determining constants are developer-release decisions, not
emergent from the network. The network can INFORM a future decision (e.g.
"buckets are uniformly huge now, step k up") but the flip itself is a code
release, applied uniformly because it's compiled in.

## Acceptance

- [ ] Tokenizer k + normalization are compile-time `const`s, identical across
      all builds of a version; no runtime/node-level variation.
- [ ] A protocol-version field (reuse HELLO `version` or add a key-scheme id)
      is checked at handshake; mismatched key-deriving constants → refuse, not
      silently mismatch.
- [ ] WO-059's k=2→k=3 step (if ever taken) is specified as a developer release
      / protocol bump, explicitly NOT driven by global counts.
- [ ] Transition rule documented: how nodes re-key local indexes or serve dual
      keys when the constant changes.
- [ ] Test: a k=2 node and a k=3 node cannot serve each other's token buckets
      (keys mismatch detected at handshake, not by empty results).
