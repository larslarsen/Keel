# WO-094 — Nodes must be able to find each other, not only find content

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Implemented and live-verified 2026-08-13** — remaining acceptance items unchecked below are gate/version cases, not the connection itself |
| **Date** | 2026-08-13 |
| **Source** | Live QA: two consented Level-2 nodes, both healthy, never connected across a full day |
| **Depends on** | WO-093 (making the failure visible) |

## The defect

There is no way for one Keel node to find another. Discovery is entirely
content-addressed and demand-driven:

- `Announce` publishes provider records **per key** — graph prefixes, catalogue
  buckets, shards. It says "I hold bucket 0a3f", never "I am a Keel node".
- The only thing that ever looks a provider up is `prewarm()`
  (`main.go:684` → `swarm_runtime.go`), triggered by an IMPRESSIONS frame when
  the user watches a video. Nothing else in the node performs a lookup, and the
  only timer in the node is the announce loop.

So two nodes connect only if **all** of these coincide: both are publishing,
one of them is browsing, and the video being watched falls in a prefix the other
node happens to hold. Two nodes with different viewing histories can run
correctly, forever, and never meet.

This is the actual reason `keel_peers` is 0 across two healthy installs. It is
not the announce timing (WO-093), not consent, and not the contribution level —
each of those was investigated and eliminated first.

## Required

1. **A rendezvous key.** One fixed, versioned key every node provides and looks
   up, meaning "a Keel node is here". Derive it from the existing protocol
   version constants so a schema change partitions rendezvous the same way it
   partitions everything else (WO-060) — nodes that cannot talk must not find
   each other.
2. **Publish it with the first announce batch**, so a node is discoverable as a
   node within seconds of starting, not at the end of a full round.
3. **Look it up on a timer**, and connect to what is found. Bounded: a small
   number of peers per round, backing off when the set is unchanged. This is the
   node's only unprompted outbound lookup, so it must be explicitly gated by the
   same outbound gate as everything else (WO-077) and must not run at Level 1,
   where announcing is off and a node has nothing to offer.
4. **Report it.** `keel_peers` becoming non-zero is the point; the status must
   distinguish "no rendezvous peers found yet" from "found peers, none speak our
   protocol version".

## Do not

- Do not connect to peers found this way for anything beyond protocol
  identification. What may be served or fetched is decided by contribution level
  and the outbound gate, and rendezvous must not become a side channel around
  either.
- Do not make the rendezvous key derived from user data. It is a constant of the
  software, identical on every install, and must carry nothing about the person
  running it.
- Do not remove content-addressed discovery. Rendezvous is how nodes meet;
  per-bucket provider records remain how data is found.

## Acceptance

- [x] Two nodes on different machines, with **no overlapping corpus**, both at
      Level 2, connect within minutes of both being up, and each reports
      `keel_peers: 1`. Verified 2026-08-13 across Linux and Windows: this node
      published, looked once, found the other and connected —
      `rendezvous: {published: true, looks: 1, last_found: 1}`, `keel_peers: 1`.
      The other machine's interface reported "Connected to 1 other Keel user" and
      "21 livestreams known", so the live index crossed the link as well: a
      connection carrying data, not just a handshake.
- [ ] A Level-1 node neither provides nor looks up the rendezvous key.
- [ ] Withdrawing consent or dropping to Level 1 stops rendezvous within one
      tick, verified through the outbound gate rather than by inspection.
- [ ] A node whose protocol version differs does not appear as a Keel peer.
- [ ] The rendezvous key is identical on two independent installs and contains
      no per-user input.

## Pushback invited

A DHT rendezvous makes every Keel node discoverable to anyone who knows the key,
which is every user of the software and anyone reading it here. That is the
trade every peer network makes to exist at all, and it reveals only that an
address runs Keel — but if the threat model in `DESIGN_v2.md` rules that out,
this order is wrong and the answer is a different mechanism (invitation-based
peering, or a bootstrap list), not a weaker version of this one.
