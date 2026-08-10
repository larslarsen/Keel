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
- Bandwidth cost: NOT a single fixed number. Per search you fetch shard G from
  several peers and union their K-tagged slices until you reach a coverage target.
  The cost is `bandwidth = peers_polled × shard_size(S)`, where S is protocol-fixed
  (corpus/M, from grouping) but `peers_polled` is set by the **coverage target**,
  which the user's **disk slider** governs. So the slider varies both this-search
  bandwidth AND cache size. Measured on the 4,527-title corpus (k=4, M=256,
  S≈18): reaching 99% coverage of a 300-video keyword polled ~22 peers at
  f=0.20, ~84 at f=0.05, ~455 at f=0.01 (the 1/f multiplier is real). That is
  superlinear-ish (≈ S·(1/f)·ln(S) from the rare-item tail) but NOT exponential.
  Crucially, **peer selection among non-zero peers is bandwidth-NEUTRAL**: a peer's
  shard yields K-videos in proportion to its shard size, so K-yield per downloaded
  byte = p = (fraction of shard that is K), independent of slice size. Picking big
  vs small slices gives the same total bandwidth (big = more per fetch but more
  yield; small = less but less yield). So "prioritize big buckets" does NOT reduce
  bandwidth — only the slider (coverage C) and self-healing (rising p) do. See the
  Refined query/serving/coverage/disk model section for the full corrected model.

## Caveats (load-bearing, record them)

1. **Non-collusion / identity.** Different peer per shard fetch AND rotate the
   swarm identity so a sequence of shard fetches is not tied to one stable peer
   ID (trajectory linkability). Current code rotates PER START (not per-fetch —
   per-fetch is stronger but costs identity churn and is not implemented). Each
   shard fetch reveals only "fetched common shard G" (near-zero info under
   grouping, since G holds thousands of tokens), so per-start rotation is
   sufficient; within a session fetches share an identity but are individually
   low-info. This matches daemon/swarm/prefix.go (rotation + prefix caching both
   required). See the Security-analysis attack #1 for the corrected framing.
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
   fetches shard G1 then G2 then G3 (the different shards for a multi-token
   query), a passive observer or colluding peers link the fetches into a
   TRAJECTORY and reconstruct "this identity searched tokens mapping to
   G1,G2,G3." **Mitigation: rotate the swarm identity so a sequence of shard
   fetches is not tied to one stable peer ID.** The current implementation
   rotates PER START (swarm_runtime.go:157-160, `EphemeralIdentity`), not
   per-fetch — per-start breaks the cross-session trajectory; within a session
   fetches share an identity, but each shard fetch reveals only "fetched common
   shard G" (near-zero info, since G holds thousands of tokens). Per-FETCH
   rotation is stronger but costs identity churn (key exchange per request) and
   is NOT implemented — do not claim per-fetch in the spec. Rotation exists to
   break trajectory linkability, not to hide individual fetches (those are
   already near-zero-info under grouping). This matches daemon/swarm/prefix.go
   (identity rotation + prefix caching are both required; neither alone).

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

### Gossip: few-bit yield vector, dictionary-indexed (Lars + agent, 2026-08-09
session-corrected — supersedes the earlier "two count types" framing)

The earlier text spoke of "per-node announced counts" and "global grouping
counts" as bandwidth gate / coverage denominator. That framing is STALE relative
to what the session actually concluded. Corrected model:

- **Global total IS the target — real, not optional.** There is a periodic global
  per-keyword total, reconstructed from per-node LOCAL sketches (each node
  maintains a sketch over its own K-matching videoIDs; the combination mechanism
  to get the network-wide distinct count is TBD in this ticket). It is the
  DENOMINATOR for the coverage bar and the AIM for the stop-condition —
  load-bearing, not display-only. It is a TARGET, not a stop gate: stop is only
  when fetched ≥ target AND saturation (new peers add ≤0 new K). The global
  total drives the search aim; it does not by itself stop anything.
