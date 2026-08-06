// SPDX-License-Identifier: Apache-2.0
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/bridge"

	_ "modernc.org/sqlite"
)

// MaxExplainContexts caps co-occurrence parents returned by ExplainVideo (WO-018).
const MaxExplainContexts = 12

// ExportSchemaVersion is the JSON export envelope schema (not the SQLite meta key).
const ExportSchemaVersion = 2

// Store is the SQLite corpus. Only place impressions are persisted.
// No time-based deletion — retention is a P1 user setting (default off).
type Store struct {
	db *sql.DB

	// liveMu guards liveNow, the set of videos the swarm currently believes are
	// live. It is pushed in from outside because the live index lives in the
	// swarm package, which imports this one.
	liveMu  sync.RWMutex
	liveNow map[string]bool
}

// SetLiveVideos records which videos the swarm reports as live right now.
//
// Suggestions rank these above everything else: a stream that is running is the
// one thing on YouTube's rail worth preserving, because it is the one thing
// whose value expires. Everything else can be watched later.
func (s *Store) SetLiveVideos(ids []string) {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = true
		}
	}
	s.liveMu.Lock()
	s.liveNow = set
	s.liveMu.Unlock()
}

// currentlyLive unions what the swarm reports with what this node has seen
// carrying a LIVE badge recently.
//
// The local half matters most today, because it works with no peers at all: a
// stream this user was shown in the last few hours is very likely still running,
// and that is the only evidence available before the network has any.
func (s *Store) currentlyLive() (map[string]bool, error) {
	out := map[string]bool{}
	s.liveMu.RLock()
	for id := range s.liveNow {
		out[id] = true
	}
	s.liveMu.RUnlock()

	cutoff := time.Now().Add(-LiveRecency).UnixMilli()
	rows, err := s.db.Query(`
SELECT DISTINCT video_id FROM impressions
WHERE observed_at >= ? AND badges_json LIKE '%LIVE%'`, cutoff)
	if err != nil {
		return out, nil // a failure here must not break suggestions
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out[id] = true
		}
	}
	return out, nil
}

