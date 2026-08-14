# WO-112 — The Live index must converge across late peer connections

| | |
|---|---|
| **Addressee** | Sr Dev (GPT-5.6 Sol, xhigh) |
| **Status** | **Implemented** 2026-08-14 — automated acceptance passes; two-machine rerun pending |
| **Date** | 2026-08-14 |
| **Source** | Two-machine QA after WO-111: search passed, but the machines held different Live counts |
| **Depends on** | WO-052, WO-089, WO-094 |

## Outcome

Two connected Level-2 Keel nodes converge on the same bounded whole Live index,
including when one node was already running before the other connected. A
production sighting carrying `StartedAt` crosses gossip instead of being rejected
by Keel's own validator.

## Confirmed defects

### Production sightings are invalidated during publication

`announceLive` sets `SeenAt` and `StartedAt` to the exact observation time.
`LiveIndex.Publish` then rounds only `SeenAt` down to the hour. Except at the
hour boundary, the wire record has `StartedAt > SeenAt`, which
`ValidLiveRecord` correctly refuses. The live daemon log contains repeated
`live publish …: validation failed` lines, and a focused reproduction fails on
that exact transformation.

### Snapshot backfill ends before a late peer can arrive

`backfillLive` checks for connected peers twelve times at five-second intervals,
then exits forever. It also stops after any peer record arrives, even though one
gossip record is not the whole index. There is no connection-triggered or
periodic replacement. In the failed run the Linux node seeded 18 local records
and pulled 9 from the already-running Windows node; the reverse transfer had no
path, leaving different counts while search and word telemetry worked.

## Required

1. Normalize the wire copy of both non-zero Live timestamps to the same coarse
   granularity before validation and publication. Preserve the exact local
   observation time (`SeenAt`) in the local index; `StartedAt` remains the
   shared earliest lower bound and may be lowered by a peer report.
2. Revalidate the normalized wire record before handing it to gossipsub.
3. Keep Live snapshot reconciliation active for the node lifetime.
4. Ask only currently connected peers advertising the exact
   `LiveSnapshotProtocol`.
5. Fetch one complete snapshot per peer connection. A valid empty or fully
   overlapping snapshot counts as a successful reconciliation.
6. Retry transport or malformed-response failures with bounded backoff.
7. Forget reconciliation state when a peer disconnects so reconnecting after
   missed gossip causes a fresh snapshot.
8. Preserve Level-2 gating, the whole-index request shape, serving limits,
   expiry, tombstones, authorless records and local-only search.

## Acceptance

- [x] A real two-node publication with `SeenAt == StartedAt` reaches the other
      node through Live gossip.
- [x] Two connected nodes starting with different local Live records converge
      in both directions.
- [x] A peer connected after an initial pass with no peers is still backfilled.
- [x] A successfully reconciled connection is not fetched repeatedly.
- [x] Disconnect and reconnect permit a new snapshot.
- [x] Invalid or failed peers cannot create a tight retry loop.
- [x] Existing Live, policy, Go, race, vet and extension tests remain green.
- [ ] Two-machine QA shows the same Live count after reconciliation.

## Do not

- Do not persist the shared Live index.
- Do not enable any Live topic, snapshot, relay or publication at Level 1.
- Do not add query-shaped or bucket-shaped Live requests.
- Do not add authorship, publisher counts or popularity ranking.
- Do not ask unrelated public-DHT peers for Live snapshots.
- Do not increment the application version.

## Implementation record (2026-08-14)

- `LiveIndex.Publish` keeps the exact local `SeenAt`, rounds both non-zero wire
  timestamps together, and validates the normalized copy before gossipsub.
- The global `receivedFromPeer` latch and fixed twelve-attempt startup loop are
  gone. A lifetime reconciliation loop tracks each connected exact-protocol
  peer, accepts complete empty/overlapping snapshots, forgets disconnected
  peers, and exponentially backs failures off to five minutes.
- `TestProductionLiveStartedAtPropagates` exercises the real production record
  shape over two nodes with snapshot service removed from the publisher, so
  only gossip can satisfy it.
- The existing non-empty-node regression now requires both sides to converge.
  Scheduler tests cover late arrival, no repeated successful fetch, reconnect
  and failure backoff; the exact-protocol filter also covers Live snapshots.

Verification: focused regressions repeated five times, `go test ./...`,
`go test -race ./...`, `go vet ./...`, and all 24 extension test files pass.
