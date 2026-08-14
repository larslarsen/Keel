# Keel — current normative architecture

**Status:** Authoritative for current engineering decisions.
**Date:** 2026-08-12.
**Rationale/history:** `DESIGN_v2.md`.
**Implementation queue:** `handoff/README.md`.

This document answers “what must the system be now?” `DESIGN_v2.md` keeps the
research, rejected alternatives and legal rationale that explain why. If the two
conflict, this document wins. A work order may change this document only when it
names the change explicitly.

## 1. Product and trust boundary

Keel is a browser extension plus one per-user local daemon. It observes rendered
recommendation surfaces on explicitly named platforms, stores the resulting
corpus locally, and computes user-facing search, analysis and recommendations.

The browser is the untrusted sensor/display zone. The daemon is the trusted
data/network zone.

| Owner | Responsibilities |
|---|---|
| Extension content script | Read rendered DOM in the isolated world; normalize records; hold a bounded in-memory backlog. |
| Extension service worker | Route messages; associate observations with tabs/windows; bridge to the local daemon. |
| Extension pages | Render daemon results and collect explicit user intent. They apply decisions; they do not own the corpus or network policy. |
| Per-user daemon | Own SQLite, contribution state, ranking/search/analysis, thumbnails, and every peer-network operation. |
| Native-host proxy | Translate one browser native-messaging stdio connection to authenticated local IPC with the daemon. No database or swarm ownership. |

Hard invariants:

- No observation data is persisted in browser storage. `chrome.storage` holds
  UI preferences and observation consent only.
- No MAIN-world scripts, fetch/XHR interception, YouTube Data API calls,
  automated crawling, raw search-query persistence, runtime JS dependencies,
  framework, bundler or build step.
- `extract.js` remains pure: DOM subtree and validated selector data in,
  normalized record or null out.
- The daemon treats extension and peer input as untrusted and validates both.
- Platforms and host permissions are named individually. Current platforms are
  `www.youtube.com` and `www.tiktok.com`; no wildcard third-platform permission.

## 2. Observation and local state

Normalized impressions are persisted only in the daemon's SQLite database.
They are kept until the user wipes them; no automatic retention sweep exists.
The extension may retain at most the bounded reconnect backlog in memory.

Page proof has tab identity:

```
tab_id -> { window_id, page_load_id, platform, surface,
            impressions, failures, rail_generation }
```

The side panel is per window. Its context is the active tab in that window, and
its proof is looked up by that tab's id. A background tab may update the corpus
but may not replace an active panel's seed or platform. Proofs are removed when
their tab closes and are never persisted.

`PANEL_CONTEXT` and panel snapshot replies carry `window_id`, `tab_id`,
`platform`, `surface`, `focus` and the matching proof when one exists. Generic
store-update broadcasts identify their source tab; a panel ignores a proof that
does not match its active tab.

## 3. Contribution contract

Contribution is a daemon-owned policy. Recording consent and contribution
consent are separate.

Recording consent covers both local observation and the default Level-1 network
uses. The affirmative screen must disclose, before the action, broad shared-data
requests. These are disclosed uses of the observed context even though requests
use broad prefixes. Live and locally derived word telemetry are not Level-1 data
practices.

The enforcement boundary is the daemon, not only the browser profile. A current,
revisioned network-data consent record in SQLite is required before any swarm
node exists. Missing or obsolete consent means network-off, independently of the
stored contribution level. Browser storage retains only that profile's
observation decision so its content observer can fail closed before sending a
record. The initial affirmative action grants the current daemon network-data
revision and only then enables observation in that profile. Level 2 remains a
separate opt-in. WO-089 implements this boundary; no build predating it is
eligible for publication or external recruitment.

