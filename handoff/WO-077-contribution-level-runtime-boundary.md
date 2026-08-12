# WO-077 — Contribution-level changes must reconfigure the running swarm

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Done for runtime transitions; Level-2 source policy superseded by WO-084 and Level-1 Live policy superseded by WO-089** |
| **Date** | 2026-08-11 |
| **Source** | Architecture review, 2026-08-11 |

## Problem

> **Correction, 2026-08-11:** This order correctly implemented runtime-safe
> replacement and the Level-1 boundary, but its Level-2 `PublishOwn=false`
> decision was wrong. Level 2 serves broad buckets containing locally produced
> and cached graph blocks. WO-084 owns that policy/store correction; Level 3 is
> the STAR boundary. Its instruction to keep user-triggered peer search at
> Level 1 is also superseded by WO-085: fetch/pre-walk remain Level 1, while
> distributed `PEER_SEARCH` becomes reciprocal at Level 2+.

> **Correction, 2026-08-12:** WO-089 moves the complete Live capability from
> Level 1 to Level 2+ and makes Level-1 word telemetry fetch-only. The table
> below remains the implementation history of WO-077, not the current outbound
> contract. Level-1 graph/seed fetch and pre-walk remain unchanged.

`SET_CONTRIBUTION` persists a new level in SQLite, but the swarm reads that
level only once when the native host starts. A displayed Level 1 can therefore
continue serving as Level 2 until the host exits. The inverse is also wrong: the
current startup mapping sets `Fetch=false` at Level 1, withholding peer search,
graph pre-walk and the full product from non-contributors. This is a consent and
product-boundary failure, not merely stale UI.

## Selected architecture

The normative matrix is `ARCHITECTURE_CURRENT.md` §§3–4:

| Capability | Level 1 | Level 2 |
|---|---:|---:|
| Seed receipt, graph/catalogue/search fetch and pre-walk | On | On |
| Live receive/relay/originate + whole snapshot serve | On | On |
| Join/relay/originate three-gram yield/token-sketch topics | Off | On, for served mirrored blocks |
| Whole word-level HLL/CMS telemetry fetch + serve | On, including aggregate local corpus | On |
| Serve cached graph/catalogue/search blocks | Off | On |
| Provider announcements | Off | On |
| Serve/publish own observations | Off | Off; Level 3+ only |

Do not collapse this into `Fetch`, `Serve`, and `ServeOwnObservations` if that
causes topic subscription and topic origination to share a gate. The runtime
policy needs independent capabilities at least equivalent to `fetch`,
`serve_mirrors`, `announce_providers`, `join_search_telemetry`,
`exchange_word_telemetry`, `live`, and `publish_own`.

Because handlers, provider loops and pubsub publishers are bound during
`swarm.Start`, the chosen reconfiguration mechanism is a supervisor-controlled
node replacement. Callers obtain the current node for each operation and may
not retain a pointer across a transition.

## Required change

Make the effective swarm policy and the persisted contribution level one
observable state machine:

```text
stored_level     persisted user choice
effective_level  policy enforced by the current node
transition       idle | stopping | starting | failed
detail           optional status/failure text
```

Persist an internal `startup_level` as the highest policy a fresh owner may
construct. Migrate the existing single contribution value into both fields. In
idle state all three levels agree. On process start, construct `startup_level`,
not `stored_level` when the latter is higher, and expose the mismatch as a
failed/pending transition. Never auto-resume an escalation after a crash.

- On 2→1, first close an atomic permission gate used by every block handler,
  provider loop and three-gram topic. In one transaction set stored/startup
  to Level 1, then detach and stop the Level-2 node. Construct a Level-1 node
  with fetching/pre-walk, word HLL/CMS and live gossip enabled but block service,
  provider announcements and three-gram topics disabled. If persistence or
  construction fails, remain at the safer gate/network-stopped state and report
  failure; never resurrect Level 2 silently.
- On 1→2, persist `stored_level=2` while retaining `startup_level=1`, replace the
  node, and commit `startup_level=2` only after Level 2 is effective. If startup
  or that activation commit fails, stop Level 2, reconstruct Level 1, and restore
  the stored choice when possible. The lower startup value remains the durable
  crash-safety backstop even if rollback persistence partly fails.
- Return both `stored_level` and `effective_level` through the bridge. They may
  differ only during a reported transition or reported failure.
- Network-dependent RPCs return a typed temporary-unavailable response during
  replacement; state/status RPCs remain available. Bootstrap stays asynchronous:
  zero reachable public peers is degraded reachability, not policy failure.
