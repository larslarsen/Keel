# WO-109 — Network status must not invent provenance or discoverability

| | |
|---|---|
| **Addressee** | Sr Dev (GPT-5.6 Luna High) |
| **Status** | **Accepted 2026-08-14** |
| **Date** | 2026-08-14 |
| **Source** | Reviewer findings on WO-093 |
| **Depends on** | WO-093 implementation present in the worktree |

## Outcome

Preserve WO-093's state machine, wire capability and UI ownership. Correct two
remaining sentences that infer facts from the wrong signal.

This is a bounded truthfulness correction, not a redesign of network health.

## Finding 1 — a current peer count cannot prove livestream provenance

`renderSwarm()` still uses `network.keel_peers === 0` to append:

```text
all from your own browsing
```

That inference is false. The Live index is ephemeral but retained: it may hold
records received from a peer that has since disconnected, and a missing or
malformed negotiated `network` object is currently coerced to zero as well.
WO-093 exists precisely because a current zero cannot explain history or
provenance.

Render the Live count neutrally: `N livestream(s) indexed.` Do not infer local,
remote, shared or observed provenance from current connected-node health in any
state. The Network headline already explains the current network state and must
remain the sole owner of that meaning.

Add DOM assertions covering at least:

- `ready` with zero current Keel nodes and a non-empty Live index;
- `off` or `fault` with a non-empty Live index; and
- negotiated `network_status:1` with the `network` object missing or malformed.

Every case may show the neutral indexed count. None may say or imply that all
records came from this machine, and the malformed case must not resurrect a raw
zero as network health.

## Finding 2 — bulk content publication cannot declare the node discoverable

After WO-093, only successful publication of the shared rendezvous key answers
“can another Keel node find this node?” `Announce()` correctly has no write path
to that health record, but its first-batch log still says:

```text
this node is now findable
```

That recreates the conflation in operational logs. A graph/content provider
record only makes those named buckets findable. Change the message to describe
the content outcome—for example, that the first graph batch's provider records
were published—without claiming node presence, Keel discoverability, readiness
or network health.

Retain the existing presence success log as the only positive log that says
other Keel nodes can find this node. Add focused coverage for the wording if it
can be exercised without a public DHT; otherwise cite the existing
`TestContentAnnounceCannotMoveNetworkHealth` separation test and make the exact
log change reviewable in the implementation record.

## Do not

- Do not change `NetworkStatus`, its transitions, backoff, presence publisher,
  rendezvous lookup or `network_status:1` negotiation.
- Do not remove the Live count or the raw DHT plumbing diagnostic.
- Do not infer Live provenance from peer count, health state or contribution
  level.
- Do not perform WO-093's public-DHT fault/recovery gate in this code order.
- Do not modify unrelated files in the dirty worktree.

## Acceptance

- Every network state remains rendered as WO-093 specifies.
- Live-index wording is neutral under valid, absent and malformed health data.
- Only the shared presence success path claims node discoverability.
- `npm test`, `go test ./...`, `go test -race ./...`, `go vet ./...` and
  `git diff --check` pass.

## Challenge

If the Live index has a separate, authoritative per-record provenance signal,
show it before proposing richer wording. The current connected-node count is
not that signal.

## Implementation and review record — 2026-08-14

The Config page now renders every non-empty Live index as the neutral
`N livestream(s) indexed.` It does not derive provenance from the current
Keel-node count, health state, or a missing/malformed health object. DOM
regressions cover `ready` with zero nodes, `off`, `fault`, and absent/malformed
health data.

The bulk content publisher now describes its first successful batch as graph
provider records and describes a zero-record round as data that cannot be
fetched. It no longer claims that the node is discoverable. The independent
presence-success path remains the only log statement that says other Keel
nodes can find this one.

Reviewer verification passed `npm test` (24 suites), `go test ./...`,
`go test -race ./...`, `go vet ./...`, and `git diff --check`. No state-machine,
protocol, consent, or retry behavior changed in this order.
