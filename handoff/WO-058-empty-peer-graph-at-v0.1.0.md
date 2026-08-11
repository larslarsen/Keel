# WO-058 — Peer graph is empty at v0.1.0: no seed, no auto peer data

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Copy + DESIGN_v2 fixed (2026-08-11); one item open** — seed-vs-narrowed-Level-2 is a product decision, not resolved here. |
| **Date** | 2026-08-08 |
| **Source** | Lars, 2026-08-08 (audit of README claims vs code) |

The README, the consent menu, and the "Your data" page all imply a live peer
graph: "your node pulls neighbourhoods other people hold and passes them on,"
"what users actually exchange today is the recommendation graph." That is false
at v0.1.0. There is no published seed and no automatic peer data, so there is
nothing to pull or relay.

## What the code actually shows

- `cli_seed.go` only has manual `build` / `import` subcommands. No download
  URL, no auto-fetch, no publish endpoint. Grep for any seed URL / publish
  mechanism returns nothing.
- `BuildSeedPack` at Level 2 is `mirrorOnly` (cli_seed.go:56 → blocks.go
  `mirrorEdges`), which builds from `peer_edges`. A fresh node has empty
  `peer_edges`, so `BuildSeedPack` returns "no blocks to seed — this node has no
  edges to share" (blocks.go:121). Level 2 cannot bootstrap Level 2: no data in,
  nobody else has a seed either.
- A seed only exists if someone at Level 3+ builds one with `--own` (discloses
  their own funnel) and hands the file to others. Nobody has done that. No
  published seed artifact exists anywhere the daemon knows.
- `MaxImplementedLevel = LevelMirror` (contribution.go:63). Levels 3–4
  (which *would* publish own edges, and STAR) are not built. So `peer_edges`
  can only be populated by manual bundle/seed import, never automatically.
- The swarm has only ever run against loopback (README Status). The only live
  cross-node feature today is the no-sender livestream index (live.go).

## Consequence (updated 2026-08-09 — bootstrap resolved via search)

At v0.1.0 every shipped Level 2 node starts with empty `peer_edges` and an empty
swarm — that part stands. BUT the empty-graph blocker for SEARCH is resolved by
WO-059's distributed peer search, not by a seed: the shard namespace is
deterministic and global, so the moment any node searches, it materializes blocks
for those shards and serves them. Another node searching an overlapping/identical
word hits the SAME shards — finds existing blocks or mirrors them. Coverage is
demand-driven and self-heals from t=0; **no pre-seeded graph (seed-pack) is
required for search to work.** The corpus converges to "whatever people search
for." So WO-058's "empty peer graph" is a real shipping-state fact for the
*suggestion walk* (which still needs a graph to walk), but it is NOT a blocker for
the *search* path — search bootstraps itself.

The seed's reduced role: the bootstrap seed and prefetching-suggestions pull
**GRAPH ONLY** (stringless), and can be regenerated from what a node downloaded
from searching others — re-pullable at any time. Strings are fetched on demand at
search time only. So the seed is no longer the load-bearing bootstrap artifact for
search; it remains relevant only for the graph-walk/suggestion path, where a
stringless graph still has to come from somewhere (a Level-3+ publisher, or the
user's own history extended by search-populated graph blocks).

Remaining open item (not resolved by WO-059): the *suggestion walk* still needs a
graph to traverse. Search populates graph blocks as a side effect, so over time a
node's graph grows from its own searches — but the first-run suggestion experience
with zero graph is still empty until the user searches. That is the honest
remaining gap; it is narrower than the original WO-058 framed it.

## What to fix (decision needed, not assumed)

1. **Tell the truth in user-facing copy.** README Research/Status, consent
   menu, and "Your data" must say the peer graph is unpopulated at v0.1.0 and
   the walk stays within your own history until a seed/bundle is imported.
   (Done 2026-08-11: all three now scope the "no automatic peer data" claim to
   the *suggestion walk* specifically, and say search reaches peers on demand
   via WO-059 with no seed needed — the old wording implied nothing moved
   automatically at all, which stopped being true once WO-059 shipped.)
2. **Either bootstrap a seed or rename the level.** Options:
   - Build and publish one seed (someone runs `seed build --own`, ships it via
     §7.3 channel) so Level 2 has something to relay. This discloses that one
     person's funnel — acceptable only as a deliberate, labelled bootstrap.
   - Or make the default experience honest: Level 2 with no seed is just
     "lend bandwidth for when a graph exists," which is a null product today.
3. **Decide the real exchange path.** §7.3 already says bundles ship over
   Zenodo/GitHub/Archive (central HTTPS), not the swarm. The swarm is a
   supplement for the tail + livestreams, and the tail needs PIR to be private
   (seed.go:20-22 — "a much larger piece of machinery," not built). The prose
   should not imply the swarm is the primary graph exchange until that is true.
   (Done 2026-08-11: DESIGN_v2's tiering table described a never-built plan —
   a ~50-200GB bulk download of popular search terms — instead of what WO-059
   actually built. Corrected to the real tier-2 shard mechanism.)

## Acceptance

- [x] User-facing copy (README, consent, page) states the peer graph is
      unpopulated at v0.1.0, no false "pull-and-relay" wording.
- [ ] A seed exists OR Level 2's empty-state behaviour is documented as expected
      and the level's promise is narrowed to match. **Still open — genuine
      product decision, not a copy fix; see options above. WO-059 adds a third
      option not in the original framing: since search self-heals a node's
      graph as a side effect of use (this doc's "Consequence" section), the
      first-run empty-suggestions gap may narrow on its own as search gets
      used, without anyone needing to publish a seed at all.**
- [x] The primary graph-exchange path (bundle over §7.3 channel vs swarm) is
      stated once, consistently, in DESIGN_v2.
