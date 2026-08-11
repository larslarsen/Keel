# WO-068 — Global word-level corpus telemetry (word tokenizer + on-demand HLL)

**Addressee:** Sr Dev (Opus)
**Status:** **Done** (2026-08-11)
**Date:** 2026-08-10 (revised 2026-08-10 after k clarification; transport 2026-08-11)
**Source:** Lars, 2026-08-10 — wants global stats that demonstrate what the
observed graph (the swarm's view of YouTube) contains: how big it is, what words
appear, and a per-word percentage bar in the search UI. NOT a search/fetch axis.

## Correction (2026-08-10)

The character tokenizer is `ShardK = 3` CHARACTER n-grams over `TokenDictAlphabet`
(letters + space, keyscheme.go). A "token" is a 3-character string, NOT a word or
phrase. There is therefore NO fixed word dictionary and NO "k=1" setting that
produces words — k=1 would be single characters, still not words. Word counts
require a SEPARATE word tokenizer (split on spaces), not a k of the character
scheme. This ticket is rebuilt on that understanding. The earlier "k=1 fixed
dictionary" and "dictionary coverage %" framing is withdrawn.

## What this is

A read-only, privacy-safe **global cardinality telemetry** layer. It answers
"what does the corpus contain?" not "fetch me graphs for word X."

Two outputs:
1. **Global distinct-word count** — "the swarm has seen N distinct words." The
   "how big is it" headline. NO coverage-% metric (there is no fixed word
   dictionary to be a denominator — words are observed, not enumerated).
2. **Per-word global percentage** — for a given word, what fraction of all
   observed graphs contain it. Drives a per-word percentage bar in the search UI
   (e.g. "trading — 12% of observed graphs").

## Design (must hold) — as built

- **Word tokenizer is space-delimited, NOT a fixed dictionary.**
  `store.NormalizeWord` / `WordsFromText` / `QueryWords` — letters-only lowercase,
  same letter runs as title `splitWords`. Vocabulary emerges from observation.
- **TRANSPORT: direct on-demand fetch, NOT gossip.**
  `WordTelemetryProtocol` (`/keel/word-telemetry/1.0.0/ks…`). UI opens → daemon
  dials connected peers → each returns a ~pack of HLL registers + Count-Min
  counters (no word strings) → local merge. Cost only when the UI asks.
- **No plaintext words on the wire.** `WordTelemetry` carries `word_registers`,
  `graph_registers`, `freq` only. Nodes agree on hash + normalization.
- **Per-word %** = CMS(graphs containing w) / HLL(graphs total), clamped to 100%.
  CMS merge is saturating **sum** across peers (disjoint catalogues); HLL is
  register-max union.
- **Stopwords** (`WordStopwords` in `words.go`) — display filter only; still
  counted locally.
- **TELEMETRY ONLY.** No word bucket fetch/serve. Storage keys stay k=3 tokens.
- **Poisoning:** multi-peer **median filter** on distinct-word cardinality before
  merge (`medianFilterWordPacks`). No signing (display-only). Token layer stays
  signed (WO-067).

## UI (search page) — two-tier nested bars

- **Top tier:** per non-stopword query word — `~X% of observed graphs`.
- **Bottom tier:** nested sub-bars per `CharTokensForWord` (existing `tokenize` on
  the isolated word → `" w "` windows). Color by opaque `token_index` (same
  palette as WO-067). May show unknown/indeterminate when no gossiped token
  sketch yet. Token estimates come from local `TokenEstimate` (push-gossip
  WO-067), not the word-stat fetch.
- **Inline token coloring in the word itself (Lars, 2026-08-11):** render the
  searched word with its constituent 3-gram tokens color-coded to match the
  bottom-tier sub-bars — each character-span of the word tinted by the
  `token_index` color of the token it belongs to, so the word visually shows
  which token each part maps to. Alignment is a BEST GUESS: the character n-gram
  windows overlap (e.g. "trading" → " tra","tra","rad","adi","din","ing"), so a
  character can belong to up to ShardK windows. Use the existing `tokenize` output
  to derive the spans; when a character sits in multiple tokens, prefer the
  left-most / first token's color (deterministic, not random) so the coloring is
  stable across renders. The color must match the sub-bar color for the same
  `token_index` — the word and its sub-bars share the palette keyed by token_index.
- **Separate** from `renderPeerProgress` (query-scoped PEER_SEARCH coverage).

## What was built (file map)

| Area | Path |
|---|---|
| Word normalize, CMS, packs | `daemon/store/words.go` |
| Direct fetch + median | `daemon/swarm/words.go` |
| RPC | `WORD_STATS` / `WORD_STATS_RESULT` in `bridge/protocol.go`, `main.go` |
| SW relay | `extension/background/sw.js` |
| Two-tier UI | `extension/page/{index.html,index.js,style.css}` |

## Acceptance

- [x] Word tokenizer: space-delimited, shared normalization, no fixed dictionary.
- [x] Local word HLL + graph HLL + CMS; on-demand peer fetch; merge locally — not gossiped.
- [x] Global distinct-word count in WORD_STATS_RESULT (no coverage-% over a dict).
- [x] Per-word global % as estimate in UI.
- [x] Two-tier bars: word % + char-token sub-bars; read-only.
- [x] Word→char-token from `CharTokensForWord` / `tokenize`, not a dictionary.
- [x] Poisoning: multi-peer median on distinct-word estimate.
- [x] Stopwords excluded from displayed word rows; empty swarm → no fabricated %.
- [x] Regression: two in-process nodes direct-fetch and merge (`TestWordTelemetryDirectFetch`).
- [x] No word bucket fetch/serve path.