| Level | Peer network | Own observations sent | What the user receives |
|---|---|---|---|
| **1 — Strictly Personal** (default) | Consumer: peer discovery, seed receipt, whole-bucket graph/catalogue/search-shard fetch needed by shared suggestions and graph pre-walk, plus fetch of the global fixed-shape word-level HLL/CMS telemetry pack. It has no Live topic or snapshot capability. It does not initiate user-triggered distributed peer search, serve any peer data, announce itself as a provider, or join three-gram yield/token-sketch topics. | None. No Live sighting, local word aggregate, recommendation edge, watch trail or stable application author. Broad requests still expose IP/timing and coarse requested prefixes. | Local search, the funnel inspector, shared suggestions/graph pre-walk and fetched global word statistics. Shared Live is unavailable. |
| **2 — Broad sharing** | Level 1 plus the full Live gossip/snapshot system, bidirectional word telemetry, user-triggered distributed peer search, and serving broad hashed-prefix buckets containing both locally produced graph blocks and cached blocks, provider announcements, and three-gram availability/sketch telemetry for everything it serves. Supporting catalogue/search data is also served only as complete broad buckets/shards. | Livestream notices, aggregate word HLL/CMS telemetry, and aggregated stringless recommendation blocks derived from local observations and mixed into complete broad buckets. No page-load ids, raw timestamps, selected-video response or ordered watch trail. | The shared Live index, search across other people's recommendation records, a self-growing shared graph and warm cache; contributed storage/data also improve local latency and network reach. |
| **3 — Cohort** | Level 2 plus threshold-protected aggregate publication. | The Level-2 broad blocks plus STAR-protected cohort measurements; never a raw ordered trail. | STAR-derived cohort/funnel comparison when built. |
| **4 — Transparency** | Level 3 plus attributed publication. | Explicitly public, attributed funnel records. | No gated reward. |

Levels 3 and 4 remain unimplemented. The UI must not allow selecting a level
whose pipeline does not exist.

Level 1 keeps the complete personal recommendation path. Receiving the common
seed, fetching whole prefix buckets for shared suggestions and pre-walking the
graph remain available without contributing the user's corpus or volunteering
the local cache. User-triggered search across other people's recommendations is
different: it creates arbitrary repeatable work for serving peers and is
therefore reciprocal at Level 2+. This is a capacity boundary, not a fee on
privacy. Level 1 retains local search and is “personal,” not “offline.” Bucket
requests still expose peer participation and coarse interests as described in
the privacy policy.

Level 1 sends no observation-derived application payload. It may request broad
shared buckets and fetch the global WO-068 HLL/CMS pack, so it is a networked
consumer rather than an offline mode. Those requests still expose IP/timing and
coarse requested prefixes. It does not answer the word protocol or merge its
local corpus into an outbound pack. At Level 2+ the pack includes locally
observed titles; it carries no plaintext words, video ids, edges or queries, but
known word guesses can still be tested against a CMS. That is aggregate
metadata, not zero disclosure.

The Live network begins at Level 2. Level 1 does not join the topic, receive or
relay gossip, fetch or serve snapshots, seed the index from local observations,
or originate sightings. Level 2+ runs the complete existing Live system. A Live
notice does not carry recommendation edges or a watch trail, but “no author” is
not a complete anonymity proof: a direct neighbour may use connection topology
and timing to infer an origin probabilistically. Level-2 consent and privacy copy
must state that residual disclosure.

Level 2 is where locally observed graph data enters the shared graph. The
privacy unit is the complete broad bucket, not one selected neighbourhood: a
node advertises a hashed prefix and answers it with every eligible local and
cached block in that prefix. Locally derived blocks are stringless aggregates,
not raw impression rows or an ordered watch trail. This is Lars's broadness
construction: contribution and cover are the same object. It must not be
implemented as a per-video lookup with decoys, and no response may label which
bucket members came from the serving user's observations versus its cache.

This does not make Level 2 zero-disclosure. A recipient sees the complete set
returned by the peer, and connection/signing metadata can link deliveries. The
privacy and consent copy must say that broad aggregated recommendation blocks
leave the device. Level 3 is distinguished by STAR-protected cohort measurement,
not by being the first level that contributes any locally derived edge.
Ordinary Level-2 blocks must not use one install-wide author key across
neighbourhoods: each neighbourhood has an unlinkable claim identity, preserved
unchanged through mirrors for replacement/deduplication. A mirror never turns a
relayed claim into a new observation by re-signing it. Deliberately stable,
cross-block attribution belongs only to Level 4.

