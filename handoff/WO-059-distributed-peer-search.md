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

### Construction B — space-aware tokenizer (the key insight, corrected)

Single-word search breaks Construction A: one word = one (possibly small) bucket
= the peer learns you searched that word. Fix: **tokenize the whole query with a
custom, precomputed, space-aware tokenizer** — NOT fixed 3-grams, NOT a borrowed
LLM vocab (BPE/WordPiece). Design the tokenizer for the privacy invariant, not
for compression.

Why not borrow BPE/WordPiece: those optimize for compression, so common words
become a SINGLE token ("recommendation" = 1 token) → its bucket is just that
word's neighbourhood → SMALL bucket → LESS private. Exactly backwards. We want
every emitted token to be common enough to have a large bucket, so we bias
toward SHORT, space-aware pieces.

Concrete scheme (illustrative, the invariant is what matters):
- Split query on word boundaries.
- Emit space-aware short tokens (spaces included, e.g. " recommendation" →
  " rec", " reco", " rec m", …). The leading/trailing space anchors the token to
  a word boundary, so tokens don't bleed across words the way position-free
  3-grams do.
- Guarantee by construction: every emitted token is in the "common enough" set,
  because short space-aware pieces appear in many words. No runtime sharding
  needed — the fixed scheme already does it.
- Ship this tokenizer as a STATIC module every node uses identically, so bucket
  keys match across nodes (a query has exactly one canonical tokenization from a
  fixed start; peers serve and clients fetch the same buckets).

- Fetch each token bucket from a different peer, fresh identity per fetch.
- Intersect locally → videos matching ALL tokens = the query.
- No peer sees the query. Works for single words AND multi-word (tokenize the
  whole string). The intersection is exact (space-anchored tokens don't
  cross-word bleed), so the local title re-check from vector 7 is mostly
  unnecessary.

This makes private search feasible for any query by decomposing it into many
common-token queries. Strictly better than word-buckets for the
common/rare-word problem, and avoids the BPE "common word = rare bucket" trap.

### The invention, restated (succinct)

Distributed search over recommendation-graph/catalogue data that is NOT on your
machine, with the peers learning nothing about your query:

1. Tokenize the entire query with a fixed, space-aware, short-token scheme
   (precomputed, shipped to all nodes) → a unique token sequence.
2. For each token, fetch its BUCKET (the full set of video IDs containing that
   token) from a DIFFERENT peer, using a FRESH ephemeral identity per fetch.
3. Each peer returns its whole precomputed bucket and learns only "this node
   wanted something in bucket T" — T is common, so near-zero info.
4. Intersect the buckets LOCALLY. The conjunction (all tokens) reconstructs the
   precise result; no peer ever saw more than one token, so none can reconstruct
   the query.
5. The privacy is a property of the method, not a reason it "can't be done."
   It IS distributed search.

### How it differs from IT-PIR (Chor–Kushilevitz–Goldreich–Sudan 1995)

IT-PIR lets a client retrieve ONE item from a server's database such that the
server learns nothing about which item. Lars's construction is a member of the
same family (multiple non-colluding servers / peers, client learns nothing about
which…) but differs in concrete, non-trivial ways:

- **Retrieval unit.** IT-PIR retrieves a single indexed item (or a bit). This
  retrieves a SET — all video IDs matching a token — i.e. set/range retrieval,
  not point retrieval.
- **Servers are untrusted but the data is replicated, not partitioned.** In
  classic IT-PIR the servers hold (overlapping or secret-shared) copies of the
  SAME database; the client queries several to hide which index it wants. Here
  each peer holds a DIFFERENT slice of the catalogue (its own corpus + what it
  mirrored), and the client fetches one token-bucket from each. So it is
  IT-PIR-style in the "multiple non-colluding responders, fresh identity"
  sense, but the peers are not replicas of one DB — they are shards of a
  decentralised graph. The non-collusion requirement is met by identity
  rotation + peer spread, not by secret-sharing the data.
