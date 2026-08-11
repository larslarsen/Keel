# WO-068 — Global word-level corpus telemetry (k=1 dictionary + periodic HLL)

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Date:** 2026-08-10
**Source:** Lars, 2026-08-10 — wants global stats that demonstrate what the
observed graph (the swarm's view of YouTube) contains: how big it is, what words
appear, and a per-word percentage bar in the search UI. NOT a search/fetch axis.

## What this is

A read-only, privacy-safe **global cardinality telemetry** layer, separate from
WO-059's k=3 token search. It answers "what does the corpus contain?" not "fetch
me graphs for word X."

Concretely, two outputs:
1. **Global distinct-word count + dictionary coverage %** — "the swarm has seen
   N distinct words; M% of the k=1 dictionary is populated." The "how big is it"
   headline.
2. **Per-word global percentage** — for a given word, what fraction of all
   observed graphs contain it. Drives a **per-word percentage bar in the search
   UI** (e.g. searching "trading" shows "trading — 12% of observed graphs").

## Design (must hold)

- **k=1 dictionary is TELEMETRY ONLY.** Nodes do NOT fetch or serve k=1 token
  buckets. Keel's storage/fetch keys stay at k=3 (WO-059). k=1 exists solely to
  produce the global word stats above. No search path queries k=1.
- **Fixed shared dictionary, versioned (WO-060).** The k=1 word list AND its
  stopword exclusions are protocol constants — compiled in, identical on every
  node, bumped with the protocol version. Nodes must agree on the vocabulary or
  the global percentage is not comparable across peers (silent partition of the
  stat, like WO-060's key-deriving constants). Folded into WO-060's constant list.
- **HLL sketch, periodic, gossip-merged.** Each node maintains a local HLL of the
  distinct words it has observed. On a timer (bounded-fanout rebroadcast, same
  family as the yield-gossip invention) it merges peers' HLLs into a swarm-wide
  estimate. HLL merge reveals only combined cardinality — no individual video,
  no raw observation, no "was word X in your graph." Fits the design rule
  (live.go:13: "gossip what is small and ephemeral, fetch what is large") and
  violates no no-raw-data rule. Reuses Opus's existing sketch code.
- **Per-word percentage** is derived from the global word HLL + the global
  distinct-graph count (also an HLL, or the existing graph cardinality): for word
  w, percentage = (distinct graphs containing w) / (distinct graphs total).
  distinct-graphs-containing-w comes from the word HLL; distinct-graphs-total
  from a graph-level HLL. Both merged the same way. Approximate (~1-2% HLL error)
  — label it as an estimate in the UI, never a precise count.
- **Stopword filtering for display.** Common stopwords ("the", "video", "live")
  dominate raw word counts and make the headline stat meaningless ("the: 900,000").
  The k=1 dictionary's stopword exclusion list (a protocol constant, WO-060) keeps
  these out of the displayed top-words and out of the "populated %" denominator.
  Stopwords are still observed locally; they are just excluded from the published
  global stat.

## UI (search page) — two-tier nested percentage bars

The search UI breaks a query into WORDS and shows global-corpus percentages,
loading progressively as swarm data arrives.

- **Top tier: one percentage bar per WORD in the query.** Each word bar shows its
  global frequency: "trading — X% of all observed graphs" (from the k=1 global
  word HLL). Fills toward the global distinct-word count as data gossiped in.
- **Bottom tier: nested sub-bars per k=3 TOKEN the word participates in.** Under
  each word bar, show one smaller bar for every token that word belongs to
  (e.g. "trading" → "day trading strategy", "trading strategy live",
  "best trading strategy"), color-coded by token so membership is visible. Each
  token sub-bar shows that token's global coverage loading up — toward (or past)
  its target global token count (from WO-067's gossiped token-sketch / yield
  estimate). All bars load in parallel as the swarm answers.
- **Color coding = token identity.** Tokens get a fixed color (CVD-safe palette,
  like WO-067's `PEER_PROGRESS_COLORS`); a word's sub-bars are colored by their
  token so the user sees which tokens a word spans. Do NOT label sub-bars with
  the token text in the UI — color carries token identity, same privacy stance as
  WO-067 (the bar must not be readable back into query structure).
- **Progressive load.** Word % and token sub-bars fill concurrently as the daemon
  streams global estimates. A sub-bar may exceed its target (token seen in more
  graphs than the current estimate assumed) — render that as "past target",
  not clamped silently.
- **Telemetry only.** All values are global-corpus estimates (k=1 word HLL +
  k=3 token sketch). This UI does NOT trigger a k=1 or k=3 bucket fetch; it is a
  read-only view of what the swarm has observed. Distinct from WO-067's
  `renderPeerProgress` per-query coverage bar (that one shows how much of a
  specific search's buckets this node has fetched — query-scoped, not corpus-wide).
  The two bars coexist in the search UI but consume different data; keep them
  separate so they are not conflated.
- Search UI lives in `extension/page/index.js` (search page) — Opus extends the
  existing `renderPeerProgress` area or adds a sibling renderer. Consumes
  swarm-status fields: global word % (k=1 HLL) + global token coverage (k=3 sketch).
- Show "~" / "est." labels to signal HLL approximation.

## Data the UI consumes (must be exposed by the daemon)

- `global_word_pct[word]` — from k=1 word HLL ÷ graph HLL (WO-068 telemetry).
- `global_token_coverage[token]` + `token_target` — from k=3 token sketch (WO-067
  gossiped estimate). The word→token membership list comes from the shared
  tokenizer (k=1 dict knows which tokens each word appears in; WO-060 constant).

## Depends on

- WO-060 — k=1 dictionary + stopword list are versioned constants.
- WO-058 — until the swarm graph is populated (discovery + observation), the
  global HLL reads ~0 / empty. The machinery is buildable now; the numbers mean
  nothing until WO-058/WO-059 land. Do NOT fake a non-zero stat on an empty swarm.
- Opus's existing sketch code (HLL) — extend, don't reimplement.

## Acceptance

- [ ] k=1 word dictionary + stopword list are compile-time constants (WO-060),
      identical across builds, version-bumped on change.
- [ ] Local word HLL maintained per node; merged with peers on a timer via
      bounded-fanout rebroadcast; merge reveals cardinality only (no raw data).
- [ ] Global distinct-word count + dictionary coverage % exposed in swarm status.
- [ ] Per-word global percentage computed from word HLL ÷ graph HLL; shown as an
      estimate in the UI.
- [ ] Search UI shows a two-tier nested bar for the entered query: top tier one
      percentage bar per WORD (global word % from k=1 HLL); bottom tier under each
      word, one color-coded sub-bar per k=3 TOKEN the word belongs to (global token
      coverage from WO-067 sketch). Bars load progressively in parallel. Read-only,
      triggers no k=1/k=3 fetch.
- [ ] Word→token membership (which tokens a word spans) derived from the shared
      tokenizer (WO-060 constant); sub-bars color-coded by token, not labeled with
      token text.
- [ ] Token sub-bar may exceed its target (rendered "past target", not clamped).
- [ ] WO-067's per-query `renderPeerProgress` coverage bar and this global
      word/token telemetry bar are distinct renderers consuming distinct data;
      not conflated in the UI.
- [ ] Stopwords excluded from displayed top-words and the coverage denominator.
- [ ] On an empty swarm (WO-058 not populated) the stat reads ~0 / "no data yet";
      no fabricated counts.
- [ ] Regression test: two in-process nodes merge word HLLs and converge on the
      same distinct-word estimate (extends WO-062 multi-node test).
- [ ] No k=1 bucket fetch/serve path exists (telemetry only — verify by code
      review; searching a word does not dial a k=1 bucket).