The shared live index remains whole-feed gossip with local search, no popularity
ranking, bounded memory and expiry. Its query privacy comes from holding the
whole index, not from a claim that publication has no metadata. At Level 2+,
subscription and relay are permanent and not tied to opening the Live tab. At
Level 1 the Live surface stays visible but unavailable with a direct Level-2
explanation; there is no partial receive-only Live mode.

TikTok Explore and Following are distinct durable ordinary-impression surfaces.
TikTok `/live` and `/@creator/live` are instead ephemeral Level-2+ Live
sightings: a record uses a canonical lowercase `@creator/live` locator (or a
rendered numeric TikTok video id), never a fabricated room id. Page-load id and
displayed slot are bridge-only and never enter SQLite, the Live wire, snapshots
or logs. Live wire v2 is `keel/live/2` and `/keel/live-snapshot/2.0.0`.

The following capabilities are independently selected; they are not inferred
from one generic “swarm enabled” boolean:

| Capability | Level 1 | Level 2+ |
|---|---:|---:|
| Peer discovery/connection | On | On |
| Live topic + live-snapshot stream | Off | Receive, relay, originate, serve whole snapshot |
| Seed receipt + graph/catalogue/search-shard fetch/pre-walk | On | On |
| User-triggered distributed peer search | Off; local search remains on | On |
| Three-gram yield + token-sketch topics | Off | Receive/relay/originate for the full served corpus |
| Whole word HLL/CMS telemetry stream | Fetch only; never serve or include local corpus | Fetch, serve and include local corpus |
| Broad graph/catalogue/search serve + provider announcements | Off | On; includes locally derived and cached members of each complete bucket/shard |
| STAR-protected cohort measurement | Off | Level 3+ only |
| Attributed funnel publication | Off | Level 4 only |

“Sketch” is not one policy category:

- `YieldTopic` and the gossiped three-gram `SketchTopic` exist to locate and
  size searchable blocks. Level 1 does not join them because it serves no
  blocks. Its fetcher treats missing yield/count data as unknown and still
  performs permitted fetch/pre-walk work; WO-085 separately gates
  user-triggered distributed `PEER_SEARCH`. Level 2 joins and originates them
  for its full local-plus-cached served corpus.
- `WordTelemetryProtocol` is the separate WO-068 display aggregate. Level 1
  fetches the whole HLL/CMS pack but does not answer the protocol or include
  local-corpus input. Level 2+ does both.
- The diagnostic HLL over recommendation edge keys used to measure cross-user
  overlap is neither of those. It is not an automatic Level-1 network protocol;
  any future automatic exchange requires its own consent/threat-model decision.

## 4. Runtime policy state machine

The daemon reports contribution state as:

```
stored_level     user choice persisted in SQLite
effective_level  policy enforced by the running network owner
transition       idle | starting | stopping | failed
detail           human-readable failure/status, when any
```

SQLite also holds an internal `startup_level`: the highest contribution policy
that may be reconstructed after a crash. In the idle state it equals
`stored_level`. During an upgrade it remains at the last successfully effective
level until activation commits. On startup, the owner constructs
`startup_level`, never the more permissive of the two, and reports a mismatch
rather than auto-escalating. Only the single daemon owner may change this state.

- **Downgrade:** immediately close the runtime permission gate to disallowed
  Level-2+ block service, provider announcements and three-gram topic
  participation. In one transaction set both `stored_level` and `startup_level`
  to the lower level, then replace the node. Do not report success until the
  effective policy is Level 1. If persistence or construction fails, keep the
  runtime at the safer gate/stopped state and report the mismatch.
- **Upgrade:** persist the explicit choice in `stored_level` while leaving
  `startup_level` at the prior effective level. Replace the node; only after the
  new policy is effective commit `startup_level` to the higher level. If startup
  or the activation commit fails, stop the higher-level node, reconstruct the
  prior policy, and restore `stored_level` when possible. The retained lower
  `startup_level` guarantees a crash cannot turn a failed upgrade into an
  automatic escalation.
- Network construction must not block the native bridge. Bootstrap and provider
  discovery remain background work; “effective” means the correctly configured
  local node exists, not that the public DHT is reachable.
- Every block serve, provider-announcement and three-gram topic path reads
  the effective runtime owner, not the SQLite setting independently. Graph
  fetch/pre-walk and word-level HLL/CMS fetch remain enabled in the Level-1
  policy; word service, Live and three-gram topics are absent.

