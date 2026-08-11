// SPDX-License-Identifier: Apache-2.0
// Thumbnail cache (WO-040).
//
// The daemon does the fetching and the storing; the extension only renders what
// it is handed. That keeps network I/O out of the component that has to stay
// frozen — an extension that fetches is an extension that changes.
//
// Each image is fetched **once, ever**, then served from disk. Contrast with
// pointing an <img> at the CDN, which re-requests on every render.
//
// Space, not time. The corpus is never evicted (WO-002: a video removed in
// month N must still have a record in month N+12), but thumbnails are
// regenerable, so they are the one thing that can be dropped under pressure.
// The budget is the user's to set.
package store

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// DefaultDiskBudget is what a node devotes to evictable cache until told
	// otherwise. Roughly 20k thumbnails.
	DefaultDiskBudget = 256 << 20
	// MinDiskBudget — below this the cache thrashes and is worse than none.
	MinDiskBudget = 16 << 20
	// maxThumbBytes rejects anything implausible for a thumbnail.
	maxThumbBytes = 2 << 20
	metaBudgetKey = "disk_budget_bytes"
)

// DiskBudget returns the byte budget for evictable cache.
func (s *Store) DiskBudget() int64 {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaBudgetKey).Scan(&v); err == nil {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= MinDiskBudget {
			return n
		}
	}
	return DefaultDiskBudget
}

// SetDiskBudget stores the budget and evicts immediately if it shrank.
func (s *Store) SetDiskBudget(bytes int64) (int64, error) {
	if bytes < MinDiskBudget {
		bytes = MinDiskBudget
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaBudgetKey, strconv.FormatInt(bytes, 10)); err != nil {
		return 0, err
	}
	return bytes, s.evictCache(bytes)
}

// CacheUsage reports current cache size and item count, across every
// evictable resource under the disk-budget slider — thumbnails and, since
// WO-067, gossiped token sketches. Both are refetchable (the CDN refills a
// dropped thumbnail, gossip refills a dropped sketch), which is what makes
// them evictable at all; nothing here is observation data.
func (s *Store) CacheUsage() (bytes int64, items int64, err error) {
	err = s.db.QueryRow(`
SELECT COALESCE(SUM(size), 0), COUNT(*) FROM (
  SELECT size FROM thumbnails
  UNION ALL
  SELECT size FROM token_sketches
)`).Scan(&bytes, &items)
	return
}

// Thumbnail returns a data URL for a video, fetching once if not cached.
//
// The fetch is the daemon's only outbound request besides bundle import, and it
// happens at most once per video for the life of the cache entry.
func (s *Store) Thumbnail(videoID string) (string, error) {
	if videoID == "" {
		return "", fmt.Errorf("video_id required")
	}
	var blob []byte
	err := s.db.QueryRow(`SELECT bytes FROM thumbnails WHERE video_id = ?`, videoID).Scan(&blob)
	if err == nil && len(blob) > 0 {
		// Touch for LRU. Best-effort: a failed touch costs an eviction ordering,
		// not correctness.
		_, _ = s.db.Exec(`UPDATE thumbnails SET last_used_at = ? WHERE video_id = ?`,
			time.Now().UnixMilli(), videoID)
		return dataURL(blob), nil
	}

	raw, err := fetchThumbnail(videoID)
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`
INSERT INTO thumbnails(video_id, bytes, size, fetched_at, last_used_at)
VALUES(?,?,?,?,?)
ON CONFLICT(video_id) DO UPDATE SET
  bytes = excluded.bytes, size = excluded.size, last_used_at = excluded.last_used_at`,
		videoID, raw, len(raw), now, now); err != nil {
		return "", err
	}
	if err := s.evictCache(s.DiskBudget()); err != nil {
		return "", err
	}
	return dataURL(raw), nil
}

func dataURL(b []byte) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}

// fetchThumbnail retrieves one image from YouTube's static CDN.
//
// No credentials, no API, and the URL is derived from the video ID. This is the
// request the page itself makes when the rail is visible.
func fetchThumbnail(videoID string) ([]byte, error) {
	url := fmt.Sprintf("https://i.ytimg.com/vi/%s/mqdefault.jpg", videoID)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thumbnail %s: %s", videoID, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxThumbBytes {
		return nil, fmt.Errorf("thumbnail larger than %d bytes", maxThumbBytes)
	}
	return b, nil
}

// evictCache drops least-recently-used entries, across every evictable
// table sharing the budget, until under it.
//
// Only the cache is evictable. Observations are never touched here — that
// distinction is the whole reason the budget exists as a space limit rather
// than the time-based retention that was deliberately removed. A single
// merged LRU ordering across thumbnails and token_sketches (rather than,
// say, always draining one table before the other) is what makes "the
// budget" mean one number a user sets once, instead of two they'd have to
// reason about separately.
func (s *Store) evictCache(budget int64) error {
	total, _, err := s.CacheUsage()
	if err != nil {
		return err
	}
	if total <= budget {
		return nil
	}
	rows, err := s.db.Query(`
SELECT 'thumb' AS kind, video_id AS id, size, last_used_at FROM thumbnails
UNION ALL
SELECT 'sketch' AS kind, CAST(token_index AS TEXT) AS id, size, last_used_at FROM token_sketches
ORDER BY last_used_at ASC`)
	if err != nil {
		return err
	}
	type victim struct {
		kind, id string
		size     int64
	}
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.kind, &v.id, &v.size, new(int64)); err != nil {
			rows.Close()
			return err
		}
		victims = append(victims, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v := range victims {
		if total <= budget {
			break
		}
		var q string
		switch v.kind {
		case "thumb":
			q = `DELETE FROM thumbnails WHERE video_id = ?`
		case "sketch":
			q = `DELETE FROM token_sketches WHERE token_index = ?`
		default:
			continue
		}
		if _, err := s.db.Exec(q, v.id); err != nil {
			return err
		}
		total -= v.size
	}
	return nil
}
