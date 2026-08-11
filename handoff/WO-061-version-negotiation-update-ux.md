# WO-061 — Version negotiation, compatibility policy, and update UX

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Done** (2026-08-10) — identify-based version observation, compat policy, update notice; no self-updater (decided against) |
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


## What was built (2026-08-10)

### Compatibility policy

The two-level split the ticket asks for maps onto two numbers that already
exist, rather than a new one:

- **Key scheme** (WO-060, `store.KeySchemeVersion`) is the incompatible level.
  It rides in the libp2p protocol id, so a mismatch means the stream is never
  opened. This is a hard refuse at the transport, not a check someone has to
  remember to write at each call site.
- **App version** (`daemon/main.go` `version`) is the compatible level. Peers on
  different app versions connect and exchange normally; the difference only
  drives what the user is told.

### Version observation — `daemon/swarm/versions.go`

Versions ride in libp2p's **identify `AgentVersion`** (`keel/0.1.0/ks1`) rather
than a Keel handshake. libp2p already exchanges it on connect, so there is no
extra round trip and — more importantly — no call site that can forget to check
it. A bespoke handshake would be a second source of truth for something the
transport already carries.

`Node.Versions(app)` returns counts over **currently connected** peers:
compatible, incompatible, newer, latest seen, and two booleans. Only connected
peers count: a version remembered from last week says nothing about whether this
node is behind now, and stale entries would make the notice sticky.

Guards that each have a test:

- **Non-Keel peers are ignored.** The public DHT connects you to dozens of
  strangers within seconds; counting them would drive the update notice from
  IPFS traffic — the mistake WO-055 fixed for the peer count.
- **Unparseable Keel-shaped agents are ignored**, not guessed at.
- **A build with an unset version never decides it is behind.** Comparing `""`
  numerically makes it zero, every real peer out-ranks it, and every such build
  would nag forever.
- **Majority is strict** (`newer*2 > total`), so one node on a development build
  cannot summon a banner for everyone it meets.

### Update UX — `extension/page/index.js`

Two messages, because they need two different responses:

- **Advised** — "A newer Keel is out. Yours still works and still connects."
- **Required** — most visible peers are on another key scheme, so nothing can be
  exchanged with them at all. Without this line the symptom is
  indistinguishable from nobody else being online (WO-058), which is the exact
  silent partition this ticket exists to prevent.

Neither blocks anything: local recording and suggestions are unaffected by what
the rest of the network runs. Shown on the full page, where network state
already lives, rather than in the side panel (WO-041 declutter).

### Update path — decided, not deferred

- **Extension:** auto-update via the Chrome Web Store once listed. Until then,
  `git pull` and Reload — INSTALL.md §Updating.
- **Daemon: no self-updater, deliberately.** A binary that rewrites itself needs
  a signed update channel and code-signing keys, and it breaks the claim the
  README makes and INSTALL.md relies on — that you can check the program you run
  against the source you built it from. A privacy tool that silently replaces
  its own binary is a worse trade than one that tells you to run two commands.
  The notice informs; the user updates.
- **Follow-up, agreed 2026-08-10 (Lars):** a later ticket covers code-signing
  keys plus a *manual* in-app update option — the user presses a button, nothing
  replaces itself unattended. That keeps the check-it-against-the-source
  property while removing the two-command step.

### Not done

Acceptance item 2's "tokenizer k in the version check" is structurally satisfied
— when WO-059 adds the tokenizer to the key scheme, a k change bumps
`KeySchemeVersion`, which is already in the protocol id and already hard-refuses.
Nothing further to build; the tokenizer itself is WO-059's.
