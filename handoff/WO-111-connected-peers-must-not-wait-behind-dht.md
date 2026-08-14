# WO-111 — Connected Keel peers must not wait behind DHT discovery

| | |
|---|---|
| **Addressee** | Sr Dev (GPT-5.6 Sol, xhigh) |
| **Status** | **Implemented** 2026-08-14 — automated acceptance passes; two-machine rerun pending |
| **Date** | 2026-08-14 |
| **Depends on** | WO-095 and accepted follow-ups WO-097/099/100/101/102 |
| **Source** | WO-095 two-machine key-scheme-2 live QA failure |

## Outcome

A distributed search can use an already-connected compatible Keel node without
waiting for the public DHT to return that node's shard or catalogue provider
record. DHT provider discovery remains the expansion path beyond direct peers;
it is no longer the gate in front of a peer the node can already reach.

This repairs the failed two-machine acceptance run. It does not change the key
scheme, query semantics, contribution entitlement, broad request unit, search
wire revision, progress-bar meanings or network-health state.

## Confirmed failure

The live requester had one connected Keel peer and successfully fetched that
peer's word-telemetry pack. `shardProviderList()` nevertheless populated its
candidate list only through `FindProvidersAsync`. During a degraded public-DHT
period the lookup found nobody to ask, so the streaming pipeline ended with no
network results even though a compatible serving node was connected.

The catalogue path has a nominal `KnownPeers` fallback, but it performs the DHT
walk first under the same timeout. If discovery consumes the deadline, the
context check prevents the fallback from running. Fixing shard nomination alone
would therefore let candidate ids arrive and still strand the title resolution
needed by the local full-query matcher.

The 484-second elapsed time is evidence of a badly stalled discovery path, but
it is not `24 × 20 seconds`: a revision-3 search accepts at most 16 distinct
tokens and launches their provider walks concurrently. This order must prove
the ordering defect directly rather than encode that arithmetic as a cause.

## Required behavior

### 1. Direct candidates are bounded and protocol-compatible

For a requested broad protocol, construct a bounded direct candidate set from:

1. currently connected peers whose identify record advertises that exact
   scheme-versioned protocol; then
2. the bounded `known_peers` set, which contains peers that previously served
   verified data.

Deduplicate by peer id, exclude this node, retain usable addresses and never
exceed the existing provider cap. A connected public-DHT routing peer which
does not advertise the Keel shard/catalogue protocol is not a search candidate.
A remembered peer with stale or incompatible protocol support fails normally at
stream negotiation and cannot bypass the scheme fence.

Do not create a new durable peer table. `known_peers` remains bounded verified
service history; a live connection is transient reachability.

### 2. Shard work tries direct candidates before DHT expansion

`runTokenWork()` tries the direct candidate set through the existing shard
protocol before starting provider discovery for that token. Each attempt keeps:

- the Level-2 distributed-search gate;
- the node-wide four-response permit;
- the per-response deadline and job-wide byte meter;
- yield screening, peer randomisation/diversity and poison resolution; and
- the current target-plus-saturation stop rules.

If the direct set does not finish the useful work, query the DHT and try new
providers, deduplicated against peers already attempted. DHT discovery is not
removed or treated as failed merely because a direct peer answered.

A peer which returns a valid shard response is remembered, including a valid
empty or explicitly incomplete response. That makes later broad catalogue and
graph requests resilient without treating a connection alone as verified
service.

### 3. Catalogue resolution uses the same ordering

Before the DHT walk, `fetchCataloguePrefixLogging()` tries connected peers that
advertise the exact catalogue protocol and then remembered peers. It preserves
WO-100/101/102's complete, incomplete, invalid, unavailable and budget
outcomes. A verified complete response returns immediately; weaker outcomes
remain bounded retries and cannot prove candidate absence.

If direct candidates do not resolve the prefix, DHT provider discovery remains
available for new peers within the request context. Search callers retain
identifier-free diagnostics, and every request still names and downloads the
complete broad catalogue prefix.

### 4. Provider ordering does not claim network health

