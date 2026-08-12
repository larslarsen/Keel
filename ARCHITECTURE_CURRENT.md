# Keel — current normative architecture

**Status:** Authoritative for current engineering decisions.
**Date:** 2026-08-11.
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

| Level | Peer network | Own observations sent | What the user receives |
|---|---|---|---|
| **1 — Strictly Personal** (default) | Full consumer: peer discovery, seed receipt, whole-bucket graph/catalogue/search-shard fetch and graph pre-walk. Also receives, relays and originates live gossip/snapshots, and exchanges the whole fixed-shape word-level HLL/CMS telemetry pack. It does not serve blocks, announce itself as their provider, or join three-gram yield/token-sketch topics. | Livestream notices plus aggregate word HLL/CMS telemetry. No raw words, recommendation edges, watch trail or stable application author. | The full product: peer search/suggestions, graph pre-walk, global word statistics and the shared live index. |
| **2 — Mirror** | Level 1 plus serving cached public blocks, provider announcements, and publication of three-gram availability/sketch telemetry for the blocks it serves. | Same live notices and word aggregates as Level 1. No recommendation edges or watch trail. | A warm shared cache; the contributed storage and bandwidth are also its local latency benefit. |
| **3 — Cohort** | Level 2 plus threshold-protected aggregate publication. | STAR-protected aggregate edge measurements; never raw trails. | Cohort comparison when built. |
| **4 — Transparency** | Level 3 plus attributed publication. | Explicitly public, attributed funnel records. | No gated reward. |

Levels 3 and 4 remain unimplemented. The UI must not allow selecting a level
whose pipeline does not exist.

Level 1 is deliberately a full consumer. Privacy is not a toll booth: receiving
the common seed, fetching whole prefix buckets and pre-walking the graph are
available without contributing the user's corpus or volunteering the local
cache. Those requests expose peer participation and coarse bucket interests as
described in the privacy policy; Level 1 is “personal,” not “offline.”

Level 1 has two narrow outbound data products: live notices, and the global
word-level telemetry pack. The latter is the WO-068 HLL/CMS fixed-shape pack,
served whole and without plaintext words, video ids, edges or queries. It is
display telemetry, not a block-discovery signal. It includes locally observed
titles so the global statistic covers the corpus; this aggregate disclosure must
be stated explicitly. Known word guesses can still be tested against a CMS, so
“no plaintext words” must not be described as zero disclosure.

The live network exists at Level 1 so the long-tail feed can both receive and
originate sightings. It does not carry recommendation edges or a watch trail.
“No author” is not a complete anonymity proof: live reports omit application
authorship and become indistinguishable after relaying, but a direct neighbour
may use connection topology and timing to infer an origin probabilistically.
Consent and privacy copy must state both Level-1 outbound products rather than
describe Level 1 as network-silent.

The shared live index remains whole-feed gossip with local search, no popularity
ranking, bounded memory and expiry. Its query privacy comes from holding the
whole index, not from a claim that publication has no metadata. Subscription and
relay are permanent at every contribution level, not tied to opening the Live
tab.

The following capabilities are independently selected; they are not inferred
from one generic “swarm enabled” boolean:

| Capability | Level 1 | Level 2+ |
|---|---:|---:|
| Peer discovery/connection | On | On |
| Live topic + live-snapshot stream | Receive, relay, originate, serve whole snapshot | Same |
| Seed receipt + graph/catalogue/search-shard fetch/pre-walk | On | On |
| Three-gram yield + token-sketch topics | Off | Receive/relay/originate for served mirrored blocks |
| Whole word HLL/CMS telemetry stream | Fetch and serve the fixed-shape aggregate, including local corpus | Same |
| Mirrored block serve + provider announcements | Off | On |
| Own edge publication | Off | Level 3+ only |

“Sketch” is not one policy category:

- `YieldTopic` and the gossiped three-gram `SketchTopic` exist to locate and
  size searchable blocks. Level 1 does not join them because it serves no
  blocks. Its fetcher treats missing yield/count data as unknown and still
  searches; Level 2 joins and originates them for the blocks it serves.
- `WordTelemetryProtocol` is the separate WO-068 display aggregate. Level 1
  fetches and answers the whole HLL/CMS pack, including local-corpus input.
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
  fetch/pre-walk, whole word-level HLL/CMS exchange, and live
  receive/relay/publish remain enabled in the Level-1 policy; three-gram topics
  are absent.

