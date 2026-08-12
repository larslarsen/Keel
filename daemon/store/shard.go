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
const shardSchemaVersion = 1

// tokenize splits text into every fixed k-length window of its normalized
// form (normalize below), sliding by one. Fixed size, not word-anchored:
// every token is exactly k characters, always, and space is an ordinary
// alphabet member rather than a marker bolted onto one token per word. A
// token is "space-aware" only in the sense that a window landing on a word
// boundary naturally contains a space and one that doesn't, doesn't — that
// difference falls out of sliding uniformly over space-including text, it is
// not special-cased.
//
// Because normalize pads the whole string with one leading and one trailing
// space, even a single-letter word produces at least one token at k=3 (a
// 1-letter word normalizes to 3 characters exactly: " x "), which is why
// TokenizeQuery below needs no fallback to a smaller k for short queries.
//
// Letters only inside a word: normalize collapses everything else (digits,
// punctuation, extra whitespace) to single-space separators, which keeps the
// vocabulary the size WO-059 measured rather than multiplying it for no
// privacy gain.
func tokenize(text string, k int) []string {
	if k <= 0 {
		return nil
	}
	norm := normalize(text)
	if len(norm) < k {
		return nil
	}
	out := make([]string, 0, len(norm)-k+1)
	for i := 0; i+k <= len(norm); i++ {
		out = append(out, norm[i:i+k])
	}
	return out
}

// normalize lowercases, collapses every run of non a-z runes to a single
// space, and pads the result with a leading and trailing space — so the
// first and last words get the same word-boundary information a window in
// the middle of the string gets for free. Empty (no letters at all) stays
// empty rather than becoming a lone space, so tokenize can tell "nothing to
// tokenize" apart from "one very short word".
//
// There is exactly one implementation of this, used by both title
// tokenization (ShardSlice, LocalShards) and query tokenization
// (TokenizeQuery) — titles and queries must pad identically, or a token
// computed for a query would never equal the token computed for the same
// text inside a title, and the whole scheme depends on that equality.
func normalize(s string) string {
	words := splitWords(s)
	if len(words) == 0 {
		return ""
	}
	return " " + strings.Join(words, " ") + " "
}

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

// TokenizeQuery tokenizes a whole search query at ShardK — always, with no
// fallback to a different k for short queries. ShardK is a versioned
// protocol constant (keyscheme.go, WO-060): every node tokenizes titles for
// ShardSlice at exactly ShardK, so a client that tokenized a query at some
// other k would compute shards no server ever populates at that width and
// silently find nothing. Because normalize pads the text, this is not the
// compromise it would be under a per-word scheme — see tokenize's doc
// comment for why even a one-letter query still yields a token at k=3.
func TokenizeQuery(query string) []string {
	return uniqueSorted(tokenize(query, ShardK))
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

// maxShardEntries bounds one shard reply, mirroring maxCatalogueRows — a
// shard on a large mirror can hold many videos, and an unbounded reply is a
// memory hazard and a way for one request to consume a node's upstream.
const maxShardEntries = 4096

// ShardSlice returns every (video, matched tokens) pair this node holds for
// one shard.
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
		for _, t := range tokenize(c.Title, ShardK) {
			if ShardOf(t) == shard {
				matched = append(matched, t)
			}
		}
		if len(matched) == 0 {
			continue
		}
		out = append(out, ShardEntry{VideoID: c.VideoID, Tokens: uniqueSorted(matched)})
		if len(out) >= maxShardEntries {
			break
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].VideoID < out[b].VideoID })
	return out, nil
}

// ShardPack is a shard reply signed as a unit — the WO-067 hardening layer
// on top of WO-059's bare ShardSlice. Same shape as CataloguePack, and for
// the same reason: entries here are votes about public facts (which tokens a
// title contains), not the requester's own data, so signing the whole bucket
// once is enough — no per-entry signature the way blocks carry, because a
// ShardEntry has no independent existence outside the bucket it was served
// in.
type ShardPack struct {
	SchemaVersion int          `json:"schema_version"`
	Shard         int          `json:"shard"`
	Entries       []ShardEntry `json:"entries"`

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
func canonicalShardPayload(entries []ShardEntry) ([]byte, error) {
	return json.Marshal(struct {
		Entries []ShardEntry `json:"entries"`
	}{entries})
}

func shardDigest(entries []ShardEntry) (string, error) {
	b, err := canonicalShardPayload(entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// BuildShardPack assembles and signs everything this node can serve for one
// shard. Mirrors BuildCataloguePack (catalogue.go) exactly.
func (s *Store) BuildShardPack(shard int, sources SourceSet, limit int) (*ShardPack, error) {
	entries, err := s.ShardSlice(shard, sources)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	pack := &ShardPack{
		SchemaVersion: shardSchemaVersion,
		Shard:         shard,
		Entries:       entries,
	}
	if pack.ContentSHA256, err = shardDigest(entries); err != nil {
		return nil, err
	}
	payload, err := canonicalShardPayload(entries)
	if err != nil {
		return nil, err
	}
	if pack.Signature, pack.PublicKey, err = s.signPayload(payload); err != nil {
		return nil, err
	}
	pack.Algorithm = signAlgorithm
	return pack, nil
}

// VerifyShardPack checks a pack's digest and, if present, its signature.
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
	digest, err := shardDigest(pack.Entries)
	if err != nil {
		return err
	}
	if digest != pack.ContentSHA256 {
		return fmt.Errorf("shard pack contents do not match its digest")
	}
	if pack.Signature != "" || pack.PublicKey != "" {
		payload, err := canonicalShardPayload(pack.Entries)
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
		for _, t := range tokenize(c.Title, ShardK) {
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
