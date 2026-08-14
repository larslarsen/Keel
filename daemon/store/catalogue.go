// SPDX-License-Identifier: Apache-2.0
// Catalogue sync (WO-052, DESIGN_BOOTSTRAP §1 and the sync design note).
//
// Blocks are stringless, so titles arrive on their own path. This is that path.
//
// Two rules shape everything here, and both are easy to violate with a change
// that looks like an optimisation:
//
//  1. **Whole-bucket derivation.** A node asks for catalogue buckets covering
//     every target in a graph bucket it fetched — never only the targets of the
//     block it actually wanted. Fetching just what is needed would make the
//     catalogue request pattern identify the block of interest, undoing the
//     prefix anonymity the graph fetch paid for. The request set must stay a
//     function of the graph bucket's public contents.
//
//  2. **Serve the whole held bucket, from one source set (WO-084).** Level 2
//     serves the complete eligible contents of each catalogue bucket it
//     advertises: rows derived from this node's own observations *and* rows
//     imported from peers, merged into one set with nothing marking which is
//     which. This rule used to say the opposite — mirrored rows only — on the
//     reasoning that a requester asking for a bucket sees exactly which of its
//     members this node holds. That is true and it is the disclosure Level 2
//     accepts: a 12-bit bucket covers thousands of videos, the request and the
//     answer are both whole buckets, and a row is public video metadata, not an
//     observation. What must not happen is the two halves diverging — the
//     catalogue, shard, yield and sketch paths all read heldCatalogue with the
//     same SourceSet, so a provider record can never name material the
//     corresponding stream refuses to return.
//
// The catalogue converges where the graph does not. A video's title, channel,
// duration and upload date are written once and never change, so a node that
// holds a row never asks again and steady-state catalogue traffic tends to zero.
// That convergence is what makes rule 1 affordable.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/keel-app/keel/daemon/bridge"
)

// catalogueSchemaVersion is bumped when CataloguePack's wire shape changes
// incompatibly.
//
// 2 (WO-097): a pack is now one bounded page of a logical prefix response
// rather than the whole bucket capped at 4,096 rows, so it carries its index
// and offset and its digest covers them.
const catalogueSchemaVersion = 2

// CataloguePrefix buckets a video for catalogue lookup.
//
// A separate namespace from BlockPrefix so the two datasets bucket
// independently. Sharing the namespace would mean a node's catalogue requests
// and graph requests landed on correlated buckets, which hands an observer a
// join between the two.
func CataloguePrefix(videoID string, bits int) string {
	if bits <= 0 || bits > 64 {
		bits = DefaultPrefixBits
	}
	sum := sha256.Sum256([]byte(catalogueDomain + videoID))
	nbytes := (bits + 7) / 8
	buf := make([]byte, nbytes)
	copy(buf, sum[:nbytes])
	if rem := bits % 8; rem != 0 {
		buf[nbytes-1] &= byte(0xff << (8 - rem))
	}
	return fmt.Sprintf("%d:%s", bits, hex.EncodeToString(buf))
}

