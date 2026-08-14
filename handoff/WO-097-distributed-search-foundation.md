# WO-097 — Complete the distributed-search index and word-target foundation

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Done** 2026-08-13 — every acceptance box below is covered by a test; see the implementation note at the end |
| **Date** | 2026-08-13 |
| **Unblocks** | WO-095 — responsive streaming peer search and UI |
| **Source** | Architecture interview following the live multi-token search failure |

## Outcome

Distributed search gets a complete, versioned foundation before its UI is made
streaming:

1. one canonical continuous query tokenizer;
2. an inverted-index generation rule that can find those query tokens at every
   title alignment without indexing useless stopword-only occurrences;
3. bounded pagination that never makes rows beyond 4,096 permanently
   unreachable;
4. retained global word telemetry that supplies an immediate, frozen search
   target without a plaintext word database; and
5. a daemon-owned query plan and local matcher that implement the settled
   any-order, quote, and normalized-word behavior.

This is repair of incomplete existing search infrastructure, not a new feature.
Do not build UI streaming in this order.

## 1. Canonical continuous query plan

Normalize the whole query once:

1. lowercase;
2. replace every run of non-`a`–`z` characters with one space;
3. trim leading and trailing separators;
4. pad the end of the whole normalized string with spaces to a multiple of
   `ShardK`; and
5. cut fixed, non-overlapping `ShardK` chunks at offsets `0, 3, 6, ...`.

Examples:

```text
world  -> [wor] [ld ]
a big  -> [a b] [ig ]
```

Spaces are consumed characters. The tokenizer never restarts at a word
boundary and never creates sliding query windows. Preserve occurrences in
character order for rendering, while fetching each distinct token value once.

Construct token ranges before display-word ranges. Intersecting those ranges
produces the render fragments used by WO-095, including one color across both
words when a token spans a space.

## 2. Title index coverage is deliberately different

The query uses one fixed grid, but a query substring can begin at any character
offset in a title. When generating a title's token-shard entries, normalize the
title the same way, append `ShardK-1` spaces at its tail, and generate every
overlapping `ShardK` window whose start lies in the unpadded normalized title
(equivalently, all three fixed-grid alignments for `ShardK=3`). Deduplicate
`(video_id, token)` membership before assigning token shards.

This is inverted-index coverage only. The extra alignments must never become
extra query tokens, peer requests, colors, or bars.

Required example: query `world -> [wor, ld ]` finds `the world today` even
though `world` begins off the title's offset-zero grid.

## 3. Filter stopword occurrences while generating the index

The giant-shard problem is primarily an indexing problem. Do not create a
special distributed structure merely to search stopword-only text such as
`is is`.

During normalized-title scanning, retain a window only when its non-space
characters overlap at least one non-stopword word occurrence. This is an
**occurrence rule**, not a banned-token-string list:

- discard windows produced solely by occurrences of stopwords;
- retain windows inside every meaningful word, including common fragments;
- retain boundary windows that touch a meaningful word, even when they also
  touch a stopword or space; and
- retain a token string such as `the` when it occurs inside a meaningful word
  such as `theory`.

Apply the same occurrence test to the query plan when choosing discovery
tokens. A token whose covered letters belong only to stopword occurrences is
not fetched. Stopwords remain in the daemon's final local matcher, so a
meaningful-word result must still satisfy them. A stopword-only query performs
local search only and visibly has no distributed discovery work.

Do not filter all common fragments. Meaningful words may still generate large
shards, so pagination remains required.

## 4. Preserve query behavior in a daemon-owned matcher

Represent the current search semantics explicitly in the query plan:

- unquoted normalized words all required in any order;
- quoted text required as an exact adjacent normalized phrase; and
- normalized word boundaries respected, so `world` matches `the world today`
  and `world-star` but is not required to match `worldwide`.

The daemon applies this matcher to candidate titles after catalogue/string
resolution. Token-shard membership is only a way to discover candidates and
must never become the final semantic test.

