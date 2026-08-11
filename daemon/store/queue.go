// SPDX-License-Identifier: Apache-2.0
// The watch queue (WO-064).
//
// A user-ordered list of videos to watch later. It lives here rather than in the
// browser for the same reason the blocklist does: DESIGN_v2 §2.1 keeps the
// extension free of stored state, so it can stay small and stop changing.
//
// Order is explicit rather than implied by insertion time. A queue whose order
// cannot be changed is a list, and reordering by rewriting timestamps would make
// "when did I add this" unanswerable.
package store

import (
	"database/sql"
	"fmt"

	"github.com/keel-app/keel/daemon/bridge"
)

// AddToQueue appends a video, ignoring one already queued.
//
// Silently idempotent: adding twice is a user pressing a button twice, not an
// instruction to watch something two times.
func (s *Store) AddToQueue(videoID, platform string, addedAt int64) error {
	if videoID == "" {
		return fmt.Errorf("video_id required")
	}
	var exists int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM watch_queue WHERE video_id = ?`, videoID).Scan(&exists)
	if exists > 0 {
		return nil
	}
	var next sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(position) FROM watch_queue`).Scan(&next)
	_, err := s.db.Exec(
		`INSERT INTO watch_queue(video_id, platform, position, added_at) VALUES(?,?,?,?)`,
		videoID, platformOf(platform), next.Int64+1, addedAt)
	return err
}

// ListQueue returns the queue in order, joined to whatever titles are known.
func (s *Store) ListQueue() ([]bridge.QueueItem, error) {
	rows, err := s.db.Query(`
SELECT q.video_id, q.platform, q.position, q.added_at,
       COALESCE((SELECT title FROM impressions WHERE video_id = q.video_id AND title != '' LIMIT 1),
                (SELECT title FROM peer_catalogue WHERE video_id = q.video_id AND title != '' LIMIT 1),
                ''),
       COALESCE((SELECT duration_s FROM impressions WHERE video_id = q.video_id AND duration_s IS NOT NULL LIMIT 1), 0)
FROM watch_queue q ORDER BY q.position ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bridge.QueueItem{}
	for rows.Next() {
		var it bridge.QueueItem
		if err := rows.Scan(&it.VideoID, &it.Platform, &it.Position, &it.AddedAt,
			&it.Title, &it.Duration); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// RemoveFromQueue drops the entry at a position in the *current* ordering.
//
// By index rather than by id, because that is what the interface offers — the
// user points at a row. Positions are renumbered so they stay dense.
func (s *Store) RemoveFromQueue(index int) error {
	items, err := s.ListQueue()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(items) {
		return fmt.Errorf("no queue entry at %d", index)
	}
	if _, err := s.db.Exec(`DELETE FROM watch_queue WHERE video_id = ?`, items[index].VideoID); err != nil {
		return err
	}
	return s.renumber()
}

// ReorderQueue moves one entry to another position.
func (s *Store) ReorderQueue(from, to int) error {
	items, err := s.ListQueue()
	if err != nil {
		return err
	}
	if from < 0 || from >= len(items) || to < 0 || to >= len(items) {
		return fmt.Errorf("cannot move %d to %d in a queue of %d", from, to, len(items))
	}
	if from == to {
		return nil
	}
	moved := items[from]
	rest := append(append([]bridge.QueueItem{}, items[:from]...), items[from+1:]...)
	reordered := append(append(append([]bridge.QueueItem{}, rest[:to]...), moved), rest[to:]...)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, it := range reordered {
		if _, err := tx.Exec(`UPDATE watch_queue SET position = ? WHERE video_id = ?`, i, it.VideoID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AdvanceQueue consumes a finished video and reports what to play next.
//
// It only acts when the finished video is actually queued. That single check is
// what keeps autoplay from being intrusive: watching something you never queued
// leaves the queue and the tab alone, and there is no playback session to start,
// end, or get stuck in. Intent is expressed by the queue's contents, not by a
// mode the user has to remember they are in.
//
// A played entry is removed, because a queue that never drains would loop.
func (s *Store) AdvanceQueue(videoID string) (*bridge.QueueItem, error) {
	items, err := s.ListQueue()
	if err != nil {
		return nil, err
	}
	at := -1
	for i, it := range items {
		if it.VideoID == videoID {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, nil
	}
	if err := s.RemoveFromQueue(at); err != nil {
		return nil, err
	}
	// The next video is whatever now occupies the position the finished one
	// vacated — the one after it, or nothing if it was last.
	rest, err := s.ListQueue()
	if err != nil {
		return nil, err
	}
	if at >= len(rest) {
		return nil, nil
	}
	next := rest[at]
	return &next, nil
}

// renumber closes gaps left by a removal.
func (s *Store) renumber() error {
	items, err := s.ListQueue()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, it := range items {
		if _, err := tx.Exec(`UPDATE watch_queue SET position = ? WHERE video_id = ?`, i, it.VideoID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