- **What nodes gossip: a 1-bit YIELD flag per token, indexed by a fixed shared
  dictionary.** The bit = "this shard is worth fetching" (above a tunable,
  network-agreed threshold — top 50% / 10% / 80% etc; the threshold is a protocol
  constant like k and M, agreed by all nodes, can be retuned). It is NOT an
  absolute count and NOT a block size — just a worth-fetching flag, so no
  magnitudes are disclosed. If finer screening is ever wanted, 2 bits (low/mid/high)
  suffice; the cost is still token_count × bits.
- **The dictionary is local and fixed** (protocol constant, like k and M). It is
  NEVER sent over the network. Only the yield-flags travel on gossip (1 bit ×
  token_count ≈ 555 bytes per node's vector at k=3 ≈ 4,437 tokens).
- **Gossip bandwidth (corrected model — Lars, 2026-08-09):** gossip is a
  REBROADCAST / phone-tree, NOT a full mesh. A node sends its vector to its d
  neighbors; those rebroadcast, so info reaches all N nodes in O(N·d)
  transmissions per period, NOT N². (Earlier O(N²) claim was wrong — that only
  holds if every node transmits to every other node directly, which gossip does
  not.) Network total per period ≈ N × d × (vector size). At k=3 that is
  N × d × ~555 bytes (N=10,000, d=20 → ~111 MB/period network-wide, ~11 KB/node/
  period — tolerable). The requirement is BOUNDED fanout (each node talks to d
  neighbors, not all N), which also bounds livestream bandwidth (same model,
  harder payload). Full-mesh N² is a non-starter and is NOT how the network works.
- **Yield must be PUSH, never pull (Lars, 2026-08-09 correction):** a pull-probe
  ("do you have K in shard G?") would require sending K to the peer — that leaks
  the search, violating the core invariant (only shard hash G is ever sent, never
  K). If the probe sent only G, it is just the normal fetch (download G, self-
  filter locally) and gives no separate yield signal. So yield can ONLY be learned
  by each node computing its flag locally and PUSHING it via bounded-fanout
  gossip. Pull is not an option. (Earlier "pull-probe alternative" suggestion is
  RETRACTED — it would leak K.)
- **Interest-clustering of neighbors (Lars, 2026-08-09):** select neighbors by
  GRAPH SIMILARITY, not randomly. Two effects raise per-fetch yield: (1) you scan
  high-overlap neighbors first (they are already top-half for you), and (2) similar
  graph neighborhoods mean more K-overlap per fetch. Self-reinforcing: similar
  peers stay neighbors. The existing WO-052 edge-sketch overlap can measure
  graph-similarity between you and a peer for this. This resolves the earlier
  "neighbor-blindness" problem (random neighbors = blind yield) by making
  neighbors high-yield by construction.
- **Livestream feature is the harder instance of this class (OPEN):** livestream
  state is shared on a short timeframe with payloads larger than 555 bytes, so it
  is a much tighter bandwidth problem under the same O(N·d) rebroadcast model. The
  networking decision (bounded-fanout rebroadcast) applies to BOTH yield-gossip
  and livestream; livestream is the stress case and its bandwidth is unresolved.
- **Skip below-threshold shards** (pull a big block for few K = bandwidth waste +
  gossip clutter). Absence of the flag = "below threshold," blurred (no magnitude
  disclosed). There is NO absolute-count N floor — that would wrongly exclude
  cheap small blocks (a size-2 block with 1 K is 50% yield and cheap to fetch).
- **k = 3 (conclusion).** Smaller k = fewer tokens = smaller gossip vector (favors
  small k) but more shards per query = more requests. Larger k buys NO smaller
  buckets (bucket size is M's job, not k's) and makes the yield-vector bigger.
  With per-fetch identity OFF (per-START only, requests are cheap, and
  prefix-hashed buckets leak near-zero info per fetch), the identity-churn driver
  disappears — so k is driven ONLY by gossip-vector size, which favors the
  smallest workable k. **Conclusion: k=3** (minimizes gossip; fewest tokens →
  smallest yield-flag vector). Not a tunable-in-practice; it is a protocol
  constant (versioned per WO-060).
- **Re-sketch / target scheduling:** track RATE OF COUNT-CHANGE per keyword (NOT
  search frequency — irrelevant and unobservable). Counts trend upward (few
  purges), so a stale target is a floor. Empirical feedback from each search
  (actual fetched count, saturation point, tail length = coverage difficulty)
  retargets the periodic sketch — no ground-truth needed. If you saturate below
  target, walk a bit more then stop; if you reach target but still find new K,
  keep going (handles stale-target underfetch).

This replaces the "per-node count gates bandwidth, global count is denominator"
framing. The yield vector drives fetch spread; saturation drives the stop; the
target is a fuzzy display hint.

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
reconstructs the result by self-filtering + unioning token-shards fetched from
several peers. For a target video V (matching query Q's tokens t1..tn) to appear
in the result, V must appear in the K-tagged slice of EVERY token's shard from the
peers you query. But no peer holds the whole corpus — each holds a SLICE (its own
corpus + mirrored, per WO-058). So peer P may have V in its t1-shard but NOT its
t2-shard. Under tag-self-filter, P's t1-slice contributes V (tagged t1) and its
t2-slice contributes nothing for V (no t2 tag) — V is simply ABSENT from P's t2
contribution. That does NOT delete V: V survives if ANY other peer has it tagged
in both shards, and the union across peers covers it. The old "a single peer with
incomplete coverage silently deletes your result" framing was WRONG — a peer
missing V in one shard only withholds its own slice of V; it cannot null V from
the union. (The pre-tag-self-filter "intersect the buckets" model is what made a
missing slice delete V; the tag-self-filter + union model does not.)

This is why there is NO authoritative global count, and it is NOT mandatory:
- You do NOT need to know "token 'xyz' has 1 video globally (rare) vs this peer
  holds 1 of 50 (thin slice)." You fetch shard G from multiple peers, self-filter
  by K-tag, and UNION. A peer contributing little just contributes little. You stop
  when SATURATION is reached (polling more distinct peers adds ≤0 new K-videos) —
  not when you hit a global total. Completeness is best-effort via coverage, not
  guaranteed via global count.
- The periodic global total (reconstructed from per-node LOCAL sketches;
  combination mechanism TBD) is the REAL target that drives the search — the
  denominator for the coverage bar and the aim for the stop. It is a TARGET, not a
  stop gate: stop only when fetched ≥ target AND saturation (new peers add ≤0 new
  K). Reaching target while still finding new K → keep going; saturating below
  target → also keep going (counts almost never decrease, true total is higher);
  KEEP WALKING RANDOM NODES until both hold, DISK SLIDER as backstop.

- **Global per-keyword total — REQUIREMENT, mechanism TBD (Lars + agent,
  2026-08-09).** The search needs a global distinct count per keyword as the
  coverage target (NOT optional, NOT display-only — it drives the stop/bar).
  Constraint from first principles: each node only holds its OWN data and cannot
  know the global total, so no message may carry it; the total must be
  reconstructed by combining per-node LOCAL sketches without any node seeing
  others' raw data. The exact combination mechanism (HLL union or other) is NOT
  specified in this ticket — neither author can derive it from first principles,
  and it must be delegated to someone with HLL expertise or the feature cut if no
  mechanism meets the constraint. Safe guard that needs no sketch internals: a
  merged total can NEVER be lower than the distinct K a node has already fetched
  itself (ground truth from its own search) — any merged result below that is
  discarded. Beyond that, the aggregation and anti-poison design is OPEN.
- **Warning design:** the primary small-network warning is the bootstrap state
  (WO-058), not per-token counts. If a per-token gradient is shown, it derives
  from the same totals.

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

Because tokenization is space-anchored (per-word), a short word within an
otherwise-normal query does NOT break it — the other words still produce tokens
(e.g. "recommendation ai" tokenizes fully at k=4; only the 2-char word "ai"
contributes nothing). The ONLY case that fails is a query that CONSISTS SOLELY
of a word too short for k (a single sub-k-char word → zero tokens). For the
local 4,527-title corpus that means: sub-3-char single words at k=4, sub-2-char
at k=3 ("ai", "ml", "go" at k=4; "go" at k=3).

So stepping is a fallback for the degenerate single-short-word query, not a
general rule: if the whole query yields zero tokens at the chosen k, step DOWN
until at least one token forms. Measured rule: k=3 handles all ≥2-char single
words; k=4 needs step-down only for sub-3-char single words. (Under grouping the
privacy rationale for stepping is gone — group shards are uniform at every k — so
what remains is purely the tokenizability floor.)

### Grouping tokens into uniform shards — kills the server-side rare-token leak

Per-token buckets can never fix the rare-token leak (attack #2/#3): a token's
bucket size is intrinsic to its frequency, so a rare token yields a tiny bucket
and the serving peer learns you wanted it. **Hashing the token KEY does not help
— the bucket contents are fixed as videos(token), so renaming the key leaves the
size unchanged.** What fixes it is GROUPING: assign each token to a shard by
`shard = hash(token) mod M`, and store the shard as `(videoID → set of tokens-in-
shard it matches)`. Fetching shard G returns a UNIFORM set; **self-filter locally**
on the token tag (keep videos whose tag-set contains K) to recover exactly
videos(K). The peer sees only "shard G" (common, many tokens) — never the token.

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

Consequence for serving — and an important correction:

The property is SERVER-SIDE: what a node returns must not leak information about
THAT node's private corpus. A small or rare bucket fingerprints the serving
node's slice. Under grouping, every shard is uniform and common, so serving a
shard does NOT reveal *which videos the node holds for YOUR keyword* — here is
why: a video V lands in shard G purely because some word in V's title hashes to
G. V is co-located with your keyword by the prefix HASH, NOT by topic. So an
observer who sees you serve V's membership in G cannot infer you are *interested*
in V's subject — V is in G by bucket coincidence, not by your interest. Grouping
therefore makes served membership **interest-blind**. (The index also mixes
classes — suggested + watched + search-cached — and carries no provenance tag,
so a served video cannot even be attributed to "suggested/watched" vs "searched."
That is plausible deniability on the observation-attribution question, separate
from the interest-blind point.)

**Threat model correction (Lars, 2026-08-09): the platform ALREADY knows you run
Keel.** They deliver the browser extension and the native-messaging host from
their servers; they can observe the host connecting and the P2P traffic pattern.
So "hide that you are a Keel user / hide that you hold V" from the platform is
IMPOSSIBLE and is NOT the thing being protected. The actual threat (per
AGENTS.md / DESIGN_v2) is **platform denial-of-service = deanonymize-to-ban at
the ACCOUNT level**: linking your recommendation fetches to your YouTube account
so they can ban you for using Keel. What grouping protects against that:
- **Interest blindness:** the platform/peer sees shard G (common, thousands of
  tokens), never the token — so they cannot infer WHAT you search.
- **Account-attribution blindness:** per-START identity rotation + no account
  identifier anywhere in the protocol means fetches cannot be linked to your
  YouTube account. This is the load-bearing protection against deanonymize-to-ban.
So the real goal — "they can't tie your searches to your account to ban you" —
IS met by grouping + rotation + account-free protocol. The earlier framing that
"grouping is NOT membership-blind, so it's an open problem requiring replicated-
PIR" was measuring against the WRONG bar (hiding you-run-Keel from the platform,
which is impossible by construction). Replicated-PIR would hide bare membership
from a *peer*, but the peer is not the platform and the platform already knows.
Record: serve-safety meets the actual threat (interest + account-attribution
blindness → prevents deanonymize-to-ban). Bare-membership-from-peer is not
protected, but that is not the threat model. (If hiding "you run Keel" from the
platform entirely is ever wanted, that needs a different approach — e.g. not
installing via their update channel, or traffic obfuscation — and is a separate
concern, not in scope here.)

The token→videoID index shards are safer to serve/share than raw per-token
buckets (for the interest-blind reason above), like the graph buckets in §7.4.
This is independent of the CLIENT's query privacy (the peer already learns
nothing about which token the requester wanted). It does NOT make the CATALOGUE
(titles) safe to share raw — titles still derive from graph buckets per §7.4;
only the token index shards and graph buckets are server-side-interest-blind.

### k-step remains a developer release

k is still a COMPILE-TIME constant (WO-060): nodes must agree on tokenization or
they partition the network. The adaptive stepping above is by QUERY LENGTH on the
client, choosing among the three precomputed k-tables — it does not change the
protocol, only which local table the client queries. So stepping is a client
local choice over precomputed tables, not a network-wide parameter flip.

**Caveat — LOCAL numbers.** Privacy scales with network size (union across
peers). The %k=1 locally becomes network-wide k≈10–50 at modest peer counts,
which is exactly why the SELF-FILTER + UNION model (not global counts) is the
correctness mechanism: a peer contributing a thin slice only withholds its own
K-tagged videos, it cannot null the union, and saturation (not a global count) is
the stop-condition. The grouping model's uniformity holds network-wide (shards
stay uniform as the corpus grows); absolute bytes scale with corpus.

