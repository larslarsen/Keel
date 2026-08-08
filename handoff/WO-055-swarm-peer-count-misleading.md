# WO-055 — Swarm status shows DHT noise as "connected peers" (misleading)

| | |
|---|---|
| **Addressee** | Engineer (Sr Dev lane) / reviewer |
| **Status** | **Fixed 2026-08-07** |
| **Date** | 2026-08-07 |
| **Source** | Lars, 2026-08-07 — "Connected to 11 peers... nobody's connected. Earlier it said 53. Obviously wrong." |

The daemon's status line ("Connected to N peers. X livestreams known. This node:
…", printed red in the terminal and broadcast to the UI) shows `peers`, which is
the libp2p/DHT connection count — mostly bootstrap/relay/stranger nodes that are
**not** running Keel. The honest "other installs" count (`keel_peers`) is computed
in the same status struct but not shown. This misleads the user into thinking many
people are connected when they are not.

## Verified facts (from code, not assumption)

- `swarmStatus()` (swarm_runtime.go:193) returns:
  - `"peers": swarmNode.Peers()` (swarm_runtime.go:200)
  - `"keel_peers": swarmNode.KeelPeers()` (swarm_runtime.go:202)
  - `"live_indexed": li.Size()` (swarm_runtime.go:206)
  - `"id"`: the node ID.
- `Peers()` (swarm/swarm.go:259-265) returns `len(n.host.Network().Peers())` — the
  raw libp2p connection table. Its own comment says: *"Most of these are not
  running Keel. Joining the public IPFS DHT means connecting to whoever else is on
  it… says nothing about whether anyone else uses this software."*
- `KeelPeers()` (swarm/swarm.go:272-281) counts only peers that speak the Keel
  block protocol (`SupportsProtocols(p, BlockProtocol)`). Its comment: *"This is
  the one worth showing a person… a bare peer count is mostly strangers routing DHT
  traffic."*
- `swarmStatus()` is what the UI receives: `STATS_RESULT` wraps `swarmStatus()`
  (main.go:132-135). The red terminal line is the daemon `logf` rendering the same
  `peers` field.

So the displayed "Connected to N peers" is `Network().Peers()` — guaranteed to be
non-zero once the DHT joins, and to churn (53 → 11) as the DHT pads/prunes
connections. That churn is exactly what Lars saw and called "obviously wrong": it
was never a count of people.

## Why it matters

The status display exists (main.go:129-131) so "a user who turns on sharing
deserves to see whether it connected to anything at all." Showing DHT noise as
"peers" defeats that — it shows a large, fluctuating number that implies a busy
peer network that does not exist. For a privacy-tool early user, a false "11 / 53
people connected" is both misleading and erodes trust in the metric.

## Fix

Show `keel_peers` (real installs) in the user-facing status, and either drop the
raw `peers` count or relabel it honestly (e.g. "DHT connections: N"). Concretely:

- In the status string / `STATS_RESULT.swarm` rendering, replace the prominent
  "peers" figure with `keel_peers`, clearly labelled as "Keel peers" / "other
  installs". If the raw DHT count is retained for diagnostics, name it
  "DHT connections" and do not present it as the headline connectivity number.
- The `keel_peers` value is already computed (swarm_runtime.go:202) — this is a
  display/wording change, not new logic.
- `live_indexed` (the 45 livestreams figure) is the in-memory gossip index size;
  it is plausibly legitimate but should be treated as lower-trust alongside the
  peer count until the swarm path is validated. No change mandated here, but flag
  it: if `keel_peers` is ~0, a non-zero `live_indexed` means the live index is
  being populated purely by DHT-stranger gossip, which is expected but worth a
  note in the UI.

## Acceptance
- [ ] The headline connectivity number in the status line and `STATS_RESULT.swarm`
      is `keel_peers` (protocol-speaking installs), not `Network().Peers()`.
- [ ] If the raw DHT count is shown at all, it is explicitly labelled "DHT
      connections" and is not the prominent figure.
- [ ] A node with `keel_peers == 0` but `peers > 0` displays "0 Keel peers"
      (honest), not "Connected to N peers".
- [ ] `go test ./daemon/...` passes.

---

## Engineer response — 2026-08-07

Correct in every particular, and the bug was mine — I put the raw libp2p
count in the interface the same day.

Fixed as specified. The panel headlines `keel_peers`, the count of peers that
speak the Keel block protocol, and reads "No other Keel users connected" when
that is zero. The libp2p figure is retained for diagnosis but labelled "DHT
connections (network plumbing)" and placed last, because it is guaranteed
non-zero the moment the node joins and churns as the DHT pads and prunes — which
is exactly the 53-to-11 movement that looked obviously wrong.

One correction to the ticket. It suggests a non-zero `live_indexed` alongside
zero Keel peers means the index is "being populated purely by DHT-stranger
gossip". It is not, and it cannot be: strangers on the IPFS DHT do not subscribe
to Keel's gossip topic. Those records come from `seedLiveFromLocal`, which
replays this node's own recent sightings out of `impressions` at startup. So the
figure is trustworthy — it is just entirely local, and the panel now says so:
"51 livestreams indexed, all from your own browsing."

Measured after: `keel_peers` 0, `peers` 31, and the panel reads honestly.
