# Engineering handoff process

Design decisions are made in documents, not in conversation. A decision that exists only in a chat
log has not been made — it will drift out from under whoever is implementing it.

## Document roles

| Document | Role | Changes |
|---|---|---|
| `ARCHITECTURE_CURRENT.md` | Normative current architecture. What the system must be now. | Rarely, and never silently |
| `DESIGN_v2.md` | Architecture rationale and decision history. Why the system is shaped this way. | Preserve history; label superseded claims |
| `BUILD_P0.md` | Standing spec for the current phase. What "done" means. | Per phase |
| `handoff/WO-NNN-*.md` | **Work orders.** A specific, bounded change with rationale and acceptance criteria. | One per change |

## Rules

1. **No engineering decision is real until it is in a document.** If it was agreed in chat, it gets
   written into the design doc or a work order before anyone implements it.
2. **Work orders are addressed to a named implementer** and state exactly what to delete, keep, and
   add. They do not editorialize.
3. **A work order that changes standing docs says so**, and the standing docs are updated in the
   same commit.
4. **Rationale is mandatory.** An implementer who does not know *why* a constraint exists will route
   around it in good faith. Every constraint cites the section it comes from.
5. **Pushback is expected.** Work orders end with an explicit invitation to challenge. Some
   constraints are load-bearing, some are stale; the implementer cannot tell them apart without
   asking.

## Index