// CataloguePack is one bounded page of a logical prefix response, signed as a
// unit. See paging.go for the framing it belongs to.
type CataloguePack struct {
	Kind          string `json:"t"`
	SchemaVersion int    `json:"schema_version"`
	Prefix        string `json:"prefix"`
	// Index is this page's position in the logical response, and Offset is
	// where it starts in the provider's rotated ordering. Both are covered by
	// the digest, so a reordered or duplicated frame cannot pass as another.
	Index   int                     `json:"index"`
	Offset  int                     `json:"offset"`
	Entries []bridge.CatalogueEntry `json:"entries"`

	ContentSHA256 string `json:"content_sha256"`
	Signature     string `json:"signature,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	Algorithm     string `json:"signature_alg,omitempty"`
}

// heldCatalogue returns the rows `sources` selects, honouring rule 2.
//
// Genuinely a union when both are selected, which the `mirrorOnly bool` this
// replaced could not express: it read peer_catalogue *or* CatalogueEntries, so
// the non-mirror branch silently dropped every imported row. Rows are merged by
// video id with non-empty fields winning, because a catalogue row is a public
// fact about a public video — two sources holding it is agreement, not a
// conflict to arbitrate (ImportCataloguePack makes the same call).
func (s *Store) heldCatalogue(sources SourceSet) ([]bridge.CatalogueEntry, error) {
	merged := map[string]bridge.CatalogueEntry{}
	add := func(c bridge.CatalogueEntry) {
		prev, seen := merged[c.VideoID]
		if !seen {
			merged[c.VideoID] = c
			return
		}
		if prev.Title == "" {
			prev.Title = c.Title
		}
		if prev.ChannelID == nil {
			prev.ChannelID = c.ChannelID
		}
		if prev.DurationS == nil {
			prev.DurationS = c.DurationS
		}
		if prev.ViewCount == nil {
			prev.ViewCount = c.ViewCount
		}
		if prev.PublishedAt == nil {
			prev.PublishedAt = c.PublishedAt
		}
		merged[c.VideoID] = prev
	}

	if sources.Peers {
		rows, err := s.db.Query(`
SELECT video_id, COALESCE(title,''), channel_id, duration_s, view_count, published_at
FROM peer_catalogue`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c bridge.CatalogueEntry
			if err := rows.Scan(&c.VideoID, &c.Title, &c.ChannelID,
				&c.DurationS, &c.ViewCount, &c.PublishedAt); err != nil {
				rows.Close()
				return nil, err
			}
			add(c)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if sources.Local {
		local, err := s.CatalogueEntries()
		if err != nil {
			return nil, err
		}
		for _, c := range local {
			add(c)
		}
	}

	out := make([]bridge.CatalogueEntry, 0, len(merged))
	for _, c := range merged {
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].VideoID < out[b].VideoID })
	return out, nil
}

// LocalCataloguePrefixes lists the catalogue buckets this node can serve.
func (s *Store) LocalCataloguePrefixes(bits int, sources SourceSet) ([]string, error) {
	all, err := s.heldCatalogue(sources)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, c := range all {
		seen[CataloguePrefix(c.VideoID, bits)] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// CatalogueRows returns every row this node holds in one prefix bucket, in the
// canonical video-id order, rotated to the traversal offset a request nonce
// selects.
//
// There is no row cap (WO-097 §6). BuildCataloguePack used to stop at 4,096
// with no continuation, so a busy prefix had rows nothing could ever fetch. The
// bound now lives on the reply — bounded pages of one logical response, see
// paging.go — never on the dataset, and a traversal cut short by the serving
// budget says so on its terminal frame instead of returning a silent prefix.
func (s *Store) CatalogueRows(prefix string, sources SourceSet, nonce uint64) ([]bridge.CatalogueEntry, int, error) {
	bits, ok := PrefixOf(prefix)
	if !ok {
		return nil, 0, fmt.Errorf("malformed prefix %q", prefix)
	}
	all, err := s.heldCatalogue(sources)
	if err != nil {
		return nil, 0, err
	}
	rows := []bridge.CatalogueEntry{}
	for _, c := range all {
		if CataloguePrefix(c.VideoID, bits) != prefix {
			continue
		}
		rows = append(rows, c)
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].VideoID < rows[b].VideoID })

	offset := PageStart(len(rows), nonce)
	out := make([]bridge.CatalogueEntry, 0, len(rows))
	for _, i := range rotate(len(rows), offset) {
		out = append(out, rows[i])
	}
	return out, offset, nil
}

// canonicalCataloguePagePayload is what a page's digest and signature cover.
// Prefix and index are inside it so a page cannot be replayed at another
// position in the response — same reasoning as canonicalShardPayload.
func canonicalCataloguePagePayload(prefix string, index int, entries []bridge.CatalogueEntry) ([]byte, error) {
	body, err := canonicalPayload(entries, nil)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Prefix string          `json:"prefix"`
		Index  int             `json:"index"`
		Body   json.RawMessage `json:"body"`
	}{prefix, index, body})
}

// SignCataloguePage assembles and signs one bounded page of a logical prefix
// response. Mirrors SignShardPage (shard.go) exactly.
func (s *Store) SignCataloguePage(prefix string, index, offset int, entries []bridge.CatalogueEntry) (*CataloguePack, error) {
	if entries == nil {
		entries = []bridge.CatalogueEntry{}
	}
	pack := &CataloguePack{
		Kind:          "page",
		SchemaVersion: catalogueSchemaVersion,
		Prefix:        prefix,
		Index:         index,
		Offset:        offset,
		Entries:       entries,
	}
	payload, err := canonicalCataloguePagePayload(prefix, index, entries)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	pack.ContentSHA256 = hex.EncodeToString(sum[:])
	if pack.Signature, pack.PublicKey, err = s.signPayload(payload); err != nil {
		return nil, err
	}
	pack.Algorithm = signAlgorithm
	return pack, nil
}

// VerifyCataloguePack checks a page's digest and, when present, its signature,
// without importing anything. ImportCataloguePack calls it and then merges.
func VerifyCataloguePack(pack *CataloguePack) error {
	if pack.SchemaVersion > catalogueSchemaVersion {
		return fmt.Errorf("catalogue schema %d is newer than this build understands (%d)",
			pack.SchemaVersion, catalogueSchemaVersion)
	}
	payload, err := canonicalCataloguePagePayload(pack.Prefix, pack.Index, pack.Entries)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != pack.ContentSHA256 {
		return fmt.Errorf("catalogue pack contents do not match its digest")
	}
	if pack.Signature != "" || pack.PublicKey != "" {
		if err := verifyPayload(payload, pack.Signature, pack.PublicKey); err != nil {
			return fmt.Errorf("catalogue pack: %w", err)
		}
	}
	return nil
}

// Encode renders a pack for transport.
func (p *CataloguePack) Encode() ([]byte, error) { return json.Marshal(p) }

// ImportCataloguePack verifies a pack and merges it.
//
// Merging is additive and idempotent: a catalogue row is a public fact about a
// public video, so the same row arriving from two peers is agreement rather
// than duplication. Unlike edges, there is nothing here to double-count.
func (s *Store) ImportCataloguePack(raw []byte) (int, error) {
	var pack CataloguePack
	if err := json.Unmarshal(raw, &pack); err != nil {
		return 0, fmt.Errorf("catalogue pack is not valid JSON: %w", err)
	}
	if err := VerifyCataloguePack(&pack); err != nil {
		return 0, err
	}
	return s.ImportCatalogueEntries(pack.Entries, pack.PublicKey)
}

// ImportCatalogueEntries merges already-verified rows.
//
// Split out from ImportCataloguePack because a paged response is verified
// frame by frame and combined into one peer answer before anything is written
// (WO-097 §6) — the requester must be able to reject a response whose terminal
// does not match its pages, and that decision happens above the row merge.
func (s *Store) ImportCatalogueEntries(entries []bridge.CatalogueEntry, publicKey string) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	source := publicKey
	if source == "" {
		source = "unsigned"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
INSERT INTO peer_catalogue(video_id, title, channel_id, duration_s, view_count, published_at, source)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(video_id) DO UPDATE SET
  title = CASE WHEN excluded.title != '' THEN excluded.title ELSE peer_catalogue.title END,
  channel_id = COALESCE(excluded.channel_id, peer_catalogue.channel_id),
  duration_s = COALESCE(excluded.duration_s, peer_catalogue.duration_s),
  view_count = COALESCE(excluded.view_count, peer_catalogue.view_count),
  published_at = COALESCE(excluded.published_at, peer_catalogue.published_at)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	n := 0
	for _, c := range entries {
		if c.VideoID == "" {
			continue
		}
		if _, err := stmt.Exec(c.VideoID, c.Title, c.ChannelID,
			c.DurationS, c.ViewCount, c.PublishedAt, source); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// MissingCataloguePrefixes returns the buckets needed to label a set of videos.
//
// Callers must pass every target in a whole graph bucket, not the targets of one
// block — see rule 1 at the top of this file. The function cannot enforce that,
// so the caller carries it.
//
// Videos already held are skipped, which is what makes catalogue traffic
// converge to nothing. That does make the request set depend on what this node
// already has, but only at bucket granularity and only over data it previously
// fetched in whole buckets, so it narrows nothing an observer did not see.
func (s *Store) MissingCataloguePrefixes(videoIDs []string, bits int) ([]string, error) {
	if len(videoIDs) == 0 {
		return nil, nil
	}
	want := map[string]bool{}
	for _, id := range videoIDs {
		if id != "" {
			want[id] = true
		}
	}

	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	// Chunked so a large bucket does not exceed SQLite's parameter limit.
	const chunk = 400
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		rows, err := s.db.Query(`
SELECT video_id FROM peer_catalogue WHERE video_id IN (`+ph+`)
UNION
SELECT video_id FROM impressions WHERE video_id IN (`+ph+`)`,
			append(append([]any{}, args...), args...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			delete(want, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	seen := map[string]bool{}
	for id := range want {
		seen[CataloguePrefix(id, bits)] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}
