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
| 076 | Keel button dead on TikTok `/live`, `/explore`, `/following` | Sr Dev (Opus) | **Superseded before implementation by WO-098** — the proposed `HOME` shortcut erased distinct feed provenance and did not put `/live` cards into Keel Live |
| 077 | Contribution-level changes must reconfigure the running swarm | Sr Dev (Claude Sonnet/Opus) | **Runtime transition done; policies partly superseded by 084/089** — gate-first replacement remains; WO-084 corrected Level-2 sources and WO-089 moves Level-1 Live plus outbound word telemetry to Level 2+ |
| 078 | Resolve the Level-1 outbound/privacy-contract contradiction | Sr Dev (Claude Sonnet/Opus) | **Historical — Level-1 Live/word decision superseded by WO-089**; its disclosure analysis and Live wire allow-list remain useful history |
| 079 | One local daemon owns SQLite and the swarm across browsers/profiles | Sr Dev (Codex) | **Implemented** 2026-08-11 — singleton owner/proxy plus WO-077 runtime-policy integration and cross-session status broadcast; automated tests pass, Windows live QA pending |
| 080 | Side-panel page proof must be tab-scoped, not extension-global | Jr Dev (opencode) | **Implemented** 2026-08-12 — pure `page_proofs.js` store keyed by sender tab; query/broadcasts carry `tab_id`/`window_id`; panel absorbs only the active tab's proof; BUG S2 race gone by construction; 20 new/updated tests, 119/119; needs live QA |
| 081 | Keel Bridge needs real compatibility negotiation | Sr Dev (Claude Sonnet/Opus) | **Done** 2026-08-12 — `HELLO` negotiates an API range and capability revisions; no application RPC before it succeeds; optional RPCs and UI controls gated on the negotiated map; owner broadcast hub joined only on negotiation (review find); `owner_ipc:1` from 079. Release/update UX in `DESIGN_v2.md` §8.1. Go + race + 103/103 extension tests pass |
| 082 | Reconcile the standing architecture and current work-order authority | Architect / Sr Dev | **Done** 2026-08-12 — normative architecture, runtime boundaries and user-facing disclosures reconciled; remaining wire/live checks stay on their implementation orders |
| 083 | Split the extension control plane at its existing responsibility boundaries | Sr Dev (Claude Opus) | **Done** 2026-08-12 — `sw.js` 1147→373 lines of wiring, no command switch, no feature state; new `background/rpc.js`, `background/panel_context.js`, `background/prefs.js` (the control plane's only storage owner) and `lib/render.js` + per-surface `render.js`. Each module is a factory taking a *slice* of the browser API. `test/background-structure.test.js` enforces no import cycles, the storage-owner rule and the absence of a switch in `sw.js`; `test/background-modules.test.js` unit-tests each owner without a browser; `test/sidepanel-smoke.test.js` closes a gap where nothing loaded the panel at all. Manifests, `content/` and `lib/native.js` untouched. 161/161 extension tests pass |
| 084 | Level 2 serves broad buckets containing its own graph blocks | Sr Dev (Claude Opus) | **Done** 2026-08-11 — capability split + `store.SourceSet` union; block schema 3 on `/keel/block/3.0.0` with per-neighbourhood claim identities preserved verbatim through relays; `peer_blocks`/`local_claims`; bucket envelope declares truncation and fails closed below the anonymity floor; announced graph/catalogue/shard/yield sets all derive from the served corpus. Go + race + fuzz + 100/100 extension tests pass; **two-machine network inspection pending** |
| 085 | Enforce reciprocal peer search and contribution-level incentives | Sr Dev (Claude Opus) | **Done** 2026-08-12 — `Policy.DistributedSearch` is its own gate-aware capability at Level 2+; `PEER_SEARCH` refuses at Level 1 with `contribution_required` in the handler *and* inside `swarm.Node.PeerSearch`, so no caller reaches a peer; negotiated as `peer_search:2` (a negotiated `1` means a pre-085 daemon, so the control stays enabled); the checkbox follows the effective level through the existing `CONTRIBUTION_STATUS` broadcast and offers a route to the setting; new `daemon/swarm/limits.go` bounds concurrency, per-peer rate and served bytes on every serve path at every level. Go + 126/126 extension tests pass |
| 086 | Show Level-2 contribution impact without retaining request history | Sr Dev (Claude Sonnet) | **Implemented** — privacy/UI shape accepted; accounting correctness closed by accepted WO-092 |
| 087 | STAR cohort funnel comparison visualizer | Product design + Sr Dev (Claude Opus) | **Deferred until Level-3 STAR data exists** — do not design against an empty cohort |
| 088 | Capability-gated controls stay visible and disabled | Sr Dev (Claude Sonnet) | **Done** 2026-08-12 — `renderContributionRows()` shared between the negotiated and un-negotiated paths; missing `contribution_runtime` now renders all four rows disabled with a desktop-update reason instead of deleting them; 4 new DOM tests, `npm test` 130/130 |
| 089 | Observation-derived Live and word sharing start at Level 2 | Sr Dev (Claude Opus) | **Implemented** 2026-08-12 — Level 1 keeps graph/seed/word consumption but has no Live capability and serves no word pack; Level 2+ keeps full Live, local word contribution and broad local-plus-cached sharing; daemon-acknowledged revisioned consent required before any swarm node exists; old daemons fail HELLO closed on missing `network_consent:1`. **Two-machine wire inspection pending** |
| 090 | Remove stale copy claiming Live works at Level 1 | Sr Dev (Claude Sonnet) | **Done** 2026-08-12 — both full-page render paths corrected and covered by DOM regression tests |
| 091 | Windows installer writes the wrong native-host manifest for Brave | Sr Dev (Claude Opus) | **Done** 2026-08-13 — live-verified plus a `windows-latest` CI job that checks the registry independently of Keel. Every defect it named was real; none was the blocker, which was `os.NewFile` on a synchronous named pipe (see the order's post-mortem) — Chromium and Firefox manifests are separate files under `%LOCALAPPDATA%\Keel\{chromium,firefox}`, derived from `LOCALAPPDATA` and re-decoded after writing; all five Windows browsers register unconditionally (the output directory was never a browser-presence test); every `reg add` is read back and compared, and any missing key, wrong value, invalid manifest or absent executable exits non-zero; no-argument Windows dispatch installs while any browser launch still proxies; `install-report.txt` is written progressively beside the executable; extension discovery decoupled from host registration. New `daemon/install_registry.go`, `daemon/install_report.go` and 25 tests through an injected command runner — no Windows host needed. Go + race + `GOOS=windows` build + 174/174 extension tests pass. **Live QA on the affected machine pending** |
| 092 | Make contribution-impact counters truthful under write and database failures | Sr Dev (Grok 4.6 Extra High) | **Accepted** 2026-08-13 — singleton/upsert/error propagation and all full-write paths verified; paged replies count only after a fully written signed terminal and never count the budget sentinel; committed as `8ff4dd7` |
| 093 | A node that cannot join the network must say so, not count forever | Sr Dev (Grok 4.6 Extra High) | **Code accepted; public-DHT gate partially complete** — WO-109 truthfulness corrections accepted; a live first-attempt timeout was shown as `retrying` and automatically recovered to shared-key `ready` without restart; current-binary two-machine discovery and controlled three-failure `fault` recovery remain |
| 094 | Nodes must be able to find each other, not only find content | Sr Dev | **Live-verified** 2026-08-13 — two machines (Linux + Windows), no shared corpus, connected within minutes; `keel_peers: 1` both sides and the live index crossed the link (21 streams). Originally — rendezvous key derived from the protocol identity, published with the first announce and looked up on a timer; gated with announcing so Level 1 neither advertises nor searches. Verified publishing on a live DHT; **two-node connection still unverified** |
| 095 | Responsive streaming distributed search and UI | Sr Dev (Claude Opus) | **Accepted and live-verified 2026-08-14** — the first two-machine run failed at 0 results after 484 s; WO-111 repaired connected-peer ordering and the corrected rerun returned network results on both machines with incremental bars |
| 096 | Multi-token peer search starvation investigation | — | **Superseded before implementation by WO-097 + WO-095** — live six-second shared-deadline diagnosis retained; foundation and responsive delivery are now separate orders |
| 097 | Complete distributed-search index, pagination, and retained word targets | Sr Dev (Claude Opus) | **Done** 2026-08-13 — new `daemon/store/queryplan.go` owns scheme-2 normalization, the continuous query grid, every-alignment title windows, the stopword-*occurrence* filter and the full-query matcher (any-order words, quoted phrases, normalized boundaries); `tokenize()` is gone and its six call sites re-pointed. `KeySchemeVersion` 1→2, with scheme-1's outputs preserved as a literal record and `TestSchemeTwoDoesNotReproduceSchemeOneTokens` asserting the fence is earned; yield/sketch topics gained `ks` names, shard/catalogue protocols went to 2.0.0. New `daemon/store/paging.go` + `daemon/swarm/paging.go` replace both 4,096-row truncations with header/signed-page/authenticated-terminal logical responses over one broad bucket, nonce-rotated traversal, and explicit `incomplete`. New `daemon/store/wordsnapshot.go` retains one refresh *round* (never a cumulative merge — CMS addition is not idempotent) with duplication-factor overlap adjustment and instant frozen targets carrying known/unknown and age. `swarm.VersionView` now reports `key_scheme`. Go + race + 201/201 extension tests pass. **Rollout partitions the swarm — see `ROADMAP.md`; live QA needs both machines on the new build** |
| 098 | Capture TikTok Explore, Following, and Live discovery correctly | Sr Dev (GPT-5.6 Terra, High) | **Code accepted 2026-08-13 — interactive QA moved to WO-103**; real cached `/live` DOM yields 4/4 valid sightings, consent/queue/Live-v2 boundaries are covered, and extension + Go + race suites pass; Explore, logged-in Following and real room states remain explicitly unverified |
| 099 | Close streaming-search lifecycle and resource-accounting gaps | Sr Dev (Claude Opus) | **Accepted** 2026-08-13 — page/session isolation, early-event preservation and downgrade cancellation landed; follow-ups 100–102 closed the reader, resolution, retirement and stop-cause boundaries |
| 100 | Finish search-budget and resolution atomicity | Sr Dev (Claude Opus) | **Accepted** 2026-08-13 — read-time aggregate ceiling, typed catalogue completion, job-local prefix memoization, shared-candidate barrier and identity-safe retirement verified with follow-ups 101–102 |
| 101 | Close distributed-search termination semantics | Sr Dev (Claude Opus) | **Accepted** 2026-08-13 — lease/spend accounting, candidate dispositions, neutral unresolved saturation and exact stop-all retirement verified; final cause propagation closed by WO-102 |
| 102 | Preserve distributed-search stop causes end to end | Sr Dev (Claude Opus) | **Accepted; two-machine stack live-verified through WO-111** — budget termination survives the real catalogue path, local failures remain retryable, cancelled waiters cannot lease refunded capacity, and malformed versus silent peers remain distinct |
| 103 | Verify TikTok Explore, Following, and Live-room DOM interactively | Lars + reviewer | **Review complete** — Explore plus active/inactive room captures accepted; observed `/following` is a creator wall and emits nothing; implementation split to WO-104 |
| 104 | Repair TikTok Explore and Live-room extraction from interactive evidence | Sr Dev (Grok 4.6) | **Accepted** 2026-08-13 — Explore author/title provenance, route/header/player agreement, late player hydration, room-badge isolation and `live_sightings:2` compatibility independently verified; committed as `0f16d9c` |
| 105 | A stale generated manifest must not disable the content observer | Sr Dev | **Accepted** 2026-08-14 — installer refreshes Chromium drift; Chromium/Firefox suites pass; Brave loads the corrected path and a new TikTok observer armed, proving the `live_policy.js` module graph loaded; remaining TikTok selector failure is WO-106 |
| 106 | The selector router must not turn TikTok into YouTube | Sr Dev (Grok 4.6 High) | **Accepted** 2026-08-14 — sender-derived `GET_SELECTORS` serves `tt` on Explore; live corpus grew `tt`/`EXPLORE`; unrelated live-QA edits moved to 107/108 |
| 107 | TikTok must never fall back to YouTube selectors | Sr Dev (Grok 4.6 High / GPT-5.6 Terra High) | **Code accepted** 2026-08-14 — platform-correct bundle, cross-platform rejection and manifest closure verified; daemon-unavailable live QA pending |
| 108 | Separate WO-106 from unrelated live-QA changes | Sr Dev (GPT-5.6 Luna High) | **Accepted** 2026-08-14 — truthful Counts corrections covered by real DOM paths; unproved startup rearm removed |
| 109 | Network status must not invent provenance or discoverability | Sr Dev (GPT-5.6 Luna High) | **Accepted** 2026-08-14 — Live-index wording is provenance-neutral under every health payload; bulk content publication no longer claims node discoverability; full extension, Go, race, vet and diff checks pass |
| 110 | Stale consent must not report success and re-prompt forever | Sr Dev (Claude Opus or GPT-5.6 Terra High) | **Accepted and live-verified** 2026-08-14 — `Store.GrantNetworkConsent` now refuses any affirmative revision below the daemon's required revision without writing the meta rows, resuming, or replacing the node; the extension checks the daemon's returned `network_consent.current` before writing local recording consent or broadcasting `CONSENT_CHANGED`, even on a non-ERROR reply; the consent screen names the version mismatch and says another click will not fix it. New store/bridge/router tests plus full Go, race, vet and 291/291 extension suites pass. Stale rev-1 consent page (old extension) claims success but observation never starts against the same rev-2-requiring daemon — gate stays closed; the rev-2 page in the updated extension granted normally |
| 111 | Connected Keel peers must not wait behind DHT discovery | Sr Dev (GPT-5.6 Sol xhigh) | **Accepted and live-verified** 2026-08-14 — the corrected release returned search results in both directions while public-DHT publication was degraded; connected exact-protocol peers no longer wait behind DHT discovery |
| 112 | The Live index must converge across late peer connections | Sr Dev (GPT-5.6 Sol xhigh) | **Implemented** 2026-08-14 — wire timestamps are normalized together before validation; lifetime once-per-connection exact-protocol snapshots replace the first-minute/global-latch backfill, with reconnect and bounded retry behavior covered; Go, race, vet and extension suites pass; two-machine count rerun pending |
| 113 | Rendezvous must survive ephemeral identity churn | Sr Dev (GPT-5.6 Sol High) | **Implemented** 2026-08-14 — an eight-connection round now scans up to 32 provider candidates so dead identities from daemon restarts cannot fill the DHT result page; verified remembered peers cover a quiet DHT walk without changing discoverability semantics; automated acceptance passes; two-machine rerun pending |
