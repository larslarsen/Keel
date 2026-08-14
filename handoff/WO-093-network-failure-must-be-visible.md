# WO-093 — A node that cannot join the network must say so, not count forever

| | |
|---|---|
| **Addressee** | Sr Dev (Grok 4.6 Extra High) |
| **Status** | **Code accepted; §6 public-DHT gate partially complete** |
| **Date** | 2026-08-13 |
| **Source** | Live QA: two level-2 nodes, both consented, neither ever saw the other |
| **Depends on** | WO-094 shared Keel discovery key and live two-node proof |

## The defect

`keel_peers` is 0 in three completely different situations and the interface
renders all three identically:

1. this node has not published anything, so no one can find it;
2. this node published fine and nobody else is running Keel;
3. this node is at Level 1, where announcing is off by policy and the count can
   never be anything but 0.

Only the second is a normal state. The first is a fault and the third is a
policy outcome, and neither is distinguishable from "the network is quiet".

The QA session that produced this order spent hours on state 1 while reading it
as state 2. That is the whole bug: the number is honest and useless.

## Confirmed root cause of state 1

`Announce` runs immediately when the swarm starts, before the DHT has
bootstrapped. Every `Provide` fails at once with `failed to find any peer in
table`, each failure is skipped as "best effort", and the only trace is a count:

    swarm: announced 0/44 graph buckets, 0/3064 catalogue buckets, 0/256 shards

The retry is `announceInterval = 6 * time.Hour`. So a node that loses the race
at startup publishes nothing for six hours, is unfindable for six hours, and
reports zero peers while looking healthy in every other respect.

Work already on `main`: the failure reason is logged rather than swallowed
(`announce published NOTHING; this node is not discoverable: %v`), the loop
waits for a non-empty routing table before the first attempt, and a round that
publishes nothing retries in 60s instead of six hours. **Not verified against a
live DHT** — do not treat it as fixed.

## Required

1. **Distinguish the three states in the data.** The daemon already knows which
   it is in: whether announcing is permitted at this level, whether the last
   round published anything, and when. Report that alongside the count rather
   than making the interface infer it from a zero.
2. **Stop counting forever.** After a bounded number of consecutive rounds that
   publish nothing, the node's status becomes a stated fault with its reason —
   not a spinner and not a zero. "Cannot reach the network" is an answer; a
   counter that never moves is not.
3. **Say when Level 1 is the reason.** At Level 1 `AnnounceProviders` is false
   and the peer count is structurally zero. The interface must say so where the
   count is shown, so nobody debugs a policy.
4. **Verify against a real DHT**, not a unit test: a node with a corpus must
   publish and then be findable by a second node. Anything less leaves this
   order open — everything before this line is inference from logs.

## Architecture decision — 2026-08-13

### 1. Measure Keel discoverability, not incidental content publication

WO-094 added one shared, versioned Keel discovery key. Its publication is the
authoritative answer to “can another Keel node find this node?” Content-provider
records are a separate answer to “who serves this bucket?” and must not stand in
for network health:

- a content key may publish while the shared discovery key fails, leaving a
  node with unrelated corpora unable to find this one; and
- the shared discovery key may publish before thousands of content keys finish,
  making the node discoverable even though the bulk round is still running.

Move shared-key publication out of the bulk graph/shard/catalogue announce
round. A small presence loop owns it independently, so a failed discovery-key
publish is retried promptly instead of waiting behind a multi-minute content
round. The existing bulk loop remains responsible only for the provider records
of data this node can serve.

### 2. One bounded in-memory state machine

The running `swarm.Node` owns one lock-protected, non-persisted network-health
record. A fresh node starts fresh because its operational state and, below
Level 4, its network identity are process-scoped. Use these states exactly:

| State | Meaning |
|---|---|
| `off` | Shared-key publication is forbidden by the effective contribution policy. At Level 1, zero connected Keel nodes is expected. |
| `starting` | Publication is permitted, but no shared-key attempt has completed yet. This includes the bounded routing-table wait. |
| `retrying` | One or two consecutive shared-key publication attempts failed; another bounded retry is scheduled. |
| `ready` | The latest shared-key publication succeeded. A zero Keel-node count now honestly means no other compatible node has been found yet. |
| `fault` | Three consecutive shared-key publication attempts failed. The node keeps retrying, but the interface states the fault instead of spinning or showing an unexplained zero. |

The record carries only operational fields:

```text
state
reason                 none | level_policy | routing_unavailable | publish_failed
announce_permitted
published
consecutive_failures
last_attempt_at
last_success_at
next_retry_at
lookup_completed
last_lookup_at
keel_peers
```

It contains no observation, query, prefix, bucket, peer id, IP address or raw
libp2p error. Raw errors remain in the local daemon log. `reason` is a bounded
enum and the daemon, not the browser, owns its user-safe explanation.

State transitions are atomic from the status reader's perspective:

- effective Level 1 derives `off` without starting the presence or shared-key
  lookup loops;
- Level 2+ starts at `starting`;
- a failed attempt increments the consecutive count and moves to `retrying`,
  then to `fault` on the third failure;