Keep the internal plan capable of carrying a later exact-versus-any-order mode,
but do not add a UI control or change the default in this order.

## 5. Key-scheme compatibility fence

Deployed scheme 1 generates different token data while claiming the intended
scheme number. Corrected nodes must not silently mix their token-derived data
with it.

- Bump `KeySchemeVersion` to 2.
- Version token-shard provider keys and protocol ids, yield topics/vectors, and
  token-sketch topics/packs through the existing scheme boundary.
- Preserve scheme-1 literal golden vectors as the deployed record and add
  scheme-2 vectors for the corrected rules.
- Scheme-2 searches exchange no token-derived data with scheme 1.

Graph and catalogue source rows remain reusable and are re-derived. No local
observation migration or deletion is required.

`KeySchemeVersion` is currently one swarm-wide compatibility fence, so this
bump also makes scheme-1 and scheme-2 binaries report themselves incompatible
for the other scheme-versioned swarm protocols during rollout, even though the
underlying graph/catalogue derivations did not change. That temporary network
partition is the explicit cost of refusing silently incompatible shard data;
it must appear in update diagnostics and release/live-QA planning. Do not
quietly add dual token service under the old scheme number.

## 6. Replace silent truncation with bounded logical responses

`ShardSlice` and catalogue pack generation currently stop at 4,096 rows. In the
shard path, iteration can select an arbitrary subset before sorting. Neither
path has a continuation, so later rows are permanently unreachable. Sorting
the truncated subset does not repair this.

For both token shards and catalogue/string prefix buckets:

- one request names the existing broad logical bucket, never a token, candidate
  id, or narrower string key;
- the provider sends a header followed by bounded page frames on that logical
  response stream and an authenticated terminal frame;
- each page is deterministically ordered, independently bounded, signed or
  covered by the response signature, and includes enough position/terminal
  metadata to detect gaps, duplicates, reordering, and truncation;
- the requester validates and combines all accepted pages as one peer response;
  and
- resource-budget termination is explicit `incomplete`, never success with a
  silent prefix.

If the existing libp2p handler cannot safely emit multiple frames, an opaque
continuation cursor may be used internally, but it remains part of one logical
broad-bucket operation. It must not narrow the requested dataset or surface as
a new UI token/bar.

Choose the first-page traversal offset from a request nonce over a stable
video-id ordering so repeated partial-budget searches do not always privilege
the same first 4,096 rows. The nonce and cursor carry no token/title/id. A full
traversal returns the same set regardless of order.

For catalogue/string prefixes, complete the logical broad-bucket traversal
even after the wanted candidate is encountered; stopping exactly on the wanted
row would turn pagination into a narrower observable request. Coalesce all
candidates in the same prefix into one traversal. Existing serving byte/rate
limits and the user's disk/network budget remain hard bounds.

## 7. Retain a global word-telemetry snapshot

The existing `WordTelemetry` already contains the required structures:

- a word HLL estimates global distinct vocabulary size;
- a graph/video HLL estimates global corpus size; and
- a Count-Min sketch estimates the number of distinct videos containing a
  supplied normalized word.

No plaintext dictionary or database row per word is needed. HLL cannot answer
a supplied word's frequency by itself; hash that word into the retained CMS.
The sketches can report approximate total vocabulary/corpus size and counts for
words supplied by a local feature. They cannot produce a top-words list without
a separate candidate vocabulary, which this order must not create.

Retain the latest accepted **refresh snapshot**, not an ever-growing merge:

1. Level 2+ periodically and on demand directly fetches current word packs from
   eligible peers.
2. Include a fresh local pack in the round without sending it anywhere. Build a
   fresh aggregate from that pack and one accepted pack per responding peer;
   preserve the existing pack validation and median poison filter. Atomically
   replace the prior retained snapshot only when the round is valid.
3. Persist the last valid aggregate and its age so restart/search can use it
   immediately. Never cumulatively add the same pack across refresh rounds;
   CMS addition is not idempotent.