- Gate `startYield` and `startSketch` behind the Level-2 search-telemetry
  capability. Today both topics start unconditionally, and
  `publishDueSketches` also publishes regardless of `Serve`. Level 1 must join
  neither topic. Do not apply that gate to the separate `WordTelemetryProtocol`
  from WO-068.
- Register `WordTelemetryProtocol` independently of block `Serve`. At Level 1
  its whole fixed-shape HLL/CMS pack includes the aggregate local corpus; do not
  pass the mirror-only selector merely because block service is off. The pack
  remains one schema with no plaintext words, ids, edges or query.

## Do not

- Do not make the UI instruct the user to restart as the privacy mechanism.
- Do not leave a stale Level-2 node serving while the stored setting says Level
  1.
- Do not disable fetch, seed receipt, graph pre-walk, whole word
  HLL/CMS exchange or the Live network at Level 1. Fetch must treat missing
  yield/token estimates as unknown and continue without the optimization.
- The original peer-search clause here is superseded by WO-085. Local search
  remains Level 1; user-triggered distributed `PEER_SEARCH` is Level 2+.
- Do not alter the meaning of Level 1; WO-078 owns that decision.
- Do not build the deferred seed pack in this order. Preserve an ungated
  Level-1 seed-consumer seam/configuration; actual seed production and delivery
  remain separately scoped.

## Acceptance

- [ ] A Level 2 → Level 1 change prevents all block service, provider
      announcements, three-gram topic participation and own-edge publication before
      success is returned.
- [ ] After that downgrade, graph/catalogue/search fetch,
      pre-walk, whole word HLL/CMS exchange and live receive/relay/origination
      still work without three-gram topic state.
- [ ] The Level-1 policy permits the deferred seed-consumer path; this ticket
      does not fabricate or publish a seed pack.
- [ ] Network inspection proves a Level-1 node neither subscribes to nor relays
      `YieldTopic`/`SketchTopic`, while Level 2 does both.
- ~~A Level 1 → Level 2 change activates cached-block service but never local
  graph-block service.~~ **Superseded by WO-084:** Level 2 activates complete
  local-plus-cached broad buckets; Level 3 remains the STAR boundary.
- [ ] The UI reports the effective, not merely stored, level.
- [ ] Tests inject stop, start and persistence failures in both directions and
      prove no stale Level-2 publisher survives a failed downgrade.
- [ ] Crash-point tests after every durable transition step prove restart never
      selects a level higher than the last successfully activated policy.
- [ ] Tests prove a Level-1 node never answers graph/catalogue/search-shard
      block streams even after it has fetched and cached those rows, while the
      distinct word-telemetry stream still answers with its fixed-shape pack.

## Challenge

Keep the replacement inside the daemon owner introduced by WO-079. Restarting
the browser-facing native host is neither necessary nor sufficient once that
host is a proxy.

---

## What was built (2026-08-11)

### Capabilities (`daemon/swarm/policy.go`, new)

`Policy` with the seven independent capabilities the ticket named — `Live`,
`Fetch`, `ServeMirrors`, `AnnounceProviders`, `JoinSearchTelemetry`,
`ExchangeWordTelemetry`, `PublishOwn` — plus `PolicyForLevel`, which is
ARCHITECTURE_CURRENT §3's table in code. `swarm.Config`'s
`Serve`/`Fetch`/`ServeOwnObservations` booleans are **replaced**, not
supplemented, so there is no second source of truth to drift. The zero value is
the strictest policy, so a `Config` built without one cannot accidentally serve.

The two mis-gatings the ticket called out are fixed at their cause:

- `startYield`/`startSketch` ran unconditionally and `publishDueSketches`
  published regardless of `Serve`. Both are now behind `JoinSearchTelemetry`,
  and Level 1 does not **join** the topics at all rather than joining and
  staying quiet.
- `WordTelemetryProtocol` was registered only when block service was on. It now
  rides `ExchangeWordTelemetry`, and — per the ticket's explicit instruction —
  the pack is built with the local corpus **included** rather than passing the
  mirror-only selector borrowed from block service. That flag had silently
  excluded every node from a global statistic it was itself reading; the
  constant is named `includeLocalCorpus` at both call sites so it reads as a
  decision rather than a copied argument.

`Fetch` is on at Level 1, which is the product-boundary half of the ticket.

