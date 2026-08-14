// SPDX-License-Identifier: Apache-2.0
// Token shards for distributed search (WO-059).
//
// A search over peers' data cannot ask "who has videos matching my query" —
// asking that literally hands the query to whoever answers. So a query is
// tokenized into short, space-aware, fixed-size pieces (tokenize), each piece
// is hashed into one of ShardM shards (ShardOf), and a node fetches a whole
// shard from a peer rather than asking for a token. A shard groups thousands
// of tokens together, so a peer serving one learns only "this node wanted
// something in shard G" — never which token, because the token is never sent.
//
// This file builds the local side: turning this node's own titles into shards
// it can serve, and turning a query into the shards a search needs to fetch.
// The network side (serving a shard to a peer, fetching one, unioning results)
// is daemon/swarm.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/keel-app/keel/daemon/bridge"
)

// shardSchemaVersion is bumped when ShardPack's wire shape changes
// incompatibly, mirroring catalogueSchemaVersion.
//
// 2 (WO-097): a pack is now one bounded page of a logical response rather than
// the whole (silently truncated) bucket, so it carries its index and offset and
// its digest covers them.
const shardSchemaVersion = 2

// splitWords lowercases and splits on runs of non a-z runes.
func splitWords(s string) []string {
	s = strings.ToLower(s)
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return words
}

// TokenizeQuery returns the distinct discovery tokens for a whole search
// query under key scheme 2 — the fixed non-overlapping grid over the whole
// normalized string, with stopword-only chunks dropped. See queryplan.go for
// the rules and why the index side deliberately differs.
//
// ShardK is a versioned protocol constant (keyscheme.go, WO-060) and there is
// no fallback to a different k for short queries: every node generates its
// title windows at exactly ShardK, so a client using another width would
// compute shards no server ever populates and silently find nothing.
//
// A stopword-only query returns nothing here, which is the intended visible
// result — no discovery tokens means no peer contact and a local-only search,
// not a search that quietly failed.
func TokenizeQuery(query string) []string {
	return BuildQueryPlan(query).DiscoveryTokens()
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// ShardOf returns which shard a token groups into.
//
// Grouping fixes the rare-token leak that hashing the token's own key cannot:
// a rare token's bucket is small no matter what the key looks like, because
// the bucket's size is the number of videos containing that token. Landing
// many tokens in one shard changes the size — the peer sees "shard G", common
// by construction, never the token that put anything there (WO-059 attack #2).
func ShardOf(token string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(shardDomain + token))
	return int(h.Sum32() % uint32(ShardM))
}

// ShardEntry is one video's matched tokens within a single shard, as served
// to a peer or consumed locally.
type ShardEntry struct {
	VideoID string   `json:"video_id"`
	Tokens  []string `json:"tokens"`
}

// ShardSlice returns every (video, matched tokens) pair this node holds for
// one shard, in the canonical video-id order.
//
// `sources` follows catalogue.go's rule 2 exactly, which WO-084 rewrote: a
// serving node returns the complete shard over the corpus it actually holds,
// local titles and imported titles together. It must be the same SourceSet
// LocalShards announced under — a shard advertised over one corpus and served
// from another is a provider record that lies.
//
// A shard is still whole and still never a token: the requester asks for shard
// G and gets everything in it, so nothing here reintroduces a per-token path.
//
// There is no row cap any more (WO-097 §6). This used to stop at 4,096 while
// iterating an unordered map, so which rows a peer received was arbitrary and
// everything past the cap was permanently unreachable; sorting the survivors
// afterwards only made the arbitrary subset look deliberate. The bound now
// lives on the *reply* — bounded pages of one logical response, see
// ShardRows and paging.go — not on the dataset.
//
// Computed at request time from the same source SearchVideos and
// heldCatalogue already read, not a separate persisted table — mirrors
// buildBlock, which derives its reply from impressions/peer_edges on the fly
// rather than keeping a redundant index in sync. At a few thousand titles
// this costs microseconds.
func (s *Store) ShardSlice(shard int, sources SourceSet) ([]ShardEntry, error) {
	if shard < 0 || shard >= ShardM {
		return nil, fmt.Errorf("shard %d out of range [0,%d)", shard, ShardM)
	}
	all, err := s.heldCatalogue(sources)
	if err != nil {
		return nil, err
	}
	out := []ShardEntry{}
	for _, c := range all {
		if c.Title == "" {
			continue
		}
		var matched []string
		for _, t := range TitleTokens(c.Title) {
			if ShardOf(t) == shard {
				matched = append(matched, t)
			}
		}
		if len(matched) == 0 {
			continue
		}
		out = append(out, ShardEntry{VideoID: c.VideoID, Tokens: uniqueSorted(matched)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].VideoID < out[b].VideoID })
	return out, nil
}

