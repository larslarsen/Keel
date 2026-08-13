// SPDX-License-Identifier: Apache-2.0
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
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
	// path is where the database actually is, which is not always where it was
	// asked to go — see Open's fallback chain.
	path string

	// liveMu guards liveNow, the set of videos the swarm currently believes are
	// live. It is pushed in from outside because the live index lives in the
	// swarm package, which imports this one.
	liveMu  sync.RWMutex
	liveNow map[string]bool
}

// LiveSighting is one local observation of a stream running.
type LiveSighting struct {
	VideoID   string
	Title     string
	ChannelID string
	SeenAt    int64
	// Platform the sighting was on.
	Platform string
	// StartedAt is the earliest this node saw the stream carrying a LIVE badge.
	// A lower bound on how long it has been running, and the only evidence
	// available — YouTube does not put a start time on a rail card.
	StartedAt int64
}

// RecentLiveSightings returns streams this node saw live since cutoff.
//
// The live index is deliberately in-memory, so a restart empties it and it
// refills only as gossip trickles in — which on a network with no peers means
// never. But this node's own sightings are already on disk in `impressions`,
// so they can be replayed at startup at no cost and with nothing persisted that
// was not already kept.
func (s *Store) RecentLiveSightings(cutoff int64) ([]LiveSighting, error) {
	rows, err := s.db.Query(`
SELECT video_id, MAX(title), MAX(channel_id), MAX(observed_at), MIN(observed_at), platform
FROM impressions
WHERE badges_json LIKE '%LIVE%'
GROUP BY platform, video_id
HAVING MAX(observed_at) >= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveSighting{}
	for rows.Next() {
		var v LiveSighting
		var title, ch sql.NullString
		if err := rows.Scan(&v.VideoID, &title, &ch, &v.SeenAt, &v.StartedAt, &v.Platform); err != nil {
			return nil, err
		}
		v.Title, v.ChannelID = title.String, ch.String
		out = append(out, v)
	}
	return out, rows.Err()
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
func (s *Store) currentlyLive(platform string) (map[string]bool, error) {
	out := map[string]bool{}
	s.liveMu.RLock()
	for id := range s.liveNow {
		out[id] = true
	}
	s.liveMu.RUnlock()

	cutoff := time.Now().Add(-LiveRecency).UnixMilli()
	rows, err := s.db.Query(`
SELECT DISTINCT video_id FROM impressions
WHERE observed_at >= ? AND badges_json LIKE '%LIVE%' AND platform = ?`, cutoff, platform)
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

// Open creates/opens the database, falling back to a usable location if the
// preferred one cannot be opened.
//
// The preferred location is the OS config directory, and on a machine where
// that is unwritable — a denied ACL, a redirected or roaming profile, a
// read-only volume — SQLite answers with SQLITE_CANTOPEN and the daemon simply
// dies at startup. The user sees a browser panel saying the desktop app is not
// running, with nothing anywhere naming a path. That is not a state worth
// preserving: a working corpus somewhere sensible beats no corpus at all, and
// the chosen path is logged either way.
//
// Continuity beats preference: if any candidate already holds a database, that
// one wins even if an earlier candidate is writable now. Otherwise the first
// writable candidate is used. Only if every candidate fails does Open return an
// error, and that error lists what was tried and why each one failed.
func Open(path string) (*Store, error) {
	candidates := dbCandidates(path)
	if len(candidates) == 0 {
		return nil, errors.New("no usable location for the database")
	}

	// An existing database keeps its contents reachable.
	chosen := ""
	for _, c := range candidates {
		// Size, not existence: preflightDatabasePath creates the file before
		// SQLite opens it, so a location that failed after the probe leaves a
		// 0-byte keel.sqlite behind. Treating that as "a corpus lives here"
		// would pin every future start to the location that just failed.
		fi, err := os.Stat(c)
		if err != nil || fi.Size() == 0 {
			continue
		}
		// Openable, not merely present: a file can exist in a writable directory
		// and still deny this user, and adopting it means every start fails on a
		// database that will never open.
		if f, err := os.OpenFile(c, os.O_RDWR, 0); err != nil {
			continue
		} else {
			_ = f.Close()
		}
		if err := preflightDatabasePath(c); err == nil {
			chosen = c
			break
		}
	}
	var problems []string
	if chosen == "" {
		for _, c := range candidates {
			if err := preflightDatabasePath(c); err != nil {
				problems = append(problems, err.Error())
				continue
			}
			chosen = c
			break
		}
	}
	if chosen == "" {
		// One candidate means the caller named it — KEEL_DB, KEEL_DATA_DIR, or
		// a packaging layout — and "any location" would be a lie that sends the
		// reader looking for a fallback bug instead of at their own setting.
		if len(candidates) == 1 {
			return nil, fmt.Errorf(
				"could not open the database at %s (this exact path was requested, "+
					"so no other location was tried — check KEEL_DB and KEEL_DATA_DIR): %s",
				candidates[0], strings.Join(problems, "; "))
		}
		return nil, fmt.Errorf("could not open a database in any of %d locations: %s",
			len(candidates), strings.Join(problems, "; "))
	}
	if chosen != candidates[0] {
		log.Printf("database: %s was not usable, using %s instead", candidates[0], chosen)
	}

	db, err := sql.Open("sqlite", chosen+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", chosen, err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: chosen}
	// sql.Open is lazy: this is the first call that actually touches the file,
	// so it is where a path problem surfaces. Name the file.
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s: %w", chosen, err)
	}
	// Best-effort: a repair failure must not stop the daemon starting.
	if err := s.repairPublishedAt(); err != nil {
		log.Printf("published_at repair skipped: %v", err)
	}
	return s, nil
}

// Path is where this store actually ended up, which is not always where it was
// asked to go. The interface reports it so a fallback is visible rather than
// mysterious.
func (s *Store) Path() string { return s.path }

// dbCandidates lists database locations in preference order.
//
// An explicit path (KEEL_DB, tests, packaging) is always first and is honoured
// alone when it is usable. The rest exist so that one unwritable directory
// cannot stop the daemon starting: LOCALAPPDATA is where the Windows installer
// already writes and verifies the native-host manifests, so it is known-good on
// exactly the machines where the config directory tends to fail.
func dbCandidates(explicit string) []string {
	// An explicitly named database is never relocated. KEEL_DB, a test, or a
	// packaging layout names a specific file on purpose; quietly opening a
	// different one would split a corpus in half or write into somebody else's.
	// Falling back is only ever right for the location Keel chose itself.
	if explicit != "" {
		return []string{explicit}
	}
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, existing := range out {
			if existing == p {
				return
			}
		}
		out = append(out, p)
	}

	add(explicit)
	if dir, err := defaultDir(); err == nil {
		add(filepath.Join(dir, "keel.sqlite"))
	}
	if v := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); v != "" {
		add(filepath.Join(v, "Keel", "keel.sqlite"))
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(exe), "keel-data", "keel.sqlite"))
	}
	add(filepath.Join(os.TempDir(), "keel", "keel.sqlite"))
	return out
}

func preflightDatabasePath(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create the folder for the database at %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open the database file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close the database file %s: %w", path, err)
	}
	// WAL writes keel.sqlite-wal and keel.sqlite-shm beside the database.
	probe := path + "-preflight"
	pf, err := os.OpenFile(probe, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("the folder %s is not writable (WAL needs to create files beside the database): %w", dir, err)
	}
	_ = pf.Close()
	_ = os.Remove(probe)
	return nil
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

// addColumnIfMissing extends a table in place. SQLite has no IF NOT EXISTS for
// columns, so presence is checked first.
// platformOf defaults an absent platform to YouTube and refuses one this build
// does not know — an unrecognised value would drop out of every scoped query.
func platformOf(p string) string {
	if p == "" || !bridge.KnownPlatforms[p] {
		return bridge.PlatformYouTube
	}
	return p
}

func (s *Store) addColumnIfMissing(table, column, decl string) {
	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n)
	if n == 0 {
		_, _ = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl)
	}
}

func (s *Store) migrate() error {
	// Schema v2: PK (page_load_id, surface, video_id); nullable channel_id + channel_unknown.
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS impressions (
  page_load_id TEXT NOT NULL,
  observed_at INTEGER NOT NULL,
  surface TEXT NOT NULL,
  context_video_id TEXT,
  context_query_hash TEXT,
  context_title TEXT,
  -- Which platform this was observed on. Defaulted rather than nullable: every
  -- row predating TikTok is a YouTube row, and a NULL here would fall out of
  -- every platform-scoped query silently.
  platform TEXT NOT NULL DEFAULT 'yt',
  slot_index INTEGER NOT NULL,
  video_id TEXT NOT NULL,
  channel_id TEXT,
  channel_unknown INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL,
  duration_s REAL,
  view_count REAL,
  published_at TEXT,
  badges_json TEXT NOT NULL DEFAULT '[]',
  hashtags_json TEXT NOT NULL DEFAULT '[]',
  sound_id TEXT,
  dwell_pct REAL,
  engagement TEXT,
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
-- Gossiped per-token cardinality sketches (WO-067) — the local half of the
-- distributed search's global-count mechanism. Evictable under the same
-- disk budget as thumbnails, not because it is observation data (it is
-- not — see sketch.go's doc comment on why a sketch cannot be enumerated or
-- tested for membership) but because it is refetchable: gossip refills it,
-- the same way the CDN refills a dropped thumbnail.
CREATE TABLE IF NOT EXISTS token_sketches (
  token_index INTEGER PRIMARY KEY,
  p INTEGER NOT NULL,
  registers BLOB NOT NULL,
  size INTEGER NOT NULL,
  last_observed INTEGER,
  last_observed_at INTEGER,
  due_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_token_sketches_lru ON token_sketches(last_used_at);
CREATE INDEX IF NOT EXISTS idx_token_sketches_due ON token_sketches(due_at);
CREATE TABLE IF NOT EXISTS channel_blocklist (
  channel_id TEXT PRIMARY KEY,
  blocked_at INTEGER NOT NULL
);
-- The watch queue (WO-064). User intent, not observation: an ordered list of
-- videos to watch later. Stored here rather than in the browser because the
-- extension holds no state (§2.1), and position is explicit so reordering does
-- not have to rewrite when something was added.
CREATE TABLE IF NOT EXISTS watch_queue (
  video_id TEXT PRIMARY KEY,
  platform TEXT NOT NULL DEFAULT 'yt',
  position INTEGER NOT NULL,
  added_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_watch_queue_pos ON watch_queue(position);

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

-- Graph claims imported from peers, kept verbatim so they can be re-served
-- unchanged (WO-084).
--
-- peer_edges above is the *reading* view: flattened rows the local graph walk
-- consumes, where many peers agreeing about an edge is signal worth summing.
-- Re-serving from that view is what WO-084 removed. Summing across sources and
-- re-signing the total under this node's own key turned every relay hop into a
-- new observation by a new source, so a claim making a loop came back larger
-- than it left, and no holder could tell an original from its echoes.
--
-- A row here is one publisher's signed statement about one neighbourhood,
-- stored as the bytes that verified. Serving it means handing it on; this node
-- adds nothing and re-signs nothing. Several publishers may hold rows for the
-- same graph_key — that is the honest shape of "many people saw this
-- neighbourhood" — while (claim_id, graph_key) is unique, so an updated claim
-- replaces its own prior version and a relay cycle re-delivering an unchanged
-- claim is a no-op.
CREATE TABLE IF NOT EXISTS peer_blocks (
  claim_id TEXT NOT NULL,
  graph_key TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 0,
  block_json BLOB NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (claim_id, graph_key)
);
CREATE INDEX IF NOT EXISTS idx_peer_blocks_key ON peer_blocks(graph_key);

-- The revision counter and public key for each neighbourhood this node
-- publishes (WO-084, claim.go).
--
-- The private key is not here: it is derived on demand from the claim root
-- secret in the meta table, so this holds nothing that could sign anything. The
-- public key is stored so an incoming claim can be recognised as this node's
-- own and dropped rather than imported as a peer's.
CREATE TABLE IF NOT EXISTS local_claims (
  graph_key TEXT PRIMARY KEY,
  public_key TEXT NOT NULL,
  revision INTEGER NOT NULL,
  content_sha256 TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_local_claims_pub ON local_claims(public_key);

-- Cumulative serving activity (WO-086). Exactly one row, counters only: no
-- peer id, query, prefix/bucket identifier or per-request timestamp — see
-- contribution_impact.go. Bounded by construction, not by a sweep: the code
-- path only ever UPDATEs this row or INSERTs once into an empty table.
CREATE TABLE IF NOT EXISTS contribution_activity (
  requests_answered INTEGER NOT NULL DEFAULT 0,
  bytes_served INTEGER NOT NULL DEFAULT 0,
  since_day TEXT NOT NULL DEFAULT ''
);
`)
	if err != nil {
		return err
	}
	if err := s.migrateToV2IfNeeded(); err != nil {
		return err
	}
	s.addColumnIfMissing("impressions", "context_title", "TEXT")
	s.addColumnIfMissing("impressions", "platform", "TEXT NOT NULL DEFAULT 'yt'")
	// WO-063: TikTok Mirror fields. Nullable; YouTube rows leave them empty.
	s.addColumnIfMissing("impressions", "hashtags_json", "TEXT NOT NULL DEFAULT '[]'")
	s.addColumnIfMissing("impressions", "sound_id", "TEXT")
	s.addColumnIfMissing("impressions", "dwell_pct", "REAL")
	s.addColumnIfMissing("impressions", "engagement", "TEXT")
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
  context_title TEXT,
  -- Which platform this was observed on. Defaulted rather than nullable: every
  -- row predating TikTok is a YouTube row, and a NULL here would fall out of
  -- every platform-scoped query silently.
  platform TEXT NOT NULL DEFAULT 'yt',
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
  slot_index, video_id, channel_id, channel_unknown, title, duration_s, view_count, published_at, badges_json,
  context_title, platform, hashtags_json, sound_id, dwell_pct, engagement
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
  badges_json=excluded.badges_json,
  context_title=COALESCE(excluded.context_title, impressions.context_title),
  hashtags_json=CASE
    WHEN excluded.hashtags_json IS NOT NULL AND excluded.hashtags_json != '[]'
      THEN excluded.hashtags_json ELSE impressions.hashtags_json END,
  sound_id=COALESCE(excluded.sound_id, impressions.sound_id),
  dwell_pct=COALESCE(excluded.dwell_pct, impressions.dwell_pct),
  engagement=COALESCE(excluded.engagement, impressions.engagement)
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
		if imp.Hashtags == nil {
			imp.Hashtags = []string{}
		}
		hashtags, _ := json.Marshal(imp.Hashtags)
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
			imp.ContextTitle, platformOf(imp.Platform),
			string(hashtags), imp.SoundID, imp.DwellPct, imp.Engagement,
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
// titleForVideo finds a title for a video from anything this node knows.
//
// Both tables, because the video being watched is often not in `impressions` at
// all. Keel captures watch-page rails and the homepage, so a video reached from
// search, subscriptions, a channel page, a link or autoplay was never recorded
// as something offered — even though the user is plainly watching it. The
// catalogue may still know its title, from a peer or from a bundle.
//
// There is no privacy question in showing this. The watched video's id is
// already recorded as `context_video_id` — it is the thing every edge hangs
// off — and a title is a public fact about a public video.
func (s *Store) titleForVideo(videoID string) *string {
	var t sql.NullString
	err := s.db.QueryRow(`
SELECT title FROM (
  SELECT context_title AS title FROM impressions
    WHERE context_video_id = ? AND context_title IS NOT NULL AND context_title != ''
  UNION ALL
  SELECT title FROM impressions WHERE video_id = ? AND title != ''
  UNION ALL
  SELECT title FROM peer_catalogue WHERE video_id = ? AND title != ''
) LIMIT 1`, videoID, videoID, videoID).Scan(&t)
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