- a successful publish moves to `ready`, clears the reason/failure count and
  records the success time; and
- success after `fault` recovers automatically to `ready` without a restart.

`published` describes the latest shared-key publication outcome; it is not a
sticky “succeeded once in this process” bit. `last_success_at` preserves the
last known success for diagnosis.

### 3. Bound attempts and continue recovery

Keep the existing 90-second maximum routing-table wait. Bound each shared-key
`Provide` to 30 seconds; never give a DHT call the node's whole lifetime
context as its only deadline. After consecutive failure `n`, retry after
`min(1 minute × 2^(n-1), 15 minutes)`: one, two, four, eight, then fifteen
minutes. Success returns to the six-hour refresh cadence. Fault is a reported
state, not a decision to stop trying.

Give each `FindPeers` round a 90-second deadline too. Run the first lookup
promptly after a successful shared-key publish. The existing modest steady-
state lookup cadence and peer limit remain unchanged. An unfinished first
lookup is rendered as “looking”; after one completes, `ready` plus zero peers
is the normal quiet-network state.

The presence loop and bulk provider loop share the node lifetime and outbound
gate. A downgrade closes the gate before either can publish again, then node
replacement supplies the Level-1 `off` state. Do not mutate contribution policy
inside this health machine.

### 4. One typed wire shape and one UI owner

Add optional bridge capability `network_status:1`. It revisions the typed
status embedded in existing replies; it does not add a new RPC or permit any
network action.

`GET_STATS.stats.swarm.network` is the authoritative UI payload. Keep existing
top-level `peers`, `keel_peers`, node id and version fields for compatibility
and diagnosis, but the interface must not infer health from them. The network
object carries the Keel-node count with the state so they are one snapshot.

The Config page's existing **Network** row owns the display. Render these
meanings, using “node” or “install,” never “user”:

- `off`: Personal sharing does not advertise this node; zero is expected, while
  the permitted download/pre-walk features still work;
- `starting`/`retrying`: joining or retrying, including the bounded attempt
  count and next retry when available;
- `ready`, lookup unfinished: discoverable and looking for other Keel nodes;
- `ready`, zero after a lookup: connected to the Keel network, with no other
  compatible nodes found yet;
- `ready`, positive: connected to that many other compatible Keel nodes; and
- `fault`: could not make this node discoverable, reason stated, automatic
  retry promised. Never leave `0` as the headline.

The raw DHT connection count may remain afterward only as explicitly labelled
“network plumbing.” It is never a count of people or Keel installs.

Remove the duplicate `keel_peers` tile from **Your impact**; the count remains
visible in the immediately preceding Network section, now with its meaning.
Keep the payload field for older extensions. This is one UI owner, not removal
of the information.

A new extension paired with a daemon that did not negotiate
`network_status:1` shows “Peer-network health needs a desktop app update” and
does not fall back to the ambiguous zero. An older extension ignores the
additive object from a new daemon.

While the Config tab is visible, refresh local `GET_STATS` on a modest timer
(ten to fifteen seconds) and immediately on tab/visibility return. Do not poll
while hidden. This makes recovery and the bounded fault visible without adding
network traffic or a second source of truth.

### 5. Required automated proof

- Pure/fake-clock transition tests cover `off`, `starting`, failures one and
  two, fault on failure three, backoff cap, success, and recovery from fault.
  Extract the transition/backoff decisions from timers and inject the clock and
  shared-key publisher; tests must not sleep for production intervals.
- A Level-1 node performs no shared-key publish or lookup and reports
  `level_policy`; its existing fetch/pre-walk/word-fetch capabilities remain
  unchanged.
- A shared-key failure cannot be hidden by successful content-provider
  publication, and a shared-key success does not wait for the bulk content
  round.
- Status snapshots contain no raw error, address, peer id, query, prefix,
  bucket or observation value.
- `network_status:1` negotiates additively; old pairs still connect.
- DOM tests cover every state above, the older-daemon update message, the
  absence of “users”/raw-zero inference, and visible-only polling.
- Existing contribution, consent, serving, discovery, Live, word and search
  gates remain unchanged under Go, race and extension suites.

### 6. Manual acceptance gate

This work order is not accepted from loopback tests alone. On the current
binary and public DHT:

1. start a consented Level-2 node with a non-empty corpus and observe it reach
   `ready` with a recent shared-key publication success;
2. start a second compatible node on another machine/network and observe both
   report at least one Keel node;
3. capture the status transition and daemon logs without retaining peer ids or
   addresses in the work order; and
4. demonstrate one failure-to-fault-to-recovery sequence, using a controlled
   blocked route if the public network cannot safely supply it naturally.

Until this gate passes, implementation may be code-accepted but WO-093 remains
live-QA pending.

## Do not

- Do not present the DHT `peers` count as people. It is guaranteed non-zero and
  says nothing about other installs (WO-055).
- Do not soften a publish failure into a warning that leaves the count at zero.

## Notes

