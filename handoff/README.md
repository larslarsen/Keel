# Engineering handoff process

Design decisions are made in documents, not in conversation. A decision that exists only in a chat
log has not been made — it will drift out from under whoever is implementing it.

## Document roles

| Document | Role | Changes |
|---|---|---|
| `DESIGN_v2.md` | Standing architecture. Why the system is shaped this way. | Rarely, and never silently |
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
| 056 | Implement Option B (data-driven selectors) for YouTube — minimal first bite | Sr Dev | **Open** |
| 057 | TikTok surface + platform-scoped panel (depends on 056) | Sr Dev | **Open** (blocked on 056) |
| 058 | Peer graph empty at v0.1.0: no seed, no auto peer data | Sr Dev | **Open** |
| 059 | Distributed search over peer data via multi-peer superset fetch + local intersection (user-invented) | Sr Dev | **Open** (proposal) |
| 060 | Protocol versioning for deterministic, node-agreeing constants (tokenizer k, bucket params) | Sr Dev | **Open** |
| 061 | Version negotiation, compatibility policy, update UX (connect-if-compatible, warn/auto-update if behind) | Sr Dev | **Open** |
| 062 | Testing strategy: fuzz + property + error-injection + regression, not review models | Sr Dev | **Open** |
| 064 | Watch queue: add-to-queue button + daemon-persisted ordered queue + play/remove/reorder | Sr Dev (Opus) | **Open** |
| 065 | Refresh suggestions button (re-draw via SUGGEST, same entropy) | Sr Dev (Opus) | **Open** |
| 066 | Live detection false-positive: non-live video flagged LIVE (loose `liveLoose`/thumbnail matcher) | Sr Dev (Hermes) | **Resolved** |