- **No cryptographic PIR protocol.** IT-PIR (both flavors) uses homomorphic or
  OT crypto so the server computes over encrypted queries. This uses NONE — it
  relies on superset fetching + local intersection. Cheaper, but it depends
  entirely on (a) common tokens (so buckets are large/low-info) and (b)
  per-fetch ephemeral identity + peer spread (so fetches can't be linked). If
  either fails, it degrades to the invertible-query leak IT-PIR was built to
  avoid. So it is "PIR-like via replication + intersection," not cryptographic
  PIR.
- **Built for a decentralised, user-held corpus, not a server.** IT-PIR assumes
  servers with the data. This assumes peers who each have a partial copy and a
  precomputed inverted index they serve as opaque buckets. The trust model is
  "peer is potentially adversarial, learns nothing from a single bucket fetch"
  rather than "server is honest-but-curious, computes over ciphertext."

So: same threat model as IT-PIR (server/peer learns nothing about the query),
same structural trick (multiple non-colluding responders + rotate identity), but
a different retrieval unit (set not point), different data layout (sharded
decentralised corpus not replicated server DB), and no crypto — it trades
IT-PIR's guarantees-for-arbitrary-queries for a cheaper scheme that holds only
when tokens are common and identities are fresh. That is a genuine variant, not
a verbatim reinvention. Do NOT claim a paper describes this exact construction —
the primitives are cited; the assembly is Lars's.

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
  token-buckets from N peers, tokenized with a custom precomputed space-aware
  short-token scheme," and "fetch each token-bucket from a different peer,
  intersect locally." These are compositions of the primitives, reasoned sound
  from them. Do NOT claim a specific paper describes exactly this for YouTube
  search — none was verified. It is a PIR-like variant (see "How it differs from
  IT-PIR"), not a verbatim reinvention.
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

2. **Rare-token floor.** Even with a space-aware short-token scheme, a genuinely
   exotic word can decompose into pieces that are themselves rare (e.g. a word
   made of unusual character sequences). A rare token's bucket is small, fetching
   it reveals more. Token length / space-anchoring is a knob: shorter, more
   common pieces are noisier (more false positives, more bandwidth) but more
   private. **Mitigation: bias the precomputed tokenizer toward short common
   pieces so even rare-word tokens stay common; accept a residual floor for
   genuinely exotic character sequences.** Documented, not eliminated. (This is
   why we design our OWN tokenizer for the privacy invariant, not borrow BPE —
   see Construction B.)

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

7. **Cross-word bleed (correctness, not privacy).** With position-free 3-grams a
   token like "men" appears in "recommendation" AND "moment","amendment", so the
   intersection is a superset needing a local title re-check. With the space-aware
   tokenizer (Construction B) tokens are anchored to word boundaries (" rec" only
   matches word-initial "rec"), so cross-word bleed is minimal and the
   intersection is near-exact. **Mitigation: the tokenizer design itself; a
   cheap local title re-check remains as a backstop.** Duplicate tokens across
   words are idempotent in the set-AND, so harmless.

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

## Privacy degradation at small scale + user warning

The bucket's k-anonymity is a function of how much data the swarm holds, not of
anything cryptographic. When the network is small, a "common" token like "rec"
may match only 3 videos across ALL peers — so fetching bucket "rec" tells the
serving peer "you searched one of these 3," k=3, no privacy. This is unavoidable
at launch (coupled to WO-058: empty peer graph). The honest move is to surface it,
not pretend.

### Token-bucket population reporting (Lars, 2026-08-08)

Each node already builds its token → videoID inverted index locally (Construction
B, server side). So it knows, per token, exactly how many videos are in that
bucket. Reporting the COUNT costs nothing extra — the table exists; expose the
size per token.

- **What it buys:** a querying node learns the population of each token bucket
  across the network — not the contents, just "token 'rec' = 4,200 videos,
  token 'xyz' = 3." A count reveals nothing about which video YOU want, so it has
  zero privacy cost. Big counts are harmless to broadcast; small counts ARE the
  warning. Self-protecting.
- **How to aggregate:** extend the existing HLL sketch infra (sketch.go:214
  `Intersection`) from "how big is the network" to "how big is each token bucket"
  — a per-token cardinality union across peers. (Brute-force alternative: fetch
  each bucket and count what comes back — but fetching a 3-video bucket IS the
  leak, so report counts SEPARATELY from contents.)
- **Attack resistance:** a node could lie, claiming 4,200 for "rec" when it holds
  3. Aggregate with **MIN across peers** — warn if any honest peer reports small;
  a liar reporting large is overridden by the minimum. A lying-INFLATED count only
  hurts the node that trusts it, and min-aggregation makes the conservative
  choice automatic.
- **Drives the warning:** aggregate bucket population → gradient (green/amber/red
  by bucket size). Per-token detail lets the client drop or substitute a token
  whose bucket is too small, or show "this query isn't private yet."

### Warning design

- **Macro signal:** `keel_peers` from `swarmStatus()` (swarm_runtime.go:193-213)
  — how many real installs are online. Simple, already available.
- **Per-query signal:** the per-token bucket populations from the reporting
  above. Threshold matches the existing STAR K floor (≥50 contributors): if the
  smallest token bucket for a query is < ~50 videos, warn.
- **Copy (honest, not scare-mongering):** "Keel is early. With this little data
  in the network, a peer serving your search could narrow down what you looked
  for. This improves as more people join." Gradient by bucket size.

Couples to WO-059 vector 3 (bucket-population inference — same signal used
benevolently as a warning) and WO-058 (empty graph is the root cause).

## Acceptance (when built)

- [ ] Static token/term → videoID inverted index built from local catalogue.
- [ ] Serve-bucket RPC returns the whole bucket for a token, no query evaluation.
- [ ] Per-fetch ephemeral identity (not per-session) + different peer per token.
- [ ] Local intersection yields the precise result; no peer observes the query.
- [ ] Test: a node searches a term another node holds, peer logs show only
      token-buckets, never the term or the result.
- [ ] Each node reports per-token bucket population counts (from its local index)
      without revealing bucket contents.
- [ ] Network-wide per-token counts aggregated via HLL sketch (sketch.go),
      MIN-across-peers for attack resistance.
- [ ] Search UI shows a gradient warning driven by aggregate bucket population
      (threshold ≈ STAR K of 50); copy states the small-network limit honestly.