## Refined query / serving / coverage / disk model (Lars + agent, 2026-08-09)

This section supersedes the naive "fetch each token bucket from a different peer,
intersect locally" framing from Constructions A/B with the corrected mechanism
derived this session. The earlier constructions describe the *privacy shape*
(multiple non-colluding responders, fresh identity, local intersection) which
still holds; this section fixes the *serving, filtering, coverage, and disk*
details that were wrong or incomplete.

### Why the keyword can never be sent to the serving node

The peer must only ever receive the **shard hash** (G = hash(K) mod M), never K
itself — sending K would leak the search (the whole privacy invariant). Therefore
the server cannot be asked "do you have videos for K?" It can only be asked "give
me shard G." The client MUST download the whole shard and find K **locally**.

### Gigantic-shard worst case (measured, 2026-08-09, 4,573-title local corpus)

A common token maps to ONE shard regardless of M, and that shard is huge.
Measured shard sizes (videos/shard) at various M:

| M | mean | max | min |
|---|------|-----|-----|
| 64 | 1,438 | 2,607 (57% of corpus) | 905 |
| 256 | 422 | 1,719 | 74 |
| 1024 | 111 | 1,470 | 1 |
| 4096 | 40 | 1,451 | 1 |

- A common token (" the", " ing") hashes to a single shard holding ~1,400–2,600
  videos NO MATTER the M — because all ~1,600 titles containing it land in one
  shard. **M does NOT fix the gigantic shard; it only changes the mean.**
