// SPDX-License-Identifier: Apache-2.0
// Corpus analysis (WO-024): what gets pushed hardest, and from where.
//
// Every figure is a count of observations. Nothing here infers intent — the
// corpus records what was shown, not why (DESIGN_v2 §6.4).
package store

import (
	"database/sql"
	"fmt"

	"github.com/keel-app/keel/daemon/bridge"
)

const analysisTopN = 15

// Analysis summarises the corpus for the analysis view.
func (s *Store) Analysis() (*bridge.AnalysisPayload, error) {
	out := &bridge.AnalysisPayload{
		TopVideos:     []bridge.AnalysisRow{},
		TopChannels:   []bridge.AnalysisRow{},
		TopEdges:      []bridge.AnalysisRow{},
		SlotHistogram: []bridge.SlotBucket{},
	}

	if err := s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT video_id) FROM impressions`).
		Scan(&out.TotalImpressions, &out.DistinctVideos); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`
SELECT COUNT(DISTINCT channel_id) FROM impressions
WHERE channel_id IS NOT NULL AND channel_id != ''`).Scan(&out.DistinctChannels); err != nil {
		return nil, err
	}
	// Imported edges are reported separately, never folded into the counts
	// above: "what YouTube showed me" has to stay a statement about this
	// machine or none of these figures are citable.
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM peer_edges`).Scan(&out.PeerEdges)
	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT source) FROM peer_edges`).Scan(&out.PeerSources)

	// Videos the user actually watched — they are the only graph roots.
	if err := s.db.QueryRow(`
SELECT COUNT(DISTINCT context_video_id) FROM impressions
WHERE context_video_id IS NOT NULL AND context_video_id != ''`).Scan(&out.WatchedVideos); err != nil {
		return nil, err
	}

	// Gravity wells: what shows up most, and how high it sits.
	rows, err := s.db.Query(`
SELECT video_id, MAX(title), COUNT(*) AS n, AVG(slot_index)
FROM impressions
GROUP BY video_id ORDER BY n DESC, video_id ASC LIMIT ?`, analysisTopN)
	if err != nil {
		return nil, err
	}
	if out.TopVideos, err = scanRows(rows, true); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(`
SELECT i.channel_id, MAX(c.name), COUNT(*) AS n, AVG(i.slot_index)
FROM impressions i
LEFT JOIN channels c ON c.channel_id = i.channel_id
WHERE i.channel_id IS NOT NULL AND i.channel_id != ''
GROUP BY i.channel_id ORDER BY n DESC, i.channel_id ASC LIMIT ?`, analysisTopN)
	if err != nil {
		return nil, err
	}
	if out.TopChannels, err = scanRows(rows, true); err != nil {
		return nil, err
	}

	// Strongest observed pairs: "after A, B showed up this often".
	erows, err := s.db.Query(`
SELECT i.context_video_id || ' → ' || i.video_id,
       MAX(i.title),
       COUNT(*) AS n,
       AVG(i.slot_index)
FROM impressions i
WHERE i.surface = 'WATCH_NEXT'
  AND i.context_video_id IS NOT NULL AND i.context_video_id != ''
GROUP BY i.context_video_id, i.video_id
ORDER BY n DESC, i.video_id ASC LIMIT ?`, analysisTopN)
	if err != nil {
		return nil, err
	}
	if out.TopEdges, err = scanRows(erows, true); err != nil {
		return nil, err
	}

	srows, err := s.db.Query(`
SELECT slot_index, COUNT(*) FROM impressions GROUP BY slot_index ORDER BY slot_index`)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var b bridge.SlotBucket
		if err := srows.Scan(&b.Slot, &b.Count); err != nil {
			return nil, err
		}
		out.SlotHistogram = append(out.SlotHistogram, b)
	}
	return out, srows.Err()
}

// scanRows reads (key, label, count, avg_slot) rows into AnalysisRow.
func scanRows(rows *sql.Rows, withSlot bool) ([]bridge.AnalysisRow, error) {
	defer rows.Close()
	out := []bridge.AnalysisRow{}
	for rows.Next() {
		var r bridge.AnalysisRow
		var label sql.NullString
		var avgSlot sql.NullFloat64
		if err := rows.Scan(&r.Key, &label, &r.Count, &avgSlot); err != nil {
			return nil, err
		}
		if label.Valid {
			v := label.String
			r.Label = &v
		}
		if withSlot && avgSlot.Valid {
			v := avgSlot.Float64
			r.MedianSlot = &v
			e := fmt.Sprintf("avg slot %.1f", v)
			r.Extra = &e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
