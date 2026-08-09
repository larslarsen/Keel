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

**Superseded in part by grouping (see empirical section):** the short-token bias
keeps tokens common, but a genuinely rare token still yields a small per-token
bucket. The measured fix is grouping tokens into uniform shards
(`shard = hash(token) mod M`), which removes per-token rarity entirely. The
tokenizer here stays as the first layer; grouping is the second.

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
- Bandwidth cost: per query = Σ (shard size over the query's tokens' shards),
  bounded and predictable; k=3 ≈ 450 (1-word) to ≈1,900 (4-word) pairs at M=256
  on the local corpus, scaling linearly with the corpus, NOT exponentially.
  See the empirical section for the full k=1..5 + grouping model.

## Caveats (load-bearing, record them)

1. **Non-collusion / identity.** Different peer per fetch AND fresh ephemeral ID
   per fetch. Current code rotates per SESSION — must tighten to per-fetch or a
   colluder links the fetches and reconstructs the query.
2. **Common-term requirement.** Safety is "tokens/terms are common." With
   per-token buckets, exotic words have few/some-rare tokens → partial leak.
   **RESOLVED by grouping (empirical section):** tokens land in uniform shards,
   so no token's rarity reaches the peer. The short-token bias remains a useful
   first layer but is no longer the sole defence.
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

2. **Rare-token floor — SOLVED by grouping, not by tokenizer tuning.** A
   genuinely exotic word decomposes into pieces that are themselves rare, and a
   rare token's per-token bucket is small (a peer serving it learns you wanted
   that token). Hashing the token key does NOT fix this — the bucket contents are
   fixed as videos(token), so renaming the key leaves the size unchanged. The fix
   is GROUPING tokens into uniform shards (`shard = hash(token) mod M`, M≈64–256,
   measured): the peer serves a common shard containing many tokens, never the
   rare token. See the empirical section — under grouping no token's rarity
   reaches the peer. The precomputed-tokenizer bias (shorter = more common) is now
   secondary; grouping is the primary control.

3. **Bucket-population inference.** An adversary running many nodes learns global
   bucket populations. Fetching a bucket known to contain exactly 3 videos tells
   them you searched one of those 3, regardless of token vs word. **SOLVED by
   grouping (see empirical section): shards are uniform (CV 0.15–0.58 at M=64–256),
   so no bucket is small enough to identify a query. The per-token population
   reporting below is still required for CORRECTNESS (completing thin-slice
   replies), not for this privacy leak.**

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

### Token-bucket population reporting — CORE, not overkill (Lars, 2026-08-08)

Each node already builds its token → videoID inverted index locally (Construction
B, server side). So it knows, per token, exactly how many videos are in that
bucket. Reporting the COUNT costs nothing extra — the table exists; expose the
size per token.

This is NOT overengineering. It is required for two independent reasons — privacy
AND correctness:

**Privacy.** A count reveals nothing about which video YOU want (zero privacy
cost). Big counts are harmless to broadcast; small counts ARE the warning.
Self-protecting. (Covered below as the warning signal.)

**Correctness — partial peer slices break the intersection.** The search
reconstructs the result by intersecting token-buckets fetched from several peers.
For a target video V (matching query Q's tokens t1..tn) to survive the
intersection, V must appear in EVERY token's bucket from the peers you query. But
no peer holds the whole corpus — each holds a SLICE (its own corpus + mirrored,
per WO-058). So peer P may have V in its t1-bucket but NOT its t2-bucket. When
you intersect P's t1-reply (has V) with P's t2-reply (missing V), V drops out.
Result: **a single peer with incomplete coverage silently deletes your search
result.** And if a peer returns a 1-element set for a token, intersecting with it
can yield at most 1 result — your multi-video search collapses to ≤1 hit. The
multi-peer fetch only reconstructs the FULL result set if each peer returns a
SUFFICIENTLY COMPLETE bucket.

This is why the global count is mandatory, not optional:
- Without it, you cannot tell whether a peer's 1-video reply means "token 'xyz'
  has 1 video globally (rare, legit)" vs "this peer only holds 1 of the ~50
  global matches (thin slice)." In the second case you've lost 49 results and
  don't know it.
- The global count tells the client "expected ≈50, got 1 from this peer → fetch
  from MORE peers to complete the union." Completion requires knowing the target
  size. Suppressing the 1-element reply does NOT help — you still lack t2's
  contribution and V is dropped; you need COMPLETION (more peers), which needs
  the global size. Same conclusion: global counts are necessary for correctness,
  independent of privacy.

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
benevolently as a warning) and WO-058 (empty graph is the root cause). The
correctness dependency makes the count-reporting subsystem required at ANY
network size, not just small — so it is core, not deferred.