- Yield is actually GOOD for common tokens: " the" shard = 52% K-match (half the
  shard is your K), so per-byte efficiency is fine. The cost is ABSOLUTE SIZE: a
  common-word search pulls a 1,400–2,600-video shard, and to collect ALL its K you
  walk many nodes, each holding a slice of that same giant shard.
- M=1024/4096 shrink the MEAN shard (rare tokens get 1-video shards) but the COMMON
  token's shard stays gigantic. So common-word search bandwidth is bounded by the
  slider, not by M. This is the inherent worst case; mitigation = slider cap +
  spread across the many holders (don't hammer one).
- The rare-token trap is handled by grouping (rare token lands in a COMMON shard
  via hashing, not isolated), so rare tokens are not the bandwidth problem —
  common tokens are.

### Local tag-self-filter prevents a peer from killing the search

Each shard is stored as `(videoID → set of tokens-in-shard it matches)`. When a
peer serves shard G, it returns its TRUE slice of G (videos it holds that fall in
G, each tagged with which tokens of G they match). The client then **keeps only
videos whose tag-set contains K** and drops the rest.

Consequence — the kill-the-search bug is avoided: a peer may hold shard G but
have ZERO videos that actually match K (its G-slice is there only because OTHER
tokens co-hash into G). Under a naive "intersect ALL fetched buckets" rule, that
peer's empty-K bucket would null the entire intersection and return nothing. With
tag-self-filter, the client simply sees no K-tagged video from that peer, drops
its contribution, and **moves on** — that peer cannot null the search. The
per-video tag is therefore LOAD-BEARING, not optional.

### Coverage, not uniformity; refetch from others

A peer may have K but only SOME of K's videos (its corpus is a slice). So no
single peer gives the full K result; the client UNIONS the K-tagged slices across
peers. To get results it must keep fetching shard G from OTHER peers until enough
of them collectively cover K. The goal is **coverage** (sufficient union), NOT
**uniformity** (every node holding the same corpus). The network does not need all
nodes to mirror everything — only enough distinct peers polled that their union
approximates the full K set. This is the bandwidth/disk trade, bounded by the
slider (below).

### Peer-selection is bandwidth-neutral; use per-node counts to skip zeros

You cannot identify which peer has the most K-videos without leaking K (you can't
ask "how many K?"). Under the no-leak assumption each peer's corpus is an unbiased
sample, so a peer's shard yields K-videos in proportion to its shard size:
K-yield-per-downloaded-byte = p (fraction of shard that is K), **independent of
slice size**. Therefore picking "big buckets" over "small buckets" gives the SAME
total bandwidth — big slices cost more per fetch but yield more K; small slices
cost less but yield less; the ratio is fixed. So:
- **Skip peers whose yield is below floor / ~0** (per the yield-vector gossip) —
  the real bandwidth saving, avoids a full fetch that yields nothing.
- **Among yield-OK peers, selection: screen to TOP-HALF yield, then RANDOM within
  that screened universe.** Filters out worthless low-yield shards (saves the
  disk/bandwidth waste of pulling them). Random within the top-half spreads load
  across many nodes → self-healing (builds K-density across many nodes, not few) →
  future bandwidth drops, and avoids hammering the few highest-yield nodes into
  bottlenecks. This is the happy-medium hybrid between pure-random (wastes
  low-yield fetches) and pure-highest-yield (concentrates load, kills healing).
  (Provisional rule; weight of the screen is tunable.)

### What drives the loop (recap, session-corrected)

- Yield vector (1 bit per token, dictionary-indexed; the bit's threshold is a
  tunable, network-agreed constant — top 50% / 10% / 80% etc) → skip shards
  below the screen. Absence from vector = below threshold, blurred.
- **Stop condition (corrected):** the global target is a TARGET, nothing more —
  NOT a stop gate in either direction. You do NOT stop AT the target if new
  fetches still yield K. You do NOT stop BELOW the target just because you're
  saturating (counts almost never decrease, so saturating below target means the
  true total is almost certainly HIGHER — you just haven't walked enough nodes).
  The ONLY stop is: fetched ≥ target AND saturation (new peers add ≤0 new
  K-videos) — i.e. you have at least the target AND nothing more is coming. Until
  then, KEEP WALKING RANDOM NODES. The DISK SLIDER is the hard backstop if you
  never satisfy both. (Reaching target while still finding new K → keep going;
  saturating below target → also keep going; the target is just a hint you're
  around complete.)

### Disk slider governs coverage (and thus bandwidth + cache), NOT a separate knob

The user's **disk slider** sets the max local storage for REFETCHABLE data
(thumbnails + peer blocks). Personal observations are held OUTSIDE this budget
(see retention classes) and never enter the math. The slider sets the **coverage
target** the client seeks:
- Smaller slider → lower coverage target → fewer peers polled (peers_polled ↓) →
  less this-search bandwidth AND a smaller cache (more future refetches).
- Larger slider → higher coverage target → more peers polled → more bandwidth now
  but a fuller cache (fewer future refetches).
So the slider varies BOTH this-search bandwidth and cache size via the coverage
target. Per-peer cost (shard size S) is protocol-fixed; the COUNT of peers is the
slider's lever. The slider is the backstop that bounds total peers_polled even if
selection is imperfect — it caps bandwidth and disk, and it is what makes
"severely reduce coverage if that might happen" automatic.

### Self-healing bootstrap (resolves WO-058's empty-graph concern)

The shard namespace is deterministic and global. When any node searches, it
materializes blocks for those shards and serves them. Another node searching an
overlapping/identical word hits the SAME shards — finds existing blocks or mirrors
them. Coverage is demand-driven and self-heals from t=0; no pre-seeded graph
(seed-pack) is required for search to work. The corpus converges to "whatever
people search for" — the useful set.

Critically, EVERY peer that polls shard G during a search CACHES G's K-tagged slice.
So a search for a rare keyword replicates that keyword's videos across many polling
peers. After the search, many more peers hold that keyword's slice → the effective
fraction p (K-density in shards) RISES → future searches for K poll FEWER peers →
bandwidth falls. The expensive long-tail fetch is **self-liquidating**: the act of
paying the bandwidth cost distributes the data, so the cost collapses for everyone
after. The FIRST searcher of a rare term pays the full 1/f×tail cost (the bootstrap
tax); everyone after rides the replicated cache. Bootstrap is transient and
self-healing, not a permanent deficit.

### Two datasets, two pulls (graph vs strings)

The corpus is two datasets: the GRAPH (stringless videoID→videoID edges) and the
STRINGS (titles/text matching graph nodes). Two pull paths:
- **Search** pulls BOTH graph blocks AND the matching strings (you need titles to
  show results).
- **Bootstrap seed + prefetching-suggestions pull GRAPH ONLY** (stringless). The
  seed can be regenerated from what this node downloaded from searching others; it
  is re-pullable at any time. Strings are fetched on demand at search time only —
  never prefetched, never in the seed. This keeps strings contained to the
  search-time path.

### Retention classes (what the disk slider does and doesn't govern)

1. **Personal observations** (your watched/suggested/searched history) — IMMUTABLE.
   Held outside the slider entirely. The ONLY data that must be permanently kept.
   Never garbage-collected, never under the slider.
2. **Thumbnails** — refetchable (re-pullable), so already under the slider. Kept or
   dropped by it; losing them is recoverable.
3. **Peer blocks (level-2 shared): graph blocks + string blocks** — also refetchable
   (deterministic shard namespace, re-pull from peers or recompute from catalogue),
   so they GO UNDER THE SLIDER. Garbage-collected when disk is tight, refetched on
   demand. Not immutable.

So the slider governs everything refetchable (thumbnails + peer blocks); only
personal observations sit outside it. Disk pressure evicts refetchable stuff (GC +
refetch); personal observations never. This keeps the §2.1 "no observation data
exposed" guarantee intact while making disk/GC the explicit constraint on the
SHARED layer only.

### Honest residual / open trade

- The slider-capped coverage is best-effort, not guaranteed-exhaustive. A search
  may show partial results (the coverage bar reflects this). That is acceptable for
  a cheap, non-cryptographic search engine; it is a stated design choice, not an
  accident.
- Serving reveals shard membership (you serve your true slice). Grouping makes that
  membership interest-blind and account-attribution-blind (hash-coincidental
  co-location, class-mixed index, no provenance tag, per-START rotation, no
  account in protocol). Against the ACTUAL threat (platform deanonymize-to-ban at
  account level), this meets the bar: the platform sees shard G (never the token)
  and cannot link fetches to your account. The platform already knows you run Keel
  (it delivered the binary), so hiding bare membership from the platform is
  impossible by construction and is NOT the goal. Replicated-PIR would hide bare
  membership from a *peer* but is out of scope and not required for the threat.
- The rare-item tail (coupon-collector) means the LAST few videos need extra peers;
  the slider's coverage cap deliberately stops before the expensive tail.

### Grouping size M — version-locked, NOT auto-varied (Lars + agent, 2026-08-09)

M (the number of shards, `shard = hash(token) mod M`) is a **protocol constant**,
locked per protocol version exactly like k (WO-060). All nodes MUST agree on M or
they shard tokens into different spaces and partition the network. Therefore M
cannot be per-node, runtime-flipped, or auto-grown.

Fixed-M trade as the corpus grows:
- **Bandwidth** scales as `S = corpus / M`. With M fixed, S grows linearly with the
  corpus, so per-search bandwidth grows linearly. (The uniformity/anonymity stays
  constant — a shard stays common.)
- **Anonymity** degrades if M is *increased* to flatten bandwidth: a larger M makes
  each shard smaller/less common, pushing back toward the rare-bucket leak that
  grouping was built to kill. So you cannot have BOTH flat bandwidth AND constant
  anonymity as the corpus grows — it is a direct trade.

Policy: choose M for the **expected steady-state corpus** (the CV measurements at
M=64–256 show uniformity holds in that range). Bandwidth grows linearly until a
deliberate, **versioned M increase** (a developer release, like a k change — never
network-driven, never automatic). Do NOT auto-vary M: it would break the
deterministic shard namespace and split the swarm. The "increase M as corpus grows
to cap bandwidth" idea is rejected for that reason; the cost is accepted linear
bandwidth growth until a version bump.

### Forward-compat note — tombstones / stale data (not in scope here)

The corpus is not immutable (videos get banned/privated/reinstated), so the record
model in this ticket must not PRECLUDE a future tombstone/status-overlay mechanism.
Concretely: records propagated through the shard mechanism should support a
status flag overlay (without force-deleting the underlying data) and bidirectional
status records (tombstone ↔ anti-tombstone) that mutually evict. This is a SEPARATE
feature ticket — spec it there, not here. Recorded so the search/shard design does
not accidentally make it impossible.

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
- [ ] Local self-filter + union yields the precise result; no peer observes the
      query (only the shard hash is sent).
- [ ] Test: a node searches a term another node holds, peer logs show only shard
      ids, never the token or the result.
- [ ] Each node gossips a few-bit YIELD vector (fraction of shard slice matching
      each token), dictionary-indexed, positive-only above a floor. NO absolute
      counts, NO block sizes disclosed. Dictionary is local/fixed, never on wire.
- **Stop condition (corrected):** the periodic global total (reconstructed from
  per-node LOCAL sketches; combination mechanism TBD) is a REAL, load-bearing
  TARGET that aims the search, NOT display-only and NOT optional — but it is NOT a
  stop gate. Stop is ONLY: fetched ≥ target AND saturation (new peers add ≤0 new
  K). You do NOT stop AT the target if new K still arrives, and do NOT stop BELOW
  it on saturation (counts almost never decrease → true total is higher, keep
  walking). Keep walking random nodes until both hold; DISK SLIDER is the hard
  backstop. This is the global count the search is built around.
- [ ] Each node gossips a 1-bit YIELD flag per dictionary token (threshold is a
      tunable, network-agreed constant: top 50% / 10% / 80% etc); flag = "worth
      fetching." NO absolute counts, NO block sizes. Dictionary local/fixed, never
      on wire. Skip below-threshold shards.
- [ ] Search UI shows a coverage bar: actual fetched count vs the periodic global
      total (reconstructed from per-node local sketches; combination mechanism
      TBD) as denominator; copy states best-effort coverage honestly.
      Primary small-network warning is bootstrap state (WO-058), not per-token.
- [ ] Tokenizer ships as a fixed space-aware scheme, **k=3** (minimizes gossip
      vector; per-fetch identity is off so identity-churn is not a k-driver).
      COMPILE-TIME constant, identical on all nodes, versioned per WO-060. Any k
      change is a developer release, never network-driven.
