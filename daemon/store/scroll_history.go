// SPDX-License-Identifier: Apache-2.0
// TikTok Mirror scroll history (WO-063).
//
// Local-only view of what the user consumed on one platform, in observation
// order, plus hashtag/sound cluster counts. Hashtag/sound aggregates never
// leave this machine (WO-063 peer-sharing rule).
package store

import (
	"database/sql"
	"encoding/json"

	"github.com/keel-app/keel/daemon/bridge"
)

// ScrollHistory returns recent impressions for a platform, oldest-first so the
// panel can show "what you just watched" at the bottom. Dedupes by video_id
// keeping the latest observation. Channel name is joined in-query — SQLite's
// default single-writer connection would deadlock on a nested QueryRow while
// rows are still open.
func (s *Store) ScrollHistory(platform string, limit int) (*bridge.ScrollHistoryResultPayload, error) {
	platform = platformOf(platform)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := &bridge.ScrollHistoryResultPayload{
		Items:         []bridge.ScrollHistoryItem{},
		HashtagCounts: map[string]int64{},
		SoundCounts:   map[string]int64{},
	}
	rows, err := s.db.Query(`
SELECT i.video_id, i.title, i.channel_id, c.name, i.observed_at, i.slot_index,
       i.hashtags_json, i.sound_id, i.dwell_pct, i.engagement, i.platform
FROM impressions i
LEFT JOIN channels c ON c.channel_id = i.channel_id
WHERE i.platform = ?
ORDER BY i.observed_at DESC, i.slot_index DESC
LIMIT ?`, platform, limit*3) // over-fetch then dedupe
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[string]bool{}
	var items []bridge.ScrollHistoryItem
	for rows.Next() {
		var it bridge.ScrollHistoryItem
		var ch, name, sound, eng sql.NullString
		var tags string
		var dwell sql.NullFloat64
		if err := rows.Scan(
			&it.VideoID, &it.Title, &ch, &name, &it.ObservedAt, &it.SlotIndex,
			&tags, &sound, &dwell, &eng, &it.Platform,
		); err != nil {
			return nil, err
		}
		if seen[it.VideoID] {
			continue
		}
		seen[it.VideoID] = true
		if ch.Valid && ch.String != "" {
			cid := ch.String
			it.ChannelID = &cid
		}
		if name.Valid && name.String != "" {
			n := name.String
			it.ChannelName = &n
		}
		_ = json.Unmarshal([]byte(tags), &it.Hashtags)
		if it.Hashtags == nil {
			it.Hashtags = []string{}
		}
		if sound.Valid && sound.String != "" {
			v := sound.String
			it.SoundID = &v
			out.SoundCounts[v]++
		}
		if dwell.Valid {
			v := dwell.Float64
			it.DwellPct = &v
		}
		if eng.Valid && eng.String != "" {
			v := eng.String
			it.Engagement = &v
		}
		for _, t := range it.Hashtags {
			if t != "" {
				out.HashtagCounts[t]++
			}
		}
		items = append(items, it)
		if len(items) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Newest last for "scroll history" reading order.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	out.Items = items
	return out, nil
}