The current node construction binds stream handlers and pubsub topics at start,
so the selected implementation is a supervisor-controlled node replacement, not
mutation of a live node:

1. Build an explicit policy with separate `live`, `fetch`,
   `serve_broad_buckets`, `include_local_graph`, `include_local_catalogue`,
   `announce_providers`, `join_search_telemetry`, `exchange_word_telemetry`,
   `publish_cohort_measurements` and `publish_attributed_funnel` capabilities.
   The graph and catalogue corpora are selected by a source set (local,
   imported, or both) derived from those capabilities, never by a boolean that
   can only choose one of the two.
2. For 2→1, close the old node's outbound permission gate, durably set stored
   and startup levels to 1, then stop/detach it and start the consumer-only node
   with no Live object, topic, snapshot handler or local seed path.
   If consumer-node startup fails, remain network-stopped and report failure;
   never resurrect Level 2.
3. For 1→2, persist the explicit user choice while retaining startup level 1,
   replace the node, and raise the startup level only after Level 2 is effective.
   Roll back to Level 1 on construction or activation-commit failure.
4. Bootstrap runs asynchronously after the correctly configured node exists.
   Lack of public peers is degraded reachability, not a failed policy change.

During node replacement `transition` is `stopping` or `starting`, RPCs that need
the replaced node return a typed temporary-unavailable result, and state reads
remain available. Only the runtime supervisor owns the node pointer; callers
cannot retain it across a transition.

## 5. One daemon, many browser clients

Exactly one process per OS user owns SQLite and libp2p.

### Processes

```
browser extension
    <native-messaging stdio>
keel-host proxy (one per browser connection)
    <local framed IPC>
keel daemon owner (one per OS user)
    -> SQLite
    -> swarm runtime
```

The proxy uses the same length-prefixed JSON envelopes in both directions and
does not interpret application payloads beyond the local owner handshake.

### Local endpoint and authentication

- Linux/macOS: Unix-domain socket in the user's runtime/config directory, mode
  `0600`, in a parent directory inaccessible to other users.
- Windows: named pipe restricted to the current user's SID.
- No TCP listener, loopback or otherwise.
- A random installation secret stored mode `0600` / current-user ACL is used in
  the proxy-owner handshake in addition to OS endpoint permissions. Failure is
  fatal for that client connection.

Owner election uses exclusive endpoint creation, not SQLite or a lock file as
the authority. A proxy first connects; if absent, one proxy spawns the owner and
all contenders wait for the endpoint. Stale Unix socket cleanup requires a
failed connect plus successful exclusive election before unlinking. Windows
uses first named-pipe-instance ownership.

The owner accepts concurrent clients and multiplexes each as an independent
bridge session over shared Store and SwarmRuntime instances. Correlation IDs are
scoped to a client connection. A client disconnect never stops the owner.

The owner is long-lived for the user session because Level 1's seed, graph and
word-statistic fetches — and Level 2's Live gossip plus provider duties — cannot
depend on a browser tab remaining open. The owner still does not construct a
swarm node until current network-data consent exists (WO-089). Packaging may
later register user-session startup; connect-or-spawn remains the recovery path.
Uninstall and an explicit owner command provide clean shutdown. Upgrades use a
version-negotiated controlled restart; a proxy must not kill an unknown owner.

## 6. Keel Bridge compatibility

Native framing remains envelope `v: 2`. That number defines the stable bootstrap
envelope, not every RPC payload revision. Compatibility is negotiated inside
`HELLO` before any other request.

```json
{
  "client_version": "0.1.0",
  "api": { "min": 1, "max": 1 },
  "required": { "core": 1, "network_consent": 1 },
  "optional": { "selectors": 1, "tiktok": 1, "scroll_history": 1,
                "peer_search": 2, "word_stats": 1, "queue": 1,
                "contribution_runtime": 1, "contribution_impact": 1,
                "live_sightings": 2 }
}
```

`HELLO_ACK` returns daemon version, selected API revision, compatibility status,
reason/code, and the exact negotiated capability map. Capability values are
positive integer schema revisions. The selected version for a capability is the
highest mutually supported revision; absence means unavailable.

