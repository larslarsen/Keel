# WO-059 — Distributed search over peer graph/catalogue data (user-invented)

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Open** (proposal, user-invented, not in repo) |
| **Date** | 2026-08-08 |
| **Source** | Lars, 2026-08-08 |

## The idea (Lars's, verbatim in spirit)

You CAN search over data that isn't on your computer. The privacy is a *property
of how you do it*, not a reason it "can't be done." Models (including this one)
kept saying "you can't search peers' data without having it locally" — that was
a sloppy translation of "you can't do it WITHOUT the peer learning your query"
into "can't be done." Both halves are false once you fetch supersets from
multiple peers and intersect locally.

### Construction A — multi-word / multi-bucket conjunction

1. Split the search into terms (words).
2. For each term, hash it into a bucket (existing `BlockPrefix`-style hashing,
   but over a TERM key, not a video ID).
3. Fetch each term's bucket from a **different peer**, using a **fresh ephemeral
   identity per fetch**.
4. Each peer returns its whole bucket (a large set, because the term is common)
   and learns only "this node wanted something in the set for term_i."
5. The conjunction (term1 AND term2 AND …) is computed **locally** on the
   user's machine. No peer ever sees more than one term, so no peer can
   reconstruct the query. The precise answer is free and hidden.

This is private conjunctive keyword search achieved by replication + local
intersection. It IS distributed search; the privacy is the adjective, not a
different category.

### Construction B — token-shingle single-word fix (the key insight)

Single-word search breaks Construction A: one word = one (possibly small) bucket
= the peer learns you searched that word. Fix: **shingle the word into tokens**
(3-letter chunks, spaces mixed in) — e.g. "recommendation" → rec, eco, com, omm,
mme, men, end, dat, ati, tio, ion.

- Each 3-letter token appears in thousands of words → each token-bucket is huge
  and low-information.
- Fetch each token bucket from a different peer, fresh identity per fetch.
- Intersect locally → the videos matching ALL tokens = the word.
- No peer sees the word. Works for single words AND multi-word (union all
  tokens).

This makes single-word private search feasible by breaking one rare query into
many common-token queries. Strictly better than word-buckets for the
common/rare-word problem.

### Server side

Each node computes its OWN table (inverted index: token/term → video IDs) from
its local catalogue and serves bucket lookups from it. Static, local, no trust
needed. The peer never evaluates the user's query — it serves a precomputed
bucket. (If a peer computed "videos containing token X" on request, it would
learn the token and recreate the invertible-hash leak — so the index must be
precomputed and served as opaque buckets.)

## What is cited vs what Lars composed

- **Primitives (real, published, citable):** PIR (Chor–Kushilevitz–Goldreich–
  Sudan 1995), PSI (many constructions), inverted indexes, character-shingling /
  K-shingles for near-duplicate detection (Manber 1994, Broder 1997).
