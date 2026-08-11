# WO-060 — Protocol versioning for deterministic, node-agreeing constants

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Done** (2026-08-10) — key scheme versioned and carried in protocol ids; tokenizer/dictionary items land with WO-059 |
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
- **The yield-gossip dictionary (WO-059).** The 1-bit yield vector is indexed by a
  FIXED shared dictionary: each bit position = one dictionary word's "worth
  fetching" flag. Nodes MUST agree on the dictionary (word list AND ordering) or
  the yield vector is gibberish across peers — bit N means different things on
  different nodes. So the dictionary is a key-deriving constant, versioned like k.
  it is local/fixed at each node (never sent on the wire; only the bit vector
  travels), but the WORD LIST and its ORDERING are protocol constants, not
  per-node choices. Bumping the dictionary (adding/removing words, re-ordering) is
  a protocol version bump, applied uniformly in a release.
- **The character tokenizer — frozen vs mutable (Lars, 2026-08-10).** `ShardK`
  (window width, currently 3 chars) is FROZEN: it will never change, so it does
  not need a mutable version-bump path — it is a compile-time `const`
  (`keyscheme.go:113`), effectively immutable. `ShardM` (bucket count, currently
  256) and `TokenDictAlphabet` are NOT frozen: bucket size MUST adjust as the
  global corpus grows, so they remain versioned/mutable constants — a ShardM
  change is a protocol-version bump, applied uniformly in a release. So WO-059's
  "tokenizer k" line below is narrowed: ShardK is frozen (no bump), ShardM +
  alphabet are the versioned ones. (The yield-vector dictionary at line 29 is
  unchanged — it must stay versioned because bit positions are fixed by it.)
- **Word-telemetry normalization (WO-068), NOT a dictionary.** Global word
  telemetry uses a space-delimited word tokenizer with a SHARED NORMALIZATION
  RULE (lowercase / punctuation / letters-vs-digits / stopwords) — a constant
  nodes must agree on so HLL merges are comparable. It is deliberately NOT a
  fixed word dictionary: there is no yield-vector slot contract for words (no
  search over words), so the word set emerges from observation. Only the
  normalization rule is versioned, not a vocabulary. See WO-068.

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
  constants under version control, not runtime choices. Same for the yield-gossip
  dictionary (word list + ordering): it must be a compiled-in constant, identical
  on every node, version-bumped when changed — not derived or negotiated.

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
- [ ] Yield-gossip dictionary (word list + ordering) is a compile-time `const`,
      identical across all builds of a version; a dictionary change is a protocol
      version bump, not a per-node or negotiated choice.


## What was built (2026-08-10)

- `daemon/store/keyscheme.go` — `KeySchemeVersion`, plus every domain separator
  that feeds a key derivation (`blockDomain`, `catalogueDomain`, `PrefixDomain`),
  gathered in one file with the reasoning for why none of them is negotiated.
- `daemon/swarm/swarm.go` — `keelProtocol(name, version, scheme)` builds
  `/keel/block/2.0.0/ks1` and `/keel/catalogue/1.0.0/ks1`. The scheme rides in
  the **protocol id**, not in a handshake message, so a mismatch fails when the
  stream is opened rather than at every call site that someone might forget to
  guard. Service version and key scheme are separate numbers: one changes when a
  block's contents change, the other when its key derivation does.
- The DHT provider domain now carries the scheme too, so provider records for
  two schemes never collide.
- **`LiveSnapshotProtocol` deliberately does not carry the scheme.** The live
  index is not bucketed — entries are keyed by platform and video id — so a
  scheme bump would partition the live mesh for no reason. Asserted in the test,
  because the natural instinct on the next bump is to add it everywhere.
- `daemon/store/keyscheme_test.go` — golden vectors pinning the actual digests
  for scheme 1. This is the only thing that can catch the failure: changing a
  domain string is valid Go, and every single-node test keeps passing because a
  node always agrees with itself.
- `daemon/swarm/swarm_test.go:TestDifferentKeySchemeCannotBeServed` — a bare
  libp2p host connects to a serving node and is accepted on `ks1`, refused on
  `ks2`, over the same connection.

### Transition rule

No migration is needed. Bucket keys are computed at request time and never
persisted — there is no prefix column in SQLite — so a bump re-keys a node's
whole view on the next advertisement. The cost of a bump is *peers*, not data:
old-scheme nodes are on different protocol ids and unreachable. A release that
cannot afford that can register handlers for both schemes for one window, which
is only possible because keys are derived rather than stored. Documented in
`keyscheme.go` alongside a warning against caching computed prefixes.

### Not done here (belongs to WO-059)

Tokenizer `k`, normalisation, letters-only, and the yield-gossip dictionary do
not exist in code. `keyscheme.go` names them as the constants that must join the
scheme when WO-059 is built, including why the dictionary's *ordering* is
protocol state even though the dictionary never travels on the wire.