// Open creates/opens the database at path (or default user config dir).
func Open(path string) (*Store, error) {
	if path == "" {
		dir, err := defaultDir()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		path = filepath.Join(dir, "keel.sqlite")
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Best-effort: a repair failure must not stop the daemon starting.
	if err := s.repairPublishedAt(); err != nil {
		log.Printf("published_at repair skipped: %v", err)
	}
	return s, nil
}

func defaultDir() (string, error) {
	if x := os.Getenv("KEEL_DATA_DIR"); x != "" {
		return x, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "keel"), nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	// Schema v2: PK (page_load_id, surface, video_id); nullable channel_id + channel_unknown.
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS impressions (
  page_load_id TEXT NOT NULL,
  observed_at INTEGER NOT NULL,
  surface TEXT NOT NULL,
  context_video_id TEXT,
  context_query_hash TEXT,
  slot_index INTEGER NOT NULL,
  video_id TEXT NOT NULL,
  channel_id TEXT,
  channel_unknown INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL,
  duration_s REAL,
  view_count REAL,
  published_at TEXT,
  badges_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (page_load_id, surface, video_id)
);
CREATE INDEX IF NOT EXISTS idx_imp_observed ON impressions(observed_at);
CREATE INDEX IF NOT EXISTS idx_imp_video ON impressions(video_id);
CREATE INDEX IF NOT EXISTS idx_imp_context ON impressions(context_video_id);
-- Display names for channels, keyed by channel_id. The extension reads the
-- name off each card; this is the durable record the catalogue releases later.
-- channel_id may itself be an @handle; names and ids can both be null for a
-- given video (channel_unknown rows carry neither).
CREATE TABLE IF NOT EXISTS channels (
  channel_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
-- Evictable thumbnail cache (WO-040). Space-bounded, never time-bounded:
-- observations are kept forever, images are regenerable.
CREATE TABLE IF NOT EXISTS thumbnails (
  video_id TEXT PRIMARY KEY,
  bytes BLOB NOT NULL,
  size INTEGER NOT NULL,
  fetched_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_thumb_lru ON thumbnails(last_used_at);
CREATE TABLE IF NOT EXISTS channel_blocklist (
  channel_id TEXT PRIMARY KEY,
  blocked_at INTEGER NOT NULL
);
-- Peers that have actually served us data (WO-052).
--
-- The public DHT is subject to a censorship attack with no available fix
-- (GO-2024-3218): flooding provider records for a key stops others discovering
-- who holds it. Discovery is the only thing that breaks — the block protocol
-- works fine peer-to-peer — so remembering peers that worked turns a censored
-- lookup into a slower one instead of a dead one.
--
-- Only peers that successfully served a verified block are kept, so this is a
-- record of what worked rather than of everyone we ever met.
CREATE TABLE IF NOT EXISTS known_peers (
  peer_id TEXT PRIMARY KEY,
  addrs TEXT NOT NULL,
  last_ok INTEGER NOT NULL,
  successes INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_known_peers_ok ON known_peers(last_ok DESC);

-- Imported measurement tuples from another node (WO-027). Kept apart from
-- impressions on purpose: "what YouTube showed me" must stay citable, so
-- foreign observations never merge into the local corpus.
CREATE TABLE IF NOT EXISTS peer_edges (
  source TEXT NOT NULL,
  from_id TEXT NOT NULL,
  to_id TEXT NOT NULL,
  surface TEXT NOT NULL,
  slot_bucket TEXT NOT NULL,
  day_bucket TEXT NOT NULL,
  cohort TEXT NOT NULL,
  count INTEGER NOT NULL,
  PRIMARY KEY (source, from_id, to_id, surface, slot_bucket, day_bucket, cohort)
);
CREATE INDEX IF NOT EXISTS idx_peer_from ON peer_edges(from_id);
-- Catalogue rows from another node. Public fact about public videos
-- (DESIGN_BOOTSTRAP §1), so these may merge freely.
CREATE TABLE IF NOT EXISTS peer_catalogue (
  video_id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  channel_id TEXT,
  duration_s REAL,
  view_count REAL,
  published_at TEXT,
  source TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	if err := s.migrateToV2IfNeeded(); err != nil {
		return err
	}
	// WO-016: apply known channel_ids across page loads for the same video_id.
	if _, err := s.BackfillChannelsFromCatalogue(); err != nil {
		return err
	}
	return nil
}

// migrateToV2IfNeeded rebuilds impressions when PK still includes slot_index
// or channel_id is NOT NULL / channel_unknown is missing.
func (s *Store) migrateToV2IfNeeded() error {
	var createSQL string
	err := s.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='impressions'`,
	).Scan(&createSQL)
	if err != nil {
		return err
	}
	needs := strings.Contains(createSQL, "video_id, slot_index") ||
		strings.Contains(createSQL, "channel_id TEXT NOT NULL") ||
		!strings.Contains(createSQL, "channel_unknown")
	if createSQL == "" || !needs {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`CREATE TABLE impressions_v2 (
  page_load_id TEXT NOT NULL,
  observed_at INTEGER NOT NULL,
  surface TEXT NOT NULL,
  context_video_id TEXT,
  context_query_hash TEXT,
  slot_index INTEGER NOT NULL,
  video_id TEXT NOT NULL,
  channel_id TEXT,
  channel_unknown INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL,
  duration_s REAL,
  view_count REAL,
  published_at TEXT,
  badges_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (page_load_id, surface, video_id)
)`,
		// channel_unknown: 1 when channel_id is null/empty
		`INSERT OR IGNORE INTO impressions_v2
  SELECT page_load_id, MIN(observed_at), surface, context_video_id, context_query_hash,
         MIN(slot_index), video_id, channel_id,
         CASE WHEN channel_id IS NULL OR channel_id = '' THEN 1 ELSE 0 END,
         title, duration_s, view_count, published_at, badges_json
  FROM impressions
  GROUP BY page_load_id, surface, video_id`,
		`DROP TABLE impressions`,
		`ALTER TABLE impressions_v2 RENAME TO impressions`,
		`CREATE INDEX IF NOT EXISTS idx_imp_observed ON impressions(observed_at)`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// lookupChannel returns a known channel_id for video_id from any prior row (WO-016).
func (s *Store) lookupChannel(tx *sql.Tx, videoID string) (*string, error) {
	var q interface {
		QueryRow(query string, args ...any) *sql.Row
	} = s.db
	if tx != nil {
		q = tx
	}
	var ch sql.NullString
	err := q.QueryRow(`
SELECT channel_id FROM impressions
WHERE video_id = ? AND channel_id IS NOT NULL AND channel_id != ''
ORDER BY observed_at ASC
LIMIT 1`, videoID).Scan(&ch)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !ch.Valid || ch.String == "" {
		return nil, nil
	}
	id := ch.String
	return &id, nil
}

// PutImpressions inserts rows. On conflict, keeps first-observed slot_index.
// When channel_id is missing, fills from a prior row for the same video_id (WO-016).
func (s *Store) PutImpressions(list []bridge.Impression) (inserted int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
INSERT INTO impressions (
  page_load_id, observed_at, surface, context_video_id, context_query_hash,
  slot_index, video_id, channel_id, channel_unknown, title, duration_s, view_count, published_at, badges_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(page_load_id, surface, video_id) DO UPDATE SET
  title=excluded.title,
  channel_id=COALESCE(excluded.channel_id, impressions.channel_id),
  channel_unknown=CASE
    WHEN COALESCE(excluded.channel_id, impressions.channel_id) IS NULL THEN 1
    ELSE 0
  END,
  duration_s=COALESCE(excluded.duration_s, impressions.duration_s),
  view_count=COALESCE(excluded.view_count, impressions.view_count),
  published_at=COALESCE(excluded.published_at, impressions.published_at),
  badges_json=excluded.badges_json
  -- slot_index intentionally NOT updated: keep first-observed slot
`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	// Display name is per-channel, not per-impression: one row per channel_id,
	// refreshed to the latest card's text. Channel names are public facts.
	chStmt, err := tx.Prepare(`
INSERT INTO channels (channel_id, name, updated_at) VALUES (?,?,?)
ON CONFLICT(channel_id) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at`)
	if err != nil {
		return 0, err
	}
	defer chStmt.Close()

	for i := range list {
		imp := &list[i]
		if err := bridge.ValidateImpression(imp); err != nil {
			continue
		}
		// Catalogue backfill: known channel for this video_id from any page load.
		if imp.ChannelID == nil || *imp.ChannelID == "" {
			if ch, err := s.lookupChannel(tx, imp.VideoID); err != nil {
				return inserted, err
			} else if ch != nil {
				imp.ChannelID = ch
				imp.ChannelUnknown = false
			}
		}
		badges, _ := json.Marshal(imp.Badges)
		unk := 0
		if imp.ChannelUnknown || imp.ChannelID == nil {
			unk = 1
		} else {
			unk = 0
			imp.ChannelUnknown = false
		}
		res, err := stmt.Exec(
			imp.PageLoadID, imp.ObservedAt, imp.Surface,
			imp.ContextVideoID, imp.ContextQueryHash,
			imp.SlotIndex, imp.VideoID, imp.ChannelID, unk, imp.Title,
			imp.DurationS, imp.ViewCount, imp.PublishedAt, string(badges),
		)
		if err != nil {
			return inserted, err
		}
		if imp.ChannelID != nil && imp.ChannelName != nil && *imp.ChannelName != "" {
			if _, err := chStmt.Exec(*imp.ChannelID, *imp.ChannelName, imp.ObservedAt); err != nil {
				return inserted, err
			}
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			inserted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// BackfillChannelsFromCatalogue sets channel_id on unknown rows from any known
// row for the same video_id. Returns how many rows were updated (WO-016).
func (s *Store) BackfillChannelsFromCatalogue() (int64, error) {
	// SQLite cannot UPDATE from the same table cleanly; materialize a map first.
	rows, err := s.db.Query(`
SELECT video_id, channel_id FROM impressions
WHERE channel_id IS NOT NULL AND channel_id != ''`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	known := map[string]string{}
	for rows.Next() {
		var vid, ch string
		if err := rows.Scan(&vid, &ch); err != nil {
			return 0, err
		}
		if _, ok := known[vid]; !ok {
			known[vid] = ch
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(known) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
UPDATE impressions
SET channel_id = ?, channel_unknown = 0
WHERE video_id = ?
  AND (channel_id IS NULL OR channel_id = '' OR channel_unknown != 0)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	var updated int64
	for vid, ch := range known {
		res, err := stmt.Exec(ch, vid)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		updated += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

var ucChannelRE = regexp.MustCompile(`^UC[\w-]{22}$`)

// ListBlocklist returns blocked channel ids in insertion order (blocked_at).
func (s *Store) ListBlocklist() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT channel_id FROM channel_blocklist ORDER BY blocked_at ASC, channel_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// BlockChannel adds a UC… id to the blocklist. Idempotent.
func (s *Store) BlockChannel(channelID string) error {
	if !ucChannelRE.MatchString(channelID) {
		return fmt.Errorf("bad channel_id")
	}
	_, err := s.db.Exec(
		`INSERT INTO channel_blocklist(channel_id, blocked_at) VALUES(?, ?)
ON CONFLICT(channel_id) DO NOTHING`,
		channelID, time.Now().UnixMilli())
	return err
}

// UnblockChannel removes a channel from the blocklist. Idempotent.
func (s *Store) UnblockChannel(channelID string) error {
	if !ucChannelRE.MatchString(channelID) {
		return fmt.Errorf("bad channel_id")
	}
	_, err := s.db.Exec(`DELETE FROM channel_blocklist WHERE channel_id = ?`, channelID)
	return err
}

// titleForVideo returns any recorded title for video_id, or nil.
func (s *Store) titleForVideo(videoID string) *string {
	var t sql.NullString
	err := s.db.QueryRow(
		`SELECT title FROM impressions WHERE video_id = ? AND title != '' LIMIT 1`,
		videoID,
	).Scan(&t)
	if err != nil || !t.Valid || t.String == "" {
		return nil
	}
	s2 := t.String
	return &s2
}

func medianInt(vals []int) float64 {
	if len(vals) == 0 {
		return 0
	}
	sort.Ints(vals)
	n := len(vals)
	if n%2 == 1 {
		return float64(vals[n/2])
	}
	return float64(vals[n/2-1]+vals[n/2]) / 2
}

// ExplainVideo returns observational funnel stats for a video_id (WO-018).
// Co-occurrence only — not YouTube's reasons.
func (s *Store) ExplainVideo(videoID string) (*bridge.ExplainResultPayload, error) {
	if videoID == "" {
		return nil, fmt.Errorf("video_id required")
	}
	out := &bridge.ExplainResultPayload{
		VideoID:       videoID,
		Title:         s.titleForVideo(videoID),
		Contexts:      []bridge.ExplainContext{},
		SlotHistogram: []bridge.SlotBucket{},
	}

	var first, last sql.NullInt64
	err := s.db.QueryRow(`
SELECT COUNT(*), MIN(observed_at), MAX(observed_at)
FROM impressions WHERE video_id = ?`, videoID).Scan(&out.TotalImpressions, &first, &last)
	if err != nil {
		return nil, err
	}
	if first.Valid {
		out.FirstObservedAt = &first.Int64
	}
	if last.Valid {
		out.LastObservedAt = &last.Int64
	}

	if err := s.db.QueryRow(`
SELECT COUNT(*) FROM impressions
WHERE video_id = ? AND surface = 'HOME'`, videoID).Scan(&out.HomeImpressions); err != nil {
		return nil, err
	}

	// Context co-occurrences (WATCH_NEXT only).
	crows, err := s.db.Query(`
SELECT context_video_id, COUNT(*) AS n
FROM impressions
WHERE video_id = ?
  AND surface = 'WATCH_NEXT'
  AND context_video_id IS NOT NULL
  AND context_video_id != ''
GROUP BY context_video_id
ORDER BY n DESC, context_video_id ASC
LIMIT ?`, videoID, MaxExplainContexts)
	if err != nil {
		return nil, err
	}
	defer crows.Close()

	type ctxRow struct {
		id    string
		count int64
	}
	var ctxs []ctxRow
	for crows.Next() {
		var r ctxRow
		if err := crows.Scan(&r.id, &r.count); err != nil {
			return nil, err
		}
		ctxs = append(ctxs, r)
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}

	for _, r := range ctxs {
		slotRows, err := s.db.Query(`
SELECT slot_index FROM impressions
WHERE video_id = ? AND context_video_id = ? AND surface = 'WATCH_NEXT'`,
			videoID, r.id)
		if err != nil {
			return nil, err
		}
		var slots []int
		for slotRows.Next() {
			var sl int
			if err := slotRows.Scan(&sl); err != nil {
				slotRows.Close()
				return nil, err
			}
			slots = append(slots, sl)
		}
		slotRows.Close()
		if err := slotRows.Err(); err != nil {
			return nil, err
		}
		out.Contexts = append(out.Contexts, bridge.ExplainContext{
			ContextVideoID: r.id,
			Title:          s.titleForVideo(r.id),
			Count:          r.count,
			MedianSlot:     medianInt(slots),
		})
	}

	hrows, err := s.db.Query(`
SELECT slot_index, COUNT(*) FROM impressions
WHERE video_id = ?
GROUP BY slot_index
ORDER BY slot_index ASC`, videoID)
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var b bridge.SlotBucket
		if err := hrows.Scan(&b.Slot, &b.Count); err != nil {
			return nil, err
		}
		out.SlotHistogram = append(out.SlotHistogram, b)
	}
	return out, hrows.Err()
}

// Stats returns aggregate counts.
func (s *Store) Stats() (*bridge.StatsPayload, error) {
	out := &bridge.StatsPayload{
		BySurface: map[string]int64{
			"WATCH_NEXT": 0,
			"HOME":       0,
			"SEARCH":     0,
			"CHANNEL":    0,
			"SHORTS":     0,
		},
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT surface, COUNT(*) FROM impressions GROUP BY surface`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var surface string
		var n int64
		if err := rows.Scan(&surface, &n); err != nil {
			return nil, err
		}
		out.BySurface[surface] = n
	}
	// channel_id null or channel_unknown flag — same notion as export header (WO-013).
	if err := s.db.QueryRow(`
SELECT
  COALESCE(SUM(CASE WHEN channel_id IS NULL OR channel_id = '' OR channel_unknown != 0 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN channel_id IS NOT NULL AND channel_id != '' AND channel_unknown = 0 THEN 1 ELSE 0 END), 0)
FROM impressions`).Scan(&out.ChannelUnknown, &out.ChannelKnown); err != nil {
		return nil, err
	}
	var first, last sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(observed_at), MAX(observed_at) FROM impressions`).Scan(&first, &last); err != nil {
		return nil, err
	}
	if first.Valid {
		out.FirstObservedAt = &first.Int64
	}
	if last.Valid {
		out.LastObservedAt = &last.Int64
	}
	var fails sql.NullString
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='extraction_failures'`).Scan(&fails)
	if fails.Valid {
		var n int64
		_, _ = fmt.Sscan(fails.String, &n)
		out.ExtractionFailures = n
	}
	return out, nil
}

// ChannelUnknownCount returns rows missing a usable channel_id (WO-013).
func (s *Store) ChannelUnknownCount() (int64, error) {
	var n int64
	err := s.db.QueryRow(`
SELECT COUNT(*) FROM impressions
WHERE channel_id IS NULL OR channel_id = '' OR channel_unknown != 0`).Scan(&n)
	return n, err
}

// RecordFailures increments the extraction failure counter.
func (s *Store) RecordFailures(n int) error {
	if n <= 0 {
		return nil
	}
	_, err := s.db.Exec(`
INSERT INTO meta(key, value) VALUES('extraction_failures', ?)
ON CONFLICT(key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + ? AS TEXT)`,
		fmt.Sprintf("%d", n), n)
	return err
}

// Count returns SELECT COUNT(*) FROM impressions.
func (s *Store) Count() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&n)
	return n, err
}

// DownloadsDir is the user-visible export location (~/Downloads or XDG).
func DownloadsDir() (string, error) {
	if x := os.Getenv("KEEL_EXPORT_DIR"); x != "" {
		return x, nil
	}
	if x := os.Getenv("XDG_DOWNLOAD_DIR"); x != "" {
		return x, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}

// exportRow is one impression in the on-disk JSON export (WO-012).
// badges is a real array, not a stringified badges_json.
type exportRow struct {
	PageLoadID       string          `json:"page_load_id"`
	ObservedAt       int64           `json:"observed_at"`
	Surface          string          `json:"surface"`
	ContextVideoID   *string         `json:"context_video_id"`
	ContextQueryHash *string         `json:"context_query_hash"`
	SlotIndex        int             `json:"slot_index"`
	VideoID          string          `json:"video_id"`
	ChannelID        *string         `json:"channel_id"`
	ChannelUnknown   bool            `json:"channel_unknown"`
	Title            string          `json:"title"`
	DurationS        *float64        `json:"duration_s"`
	ViewCount        *float64        `json:"view_count"`
	PublishedAt      *string         `json:"published_at"`
	Badges           json.RawMessage `json:"badges"`
}

// ExportToFile writes the full corpus to path. Returns row count and file size.
// Path should already be chosen by the caller (typically under Downloads).
func (s *Store) ExportToFile(path, daemonVersion string) (rows int64, bytesWritten int64, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, 0, err
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()

	count, err := s.Count()
	if err != nil {
		return 0, 0, err
	}
	unknownCh, err := s.ChannelUnknownCount()
	if err != nil {
		return 0, 0, err
	}

	// Stream: header object opening + impressions array, row by row.
	// Manual framing keeps peak memory O(1) relative to corpus size.
	// channel_unknown_count: WO-013 — scrolled rail cards often lack channel_id.
	header := fmt.Sprintf(
		`{"schema_version":%d,"daemon_version":%s,"exported_at":%s,"row_count":%d,"channel_unknown_count":%d,"channel_known_count":%d,"impressions":[`,
		ExportSchemaVersion,
		mustJSONString(daemonVersion),
		mustJSONString(time.Now().UTC().Format(time.RFC3339Nano)),
		count,
		unknownCh,
		count-unknownCh,
	)
	if _, err := f.WriteString(header); err != nil {
		return 0, 0, err
	}

	qrows, err := s.db.Query(`
SELECT page_load_id, observed_at, surface, context_video_id, context_query_hash,
       slot_index, video_id, channel_id, channel_unknown, title,
       duration_s, view_count, published_at, badges_json
FROM impressions
ORDER BY observed_at ASC, page_load_id ASC, slot_index ASC`)
	if err != nil {
		return 0, 0, err
	}
	defer qrows.Close()

	first := true
	var written int64
	for qrows.Next() {
		var r exportRow
		var ctxVid, ctxQ, chID, pub sql.NullString
		var dur, views sql.NullFloat64
		var unk int
		var badgesStr string
		if err := qrows.Scan(
			&r.PageLoadID, &r.ObservedAt, &r.Surface, &ctxVid, &ctxQ,
			&r.SlotIndex, &r.VideoID, &chID, &unk, &r.Title,
			&dur, &views, &pub, &badgesStr,
		); err != nil {
			return 0, 0, err
		}
		if ctxVid.Valid {
			r.ContextVideoID = &ctxVid.String
		}
		if ctxQ.Valid {
			r.ContextQueryHash = &ctxQ.String
		}
		if chID.Valid {
			r.ChannelID = &chID.String
		}
		r.ChannelUnknown = unk != 0
		if dur.Valid {
			r.DurationS = &dur.Float64
		}
		if views.Valid {
			r.ViewCount = &views.Float64
		}
		if pub.Valid {
			r.PublishedAt = &pub.String
		}
		if badgesStr == "" || !json.Valid([]byte(badgesStr)) {
			r.Badges = json.RawMessage("[]")
		} else {
			r.Badges = json.RawMessage(badgesStr)
		}

		if !first {
			if _, err := f.WriteString(","); err != nil {
				return 0, 0, err
			}
		}
		first = false
		// Encode without trailing newline for compact array elements.
		b, err := json.Marshal(r)
		if err != nil {
			return 0, 0, err
		}
		if _, err := f.Write(b); err != nil {
			return 0, 0, err
		}
		written++
	}
	if err := qrows.Err(); err != nil {
		return 0, 0, err
	}
	if _, err := f.WriteString("]}"); err != nil {
		return 0, 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		return written, 0, err
	}
	// Prefer counted rows from SELECT; written should match.
	if written != count {
		return written, fi.Size(), fmt.Errorf("export row mismatch: wrote %d count %d", written, count)
	}
	return written, fi.Size(), nil
}

// Wipe deletes every impression and VACUUMs so the file shrinks (WO-012).
// Does not modify meta (schema version / extraction_failures).
func (s *Store) Wipe() (deleted int64, err error) {
	res, err := s.db.Exec(`DELETE FROM impressions`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	// VACUUM cannot run inside a transaction; modernc/sqlite accepts it here.
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return n, fmt.Errorf("delete ok but VACUUM failed: %w", err)
	}
	return n, nil
}

func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// DB exposes the underlying handle for tests.
func (s *Store) DB() *sql.DB { return s.db }

// MaxSearchLimit caps how many hits one SEARCH returns.
const MaxSearchLimit = 200

// searchTerms splits a query into lowercase terms. Quoted phrases are kept
// whole so "foo bar" matches adjacently rather than as two loose terms.
func searchTerms(q string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
			if !inQuote {
				flush()
			}
		case !inQuote && (r == ' ' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// SearchVideos searches the local catalogue by title and channel.
//
// Every term must match (AND), which is what makes it behave like a search
// engine rather than a suggestion box — YouTube's own search quietly drops
// terms to surface trending content (User Utility Architecture §1).
//
// Results are the deduplicated catalogue view: one row per video_id, not one
// per observation, so a video seen fifty times appears once with seen = 50.
func (s *Store) SearchVideos(query string, limit int) (*bridge.SearchResultPayload, error) {
	terms := searchTerms(query)
	out := &bridge.SearchResultPayload{Query: query, Hits: []bridge.SearchHit{}}
	if len(terms) == 0 {
		return out, nil
	}
	if limit <= 0 || limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	var where strings.Builder
	args := []any{}
	for i, t := range terms {
		if i > 0 {
			where.WriteString(" AND ")
		}
		// COALESCE so a null channel_id never excludes an otherwise-matching row.
		where.WriteString("(LOWER(title) LIKE ? OR LOWER(COALESCE(channel_id,'')) LIKE ?)")
		like := "%" + t + "%"
		args = append(args, like, like)
	}

	// Union the peer catalogue: a video that arrived in a bundle is part of the
	// searchable catalogue even though nobody here observed it (WO-031).
	countSQL := `SELECT COUNT(*) FROM (
  SELECT video_id FROM impressions WHERE ` + where.String() + ` GROUP BY video_id
  UNION
  SELECT video_id FROM peer_catalogue WHERE ` + where.String() + `
)`
	if err := s.db.QueryRow(countSQL, append(append([]any{}, args...), args...)...).Scan(&out.Total); err != nil {
		return nil, err
	}

	// Rank: most-observed first (repetition is the signal this corpus has),
	// then view count, then recency. No relevance scoring — every term already
	// matched, so ordering by corpus evidence beats a synthetic score.
	// seen = 0 marks a video known only from a peer bundle: catalogued here,
	// never observed here. Local observations still rank first.
	rowsSQL := `
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
  WHERE ` + where.String() + `
  GROUP BY video_id
  UNION ALL
  SELECT video_id, title, channel_id, duration_s, view_count, published_at, 0 AS seen, 0 AS last_seen
  FROM peer_catalogue
  WHERE ` + where.String() + `
    AND video_id NOT IN (SELECT video_id FROM impressions)
)
ORDER BY seen DESC, view_count DESC, last_seen DESC
LIMIT ?`
	dual := append(append([]any{}, args...), args...)
	rows, err := s.db.Query(rowsSQL, append(dual, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var h bridge.SearchHit
		var title sql.NullString
		var ch, pub sql.NullString
		var dur, views sql.NullFloat64
		if err := rows.Scan(&h.VideoID, &title, &ch, &dur, &views, &pub, &h.Seen, &h.LastSeenAt); err != nil {
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
		out.Hits = append(out.Hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Truncated = out.Total > int64(len(out.Hits))
	return out, nil
}
