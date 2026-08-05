// SPDX-License-Identifier: Apache-2.0
// Peer data: importing another node's measurements (WO-027).
//
// Imported rows live in their own tables and never merge into `impressions`.
// That separation is load-bearing: "what YouTube showed me" has to stay a
// statement about this machine, or every figure the project publishes becomes
// unciteable. Suggestions draw on the merged graph; analysis does not.
package store

import (
	"database/sql"
	"fmt"

	"github.com/keel-app/keel/daemon/bridge"
)

// ImportEdges replaces everything previously imported from source.
//
// Replace rather than accumulate: counts inside a bundle are already
// cumulative, so importing a newer bundle on top of an older one would
// double-count every edge they share.
func (s *Store) ImportEdges(source string, obs []bridge.EdgeObservation, cat []bridge.CatalogueEntry) (int64, int64, error) {
	if source == "" {
		return 0, 0, fmt.Errorf("source required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM peer_edges WHERE source = ?`, source); err != nil {
		return 0, 0, err
	}
	stmt, err := tx.Prepare(`
INSERT INTO peer_edges(source, from_id, to_id, surface, slot_bucket, day_bucket, cohort, count)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(source, from_id, to_id, surface, slot_bucket, day_bucket, cohort)
DO UPDATE SET count = excluded.count`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	var edges int64
	for _, o := range obs {
		if o.From == "" || o.To == "" || o.Count <= 0 {
			continue // a malformed row is dropped, not fatal — bundles are untrusted
		}
		if _, err := stmt.Exec(source, o.From, o.To, o.Surface, o.SlotBucket, o.DayBucket, o.Cohort, o.Count); err != nil {
			return 0, 0, err
		}
		edges++
	}

	cstmt, err := tx.Prepare(`
INSERT INTO peer_catalogue(video_id, title, channel_id, duration_s, view_count, published_at, source)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(video_id) DO UPDATE SET
  title = CASE WHEN excluded.title != '' THEN excluded.title ELSE peer_catalogue.title END,
  channel_id = COALESCE(excluded.channel_id, peer_catalogue.channel_id),
  duration_s = COALESCE(excluded.duration_s, peer_catalogue.duration_s),
  view_count = COALESCE(excluded.view_count, peer_catalogue.view_count),
  published_at = COALESCE(excluded.published_at, peer_catalogue.published_at)`)
	if err != nil {
		return 0, 0, err
	}
	defer cstmt.Close()

	var entries int64
	for _, c := range cat {
		if c.VideoID == "" {
			continue
		}
		if _, err := cstmt.Exec(c.VideoID, c.Title, c.ChannelID, c.DurationS, c.ViewCount, c.PublishedAt, source); err != nil {
			return 0, 0, err
		}
		entries++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return edges, entries, nil
}

// PeerSummary is one imported node.
type PeerSummary struct {
	Source string `json:"source"`
	Edges  int64  `json:"edges"`
}

// Peers lists imported sources so the UI can show, and remove, what is merged.
func (s *Store) Peers() ([]PeerSummary, error) {
	rows, err := s.db.Query(`SELECT source, COUNT(*) FROM peer_edges GROUP BY source ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerSummary{}
	for rows.Next() {
		var p PeerSummary
		if err := rows.Scan(&p.Source, &p.Edges); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ForgetPeer removes everything imported from one source.
func (s *Store) ForgetPeer(source string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM peer_edges WHERE source = ?`, source)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.Exec(`DELETE FROM peer_catalogue WHERE source = ?`, source); err != nil {
		return n, err
	}
	return n, nil
}

// bucketWeight turns a slot bucket back into a ranking weight.
//
// Peer edges arrive bucketed, so the exact average slot local edges use is not
// available — by design. The midpoint of each bucket is the honest
// approximation, and the loss of precision is the privacy the bucketing bought.
func bucketWeight(bucket string) float64 {
	switch bucket {
	case "0":
		return 1.0 / (1.0 + 0.0/8.0)
	case "1":
		return 1.0 / (1.0 + 1.0/8.0)
	case "2":
		return 1.0 / (1.0 + 2.0/8.0)
	case "3-5":
		return 1.0 / (1.0 + 4.0/8.0)
	case "6-10":
		return 1.0 / (1.0 + 8.0/8.0)
	default: // "11+"
		return 1.0 / (1.0 + 14.0/8.0)
	}
}

// peerGraph builds adjacency from imported edges, summed across every source.
//
// Summing is what gives the walk depth: the same pair observed by three people
// is a stronger edge than one person seeing it three times, and neither is
// distinguishable from the other at this layer. That is a known limitation, not
// an oversight — without a sybil defence, "many people saw this" cannot be
// proven (DESIGN_v2 §6.4).
func (s *Store) peerGraph() (map[string][]edge, int, error) {
	rows, err := s.db.Query(`
SELECT from_id, to_id, slot_bucket, SUM(count)
FROM peer_edges
WHERE surface = 'WATCH_NEXT' AND from_id != to_id
GROUP BY from_id, to_id, slot_bucket`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	acc := map[string]map[string]float64{}
	n := 0
	for rows.Next() {
		var from, to, bucket string
		var count sql.NullInt64
		if err := rows.Scan(&from, &to, &bucket, &count); err != nil {
			return nil, 0, err
		}
		if acc[from] == nil {
			acc[from] = map[string]float64{}
		}
		acc[from][to] += float64(count.Int64) * bucketWeight(bucket)
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	g := make(map[string][]edge, len(acc))
	for from, tos := range acc {
		for to, w := range tos {
			g[from] = append(g[from], edge{to: to, weight: w})
		}
	}
	return g, n, nil
}