**One gap found while testing:** `Node.FetchFrom` (direct dial, skipping
discovery) bypassed the `Fetch` capability entirely. No production caller today,
so it was not a live bug, but it is an exported method that would let a future
caller escape the policy — a direct dial is still a request and still discloses
the bucket. Now gated.

### Runtime gate (`daemon/swarm/swarm.go`)

`Node.outbound` (`atomic.Bool`, open at construction, no reopen) with
`mayServeBlocks`/`mayAnnounce`/`mayGossipSearchTelemetry`/
`mayExchangeWordTelemetry`. Every block/catalogue/shard handler re-checks per
request; `Announce` and both gossip publishers re-check per tick. `CloseOutbound`
shuts it in one store.

This is what makes a downgrade immediate rather than eventual. Stopping a libp2p
host is not instant and requests keep arriving while it winds down, so the
promise is kept by the gate, not the teardown — `TestShuttingTheGateStopsServiceOnALiveNode`
proves service stops on a node that is still fully up, same host, same
registered handlers, same live connection.

`Node.Serving()` is exported (not a test seam) because "what level is stored"
and "is this thing still serving" genuinely differ during a transition, and the
second is what a status display should show.

### Two-value durable state (`daemon/store/contribution.go`)

`startup_level` alongside `contribution_level`, with
`SetContributionAndStartupLevel` (one transaction, for downgrades),
`SetStartupLevel` (the upgrade activation commit) and `StartupLevel()`, which
**clamps to the stored level on read** — a startup level above the stored choice
can only be corruption or a partly-applied downgrade, and the safe reading of
leftover state is the user's choice. Migration is implicit and needs no schema
change: absent means "follow the single stored value", which is correct because
a database that has only ever had one value has by definition never been
mid-transition.

### Supervisor (`daemon/contribution_runtime.go`, new)

Owns the node pointer and every transition. Each node gets **its own cancellable
context** — `Node.Close` stops the host but not the publish/consume loops started
under `Start`'s context, so without this a replaced node keeps gossiping from the
grave.

Ordering is asymmetric because the unsafe outcome differs by direction:

- **Down:** shut the gate → persist both levels in one transaction → tear down →
  construct Level 1. If persistence or construction fails, stay stopped and
  report; never resurrect Level 2.
- **Up:** persist `stored` only → replace → commit `startup` *after* the higher
  policy is effective. On failure, roll back to the prior policy; the retained
  lower `startup` is the durable backstop even if rollback persistence fails.
- **Start:** constructs `startup_level`, never the higher `stored_level`, and
  reports the mismatch as a failed transition instead of auto-escalating.

`launchFn` is injectable so the failure paths are actually exercised — a
cancelled context does **not** fail libp2p host construction, which the first
draft of these tests assumed and which would have left the injection tests
passing vacuously.

### Bridge and UI

`CONTRIBUTION_RESULT` carries `stored_level`, `effective_level`, `startup_level`,
`transition`, `detail` and `levels_disagree`. `level` is retained for the
existing reader but now carries the **effective** level. `ErrorPayload` gained
`Detail` so a failed transition can return what is actually running alongside
the reason. `LIVE_SEARCH`/`PEER_SEARCH`/`WORD_STATS` return typed
`network_busy` during a replacement; state/status RPCs stay available.
`extension/page/index.js` checks the effective level, shows "Applying your
choice to the network…" mid-transition, and states plainly which level is being
enforced when the two disagree.

### Tests

14 new (`daemon/contribution_runtime_test.go`, `daemon/swarm/policy_test.go`),
covering every acceptance criterion: L1 refuses graph/catalogue/shard streams
*from a deliberately populated cache* while answering word telemetry from that
same cache; L1 joins neither three-gram topic while L2 joins both; L1 still
fetches and its walk returns results; the gate stops a live node; both
transition directions on real nodes; failure injection at construction (both
directions, plus the doubly-failed case) and at persistence (a genuinely closed
database, not a mock); crash points after each durable step.

Each safety boundary was verified **red-then-green** by reverting the fix and
confirming the test fails: L1-fetch (3 tests catch it), the outbound gate, and
the topic-join gating.

Full suite green, including under `-race`. Swarm coverage 81.3% → 82.2%;
`scripts/check-coverage.sh` ratchet holds. `npm test` 99/99 unaffected.

### Not done here, deliberately

Seed production/delivery (out of scope per "Do not"). The Level-1 seed-consumer
seam is already ungated — verified: nothing in the import path consults the
level, and only the operator-invoked `keel seed build` CLI reads it.