4. Search reads a fast immutable snapshot and never waits for refresh.

Level 1 may fetch and retain these public aggregate sketches for future corpus
statistics, but remains receive-only under WO-089: it never serves or relays a
local or cached word pack. Level 2+ also serves only the original aggregate
authorized by WO-089; do not invent sketch gossip or cached-pack relay.

## 8. Estimate overlap without a word dictionary

Peer catalogues overlap, so summing per-peer CMS counters overcounts. Retain
both the raw merged estimate and an overlap-adjusted estimate for each locally
supplied word.

For each refresh round, compute the average duplication factor from the word
packs already in hand:

```text
duplication factor = sum(source graph-HLL estimates) / merged graph-HLL estimate
adjusted word count = summed CMS estimate / duplication factor
```

Here, sources include the fresh local pack and every accepted peer pack in the
round.

Clamp the factor to at least 1 and record when it is unavailable. This is an
average correction, not a claim that every word has identical overlap. CMS
collision error also biases upward.

Expose the rounded-up adjusted estimate as the search target together with
`known`, snapshot age, and an uncertainty marker. Retain the raw estimate for
diagnostics/corpus-stat presentation, not as a mandatory search target: on
mirrored corpora it can be impossible to reach. WO-095's saturation requirement
is the second guard against stopping on an underestimated target, and its UI
allows actual counts to exceed 100%.

Freeze the chosen target when a search starts. A refresh never changes an
active search's denominator.

## 9. APIs supplied to WO-095

The foundation exposes daemon-internal operations for:

- canonical query plan plus token-first render fragments;
- eligible discovery-token ids and the words each token can advance;
- local full-query title matching;
- complete paged token-shard responses;
- complete broad catalogue/string-prefix resolution, coalesced by prefix; and
- an immutable per-word target snapshot with known/unknown and age.

These APIs return explicit complete/incomplete/error states. They do not log or
persist raw query text. The raw query and parsed plan live only in bounded
memory for the active local request/search.

## Privacy and scope

- Never persist or log a raw/normalized query or token text (`DESIGN_v2.md`
  §2.1 and §4.2).
- Pagination must not turn a broad token shard or string prefix into a request
  for one token, candidate, title, or video id.
- Do not change Level-1 outbound behavior, Level-2 reciprocal search, Live,
  graph-block serving, contribution copy, or consent.
- Do not add browser permissions, browser storage, a dependency, framework,
  bundler, or build step.
- Do not build a vocabulary table or special stopword search index.

## Implementation boundaries

| Area | Owner |
|---|---|
| Scheme-2 normalization, query plan, title-window generation | `daemon/store` |
| Stopword occurrence spans and full-query matcher | `daemon/store` |
| Token-shard logical pagination | daemon store + swarm shard protocol |
| Catalogue/string logical pagination | daemon store + swarm catalogue protocol |
| Refresh-round collection and Level policy | daemon swarm/runtime |
| Retained telemetry snapshot | daemon store/runtime |
| Render-plan protocol shape | daemon bridge protocol; consumed later by WO-095 |

Do not add the WO-095 asynchronous job or UI in this order.

## Acceptance

- [x] Golden vectors prove `world -> [wor, ld ]` and
      `a big -> [a b, ig ]`; only the whole normalized tail is padded.
- [x] Query tokenization is one fixed pass; title generation uses every
      alignment. `world` finds `the world today` without extra query tokens.
- [x] Stopword-only occurrences generate no shard entries; boundary windows
      touching meaningful words remain; `the` inside `theory` is not removed by
      a token-string blacklist.
- [x] Stopword-only queries remain local; mixed queries still enforce their
      stopwords in the final matcher.
- [x] Meaningful windows remain indexed even when this client does not choose
      them for a particular query.
- [x] Scheme 1 and scheme 2 token-derived namespaces cannot mix; source
      catalogue/graph rows require no migration.
- [x] A token shard and a catalogue prefix containing more than 4,096 rows are
      completely reachable and deterministically deduplicated across pages.