Direct success is evidence that one request worked. It must not set the
daemon's public-DHT health to `ready`, claim provider publication succeeded, or
change Keel-node counts. WO-093/109's shared rendezvous key remains the sole
discoverability-health authority.

## Acceptance

- [x] With a compatible serving peer already connected and provider discovery
      deliberately stalled, a matching shard result and its broad catalogue
      title reach the streaming matcher before the DHT lookup is released.
- [x] A complete direct catalogue response returns without invoking DHT
      discovery; incomplete, unavailable and invalid direct outcomes may still
      expand through the DHT.
- [x] A connected non-Keel/DHT routing peer is never sent a shard or catalogue
      request merely because it is connected.
- [x] One peer present in connected, remembered and DHT sets is attempted at
      most once per token/prefix traversal.
- [x] Direct and discovered shard attempts together obey the existing
      `maxProvidersPerToken` bound, four-response semaphore, byte budget,
      cancellation and Level-2 gate.
- [x] Requests remain whole numeric shards and whole catalogue prefixes; no
      token, candidate id or selected video is added to either wire request.
- [x] Search-path logs expose no query, token, shard, catalogue prefix, video
      id or peer id.
- [x] Existing DHT-only discovery, remembered-peer fallback, pagination,
      poison, saturation, race, Go and extension tests remain green.
- [ ] Repeat WO-095's two-machine key-scheme-2 QA after both machines run the
      corrected binary.

## Do not

- Do not bypass exact protocol/version negotiation or the Level-2 search gate.
- Do not ask for one token, title, candidate id or selected video; the privacy
  unit remains the complete broad shard/prefix.
- Do not remove DHT discovery, turn connected peers into provider records, or
  infer public discoverability from a successful direct request.
- Do not serialize token workers, remove the four-response bound, add a
  whole-query clock, or weaken byte accounting.
- Do not persist query/job/provider-attempt state or add a browser permission,
  storage owner, dependency or wire revision.
- Do not fold the owner-socket `broken pipe` lifecycle defect into this order;
  session cancellation/reconnect is a separate implementation boundary.

## Stop conditions for the implementer

Return to architecture review if the correction appears to require a narrow
request, a key-scheme or wire revision, Level-1 distributed search, durable
query/provider state, or a new network-health meaning.

---

## Implementation record (2026-08-14)

No stop condition was hit.

| Requirement | Implementation |
|---|---|
| Exact connected candidates | `Node.connectedProviders` reads currently connected peer ids and admits only peers whose identify record advertises the exact scheme-versioned shard or catalogue protocol. |
| Verified direct history | `Node.rememberedProviders` rebuilds bounded address info from the existing `known_peers` table and deduplicates it against earlier sources. No new persistence was added. |
| Stalled-DHT test seam | `Node.providerLookup` defaults to `IpfsDHT.FindProvidersAsync`; tests replace only that directory call while retaining real libp2p streams, signatures, pagination and stores. |
| Shard ordering | `runTokenWork` tries connected, then remembered, then DHT candidates under one 20-peer set. Every real attempt still holds the node-wide response permit and per-response timeout; valid replies are remembered before broad catalogue resolution. |
| Catalogue ordering | `fetchCataloguePrefixLogging` applies the same connected/remembered/DHT order, one dedupe set and the historical bounded attempt ceiling while preserving complete/incomplete/invalid/unavailable/budget outcomes and context cancellation. |
| Health semantics | None of the direct paths writes `networkHealth`; presence publication remains unchanged. |

New regressions in `daemon/swarm/connected_provider_test.go` prove:

- a complete result crosses a connected peer's real shard and catalogue streams
  while every DHT lookup is held open;
- a direct catalogue completion invokes no provider lookup;
- an invalid direct catalogue response expands to a new DHT provider;
- an unrelated connected libp2p node is not nominated;
- connected/remembered/DHT duplicates are requested once per token and prefix;
  and
- one direct shard candidate reduces the DHT allowance from 20 to 19.

Verification: `go test ./...`, `go test -race ./...`, `go vet ./...`, the
focused regressions repeated ten times, and all 24 extension test files pass.
The two-machine rerun remains the only unchecked acceptance line.
