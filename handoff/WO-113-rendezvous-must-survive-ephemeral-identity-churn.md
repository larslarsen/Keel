# WO-113 — Rendezvous must survive ephemeral identity churn

| | |
|---|---|
| **Addressee** | Sr Dev (GPT-5.6 Sol, High) |
| **Status** | **Implemented** 2026-08-14 — automated acceptance passes; two-machine rerun pending |
| **Date** | 2026-08-14 |
| **Source** | Two-machine QA after the WO-112 release: both nodes reported discoverable but neither found a Keel peer |
| **Depends on** | WO-052, WO-093, WO-094 |

## Outcome

Restarting serving-level daemons does not let their expired ephemeral peer IDs
hide the current nodes behind the rendezvous lookup's connection budget. A
quiet or degraded public-DHT walk can also reconnect a verified remembered
peer without changing what `discoverable` means.

## Confirmed defect

Serving levels below Level 4 deliberately create a fresh libp2p identity for
every daemon process. Public-DHT provider records for the shared rendezvous key
outlive that process. `FindPeers` previously passed its eight-connection budget
directly to `FindProvidersAsync`, so eight dead identities from release and QA
restarts could fill the result set before either current identity appeared.

The live Linux log proves the sequence: one WO-112 run connected and backfilled
from the Windows node, then two quick restarts published the shared key under
fresh peer IDs but found no Keel peer. SQLite remembers only pre-restart Windows
peer IDs. This is why both nodes can truthfully report that they are
discoverable while showing zero connected Keel peers.

WO-112 did not change rendezvous publication, lookup, connection management,
identity generation, the block protocol or the peer counter. Installing it
required daemon restarts, which exposed the existing identity-churn failure.

## Required

1. Keep the successful-connection budget at eight.
2. Scan a separately bounded, wider set of rendezvous provider candidates so
   dead ephemeral identities do not consume the connection budget.
3. Stop the lookup as soon as the successful-connection budget is reached.
4. Deduplicate provider identities within a lookup and skip self, addressless
   and already-connected candidates.
5. When a DHT round connects nobody, allow verified remembered peers to be
   dialed directly. A direct dial must not be recorded as proof that this node
   is publicly discoverable.
6. Preserve the outbound contribution gate, ephemeral identities, exact
   protocol negotiation and all Level-1 behavior.

## Acceptance

- [x] Eight dead provider records followed by a live ninth provider connect to
      the live peer with an eight-connection budget.
- [x] A quiet DHT walk can dial a verified remembered peer.
- [x] An already-connected remembered peer is not redialed or counted as new.
- [x] Focused rendezvous regressions pass repeatedly.
- [ ] Full Go, race, vet and extension suites pass.
- [ ] Two freshly restarted machines return to `keel_peers: 1`.
- [ ] Their Live indices converge after the connection.

## Do not

- Do not make the peer identity stable below Level 4.
- Do not equate a remembered direct connection with public discoverability.
- Do not remove or widen the eight-successful-connection budget.
- Do not add LAN-only discovery, a central rendezvous server or a new runtime
  dependency.
- Do not increment the application version.

## Implementation record (2026-08-14)

- Rendezvous scans up to 32 provider candidates while retaining the caller's
  eight-successful-connection ceiling.
- The lookup owns a cancellable child context and ends immediately once that
  ceiling is reached.
- The existing verified-peer store supplies a direct fallback only when the
  DHT round connected nobody; network health remains owned exclusively by the
  shared-key publication loop.
- `TestFindPeersScansPastStaleEphemeralProviders` reproduces the release-restart
  failure with eight dead identities and one live ninth identity.
