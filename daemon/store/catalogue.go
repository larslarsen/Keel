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

const catalogueSchemaVersion = 1

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

// CataloguePack is one bucket of catalogue rows, signed as a unit.
type CataloguePack struct {
	SchemaVersion int                     `json:"schema_version"`
	Prefix        string                  `json:"prefix"`
	Entries       []bridge.CatalogueEntry `json:"entries"`

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

// BuildCataloguePack assembles every row this node holds in one bucket.
func (s *Store) BuildCataloguePack(prefix string, sources SourceSet, limit int) (*CataloguePack, error) {
	bits, ok := PrefixOf(prefix)
	if !ok {
		return nil, fmt.Errorf("malformed prefix %q", prefix)
	}
	if limit <= 0 {
		limit = 4096
	}
	all, err := s.heldCatalogue(sources)
	if err != nil {
		return nil, err
	}

	pack := &CataloguePack{
		SchemaVersion: catalogueSchemaVersion,
		Prefix:        prefix,
		Entries:       []bridge.CatalogueEntry{},
	}
	for _, c := range all {
		if CataloguePrefix(c.VideoID, bits) != prefix {
			continue
		}
		pack.Entries = append(pack.Entries, c)
		if len(pack.Entries) >= limit {
			break
		}
	}
	sort.Slice(pack.Entries, func(a, b int) bool {
		return pack.Entries[a].VideoID < pack.Entries[b].VideoID
	})

	if pack.ContentSHA256, err = contentDigest(pack.Entries, nil); err != nil {
		return nil, err
	}
	payload, err := canonicalPayload(pack.Entries, nil)
	if err != nil {
		return nil, err
	}
	if pack.Signature, pack.PublicKey, err = s.signPayload(payload); err != nil {
		return nil, err
	}
	pack.Algorithm = signAlgorithm
	return pack, nil
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
	if pack.SchemaVersion > catalogueSchemaVersion {
		return 0, fmt.Errorf("catalogue schema %d is newer than this build understands (%d)",
			pack.SchemaVersion, catalogueSchemaVersion)
	}
	digest, err := contentDigest(pack.Entries, nil)
	if err != nil {
		return 0, err
	}
	if digest != pack.ContentSHA256 {
		return 0, fmt.Errorf("catalogue pack contents do not match its digest")
	}
	if pack.Signature != "" || pack.PublicKey != "" {
		payload, err := canonicalPayload(pack.Entries, nil)
		if err != nil {
			return 0, err
		}
		if err := verifyPayload(payload, pack.Signature, pack.PublicKey); err != nil {
			return 0, fmt.Errorf("catalogue pack: %w", err)
		}
	}
	if len(pack.Entries) == 0 {
		return 0, nil
	}

	source := pack.PublicKey
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
	for _, c := range pack.Entries {
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