- **The assemblies (Lars's, NOT found pre-built in a paper):** "intersect N
  word-buckets from N peers," and "shingle a word into 3-letter tokens, fetch
  each token-bucket from a different peer, intersect locally." These are
  compositions of the primitives, reasoned sound from them. Do NOT claim a
  specific paper describes exactly this for YouTube search — none was verified.
- **In the repo:** NONE of this exists. What's there: video-ID prefix buckets
  (prefix.go, graph fetch only), per-SESSION ephemeral identity
  (swarm_runtime.go:72, not per-fetch), HLL sketches (sketch.go, for network
  size/overlap, not search), WO-052:176-188 DEFERS PSI. DESIGN_BOOTSTRAP:78
  inverted index is LOCAL search only.

## Why this matters / where it sits

- It is the honest answer to "can I search peer data." Yes. The repo's current
  posture (replicate bucket → search locally) is the SAME idea at video-ID
  granularity; Lars generalized it to term/token granularity for text search.
- It does NOT replace the video-ID graph bucket (graph traversal: what-follows-
  video-X). Both coexist: graph bucket for walking, term/token bucket for the
  search box.
- Bandwidth cost: N full buckets per query (N terms or N tokens). Cheap in
  information, not in bytes — same trade as the existing bucket.

## Caveats (load-bearing, record them)

1. **Non-collusion / identity.** Different peer per fetch AND fresh ephemeral ID
   per fetch. Current code rotates per SESSION — must tighten to per-fetch or a
   colluder links the fetches and reconstructs the query.
2. **Common-term requirement.** Safety is "tokens/terms are common." Exotic
   words have few/some-rare tokens → partial leak. Acceptable, document it.
3. **Static index required.** Token→videoID index must be precomputed and served
   as opaque buckets, never computed from the request.
4. **Rare-term residual leak.** Single rare token still reveals more than a
   common one. The shingle helps but doesn't zero it.

## Security analysis (attack vectors + mitigations)

Compiled 2026-08-08 from Lars's challenge "find ways to break it." All are
engineering/parameter problems, not conceptual breaks — the construction is
sound. Listed in priority order.

1. **Linkage / collusion attack (BREAKS IT IF DONE LAZILY).** If one identity
   fetches token "rec" then "eco" then "com", a passive observer or colluding
   peers link the fetches and reconstruct "recommendation" by intersecting which
   words contain all three. **Mitigation: fresh ephemeral identity PER FETCH,
   spread fetches across different peers.** Non-negotiable. Current code rotates
   per SESSION (swarm_runtime.go:72) — must tighten to per-fetch or the whole
   construction collapses to the invertible-query leak it was built to avoid.

2. **Rare-token floor.** Shingling a rare word can still produce rare tokens
   ("xyzzy" → "xyz","yzz","zzy", all rare). A rare token's bucket is small,
   fetching it reveals more. Token length is a knob: shorter = more common but
   noisier (more false positives, more bandwidth). **Mitigation: tune token
   length so even rare-word tokens stay common; accept a residual floor for
   genuinely exotic character sequences.** Documented, not eliminated.

3. **Bucket-population inference.** An adversary running many nodes learns global
   bucket populations. Fetching a bucket known to contain exactly 3 videos tells
   them you searched one of those 3, regardless of token vs word. **Mitigation:
   pad buckets to a minimum size, or only serve buckets above a popularity
   floor.**

4. **Temporal / fetch-count attack.** "This identity fetched 11 token-buckets in
   200ms" reveals query structure (single word ≈ N tokens, multi-word ≈ sum),
   even without knowing WHICH word. **Mitigation: pad fetches to a fixed count,
   or batch timing so volume doesn't encode structure.**

5. **Catalogue-poisoning.** Buckets are served from each node's local index, so a
   malicious peer can drop or add videos. Because you intersect across MULTIPLE
   peers, one poisoned bucket is caught by disagreement (a video in 3 of 4
   token-buckets but absent in the 4th is suspect). **Mitigation: extend the
   existing seed-pack per-block signature (seed.go) to token-buckets; treat
   cross-peer disagreement as a poison signal.** Consensus across peers catches
   lies about absence too.

6. **Single-source / cold-start limit.** If only one peer holds videos for token
   X (long tail), fetching X's bucket means that peer knows you wanted something
   in X and is the only source, so it correlates with its own corpus. **This is
   the fundamental long-tail limit and is exactly why WO-058 (empty peer graph)
   matters: with no seed, every fetch goes to the one node with data, which is
   you or nobody. The construction makes fetches private; it does NOT fix
   cold-start.** Coupled to WO-058.

7. **Substring false positives (correctness, not privacy).** Token "men" appears
   in "recommendation" AND "moment","amendment", etc. Intersection of all
   token-buckets for "recommendation" yields "videos containing ALL tokens as
   substrings" — a superset. **Mitigation: after intersection, locally re-check
   each candidate title actually contains the word (cheap, exact, on-device).**
   Duplicate tokens across words are idempotent in the set-AND, so harmless.

## Primitive citations (from the literature)

- **Inverted index** — term → document/video IDs. Standard IR since Salton's
  SMART system (1960s); every search engine. Here: each node builds
  token/term → videoID from its local catalogue, serves buckets from it. Local,
  no crypto.
- **Character-shingling / K-shingles** — Manber 1994 ("Finding Similar Files in
  a Large File System"), Broder 1997 (near-duplicate detection via shingles +
  MinHash). Your use: shingle a word into 3-grams so one word becomes a SET of
  common chunks; a bucket keyed by a chunk is large because the chunk appears in
  many words.
- **Private Information Retrieval (PIR)** — Chor, Kushilevitz, Goldreich, Sudan,
  1995. Client retrieves from a server's DB such that the server learns NOTHING
  about which item. Two flavors: information-theoretic (needs multiple
  non-colluding servers) and computational (one server, crypto-heavy). **Lars's
  construction is essentially the information-theoretic multi-server PIR, except
  it retrieves a SET ("all items matching token t") rather than one item. The
  multi-peer + fresh-identity + take-the-whole-bucket part IS the "multiple
  non-colluding servers" requirement of IT-PIR.** Honest citation: this is a
  PIR variant, user-composed.
- **Private Set Intersection (PSI)** — two parties compute the intersection of
  their sets without revealing more. Extensive literature (DH-based, OT-based).
  NOTE: the construction does NOT need full PSI — it fetches supersets from peers
  and intersects LOCALLY, so it is closer to PIR than PSI. WO-052:176-188
  deferred PSI; this design sidesteps it. (Revisit only if exact peer-to-peer
  set intersection is ever needed for its own sake.)
- **k-anonymity and why the v1 buffer failed** — a release is k-anonymous if each
  record is indistinguishable from k-1 others. The v1 k-anonymity buffer
  (prefix.go:7-14) hid a real query among decoys; repeated observation separated
  the real one via intersection attack. This construction survives because there
  is NO real-versus-decoy distinction — the node genuinely takes the whole
  bucket, so repetition adds no signal.

## Acceptance (when built)

- [ ] Static token/term → videoID inverted index built from local catalogue.
- [ ] Serve-bucket RPC returns the whole bucket for a token, no query evaluation.
- [ ] Per-fetch ephemeral identity (not per-session) + different peer per token.
- [ ] Local intersection yields the precise result; no peer observes the query.
- [ ] Test: a node searches a term another node holds, peer logs show only
      token-buckets, never the term or the result.