## Empirical tokenizer evaluation (measured 2026-08-09, supersedes the 08-08 table)

Measured against **4,527 distinct** real YouTube titles from the live Keel DB
(`.config/keel/keel.sqlite`, `impressions` table). The earlier 3,796-number
table was stale AND internally inconsistent (it claimed k=2 has the FEWEST
tokens per query — false: smaller tokens split a word into MORE pieces, so
k=2 emits ~30 tokens/title vs k=3's ~25). The earlier table also reported only
the MEDIAN bucket, which hid the mean that actually drives bytes. Corrected
below. Tokenization: space-anchored, letters-only (`' rec'`), as the design
specifies. Bucket size = number of titles containing the token.

### Per-token buckets, k=1..5 (the unit is the token bucket)

| k | tokens/title | mean bucket | median bucket | 1-word bytes | 4-word bytes | % k=1 (leak) |
|---|---|---|---|---|---|---|
| 1 | 16.5 | 2848 | 3054 | 34,140 | 49,431 | 0.0% |
| 2 | 30.1 | 233 | 51 | 7,223 | 33,466 | 9.2% |
| **3** | **25.1** | **25** | **6** | **480** | **5,644** | **23.8%** |
| 4 | 18.2 | 6.7 | 2 | 49 | 1,211 | 39.7% |
| 5 | 12.5 | 3.7 | 1 | 31 | 458 | 52.0% |

"bytes" = Σ bucket sizes over the query's tokens (the real cost; it is the
PRODUCT N×mean_bucket, NOT median and NOT token count). Findings:

- **Per-query bytes fall monotonically as k rises.** k=2 → 33,466; k=3 → 5,644
  (6× less); k=4 → 1,211 (28× less than k=2). The earlier "k=2 optimal" was
  exactly backwards: bigger k shrinks buckets, and the bucket-size collapse
  dominates the small token-count rise (33→30 tokens from k=2 to k=3).
- **k=3 is the bandwidth/privacy knee.** Cheapest scheme with still-acceptable
  rare-token privacy (23.8% k=1). k=1/k=2 are private but brutal in bytes;
  k=4/k=5 are cheap but half their tokens leak. **Default k=3.**
- **Space-anchoring does not move bytes** (identical columns with/without it) —
  it is a CORRECTNESS guard (stops "men" matching "moment" across words), not a
  bandwidth lever. Keep it.

### Adaptive stepping by query length

A short query has few tokens, so stepping DOWN to a smaller k keeps buckets
common (private) and tolerates the byte cost; a long query has many tokens, so
stepping UP to a larger k shrinks buckets cheaply (the rare-token risk is
diluted across many tokens). Measured on "algorithm" (1 word):

| k | tokens | bytes |
|---|---|---|
| 2 | 8 | 7,223 |
| 3 | 7 | 480 |
| 4 | 6 | 49 |