`core:1` covers impression ingestion, stats, export/wipe and clean disconnected
behavior. Missing required capability or non-overlapping API range fails the
session closed: the extension renders “desktop app update required” and does not
send application RPCs. Optional controls are disabled with an actionable reason
based on negotiated capabilities, not hidden or probed by attempting an RPC and
interpreting “unknown type.”

Privacy/security behavior never silently downgrades. In particular, Level 2
controls require `contribution_runtime:1`; an older daemon may continue the
local core but the extension must not claim it can change effective networking.
`contribution_impact:1` is optional the same way (WO-086): its absence disables
the Level-2 impact panel with an update reason, never an invented zero.
`peer_search:2` carries WO-085's reciprocal contract on the same principle in
the other direction: a negotiated `1` means the peer daemon has no level rule,
so the extension leaves the control enabled rather than imposing a restriction
the daemon does not enforce. Enforcement itself never depends on the negotiated
revision — the daemon refuses at Level 1 whatever the client claims to speak.
`live_sightings:2` (WO-104) is a bridge-only relaxation: a `LIVE_ROOM` sighting
may omit `title` because the rendered room header has none. Revision 1 still
requires a title. A revision-2 extension must not send the relaxed shape to a
daemon that negotiated revision 1. The Live gossip/snapshot record is
unchanged and already permitted an absent `t`.
Application semantic versions are diagnostic/update information, not the
compatibility algorithm.

The native proxy and daemon-owner IPC has its own required `owner_ipc:1`
handshake so a replaced proxy cannot feed incompatible frames to an old owner.
The owner is also the authority for global runtime status: after a contribution
transition it sends `CONTRIBUTION_STATUS` with an owner-event id to every
authenticated proxy session. Request correlation remains scoped to the session
that initiated the change.

## 7. Extension module boundaries

Plain ES modules only. The service worker is the composition root and owns no
feature logic beyond wiring browser events to modules. Implemented by WO-083
(WO-080 having supplied the pure tab-proof store first, so the correct state was
extracted rather than the global defect).

Boundaries:

| Module | Owns |
|---|---|
| `lib/native.js` | Native bridge connection, HELLO negotiation, pending requests and reconnect. |
| `background/page_proofs.js` | Pure tab-keyed proof store and bounded lifecycle operations. No browser APIs. |
| `background/panel_context.js` | Active-tab/window lookup, panel enable/close policy, panel-port bookkeeping and context broadcasts. |
| `background/prefs.js` | Browser-storage adapter for hide and observation-consent preferences only. |
| `background/rpc.js` | Validated extension-message dispatch, daemon capability gates and the bounded disconnected-impression buffer. |
| `background/sw.js` | Instantiate modules; register listeners; connect dependencies. |
| `lib/render.js` | Escaping and formatting identical across both surfaces. |
| `sidepanel/render.js`, `page/render.js` | Escaped DOM rendering helpers without transport state. |
| Existing surface controllers | User interactions and calls into rendering/RPC helpers. |

Each module is a factory taking explicit dependencies and receives a *slice* of
the browser API, not the whole adapter. That is what makes §2.1 checkable rather
than merely asserted: only `background/prefs.js` is handed storage, so no other
part of the control plane can put observation data there even by mistake.
`test/background-structure.test.js` enforces the slice rule, the absence of
import cycles, the absence of a command switch in `sw.js`, and — extension-wide,
beyond the control plane — that nothing writes any storage key but the two known
preferences. Two surfaces outside the control plane still read storage directly
and deliberately: `content/hide.js` (hide mode, before first paint) and
`sidepanel/index.js` (consent, for its banner). Both read a user preference,
never an observation; see WO-083's recorded boundary adjustments.

No framework, dependency, bundler, TypeScript or generated code is introduced.
WO-080's tab-proof model lands before the split so the global is not merely
moved into a new file.

## 8. Implementation status and order

