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
- **TRANSPORT: direct on-demand fetch, NOT gossip (Lars, 2026-08-11).** Word
  stats are display-only — they are never needed during a search (unlike token
  counts, which drive FetchShard's stop-condition), so there is no reason to
  continuously gossip them. A node that wants the global word stat sends a direct
  request to peers (reusing WO-059's peer-fetch path); each responder returns its
  256-byte word HLL; the requester merges locally. Cost is paid ONLY when the UI
  actually shows the stat — zero when idle. This is the cheapest bandwidth option
  and the right one here. (Contrast WO-067 token sketches, which stay on gossip:
  the token YIELD flag MUST be push-only because pulling it would leak K, and the
  token count sketch is load-bearing for the search stop-condition, needed the
  instant a search starts. Words have neither constraint.)
- **No fixed word dictionary, and no plaintext words on the wire — by design.**
  The HLL is CONTENT-ADDRESSED BY HASH: each observed word is normalized
  (shared rule, WO-060) and hashed locally into register positions; the word
  string is consumed and never leaves the node. What travels on the direct fetch
  is the 256-byte register array (`R []byte`, `P=8` → 2^8=256 registers, 1 byte
  each — same shape as store.TokenSketch), never the word "trading". Nodes agree
  on register positions because they agree on the HASH FUNCTION + normalization
  rule, not on a vocabulary. Merge = HLL max-register union (sketch_store
  MergeTokenSketch pattern); neither side sends word strings. This is why no fixed
  word dictionary is needed: the character scheme's dictionary exists only to map
  tokens → fixed yield-vector slots, which words don't use.
- **Gossip cost note (why direct beats it here):** per the token scheme,
  gossiping sketches is throttled to 20/tick (sketch.go) but always-on — every
  node publishes every tick regardless of demand. A single merged word HLL would
  be ~256B/min always-on; a direct fetch is ~256B × responders ONLY when the UI
  requests it. For a stat rarely viewed, direct wins. Aggregate distinct-word
  count + per-word frequency ARE the feature and are intentionally visible to
  peers; the design hides only which graphs / who observed what.
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
- **Poisoning (defense for accuracy, display-only — Lars, 2026-08-11).** A poisoned
  word HLL cannot break the app: word bars feed NO search decision (the
  load-bearing stop-condition reads the TOKEN sketch, WO-067, which already has
  shard-reply signing + cross-peer poison detection). Word poisoning only shows a
  wrong percentage. Still defend, for accurate display. HLL merge is a
  register-MAX union, so an adversary can only INFLATE registers (over-state the
  corpus), never hide a word — low stakes. Lightweight defense: fetch the word HLL
  from several peers and MEDIAN / majority-merge the per-peer estimates, ignoring
  outliers. No authorship or signing needed (messages carry no author, live.go:19)
  — redundancy suffices because it is display-only. Do NOT pull in WO-051/STAR
  threshold-encryption: that is for count DISCLOSURE under contribution levels,
  overkill for a display bar. Contrast WO-067 token poisoning (load-bearing, so
  signed + cross-peer detected).

## UI (search page) — two-tier nested percentage bars

The search UI breaks a query into WORDS and shows global-corpus percentages,
loading progressively as swarm data arrives.

- **Top tier: one percentage bar per WORD in the query.** Each word bar shows its
  global frequency (from the global word HLL). Fills as data arrives from the
  direct peer fetch.
- **Bottom tier: nested sub-bars per ShardK CHARACTER-TOKEN the word contains.**
  A word ("trading") is sliced by the EXISTING character tokenizer into its 3-char
  n-grams (" tra", "tra", "rad", "adi", "din", "ing"). Under the word bar, show
  one smaller bar per such n-gram, color-coded by token, each showing that
  character-token's global coverage loading toward (or past) its target (from
  WO-067's push-only token-sketch / yield estimate). All bars load in parallel.
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

## Data the UI consumes (daemon-exposed, via direct fetch)

- `global_word_pct[word]` — word HLL ÷ graph HLL (WO-068 telemetry), obtained by
  a direct peer request (NOT gossip) when the UI opens.
- `global_token_coverage[token]` + `token_target` — from k=3 character sketch
  (WO-067 gossiped estimate, push-only per WO-059 §yield-must-be-push). The
  word→char-token list is produced by running the existing `Tokenize` on the word;
  no dictionary lookup.

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
- [ ] Local word HLL per node; fetched on demand via direct peer request (reusing
      WO-059 peer-fetch), merged locally — NOT gossiped on a timer. Merge reveals
      cardinality only (no raw data).
- [ ] Global distinct-word count exposed in swarm status (NO coverage-% metric).
- [ ] Per-word global percentage from word HLL ÷ graph HLL; shown as estimate.
- [ ] Search UI two-tier bar: top = per-WORD global %; bottom = per ShardK
      char-token inside the word (from existing `Tokenize`), color-coded, loading
      in parallel, may exceed target. Read-only, no fetch.
- [ ] Word→char-token membership from `Tokenize(word)`, not a dictionary.
- [ ] Poisoning defense: fetch word HLL from several peers, median/majority-merge
      (display-only, no authorship/signing needed); adversary can only inflate,
      never hide a word. No WO-051/STAR threshold-encryption for this bar.
- [ ] Stopwords excluded from displayed top-words (display-time filter, WO-060
      rule); no fabricated counts on empty swarm.
- [ ] Regression test: two in-process nodes merge word HLLs, converge on same
      distinct-word estimate (extends WO-062 multi-node test).
- [ ] No word bucket fetch/serve path exists (telemetry only).