// ShardRows is ShardSlice rotated to the traversal offset a request nonce
// selects — the row order one logical response walks.
//
// The rotation is what stops every partial-budget traversal from returning the
// same first rows forever (WO-097 §6). A traversal that runs to completion
// returns the same set whatever the nonce was; only the order changes, and the
// caller reassembles by video id regardless.
func (s *Store) ShardRows(shard int, sources SourceSet, nonce uint64) ([]ShardEntry, int, error) {
	all, err := s.ShardSlice(shard, sources)
	if err != nil {
		return nil, 0, err
	}
	offset := PageStart(len(all), nonce)
	out := make([]ShardEntry, 0, len(all))
	for _, i := range rotate(len(all), offset) {
		out = append(out, all[i])
	}
	return out, offset, nil
}

// ShardPack is a shard reply signed as a unit — the WO-067 hardening layer
// on top of WO-059's bare ShardSlice. Same shape as CataloguePack, and for
// the same reason: entries here are votes about public facts (which tokens a
// title contains), not the requester's own data, so signing the whole bucket
// once is enough — no per-entry signature the way blocks carry, because a
// ShardEntry has no independent existence outside the bucket it was served
// in.
type ShardPack struct {
	Kind          string       `json:"t"`
	SchemaVersion int          `json:"schema_version"`
	Shard         int          `json:"shard"`
	// Index is this page's position in the logical response, and Offset is
	// where it starts in the provider's rotated ordering. Both are covered by
	// the digest, so a reordered or duplicated frame cannot pass as another.
	Index   int          `json:"index"`
	Offset  int          `json:"offset"`
	Entries []ShardEntry `json:"entries"`

	ContentSHA256 string `json:"content_sha256"`
	Signature     string `json:"signature,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	Algorithm     string `json:"signature_alg,omitempty"`
}

// canonicalShardPayload is the exact byte sequence a ShardPack's digest and
// signature cover — a shard-typed twin of bundle.go's canonicalPayload,
// which is hardcoded to (CatalogueEntry, EdgeObservation) and can't be
// reused here directly. ShardSlice already returns entries sorted by
// VideoID with each Tokens slice deduped and sorted, so this needs no
// re-sorting to be deterministic across an unchanged corpus.
//
// Index and shard are inside the payload (WO-097 §6): a page's signature has
// to bind it to its position in the response, or a peer could replay page 0
// as page 3 and the terminal's digest list would still line up.
func canonicalShardPayload(shard, index int, entries []ShardEntry) ([]byte, error) {
	return json.Marshal(struct {
		Shard   int          `json:"shard"`
		Index   int          `json:"index"`
		Entries []ShardEntry `json:"entries"`
	}{shard, index, entries})
}

func shardDigest(shard, index int, entries []ShardEntry) (string, error) {
	b, err := canonicalShardPayload(shard, index, entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// SignShardPage assembles and signs one bounded page of a logical shard
// response. Mirrors SignCataloguePage (catalogue.go) exactly.
func (s *Store) SignShardPage(shard, index, offset int, entries []ShardEntry) (*ShardPack, error) {
	if entries == nil {
		entries = []ShardEntry{}
	}
	pack := &ShardPack{
		Kind:          "page",
		SchemaVersion: shardSchemaVersion,
		Shard:         shard,
		Index:         index,
		Offset:        offset,
		Entries:       entries,
	}
	var err error
	if pack.ContentSHA256, err = shardDigest(shard, index, entries); err != nil {
		return nil, err
	}
	payload, err := canonicalShardPayload(shard, index, entries)
	if err != nil {
		return nil, err
	}
	if pack.Signature, pack.PublicKey, err = s.signPayload(payload); err != nil {
		return nil, err
	}
	pack.Algorithm = signAlgorithm
	return pack, nil
}

// VerifyShardPack checks a page's digest and, if present, its signature.
//
// An unsigned pack is accepted here (matches ImportCataloguePack's policy —
// refusing one would break interop with any future build that ships
// unsigned for some reason), but the caller decides what "unsigned" is worth:
// FetchShard treats a signed-and-internally-consistent claim as more
// trustworthy than an unsigned one when two peers disagree about a video.
func VerifyShardPack(pack *ShardPack) error {
	if pack.SchemaVersion > shardSchemaVersion {
		return fmt.Errorf("shard pack schema %d is newer than this build understands (%d)",
			pack.SchemaVersion, shardSchemaVersion)
	}
	digest, err := shardDigest(pack.Shard, pack.Index, pack.Entries)
	if err != nil {
		return err
	}
	if digest != pack.ContentSHA256 {
		return fmt.Errorf("shard pack contents do not match its digest")
	}
	if pack.Signature != "" || pack.PublicKey != "" {
		payload, err := canonicalShardPayload(pack.Shard, pack.Index, pack.Entries)
		if err != nil {
			return err
		}
		if err := verifyPayload(payload, pack.Signature, pack.PublicKey); err != nil {
			return fmt.Errorf("shard pack: %w", err)
		}
	}
	return nil
}

// LocalShards lists the shards this node holds anything in — what gets
// announced, mirroring LocalPrefixes/LocalCataloguePrefixes. ShardM is small
// (256), so this is cheap even though it retokenizes every held title.
func (s *Store) LocalShards(sources SourceSet) ([]int, error) {
	all, err := s.heldCatalogue(sources)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	for _, c := range all {
		for _, t := range TitleTokens(c.Title) {
			seen[ShardOf(t)] = true
		}
	}
	out := make([]int, 0, len(seen))
	for sh := range seen {
		out = append(out, sh)
	}
	sort.Ints(out)
	return out, nil
}

// TitlesFor resolves a set of video ids to search hits, for videos found
// through peer search that may not be in the local catalogue's search index
// under any query term. Mirrors SearchVideos' row shape (impressions ∪
// peer_catalogue, local observations ranked first) but keyed by explicit id
// rather than a LIKE match.
//
// Every id in the input comes back, titled or not — an id with no locally
// known title still gets an entry with an empty Title rather than being
// dropped. A real find with no title beats silently discarding it, and
// deliberately does NOT trigger a live catalogue fetch to fill the gap:
// fetching catalogue buckets for exactly the ids a search matched would bind
// that fetch pattern to the query, the same correlation
// MissingCataloguePrefixes' doc comment warns against (it requires a whole
// graph bucket's targets, never a search result's subset). See WO-067.
func (s *Store) TitlesFor(ids []string) ([]bridge.SearchHit, error) {
	uniq := uniqueSorted(ids)
	if len(uniq) == 0 {
		return nil, nil
	}
	out := make(map[string]bridge.SearchHit, len(uniq))
	const chunk = 400
	for start := 0; start < len(uniq); start += chunk {
		end := start + chunk
		if end > len(uniq) {
			end = len(uniq)
		}
		batch := uniq[start:end]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		rows, err := s.db.Query(`
SELECT video_id, title, channel_id, duration_s, view_count, published_at, seen, last_seen FROM (
  SELECT video_id,
         MAX(title)        AS title,
         MAX(channel_id)   AS channel_id,
         MAX(duration_s)   AS duration_s,
         MAX(view_count)   AS view_count,
         MAX(published_at) AS published_at,
         COUNT(*)          AS seen,
         MAX(observed_at)  AS last_seen
  FROM impressions
  WHERE video_id IN (`+ph+`)
  GROUP BY video_id
  UNION ALL
  SELECT video_id, title, channel_id, duration_s, view_count, published_at, 0 AS seen, 0 AS last_seen
  FROM peer_catalogue
  WHERE video_id IN (`+ph+`)
    AND video_id NOT IN (SELECT video_id FROM impressions)
)`, append(append([]any{}, args...), args...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h bridge.SearchHit
			var title sql.NullString
			var ch, pub sql.NullString
			var dur, views sql.NullFloat64
			if err := rows.Scan(&h.VideoID, &title, &ch, &dur, &views, &pub, &h.Seen, &h.LastSeenAt); err != nil {
				rows.Close()
				return nil, err
			}
			h.Title = title.String
			if ch.Valid {
				v := ch.String
				h.ChannelID = &v
			}
			if pub.Valid {
				v := pub.String
				h.PublishedAt = &v
			}
			if dur.Valid {
				v := dur.Float64
				h.DurationS = &v
			}
			if views.Valid {
				v := views.Float64
				h.ViewCount = &v
			}
			out[h.VideoID] = h
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	hits := make([]bridge.SearchHit, 0, len(uniq))
	for _, id := range uniq {
		if h, ok := out[id]; ok {
			hits = append(hits, h)
		} else {
			hits = append(hits, bridge.SearchHit{VideoID: id})
		}
	}
	return hits, nil
}