The decisions above are the current contract. WO-077/078 implemented the earlier
Level-1 capability boundary: fetch/pre-walk, Live gossip and whole word
telemetry on; block service/provider announcements and three-gram topics off.
WO-089 supersedes its Level-1 outbound decision: fetch/pre-walk and
word-statistics fetch remain on, while the entire Live capability and word
serve/local contribution move to Level 2+. Contribution changes already replace
the running node immediately with crash-safe stored/effective/startup state. WO-079 supplies the one
authenticated per-user owner and broadcasts terminal policy status across its
browser sessions. WO-084 replaced WO-077's mirror-only Level-2 source filter:
Level 2 now serves the union of locally derived and imported claims in each
broad bucket, blocks are preserved claims under per-neighbourhood identities at
schema 3 on `/keel/block/3.0.0`, and the announced graph/catalogue/shard/yield
sets are all computed from the corpus actually served.
WO-080 replaced the global page proof with a bounded, in-memory tab-keyed store;
same-platform tabs can no longer overwrite each other's panel seed. Extension/
daemon capability negotiation (WO-081) and `owner_ipc:1` are implemented.
WO-085 made distributed peer search reciprocal: `Policy.DistributedSearch` is a
capability of its own, on at Level 2+ alongside broad serving and gate-aware
like it, so a downgrade stops searches before teardown begins. `PEER_SEARCH`
refuses at Level 1 with `contribution_required` plus a structured detail, both
in the RPC handler and again inside `swarm.Node.PeerSearch`, so no caller
reaches a peer. The boundary is negotiated as `peer_search:2`; a client that
negotiates `1` is pre-WO-085 and leaves the control enabled, while the daemon
enforces regardless of the negotiated revision. Serving limits
(`daemon/swarm/limits.go`) bound concurrency, per-peer request rate and served
bytes on every serve path at every level, independently of contribution.
WO-088 made capability-gated controls stay visible and disabled rather than
vanishing. WO-083 split the service worker's control plane into the module
boundaries §7 now describes, with the structural rules enforced by test rather
than by convention. Every original architecture-review implementation gap is
closed. WO-082's final policy audit then exposed two release-boundary defects
now owned by WO-089: the default Level-1 node must not run Live, and the
independently starting daemon must prove the corrected initial disclosure
before beginning Level-1 network work. WO-089 removes Live from Level 1
entirely, makes word telemetry fetch-only there, and makes initial
recording/consumer-network consent enforceable by the daemon.
WO-090 corrected the final two full-page strings that implied Live still worked
at Level 1 and added DOM regression coverage. WO-082's closing Graphify pass
found no remaining current architecture, runtime or disclosure contradiction.

Senior-development order:

1. ~~WO-077 + WO-078 — Level-1 runtime boundary and disclosure contract.~~ Implemented.
2. ~~WO-079 — single daemon owner + native proxy.~~ Implemented, including
   WO-077 runtime-policy/status integration; Windows live QA remains.
3. ~~WO-084 — Level 2 serves broad buckets containing its own graph blocks.~~
   Implemented.
4. ~~WO-081 — negotiate extension/daemon compatibility.~~ Implemented
   (`owner_ipc:1` from WO-079; application HELLO/ACK + session gate).
5. ~~WO-080 — make proofs tab-scoped.~~ Implemented; multi-tab live QA remains.
6. ~~WO-085 — enforce reciprocal Level-2 distributed peer search.~~ Implemented,
   including the per-node serving limits, which are independent of level.
7. ~~WO-088 — keep unavailable capability controls visible and disabled.~~
   Implemented.
8. ~~WO-083 — split the extension control plane after state ownership is
   correct.~~ Implemented; `sw.js` is 373 lines of wiring with no command
   switch, and `test/background-structure.test.js` keeps it that way.
9. ~~WO-089 — move the whole Live capability and outbound word telemetry to
   Level 2+, retain Level-1 graph/seed/word consumption, and enforce the
   corrected initial consent before the daemon constructs Level 1.~~
   Implemented.
10. ~~WO-090 — correct the final stale Level-1 Live strings and add DOM
    regression coverage.~~ Implemented.
11. ~~WO-082 — close the final consistency audit after the runtime and
    user-facing disclosures agree.~~ Done 2026-08-12.

Do not publish or recruit external testers from a build whose displayed
contribution level differs from its effective graph-sharing policy, or whose
daemon cannot prove the current network-data consent revision.