`Announce` publishing thousands of records one at a time is a separate concern:
each `Provide` is a full DHT walk, so a real corpus takes hours before the node
is discoverable. A bounded-concurrency version exists on this machine, compiles
and passes the Go suite, and is uncommitted and unverified — treat it as a
starting point, not a fix.

The bounded-concurrency provider implementation is now on `main`. It improves
bulk throughput but does not replace the independent shared-key health machine
above; conflating them would recreate the same ambiguous state with faster
content publication.

## Implementation record — 2026-08-14

Everything in §§1–5 is implemented, including WO-109's accepted truthfulness
corrections. Section 6 is partially complete; see the live evidence and
remaining proof below.

**Daemon.** `daemon/swarm/health.go` is new and owns the whole machine: the five
states, the four-value reason enum, the `NetworkStatus` snapshot, the backoff
and the presence loop. `Node.health` is built in `newNode` from
`Policy.AnnounceProviders`, so a Level-1 node reports `off`/`level_policy`
before any goroutine exists and `RunPresence` returns immediately without
publishing. `publishPresence` (in `rendezvous.go`) is the only writer of the
publication outcome and bounds each `Provide` to 30 seconds. `Announce` no
longer publishes the rendezvous key at all — the two were conflated at the top
of that function, which is precisely how a successful content round could hide
a failed discovery-key publish, and how a successful discovery-key publish had
to queue behind a multi-minute content round.

`rendezvousLoop` now waits on `Node.Published()` instead of a 30-second timer,
so the first lookup runs when this node actually becomes findable, and each
round is bounded to 90 seconds. `swarmStatus` reports `network`;
`keel_peers` and `peers` stay for compatibility and diagnosis.

**Wire.** `network_status:1` is offered in `DaemonCaps` and requested in
`CLIENT_OPTIONAL`. It gates no RPC.

**Interface.** `renderSwarm` renders `sw.network` through `networkHeadline`,
which has a sentence for every state and never leaves a bare count as the
headline. Without the negotiated capability it says the desktop app needs
updating and withholds the count rather than falling back to it. The Config tab
re-reads `GET_STATS` every 12 seconds while visible and on visibility return,
and not at all while hidden. The duplicate `keel_peers` tile is gone from **Your
impact**; the payload field remains for older extensions.

**Proof.** `daemon/swarm/health_test.go` (15 tests) drives the loop through an
injected clock and publisher — no test waits a production interval. It covers
`off`, `starting`, failures one and two, fault on three, the observed backoff
schedule and its cap, success, recovery from fault without a restart,
`routing_unavailable` as its own reason, the bounded routing wait,
gate-shut→`off` without mutating policy, cancellation not counting as a
failure, and a JSON snapshot asserted to contain only the eleven permitted
fields and no peer id, address, prefix or raw error. Two tests use a real
isolated node to show `Announce` cannot move the health record in either
direction, and one shows a Level-1 node keeps `Fetch`/`FetchWordTelemetry` and
records no lookup. `daemon/bridge/hello_network_status_test.go` covers additive
negotiation both ways. `test/network-status-dom.test.js` covers every rendered
state, neutral Live wording, the older-daemon message, the absence of "user"
and of a raw-zero headline, visible-only polling, and the removed tile.

Go, `go test -race` and 287/287 extension tests pass.

## Reviewer findings — 2026-08-14

The initial review found that the health machine, independent shared-key
publication, capability negotiation, bounded recovery and state-specific UI
passed, while two remaining sentences inferred facts from the wrong signal and
prevented code acceptance:

1. the UI says a non-empty Live index is “all from your own browsing” whenever
   the current Keel-node count is zero, even though cached peer sightings can
   outlive a connection and a missing health object is also coerced to zero;
2. the bulk content first-batch log says “this node is now findable,” although
   this order makes shared-key presence the only authoritative proof of node
   discoverability.

WO-109's bounded wording and regression correction is accepted. It did not
reopen the state machine or protocol design. Section 6 remains the separate
public-DHT acceptance gate.

## Live public-DHT evidence — 2026-08-14

On the current Level-2 binary, the Network row exposed a real first-attempt
presence failure as `retrying` with a one-minute retry instead of showing an
ambiguous zero. The local daemon log identified the bounded raw cause as
`context deadline exceeded`. Without restarting the daemon, the scheduled
retry then published the shared discovery key and logged that other Keel nodes
could find this one. This establishes a real public-DHT
`retrying` -> `ready` recovery and confirms that successful bulk graph-provider
publication did not mask the initial presence failure.

This is not the required `fault` recovery: only one consecutive attempt failed,
not three. The raw DHT connection count was non-zero throughout and correctly
remained labelled network plumbing; it did not decide either state.

## What is not proved

Section 6 remains open, but its first public-network evidence now exists: the
current node recovered from one real timed-out presence publication and reached
`ready` on the public DHT without restart. Still unproved on the current binary:
a second machine finding this node, and a controlled three-failure
`fault` -> recovery sequence. The fake-clock tests cover that latter transition
mechanically; they do not replace the live gate. Treat WO-093 as code-accepted,
not release-accepted.
