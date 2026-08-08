# WO-061 — Version negotiation, compatibility policy, and update UX

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Open** |
| **Date** | 2026-08-08 |
| **Source** | Lars, 2026-08-08 (split from WO-060: "versioning should be its own ticket — a system to connect-if-compatible, warn/auto-update if behind") |

## The problem (restated from Lars)

The constants that decide storage/fetch keys must be deterministic and identical
on every node (WO-060). But that's the STATIC rule. The LIVE question is: what
does a node DO when it meets a peer on a different version? Two failure modes to
avoid:
1. **Silent partition** — a node computes different keys, fetches, finds nothing,
   concludes "the network is empty" (loops into WO-058's confusion).
2. **Hard block on every difference** — refusing to talk to any peer on a
   different version needlessly isolates nodes that could still cooperate.

The desired behaviour (Lars's words): *let people connect if the version doesn't
fork the protocol, but warn them to download / auto-update if they're behind the
network.*

## How production distributed apps do this (reference patterns)

- **libp2p** (Keel already uses it): protocol IDs are versioned
  (`/keel/block/1.0.0`). Nodes negotiate per-protocol; if two nodes speak
  different MAJORS they don't exchange that protocol. Compatible minors coexist.
- **BitTorrent**: `protocol_version` in handshake + feature bitfield. Old clients
  connect but can't request new features.
- **Matrix / Signal / Element**: client version tracked; "update available"
  banner when behind; desktop/Electron clients auto-update silently or with a
  prompt.
- **Browser extensions** (Keel's delivery channel): auto-update via the store.
- **Common "behind the network" signal**: a node counts the versions of peers it
  sees; if the MAJORITY are newer, it warns/updates. Local observation, no
  consensus, no blockchain.

The universal split: **compatible difference → connect + degrade + warn;
incompatible difference → hard-refuse.** Never silent-partition.

## Requirements for Keel

1. **Two-level version in the handshake.** Reuse HELLO/HELLO_ACK `version`
   (`bridge/protocol.go:144-155`), but define it as:
   - **Protocol major** — changes when key-deriving constants change (tokenizer
     k, hash scheme, bucket params per WO-060). Different major = INCOMPATIBLE.
   - **App/feature minor** — new RPCs, new bucket types, non-key changes.
     Different minor = COMPATIBLE (degrade, warn).
   This split is what lets a k=2 node and a k=3 node be HARD-refused (different
   major, keys don't match) while a node missing a new optional RPC still
   connects (same major, different minor).

2. **Connect-if-compatible policy.**
   - Same major → connect. If minor differs, both nodes operate at the lower
     common feature level and the behind node gets a "you're missing X" note.
   - Different major → refuse the protocol cleanly (explicit error, not empty
     results). Log "peer on incompatible protocol vX, refused" so it's debuggable,
     not silent.

3. **"Behind the network" detection (local, no consensus).** Track versions of
   peers seen (DHT + known peers). If the majority run a newer minor/major, show
   an update banner. This is a LOCAL count of observed peers — no global state,
   no blockchain.

4. **Update UX.**
   - **Banner, not block**, for "behind but compatible": "A new Keel is available
     — you're missing feature X." Links to download / triggers update.
   - **Auto-update where the platform allows:** the browser extension auto-updates
     via the store (Brave/Chrome); the daemon self-updates or is updated by the
     extension. Define the daemon update path explicitly (self-update binary vs
     extension-triggered).
   - **Hard-incompatible (different major):** banner becomes "required update to
     keep using the network" — because the node literally cannot serve the new
     keys.

5. **No silent partition, ever.** Every version mismatch must produce either a
   clear connect+degrade+warn, or an explicit refuse-with-log. Never "connects
   but finds nothing." This is the testable invariant (see Acceptance).

## Relationship to WO-060

- WO-060 = STATIC: which constants must be hard-coded, versioned, no node
  freedom (tokenizer k, hash params). It defines WHAT is versioned.
- WO-061 = DYNAMIC: the runtime system that negotiates versions at handshake,
  decides connect/refuse, detects "behind," and drives update UX. It defines HOW
  versioning behaves in the live network.
- Together they prevent the partition trap: WO-060 makes the constant deterministic;
  WO-061 makes mismatches explicit and recoverable instead of silent.

## Acceptance

- [ ] Handshake carries protocol-major + app-minor; policy: same major connects
      (degrade+warn on minor gap), different major refuses with explicit error.
- [ ] Tokenizer k / key-scheme id included in the version check (k=2 vs k=3 →
      different major → hard-refuse, never empty-result silence).
- [ ] Node tracks observed peer versions; majority-newer triggers update banner.
- [ ] Update UX defined for both delivery channels: extension auto-update via
      store; daemon self-update or extension-triggered update.
- [ ] Test: a k=2 node and k=3 node refuse at handshake with a logged reason
      (NOT silent empty fetch); a node missing a new optional RPC still connects
      at the lower feature level with a warning.
