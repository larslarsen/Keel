# WO-093 — A node that cannot join the network must say so, not count forever

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Ready** |
| **Date** | 2026-08-13 |
| **Source** | Live QA: two level-2 nodes, both consented, neither ever saw the other |

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
