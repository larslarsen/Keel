# WO-068 — Global word-level corpus telemetry (word tokenizer + periodic HLL)

**Addressee:** Sr Dev (Opus)
**Status:** Open
**Date:** 2026-08-10 (revised 2026-08-10 after k clarification)
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

## Design (must hold)

- **Word tokenizer is space-delimited, NOT a fixed dictionary.** Count whatever
  words appear in the observed corpus. No pre-enumerated word list, no fixed
  vocabulary. The ONLY thing nodes must agree on is the NORMALIZATION RULE
  (lowercase? strip punctuation? letters-only or include digits? split on
  whitespace) — a normalization constant (WO-060), not a vocabulary. As long as
  every node normalizes words identically, the HLL merge is comparable. The word
  set emerges from observation. (Contrast with the character scheme's fixed
  `TokenDictSize = 27^3` dictionary, which exists only because the yield vector
  needs predetermined bit slots — irrelevant here, no yield vector for words.)
- **No fixed word dictionary, and no plaintext words on the wire — by design.**
  The HLL is CONTENT-ADDRESSED BY HASH: each observed word is normalized
  (shared rule, WO-060) and hashed locally into register positions; the word
  string is consumed and never leaves the node. What gossips is the 256-byte
  register array (`R []byte`, `P=8` → 2^8=256 registers, 1 byte each — same
  shape as store.TokenSketch), never the word "trading". Nodes agree on register
  positions because they agree on the HASH FUNCTION + normalization rule, not on
  a vocabulary. Merge = HLL max-register union (sketch_store MergeTokenSketch
  pattern); neither side sends word strings. This is why no fixed word
  dictionary is needed: the character scheme's dictionary exists only to map
  tokens → fixed yield-vector slots, which words don't use.
- **Gossip cost is trivial via ONE merged global word-HLL, not per-word.**
  Gossip a single per-node word-HLL (~256B) on a slow timer (e.g. once/min),
  not one sketch per word. Merging N peers' single HLLs yields the global
  distinct-word estimate in one round. Contrast per-word sketches (256B × N_words,
  throttled to 20/tick in the token scheme — slow to converge over a huge
  vocabulary). The merged-HLL approach is ~256B/min — negligible, and the
  minimal-leak option (registers only, no word strings). Aggregate
  distinct-word count + per-word frequency ARE the feature and are intentionally
  visible to peers; the design hides only which graphs / who observed what.
- **Per-word percentage** = (distinct graphs containing w) / (distinct graphs
  total). distinct-graphs-containing-w from the word HLL; distinct-graphs-total
  from a graph-level HLL. Approximate (~1-2% HLL error) — label as estimate.
- **Stopword filtering for display only.** Common stopwords ("the", "video",
  "live") dominate raw counts ("the: 900,000"). Filter at display time from the
  normalized word stream (a normalization/stopword rule, WO-060) — NOT a fixed
  dictionary. Stopwords still observed locally; just excluded from the published
  stat and the shown top-words.
- **TELEMETRY ONLY.** No word bucket fetch/serve. Keel's storage/fetch keys stay
  at k=3 character tokens (WO-059). This layer never triggers a fetch.

## UI (search page) — two-tier nested percentage bars

The search UI breaks a query into WORDS and shows global-corpus percentages,
loading progressively as swarm data arrives.

- **Top tier: one percentage bar per WORD in the query.** Each word bar shows its
  global frequency (from the global word HLL). Fills as data gossiped in.
- **Bottom tier: nested sub-bars per ShardK CHARACTER-TOKEN the word contains.**
  A word ("trading") is sliced by the EXISTING character tokenizer into its 3-char
  n-grams (" tra", "tra", "rad", "adi", "din", "ing"). Under the word bar, show
  one smaller bar per such n-gram, color-coded by token, each showing that
  character-token's global coverage loading toward (or past) its target (from
  WO-067's gossiped token-sketch / yield estimate). All bars load in parallel.
  Word→char-token membership is EXACT by construction: the word is the source text
  for those n-grams (run the existing `Tokenize` on the word). Never re-derive
  from a dictionary.
- **Color coding = token identity** (CVD-safe palette, like WO-067's
  `PEER_PROGRESS_COLORS`); sub-bars colored by token, not labeled with token text.
- **Progressive load.** Word % and char-token sub-bars fill concurrently; a
  sub-bar may exceed target ("past target", not clamped).
- **Telemetry only.** Distinct from WO-067's `renderPeerProgress` per-query
  coverage bar (query-scoped fetch coverage, not corpus-wide frequency). Keep the
  two renderers separate, consuming distinct data.
- Show "~" / "est." labels for HLL approximation.

## Data the UI consumes (daemon-exposed)

- `global_word_pct[word]` — word HLL ÷ graph HLL (WO-068 telemetry).
- `global_token_coverage[token]` + `token_target` — from k=3 character sketch
  (WO-067 gossiped estimate). The word→char-token list is produced by running the
  existing `Tokenize` on the word; no dictionary lookup.

## Depends on

- WO-060 — the word NORMALIZATION rule (and stopword rule) is a shared constant;
  ShardK itself is FROZEN (never changes) so needs no mutable versioning, but
  ShardM / alphabet ARE versioned (bucket size adjusts as corpus grows).
- WO-058 — until the swarm graph is populated, the global HLL reads ~0. Machinery
  buildable now; numbers mean nothing until WO-058/WO-059 land. No fabricated
  stats on an empty swarm.
- Opus's existing sketch code (HLL) — extend, don't reimplement.

## Acceptance

- [ ] Word tokenizer: space-delimited, shared normalization rule (WO-060),
      NO fixed word dictionary — counts whatever words appear in the corpus.
- [ ] Local word HLL per node; merged with peers on a timer via bounded-fanout
      rebroadcast; merge reveals cardinality only (no raw data).
- [ ] Global distinct-word count exposed in swarm status (NO coverage-% metric).
- [ ] Per-word global percentage from word HLL ÷ graph HLL; shown as estimate.
- [ ] Search UI two-tier bar: top = per-WORD global %; bottom = per ShardK
      char-token inside the word (from existing `Tokenize`), color-coded, loading
      in parallel, may exceed target. Read-only, no fetch.
- [ ] Word→char-token membership from `Tokenize(word)`, not a dictionary.
- [ ] Stopwords excluded from displayed top-words (display-time filter, WO-060
      rule); no fabricated counts on empty swarm.
- [ ] Regression test: two in-process nodes merge word HLLs, converge on same
      distinct-word estimate (extends WO-062 multi-node test).
- [ ] No word bucket fetch/serve path exists (telemetry only).