The current node construction binds stream handlers and pubsub topics at start,
so the selected implementation is a supervisor-controlled node replacement, not
mutation of a live node:

1. Build an explicit policy with separate `live`, `fetch`, `serve_mirrors`,
   `announce_providers`, `join_search_telemetry`,
   `exchange_word_telemetry` and `publish_own` capabilities.
2. For 2→1, close the old node's outbound permission gate, durably set stored
   and startup levels to 1, then stop/detach it and start the consumer-only node.
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

The owner is long-lived for the user session because Level 1's live gossip
relay—and Level 2's provider duties—cannot depend on a browser tab remaining open. Packaging may
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
  "required": { "core": 1 },
  "optional": { "selectors": 1, "tiktok": 1, "scroll_history": 1,
                "peer_search": 1, "word_stats": 1, "queue": 1,
                "contribution_runtime": 1 }
}
```

`HELLO_ACK` returns daemon version, selected API revision, compatibility status,
reason/code, and the exact negotiated capability map. Capability values are
positive integer schema revisions. The selected version for a capability is the
highest mutually supported revision; absence means unavailable.

`core:1` covers impression ingestion, stats, export/wipe and clean disconnected
behavior. Missing required capability or non-overlapping API range fails the
session closed: the extension renders “desktop app update required” and does not
send application RPCs. Optional UI is hidden/disabled based on negotiated
capabilities, not by attempting an RPC and interpreting “unknown type.”

Privacy/security behavior never silently downgrades. In particular, Level 2
controls require `contribution_runtime:1`; an older daemon may continue the
local core but the extension must not claim it can change effective networking.
Application semantic versions are diagnostic/update information, not the
compatibility algorithm.

The native proxy and daemon-owner IPC has its own required `owner_ipc:1`
handshake so a replaced proxy cannot feed incompatible frames to an old owner.

## 7. Extension module boundaries

Plain ES modules only. The service worker is the composition root and owns no
feature logic beyond wiring browser events to modules.

Target boundaries:

| Module | Owns |
|---|---|
| `lib/native.js` | Native bridge connection, HELLO negotiation, pending requests and reconnect. |
| `background/page_proofs.js` | Pure tab-keyed proof store and bounded lifecycle operations. No browser APIs. |
| `background/panel_context.js` | Active-tab/window lookup, panel enable/close policy and context broadcasts. |
| `background/prefs.js` | Browser-storage adapter for hide and observation-consent preferences only. |
| `background/rpc.js` | Validated extension-message dispatch and daemon capability gates. |
| `background/sw.js` | Instantiate modules; register listeners; connect dependencies. |
| `sidepanel/render.js`, `page/render.js` | Escaped DOM rendering helpers without transport state. |
| Existing surface controllers | User interactions and calls into rendering/RPC helpers. |

No framework, dependency, bundler, TypeScript or generated code is introduced.
WO-080's tab-proof model lands before the split so the global is not merely
moved into a new file.

## 8. Implementation status and order

The decisions above are normative but not yet all implemented. The existing
build now uses one authenticated per-user daemon owner with browser native-host
proxies (WO-079). It correctly starts live gossip at Level 1, but sets
`Fetch=false` and does not answer word telemetry there, so it is not yet the
full consumer specified above. It also joins the three-gram yield/token-sketch
topics at Level 1, starts the token-sketch publisher without a Level-2 gate,
applies contribution changes only after process restart, performs no
extension/daemon capability negotiation beyond required `owner_ipc:1`, and
holds one global page proof. Those differences are known violations, not an
alternative supported behavior.

Senior-development order:

1. WO-077 + WO-078 — preserve Level-1 consumption and live gossip, stop every
   Level-2+ outbound path on downgrade, and make contribution transitions
   effective at runtime.
2. ~~WO-079 — single daemon owner + native proxy.~~ Owner/proxy implemented;
   WO-077 supplies runtime-policy/status integration and Windows live QA remains.
3. WO-081 — negotiate extension/daemon compatibility (`owner_ipc:1` is done).
4. WO-080 — make proofs tab-scoped.
5. WO-083 — split the extension control plane after state ownership is correct.
6. WO-082 — final consistency audit and removal of temporary “pending” notes.

Do not publish or recruit external testers from a build whose displayed
contribution level differs from its effective graph-sharing policy.