- [x] Partial pagination is explicit and varying request nonces do not always
      return the same first subset.
- [x] Resolving a candidate downloads the whole logical broad string prefix;
      page requests reveal no candidate id or early-found stopping point.
- [x] Re-fetching unchanged word packs replaces the retained round instead of
      doubling CMS counts.
- [x] Tests with disjoint and mirrored peer catalogues exercise raw and
      overlap-adjusted counts; mirrored peers do not create an unreachable
      search target.
- [x] A search target is available from the retained snapshot without network
      I/O, is frozen per search, can be unknown, and carries snapshot age.
- [x] Level 1 can receive/fetch a word snapshot but never answers or relays one.
- [x] No plaintext word dictionary, raw-query persistence/logging, new browser
      storage, permission, or dependency is introduced.

## Stop conditions for the implementer

Return to architecture review if implementation appears to require:

- narrowing a paged request to the candidate or token being sought;
- dropping rows beyond a fixed cap while reporting success;
- a word dictionary/top-words database;
- cumulative merging of retained CMS packs across refresh rounds;
- Level-1 outbound data;
- changing the settled exact/any-order/normalized-word matching behavior; or
- extension-side indexing, target calculation, or query persistence.

---

## Implementation note (Sr Dev, 2026-08-13)

Landed as specified. No stop condition was hit and no clause needed
renegotiating. Where the work is:

| Requirement | Where |
|---|---|
| §1 canonical query plan, render fragments | `daemon/store/queryplan.go` |
| §2 every-alignment title windows | `TitleTokens`, same file |
| §3 stopword *occurrence* filter | `meaningfulMask`/`spanIsMeaningful`, same file |
| §4 daemon-owned matcher | `QueryPlan.MatchTitle`, same file |
| §5 scheme-2 fence | `daemon/store/keyscheme.go`, `keelTopic`, protocol 2.0.0 |
| §6 bounded logical responses | `daemon/store/paging.go`, `daemon/swarm/paging.go` |
| §7 retained refresh snapshot | `daemon/store/wordsnapshot.go`, `Node.RefreshWordSnapshot` |
| §8 overlap adjustment | `BuildWordSnapshot`, `WordSnapshot.Target` |
| §9 API surface | `BuildQueryPlan`, `DiscoveryTokens`, `WordsAdvancedByToken`, `MatchTitle`, `fetchShardPages`, `ResolveCandidateTitles`, `WordTargets` |

Three things worth flagging to the architect rather than leaving in the diff:

1. **The old tokenizer is deleted, not kept behind a flag.** §5 asks for the
   scheme-1 golden vectors to be preserved as the deployed record, which is
   done — but as a *data table* in `keyscheme_test.go`, not as a live
   `tokenizeScheme1()`. Keeping dead production code to satisfy a test would
   have re-created exactly the dual-service path §5 forbids.

2. **Every-alignment indexing is deliberately over-broad, and the matcher is
   what pays for it.** Scheme 1 could not produce the token `men` from
   `recommendation`; scheme 2 does, because the letters really are there at
   offsets 5–7. `TestIndexCoverageIsBroaderThanTheMatcher` pins both halves,
   because the day someone "optimizes" by trusting shard membership, a search
   for `men` starts returning `recommendation`.

3. **An explicitly incomplete peer traversal no longer feeds the saturation
   streak.** §6 does not say this outright, but a peer that stopped on its own
   budget still holds rows it did not send, so counting its partial answer as
   a miss would let rate-limiting read as "the network is exhausted" — the
   silent-truncation failure in a different costume. `FetchShard` treats the
   two states separately.

The `>4,096 rows` acceptance line is tested at store level with a 4,200-row
shard and a 9,000-row catalogue bucket. A 12-bit prefix cannot be made to hold
4,096 rows without millions of videos, so that test widens the bucket to 1 bit;
the prefix arithmetic is width-independent and the property under test is the
absence of a cap. The network round trip is proven separately at multi-page
sizes over a real libp2p stream.