| WO | Title | Addressee | Status |
|---|---|---|---|
| 001 | P0 re-baseline: rename, daemon-first, permission minimisation | Sr Dev (Grok) | Done |
| 002 | Remove the retention sweep | Sr Dev (Grok) | Folded into 004 |
| 003 | P0 review findings | Sr Dev (Grok) | Folded into 004 |
| 004 | P0 fix list — single prioritised queue | Sr Dev (Grok) | Implemented — **live QA failed**, see 005 |
| 005 | Lockup `channel_id`; fixtures that were not captured | Sr Dev (Grok) | Done |
| 006 | CPU storm freezes the browser (tab-switch hard freeze) | Sr Dev (Grok) | Implemented, live-verified |
| 007 | Rail replacement reuses slot numbers; panel shows repeated numbers | Sr Dev (Grok) | Implemented, live-verified |
| 008 | Self-heal: reconnect daemon link and re-arm observers after silent death | Sr Dev (Grok) | Done — live-verified, ≤30 s gap |
| 009 | Stop showing recommendations (display only) | Sr Dev (Grok) | Done — live-verified |
| 010 | Collect suggestions everywhere, not just watch pages | Sr Dev (Grok) | Done — live-verified (616 HOME rows) |
| 011 | P0 acceptance audit | Sr Dev / Jr Dev | **Done — P0 closed** |
| 012 | Export and wipe (first P1 feature) | Sr Dev (Grok) | Done — live-verified |
| 013 | `channel_id` missing past the initial rail | Sr Dev (Grok) | Done — gap surfaced |
| 014 | SidePanel: suggestions first, rest collapsed | Sr Dev (Grok) | **Done** |
| 015 | Channel hard block | Sr Dev (Grok) | Done, then **superseded by 016** (page-level approach reversed) |
| 016 | Daemon owns blocking; channel backfill | Sr Dev (Grok) | Done — live-verified (unknown 42%→30%) |
| 017 | Panel: scroll, and hide control becomes an icon | Sr Dev (Grok) | Done |
| 018 | Funnel inspector — why was this recommended | Sr Dev (Grok) | Done — verified vs corpus |
| 019 | ~~Retention setting~~ | — | **Dropped** — wipe covers it; aggregate, don't delete |
| 020 | Installer — self-registering native host | Claude | Done |
| 021 | Panel only on YouTube tabs | Claude | Done |
| 022 | Local search + full-page surface | Claude | Done |
| 023 | Suggestion engine with entropy control | Claude | Done |
| 024 | Analysis tab | Claude | Done |
| 025 | Pre-publication sweep | Claude | Done |
| 039 | **Panel thumbnails + clickable titles** | Sr Dev (Grok) | **Done** |
| 040 | Panel auto-opens on full-page video link click; panel links navigate in place | Jr Dev (opencode) | **Done** |
| 041 | Side panel declutter — design owned by reviewer | Sr Dev (Grok) | **Mostly resolved — visual call outstanding** |
| 042 | Panel back navigation — browser Back skips `tabs.update` entries | Sr Dev (Grok) | **Fixed — needs live QA** |
| 043 | Hiding must follow the panel, not be permanent | Anyone | **Done** |
| 044 | Channel display name under panel thumbnails | Jr Dev (opencode) | **Done** |
| 045 | LIVE pill on panel thumbnails | Jr Dev (opencode) | **Done** |
| 046 | **Panel must show our suggestions, not YouTube's** | Anyone | **Done — pending live QA** |
| 047 | **Remove person-to-person sharing UX** | Anyone | **Done** |
| 048 | **Privacy policy** | Anyone | **Done** |
| 049 | **In-extension consent screen** | Anyone | **Done** |
| 050 | **Recapture fixtures logged out** | Jr Dev | **Done — LIVE via Portland Andy splice (see ticket)** |
| 051 | **Contribution level control** | Anyone | **Done** |
| 052 | **Level 2: catalogue sharing over the swarm** | Sr Dev | **Built — awaiting two-machine test** |
| 053 | Dependency-squatting / name-hijack audit | Engineer | **Done — toolchain pinned, CI reporting** |
| 054 | Live promotion ignored the 1-hour rule | Engineer | **Fixed** |
| 055 | Swarm status showed DHT noise as peers | Engineer | **Fixed** |
| 056 | Implement Option B (data-driven selectors) for YouTube — minimal first bite | Sr Dev | **Done** 2026-08-07 |
| 057 | TikTok surface + platform-scoped panel (depends on 056) | Sr Dev | **Built** 2026-08-08 — platform dimension, `selectors_tt.json`, platform-scoped panel, tiktok.com permissions all landed; selectors unverified against live TikTok (fixture is hand-authored, see WO-063) |
| 058 | Peer graph empty at v0.1.0: no seed, no auto peer data | Sr Dev | **Resolved** — copy + DESIGN_v2 fixed, no seed planned, relies on WO-059 self-healing |
| 059 | Distributed search over peer data via multi-peer superset fetch + local intersection (user-invented) | Sr Dev | **Phase 1+2 done** — tokenizer, shards, serve/fetch RPC, search UI; rest split to 067 |
| 060 | Protocol versioning for deterministic, node-agreeing constants (tokenizer k, bucket params) | Sr Dev | **Done** — key scheme versioned, carried in protocol ids |
| 061 | Version negotiation, compatibility policy, update UX (connect-if-compatible, warn/auto-update if behind) | Sr Dev | **Done** — identify-based version observation, compat policy, update notice |
| 062 | Testing strategy: fuzz + property + error-injection + regression, not review models | Sr Dev | **Done** — discovery proven, property tests, wire fuzzing, CI ratchet (80% floor not met; ratchet instead) |
| 063 | TikTok panel: the Mirror (no re-rank rails) | Sr Dev | **Built** — video_id via xgwrapper, hashtags/sound extract, SCROLL_HISTORY + panel history; dwell/engagement observers thin |
| 064 | Watch queue: add-to-queue button + daemon-persisted ordered queue + play/remove/reorder | Sr Dev (Opus) | **Done** |
| 065 | Refresh suggestions button (re-draw via SUGGEST, same entropy) | Sr Dev (Opus) | **Done** |
| 066 | Live detection false-positive: non-live video flagged LIVE (loose `liveLoose`/thumbnail matcher) | Sr Dev (Hermes) | **Resolved** |
| 067 | Distributed search: yield-gossip, global count, coverage UI, hardening (split from 059) | Sr Dev | **Done** |
| 068 | Global word-level corpus telemetry: space-delimited words + on-demand HLL/CMS pack fetch → distinct-word count, per-word % + nested char-token bars | Sr Dev (Opus) | **Done** |
| 069 | SUGGEST intermittently times out (8s native-bridge client cap vs synchronous graph walk on cold DB) | Sr Dev (Opus) | **Done** — defensive goroutine-offload + timeout guard landed; original "cold DB" root cause not confirmed by direct benchmarking (documented in ticket) |
| 070 | PEER_SEARCH times out on multi-word queries with no/empty peers (8s bridge cap; token-fetch stall) | Sr Dev (Opus) | **Done** — zero-peers fast path in `PeerSearch`, `peerSearchTimeout` cut to 6s, handler off the bridge thread |
| 071 | Panel not context-aware per platform: stale YT data on tab switch; TikTok shows YT counts | Sr Dev (Opus) | **Done** — panel gated to YT/TikTok watch pages via `tabs.onUpdated`/`action.onClicked`; TikTok-counts bug was `rememberPage` dropping `platform` on its rail-generation reset |
| 072 | Same channel name twice in "Channels seen most" (two channel_ids, not canonicalized) | Sr Dev (Opus) | **Open** |
| 073 | Panel must follow the ACTIVE tab's platform, not the last page any tab reported (context broadcasts + panel consumption; follow-on from 071 live QA) | Sr Dev (Opus) | **Done** 2026-08-11 — SW broadcasts active-tab context (`PANEL_CONTEXT`/`PANEL_CONTEXT_QUERY`, `closePanelInWindow` on leave, wired to onActivated/onUpdated/sweep); panel consumes it, seeds only same-platform proofs, guards absorb; 10 new tests, 87/87; needs live QA |
| 074 | TikTok's For-You feed must open the panel — WO-071's gate was YouTube-shaped (TikTok never navigates to `/@author/video/…`; FYP URL stays `/`) | Sr Dev (Opus) | **Done** 2026-08-11 — gate now platform-aware via `panelAllowedFor`: YT = WATCH_NEXT only (unchanged), TT = WATCH_NEXT + HOME(FYP); button on FYP opens panel, panel closes only leaving TikTok; 5 new tests, 92/92; needs live QA |
| 075 | Toolbar button is a toggle — closes an already-open panel (open() was a no-op, panel unclosable from toolbar) | Sr Dev (Opus) | **Done** 2026-08-11 — click handler toggles via `panelOpen()` port counter + `closePanelInWindow`; YT watch + TT FYP covered; 3 new tests, 95/95; needs live QA. Follow-up: per-window toggle via `PANEL_HANDSHAKE` (a panel in one window no longer dead-clicks another window) + 4 tests, 99/99, **live-verified** |
| 076 | Keel button dead on TikTok `/live`, `/explore`, `/following` — `surfaceFromUrl` maps only `/`, `/foryou`, `/@author/video`, `/@author/live` for tt; everything else is `surface:null` → panel gate closed, observer idle | Sr Dev (Opus) | **Open** — same treatment as WO-074's FYP: classify the three feeds as HOME; panel opens/closes there, WO-063 mirror arms; live-verify card extraction on each page (selector gap → WO-063) |
| 077 | Contribution-level changes must reconfigure the running swarm | Sr Dev (Claude Sonnet/Opus) | **Runtime transition done; policies partly superseded by 084/089** — gate-first replacement remains; WO-084 corrected Level-2 sources and WO-089 moves Level-1 Live plus outbound word telemetry to Level 2+ |
| 078 | Resolve the Level-1 outbound/privacy-contract contradiction | Sr Dev (Claude Sonnet/Opus) | **Historical — Level-1 Live/word decision superseded by WO-089**; its disclosure analysis and Live wire allow-list remain useful history |
| 079 | One local daemon owns SQLite and the swarm across browsers/profiles | Sr Dev (Codex) | **Implemented** 2026-08-11 — singleton owner/proxy plus WO-077 runtime-policy integration and cross-session status broadcast; automated tests pass, Windows live QA pending |
| 080 | Side-panel page proof must be tab-scoped, not extension-global | Jr Dev (opencode) | **Implemented** 2026-08-12 — pure `page_proofs.js` store keyed by sender tab; query/broadcasts carry `tab_id`/`window_id`; panel absorbs only the active tab's proof; BUG S2 race gone by construction; 20 new/updated tests, 119/119; needs live QA |
| 081 | Keel Bridge needs real compatibility negotiation | Sr Dev (Claude Sonnet/Opus) | **Done** 2026-08-12 — `HELLO` negotiates an API range and capability revisions; no application RPC before it succeeds; optional RPCs and UI controls gated on the negotiated map; owner broadcast hub joined only on negotiation (review find); `owner_ipc:1` from 079. Release/update UX in `DESIGN_v2.md` §8.1. Go + race + 103/103 extension tests pass |
| 082 | Reconcile the standing architecture and current work-order authority | Architect / Sr Dev | **Done** 2026-08-12 — normative architecture, runtime boundaries and user-facing disclosures reconciled; remaining wire/live checks stay on their implementation orders |
| 083 | Split the extension control plane at its existing responsibility boundaries | Sr Dev (Claude Opus) | **Done** 2026-08-12 — `sw.js` 1147→373 lines of wiring, no command switch, no feature state; new `background/rpc.js`, `background/panel_context.js`, `background/prefs.js` (the control plane's only storage owner) and `lib/render.js` + per-surface `render.js`. Each module is a factory taking a *slice* of the browser API. `test/background-structure.test.js` enforces no import cycles, the storage-owner rule and the absence of a switch in `sw.js`; `test/background-modules.test.js` unit-tests each owner without a browser; `test/sidepanel-smoke.test.js` closes a gap where nothing loaded the panel at all. Manifests, `content/` and `lib/native.js` untouched. 161/161 extension tests pass |
| 084 | Level 2 serves broad buckets containing its own graph blocks | Sr Dev (Claude Opus) | **Done** 2026-08-11 — capability split + `store.SourceSet` union; block schema 3 on `/keel/block/3.0.0` with per-neighbourhood claim identities preserved verbatim through relays; `peer_blocks`/`local_claims`; bucket envelope declares truncation and fails closed below the anonymity floor; announced graph/catalogue/shard/yield sets all derive from the served corpus. Go + race + fuzz + 100/100 extension tests pass; **two-machine network inspection pending** |
| 085 | Enforce reciprocal peer search and contribution-level incentives | Sr Dev (Claude Opus) | **Done** 2026-08-12 — `Policy.DistributedSearch` is its own gate-aware capability at Level 2+; `PEER_SEARCH` refuses at Level 1 with `contribution_required` in the handler *and* inside `swarm.Node.PeerSearch`, so no caller reaches a peer; negotiated as `peer_search:2` (a negotiated `1` means a pre-085 daemon, so the control stays enabled); the checkbox follows the effective level through the existing `CONTRIBUTION_STATUS` broadcast and offers a route to the setting; new `daemon/swarm/limits.go` bounds concurrency, per-peer rate and served bytes on every serve path at every level. Go + 126/126 extension tests pass |
| 086 | Show Level-2 contribution impact without retaining request history | Sr Dev (Claude Sonnet) | **Implemented — WO-092 correctness follow-up open**; privacy/UI shape accepted, but full-write accounting and database singleton/error behavior need correction |
| 087 | STAR cohort funnel comparison visualizer | Product design + Sr Dev (Claude Opus) | **Deferred until Level-3 STAR data exists** — do not design against an empty cohort |
| 088 | Capability-gated controls stay visible and disabled | Sr Dev (Claude Sonnet) | **Done** 2026-08-12 — `renderContributionRows()` shared between the negotiated and un-negotiated paths; missing `contribution_runtime` now renders all four rows disabled with a desktop-update reason instead of deleting them; 4 new DOM tests, `npm test` 130/130 |
| 089 | Observation-derived Live and word sharing start at Level 2 | Sr Dev (Claude Opus) | **Implemented** 2026-08-12 — Level 1 keeps graph/seed/word consumption but has no Live capability and serves no word pack; Level 2+ keeps full Live, local word contribution and broad local-plus-cached sharing; daemon-acknowledged revisioned consent required before any swarm node exists; old daemons fail HELLO closed on missing `network_consent:1`. **Two-machine wire inspection pending** |
| 090 | Remove stale copy claiming Live works at Level 1 | Sr Dev (Claude Sonnet) | **Done** 2026-08-12 — both full-page render paths corrected and covered by DOM regression tests |
| 091 | Windows installer writes the wrong native-host manifest for Brave | Sr Dev (Claude Opus) | **Implemented** 2026-08-12 — Chromium and Firefox manifests are separate files under `%LOCALAPPDATA%\Keel\{chromium,firefox}`, derived from `LOCALAPPDATA` and re-decoded after writing; all five Windows browsers register unconditionally (the output directory was never a browser-presence test); every `reg add` is read back and compared, and any missing key, wrong value, invalid manifest or absent executable exits non-zero; no-argument Windows dispatch installs while any browser launch still proxies; `install-report.txt` is written progressively beside the executable; extension discovery decoupled from host registration. New `daemon/install_registry.go`, `daemon/install_report.go` and 25 tests through an injected command runner — no Windows host needed. Go + race + `GOOS=windows` build + 174/174 extension tests pass. **Live QA on the affected machine pending** |
| 092 | Make contribution-impact counters truthful under write and database failures | Sr Dev (Claude Sonnet) | **Ready after WO-091** — full/short/error writes and atomic singleton/error propagation; no privacy-model change |
| 093 | A node that cannot join the network must say so, not count forever | Sr Dev | **Ready** — `keel_peers: 0` renders identically for "not announcing", "announced, nobody there" and "Level 1, structurally zero"; root cause of the first confirmed (announce races the DHT bootstrap, then retries in 6h); requires a stated fault state and verification against a real DHT |