Rule: **≤2 words → k=2; 3–4 words → k=3; ≥5 words → k=4.** Precompute the
inverted index at k=2, 3, 4 (3× storage, cheap; the client picks k, the serving
peer needs that k's table).

### Grouping tokens into uniform shards — kills the server-side rare-token leak

Per-token buckets can never fix the rare-token leak (attack #2/#3): a token's
bucket size is intrinsic to its frequency, so a rare token yields a tiny bucket
and the serving peer learns you wanted it. **Hashing the token KEY does not help
— the bucket contents are fixed as videos(token), so renaming the key leaves the
size unchanged.** What fixes it is GROUPING: assign each token to a shard by
`shard = hash(token) mod M`, and store the shard as `(videoID → set of tokens-in-
shard it matches)`. Fetching shard G returns a UNIFORM set; intersect locally on
the token tag to recover exactly videos(token). The peer sees only "shard G"
(common, many tokens) — never the token.

This is the §7.4 prefix-bucket pattern reapplied to the token index. It is
**server-side privacy**: the serving peer cannot link you to a token, because
one shard contains many tokens. (§7.4 prefix buckets bought CLIENT-side privacy
— hid the client's query. Here the shard hides the token from the peer. Different
direction, same mechanism.)

Measured (4,527 titles; byte proxy = (videoID, token) pairs per shard; CV =
coefficient of variation, lower = more equal):

| k | M | distinct tokens | shard median | shard mean | shard max | CV |
|---|---|---|---|---|---|---|
| 3 | 64 | 4,432 | 1,715 | 1,746 | 2,994 | 0.26 |
| 3 | 256 | 4,432 | 383 | 436 | 1,913 | 0.58 |
| 4 | 64 | 12,161 | 1,286 | 1,271 | 1,752 | 0.15 |
| 4 | 256 | 12,161 | 307 | 318 | 761 | 0.31 |

**M must be small relative to the token count** (M=64–256). At M=4096 with
4,432 tokens most shards go empty and CV explodes. At M=64–256 the shards are
near-uniform (CV 0.15–0.58) and the old leak is gone: old k=3 had 2,141 tokens
with ≤5 videos (a per-token leak); under grouping no token's rarity reaches the
peer — only the uniform shard does.

Per-query bytes under grouping (M=256 target; proportional to shard size):
k=3 ≈ 450 (1-word) to ≈1,900 (4-word) pairs; k=4 ≈ 180 to ≈760. Bounded,
predictable, linear in corpus, NOT exponential.

Consequence for sharing: because the shard is uniform AND its contents are a
fixed public function of the corpus (identical for every node), a peer serving a
shard learns nothing about the requester that is not already public. So the
**per-fetch-ephemeral-identity requirement (caveat #1) becomes good hygiene, not
the load-bearing anonymizer — the uniform shard is.** The token→videoID index
shards are safe to serve/share (server-side-private), exactly like the graph
buckets in §7.4. This does NOT make the CATALOGUE (titles) safe to share raw —
titles still derive from graph buckets per §7.4; only the token index shards and
graph buckets are server-side-private. Client-side query reconstruction is
unchanged.

### k-step remains a developer release

k is still a COMPILE-TIME constant (WO-060): nodes must agree on tokenization or
they partition the network. The adaptive stepping above is by QUERY LENGTH on the
client, choosing among the three precomputed k-tables — it does not change the
protocol, only which local table the client queries. So stepping is a client
local choice over precomputed tables, not a network-wide parameter flip.

**Caveat — LOCAL numbers.** Privacy scales with network size (union across
peers). The %k=1 locally becomes network-wide k≈10–50 at modest peer counts,
which is exactly why the global count-reporting below is required for
correctness (a thin-slice peer's small reply must not collapse the result). The
grouping model's uniformity holds network-wide (shards stay uniform as the corpus
grows); absolute bytes scale with corpus.

## Acceptance (when built)

- [ ] Static token/term → videoID inverted index built from local catalogue, at
      **k=2, 3, and 4** (precomputed; adaptive stepping picks among them by query
      length: ≤2 words → k=2, 3–4 → k=3, ≥5 → k=4).
- [ ] Tokens grouped into **uniform shards** (`shard = hash(token) mod M`,
      M≈64–256; measured CV 0.15–0.58). Shard stored as
      `(videoID → set of tokens-in-shard matched)`, fetched whole, intersected
      locally on the token tag. No per-token bucket is ever served.
- [ ] Serve-shard RPC returns the whole shard for a token's group, no query
      evaluation; the peer learns only the common shard, never the token
      (server-side privacy — see empirical section).
- [ ] Ephemeral identity per fetch (not per-session) + different peer per shard
      (good hygiene; the uniform shard is the load-bearing anonymizer, so this is
      no longer the sole defence).
- [ ] Local intersection yields the precise result; no peer observes the query.
- [ ] Test: a node searches a term another node holds, peer logs show only shard
      ids, never the token or the result.
- [ ] Each node reports per-shard population counts (from its local index) without
      revealing shard contents.
- [ ] Network-wide per-shard counts aggregated via HLL sketch (sketch.go),
      MIN-across-peers for attack resistance; used to COMPLETE thin-slice replies
      (a 1-element reply must not collapse the result set).
- [ ] Search UI shows a gradient warning driven by aggregate shard population
      (threshold ≈ STAR K of 50); copy states the small-network limit honestly.
- [ ] Tokenizer ships as k=2/3/4 tables, letters-only, space-anchored (measured
      optimal: k=3 is the bandwidth/privacy knee; NOT the stale "k=2 optimal"
      claim); not BPE/WordPiece; COMPILE-TIME constant, identical on all nodes,
      versioned per WO-060. Any k change is a developer release, never
      network-driven.
