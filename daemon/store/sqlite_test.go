// SPDX-License-Identifier: Apache-2.0
package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

func TestPutIdempotentByVideo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := "contextvid01"
	ch := "@ch"
	imp := bridge.Impression{
		PageLoadID:     "11111111-1111-4111-8111-111111111111",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		SlotIndex:      0,
		VideoID:        "abcdefghijk",
		ChannelID:      &ch,
		Title:          "Hello",
		Badges:         []string{},
	}
	// Same video, different slot_index — must not create a second row; keep first slot
	dup := imp
	dup.SlotIndex = 5
	dup.Title = "Hello updated"

	if _, err := st.PutImpressions([]bridge.Impression{imp, dup}); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count %d want 1", count)
	}
	var slot int
	var title string
	if err := st.DB().QueryRow(
		`SELECT slot_index, title FROM impressions WHERE video_id=?`, imp.VideoID,
	).Scan(&slot, &title); err != nil {
		t.Fatal(err)
	}
	if slot != 0 {
		t.Fatalf("slot_index %d want 0 (first observed)", slot)
	}
	if title != "Hello updated" {
		t.Fatalf("title %q want updated metadata", title)
	}

	// Old rows are retained (no sweep)
	old := imp
	old.VideoID = "oldvideoid1"
	old.SlotIndex = 1
	old.ObservedAt = time.Now().Add(-100 * 24 * time.Hour).UnixMilli()
	if _, err := st.PutImpressions([]bridge.Impression{old}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 2 {
		t.Fatalf("total %d want 2 (no retention delete)", stats.Total)
	}

	// Null channel is valid (live lockup DOM)
	nullCh := bridge.Impression{
		PageLoadID:     "11111111-1111-4111-8111-111111111111",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		SlotIndex:      3,
		VideoID:        "nochannel01",
		ChannelID:      nil,
		ChannelUnknown: true,
		Title:          "No channel",
		Badges:         []string{},
	}
	if _, err := st.PutImpressions([]bridge.Impression{nullCh}); err != nil {
		t.Fatal(err)
	}
	var unk int
	var cid sql.NullString
	if err := st.DB().QueryRow(
		`SELECT channel_id, channel_unknown FROM impressions WHERE video_id=?`, "nochannel01",
	).Scan(&cid, &unk); err != nil {
		t.Fatal(err)
	}
	if cid.Valid {
		t.Fatalf("channel_id want NULL got %q", cid.String)
	}
	if unk != 1 {
		t.Fatalf("channel_unknown %d want 1", unk)
	}
}

func seedImpressions(t *testing.T, st *Store, n int) {
	t.Helper()
	ctx := "contextvid01"
	list := make([]bridge.Impression, 0, n)
	for i := 0; i < n; i++ {
		vid := fmtVideoID(i)
		var ch *string
		unk := true
		badges := []string{}
		if i%2 == 0 {
			c := "UCdy1IW4I7DnkU_3v0zMWDpQ"
			ch = &c
			unk = false
			badges = []string{"LIVE"}
		}
		list = append(list, bridge.Impression{
			PageLoadID:     "11111111-1111-4111-8111-111111111111",
			ObservedAt:     time.Now().UnixMilli() + int64(i),
			Surface:        "WATCH_NEXT",
			ContextVideoID: &ctx,
			SlotIndex:      i,
			VideoID:        vid,
			ChannelID:      ch,
			ChannelUnknown: unk,
			Title:          "Title " + vid,
			Badges:         badges,
		})
	}
	if _, err := st.PutImpressions(list); err != nil {
		t.Fatal(err)
	}
}

// 11-char video ids for tests (unique for each i).
func fmtVideoID(i int) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"
	b := make([]byte, 11)
	n := i
	for j := 0; j < 11; j++ {
		b[j] = chars[n%len(chars)]
		n /= len(chars)
	}
	return string(b)
}

func TestExportAndWipe(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.sqlite")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seedImpressions(t, st, 12)
	// Spot-check null channel row exists (odd indices).
	var nullCount int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM impressions WHERE channel_id IS NULL`,
	).Scan(&nullCount); err != nil {
		t.Fatal(err)
	}
	if nullCount == 0 {
		t.Fatal("expected null channel_id rows for round-trip")
	}

	exportPath := filepath.Join(dir, "out.json")
	rows, nbytes, err := st.ExportToFile(exportPath, "0.1.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if rows != 12 {
		t.Fatalf("rows %d want 12", rows)
	}
	if nbytes <= 0 {
		t.Fatalf("bytes %d", nbytes)
	}
	fi, err := os.Stat(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != nbytes {
		t.Fatalf("stat size %d vs returned %d", fi.Size(), nbytes)
	}

	raw, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion       int              `json:"schema_version"`
		DaemonVersion       string           `json:"daemon_version"`
		ExportedAt          string           `json:"exported_at"`
		RowCount            int64            `json:"row_count"`
		ChannelUnknownCount int64            `json:"channel_unknown_count"`
		ChannelKnownCount   int64            `json:"channel_known_count"`
		Impressions         []map[string]any `json:"impressions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if doc.SchemaVersion != ExportSchemaVersion {
		t.Fatalf("schema %d", doc.SchemaVersion)
	}
	if doc.RowCount != 12 || int64(len(doc.Impressions)) != 12 {
		t.Fatalf("row_count %d len %d", doc.RowCount, len(doc.Impressions))
	}
	// seedImpressions: even i have channel, odd i null → 6 unknown, 6 known
	if doc.ChannelUnknownCount != 6 || doc.ChannelKnownCount != 6 {
		t.Fatalf("channel counts unknown=%d known=%d want 6/6",
			doc.ChannelUnknownCount, doc.ChannelKnownCount)
	}
	stats, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelUnknown != 6 || stats.ChannelKnown != 6 {
		t.Fatalf("stats channel unknown=%d known=%d", stats.ChannelUnknown, stats.ChannelKnown)
	}
	// Count(*) matches
	var cnt int64
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != doc.RowCount {
		t.Fatalf("COUNT(*) %d vs export %d", cnt, doc.RowCount)
	}

	// Spot-check: a null channel_id and a non-empty badges array.
	var sawNullCh, sawBadges bool
	for _, imp := range doc.Impressions {
		if imp["channel_id"] == nil {
			sawNullCh = true
		}
		if b, ok := imp["badges"].([]any); ok && len(b) > 0 {
			sawBadges = true
			// badges must not be a string
		}
		if _, isStr := imp["badges"].(string); isStr {
			t.Fatal("badges must be array not string")
		}
	}
	if !sawNullCh {
		t.Fatal("export missing null channel_id")
	}
	if !sawBadges {
		t.Fatal("export missing non-empty badges array")
	}

	// Wipe
	deleted, err := st.Wipe()
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 12 {
		t.Fatalf("deleted %d want 12", deleted)
	}
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("after wipe COUNT(*)=%d", cnt)
	}

	// Wipe-then-export empty corpus
	emptyPath := filepath.Join(dir, "empty.json")
	rows, _, err = st.ExportToFile(emptyPath, "0.1.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("empty export rows %d", rows)
	}
	raw, err = os.ReadFile(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.RowCount != 0 || len(doc.Impressions) != 0 {
		t.Fatalf("empty doc %+v", doc)
	}
}

func TestChannelsTableUpsertsName(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := "contextvid01"
	ch := "UCdy1IW4I7DnkU_3v0zMWDpQ"
	name := "Tommy TV"
	imp := bridge.Impression{
		PageLoadID:     "11111111-1111-4111-8111-111111111111",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		SlotIndex:      0,
		VideoID:        "chanvid001",
		ChannelID:      &ch,
		ChannelName:    &name,
		Title:          "A video",
		Badges:         []string{},
	}
	if _, err := st.PutImpressions([]bridge.Impression{imp}); err != nil {
		t.Fatal(err)
	}

	var got string
	if err := st.db.QueryRow(
		`SELECT name FROM channels WHERE channel_id = ?`, ch,
	).Scan(&got); err != nil {
		t.Fatalf("channels row missing: %v", err)
	}
	if got != name {
		t.Fatalf("want %q, got %q", name, got)
	}

	// A later card with a renamed/updated handle replaces the stored name.
	name2 := "Tommy TV Live"
	imp2 := imp
	imp2.ChannelName = &name2
	imp2.PageLoadID = "22222222-2222-4222-8222-222222222222"
	if _, err := st.PutImpressions([]bridge.Impression{imp2}); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(
		`SELECT name FROM channels WHERE channel_id = ?`, ch,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != name2 {
		t.Fatalf("want %q, got %q", name2, got)
	}

	// channel_name=null must not create a row or clobber the stored name.
	imp3 := imp2
	imp3.ChannelName = nil
	imp3.PageLoadID = "33333333-3333-4333-8333-333333333333"
	if _, err := st.PutImpressions([]bridge.Impression{imp3}); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(
		`SELECT name FROM channels WHERE channel_id = ?`, ch,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != name2 {
		t.Fatalf("null channel_name clobbered name: want %q, got %q", name2, got)
	}
}

func TestChannelBackfillAndBlocklist(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := "contextvid01"
	ch := "UCdy1IW4I7DnkU_3v0zMWDpQ"
	// First observation has channel
	known := bridge.Impression{
		PageLoadID:     "11111111-1111-4111-8111-111111111111",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		SlotIndex:      0,
		VideoID:        "backfillvid1",
		ChannelID:      &ch,
		Title:          "Known",
		Badges:         []string{},
	}
	// Second page load, same video, no channel — should backfill
	unk := bridge.Impression{
		PageLoadID:     "22222222-2222-4222-8222-222222222222",
		ObservedAt:     time.Now().UnixMilli() + 1,
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		SlotIndex:      3,
		VideoID:        "backfillvid1",
		ChannelID:      nil,
		ChannelUnknown: true,
		Title:          "Unknown at observe",
		Badges:         []string{},
	}
	if _, err := st.PutImpressions([]bridge.Impression{known}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutImpressions([]bridge.Impression{unk}); err != nil {
		t.Fatal(err)
	}
	var gotCh sql.NullString
	var gotUnk int
	if err := st.DB().QueryRow(
		`SELECT channel_id, channel_unknown FROM impressions WHERE page_load_id=? AND video_id=?`,
		unk.PageLoadID, unk.VideoID,
	).Scan(&gotCh, &gotUnk); err != nil {
		t.Fatal(err)
	}
	if !gotCh.Valid || gotCh.String != ch {
		t.Fatalf("channel backfill got %v want %s", gotCh, ch)
	}
	if gotUnk != 0 {
		t.Fatalf("channel_unknown %d want 0", gotUnk)
	}

	// Blocklist persists
	if err := st.BlockChannel(ch); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListBlocklist()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != ch {
		t.Fatalf("blocklist %+v", list)
	}
	// Corpus still has rows
	var cnt int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM impressions WHERE channel_id=?`, ch).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt < 2 {
		t.Fatalf("corpus count %d", cnt)
	}
	if err := st.UnblockChannel(ch); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListBlocklist()
	if len(list) != 0 {
		t.Fatalf("unblock left %v", list)
	}

	// Catalogue backfill of existing unknowns
	orphanVid := "orphanvid001"
	orphan := bridge.Impression{
		PageLoadID:     "33333333-3333-4333-8333-333333333333",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		SlotIndex:      1,
		VideoID:        orphanVid,
		ChannelID:      nil,
		ChannelUnknown: true,
		Title:          "Orphan first",
		Badges:         []string{},
	}
	// Insert orphan before any known channel for that video
	if _, err := st.PutImpressions([]bridge.Impression{orphan}); err != nil {
		t.Fatal(err)
	}
	// Later a different row learns the channel (simulate via direct SQL then Backfill)
	// Actually insert a known row for same video
	laterKnown := bridge.Impression{
		PageLoadID:     "44444444-4444-4444-8444-444444444444",
		ObservedAt:     time.Now().UnixMilli() + 10,
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		SlotIndex:      0,
		VideoID:        orphanVid,
		ChannelID:      &ch,
		Title:          "Later known",
		Badges:         []string{},
	}
	if _, err := st.PutImpressions([]bridge.Impression{laterKnown}); err != nil {
		t.Fatal(err)
	}
	n, err := st.BackfillChannelsFromCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("backfill updated %d want ≥1", n)
	}
	if err := st.DB().QueryRow(
		`SELECT channel_id, channel_unknown FROM impressions WHERE page_load_id=?`,
		orphan.PageLoadID,
	).Scan(&gotCh, &gotUnk); err != nil {
		t.Fatal(err)
	}
	if !gotCh.Valid || gotCh.String != ch || gotUnk != 0 {
		t.Fatalf("orphan after backfill ch=%v unk=%d", gotCh, gotUnk)
	}
}

func TestExplainVideo(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "explain.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctxA := "contextvid0A"
	ctxB := "contextvid0B"
	target := "targetvid001"
	// Context A has a title as its own impression (user watched it later as rec)
	if _, err := st.PutImpressions([]bridge.Impression{
		{
			PageLoadID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			ObservedAt: time.Now().UnixMilli(),
			Surface:    "HOME",
			SlotIndex:  0,
			VideoID:    ctxA,
			Title:      "Context A Title",
			Badges:     []string{},
		},
		// Target appears after A three times at slots 1,1,3 → median 1
		{
			PageLoadID:     "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			ObservedAt:     time.Now().UnixMilli() + 1,
			Surface:        "WATCH_NEXT",
			ContextVideoID: &ctxA,
			SlotIndex:      1,
			VideoID:        target,
			Title:          "Target Video",
			Badges:         []string{},
		},
		{
			PageLoadID:     "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
			ObservedAt:     time.Now().UnixMilli() + 2,
			Surface:        "WATCH_NEXT",
			ContextVideoID: &ctxA,
			SlotIndex:      1,
			VideoID:        target,
			Title:          "Target Video",
			Badges:         []string{},
		},
		{
			PageLoadID:     "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
			ObservedAt:     time.Now().UnixMilli() + 3,
			Surface:        "WATCH_NEXT",
			ContextVideoID: &ctxA,
			SlotIndex:      3,
			VideoID:        target,
			Title:          "Target Video",
			Badges:         []string{},
		},
		// After B once at slot 5 — no title for B
		{
			PageLoadID:     "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
			ObservedAt:     time.Now().UnixMilli() + 4,
			Surface:        "WATCH_NEXT",
			ContextVideoID: &ctxB,
			SlotIndex:      5,
			VideoID:        target,
			Title:          "Target Video",
			Badges:         []string{},
		},
		// HOME once
		{
			PageLoadID: "ffffffff-ffff-4fff-8fff-ffffffffffff",
			ObservedAt: time.Now().UnixMilli() + 5,
			Surface:    "HOME",
			SlotIndex:  2,
			VideoID:    target,
			Title:      "Target Video",
			Badges:     []string{},
		},
	}); err != nil {
		t.Fatal(err)
	}

	ex, err := st.ExplainVideo(target)
	if err != nil {
		t.Fatal(err)
	}
	if ex.TotalImpressions != 5 {
		t.Fatalf("total %d want 5", ex.TotalImpressions)
	}
	if ex.HomeImpressions != 1 {
		t.Fatalf("home %d want 1", ex.HomeImpressions)
	}
	if ex.Title == nil || *ex.Title != "Target Video" {
		t.Fatalf("title %+v", ex.Title)
	}
	if len(ex.Contexts) != 2 {
		t.Fatalf("contexts %d want 2", len(ex.Contexts))
	}
	// Ordered by count desc: A then B
	if ex.Contexts[0].ContextVideoID != ctxA || ex.Contexts[0].Count != 3 {
		t.Fatalf("ctx0 %+v", ex.Contexts[0])
	}
	if ex.Contexts[0].Title == nil || *ex.Contexts[0].Title != "Context A Title" {
		t.Fatalf("ctx0 title %+v", ex.Contexts[0].Title)
	}
	if ex.Contexts[0].MedianSlot != 1 {
		t.Fatalf("median A %v want 1", ex.Contexts[0].MedianSlot)
	}
	if ex.Contexts[1].ContextVideoID != ctxB || ex.Contexts[1].Count != 1 {
		t.Fatalf("ctx1 %+v", ex.Contexts[1])
	}
	if ex.Contexts[1].Title != nil {
		t.Fatalf("ctx B should have null title, got %v", *ex.Contexts[1].Title)
	}

	// Single impression only
	once := "oncevid00001"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID:     "11111111-1111-4111-8111-111111111111",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctxA,
		SlotIndex:      0,
		VideoID:        once,
		Title:          "Once",
		Badges:         []string{},
	}}); err != nil {
		t.Fatal(err)
	}
	ex1, err := st.ExplainVideo(once)
	if err != nil {
		t.Fatal(err)
	}
	if ex1.TotalImpressions != 1 || len(ex1.Contexts) != 1 {
		t.Fatalf("single %+v", ex1)
	}

	// HOME-only video
	homeOnly := "homeonlyvid1"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "22222222-2222-4222-8222-222222222222",
		ObservedAt: time.Now().UnixMilli(),
		Surface:    "HOME",
		SlotIndex:  4,
		VideoID:    homeOnly,
		Title:      "Home only",
		Badges:     []string{},
	}}); err != nil {
		t.Fatal(err)
	}
	exH, err := st.ExplainVideo(homeOnly)
	if err != nil {
		t.Fatal(err)
	}
	if exH.TotalImpressions != 1 || exH.HomeImpressions != 1 || len(exH.Contexts) != 0 {
		t.Fatalf("home-only %+v", exH)
	}
}

func TestExportManyBridgePayloadSmall(t *testing.T) {
	// ≥5000 rows: export file may be large; EXPORT_RESULT bridge payload is path only.
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "big.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Insert in batches — validation allows WATCH_NEXT with context.
	const total = 5000
	ctx := "contextvid01"
	for batch := 0; batch < total; batch += 500 {
		list := make([]bridge.Impression, 0, 500)
		for i := 0; i < 500 && batch+i < total; i++ {
			idx := batch + i
			vid := fmtVideoID(idx)
			list = append(list, bridge.Impression{
				PageLoadID:     "22222222-2222-4222-8222-222222222222",
				ObservedAt:     time.Now().UnixMilli() + int64(idx),
				Surface:        "WATCH_NEXT",
				ContextVideoID: &ctx,
				SlotIndex:      idx,
				VideoID:        vid,
				ChannelID:      nil,
				ChannelUnknown: true,
				Title:          "T " + vid,
				Badges:         []string{},
			})
		}
		if _, err := st.PutImpressions(list); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n < 5000 {
		// PK is (page_load_id, surface, video_id) — fmtVideoID must be unique.
		t.Fatalf("count %d want ≥5000 (video id collision?)", n)
	}

	path := filepath.Join(dir, "big.json")
	rows, nbytes, err := st.ExportToFile(path, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if rows != n {
		t.Fatalf("rows %d count %d", rows, n)
	}
	// Simulated bridge payload size (what EXPORT returns).
	payload, _ := json.Marshal(bridge.ExportResultPayload{
		Path: path, Rows: rows, Bytes: nbytes,
	})
	if len(payload) > 64*1024 {
		// Far under 1 MiB host→browser; flag if result meta is unexpectedly huge.
		t.Fatalf("EXPORT_RESULT payload %d bytes unexpectedly large", len(payload))
	}
	if len(payload) > bridge.MaxHostToBrowser {
		t.Fatalf("would exceed host→browser cap")
	}
}

func TestSearchVideos(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := "contextvid01"
	put := func(page, vid, title, ch string) {
		t.Helper()
		if _, err := st.PutImpressions([]bridge.Impression{{
			PageLoadID: page, ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
			ContextVideoID: &ctx, SlotIndex: 0,
			VideoID: vid, Title: title, ChannelID: &ch,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	put("11111111-1111-4111-8111-111111111111", "aaaaaaaaaa1", "Go concurrency patterns", "UCaaaaaaaaaaaaaaaaaaaaaa")
	put("22222222-2222-4222-8222-222222222222", "aaaaaaaaaa1", "Go concurrency patterns", "UCaaaaaaaaaaaaaaaaaaaaaa")
	put("33333333-3333-4333-8333-333333333333", "bbbbbbbbbb2", "Rust ownership explained", "UCbbbbbbbbbbbbbbbbbbbbbb")

	// Deduplicated to one row per video, carrying the observation count.
	r, err := st.SearchVideos("go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 || len(r.Hits) != 1 {
		t.Fatalf("want 1 hit, got total=%d hits=%d", r.Total, len(r.Hits))
	}
	if r.Hits[0].Seen != 2 {
		t.Fatalf("want seen=2 (two page loads, one video), got %d", r.Hits[0].Seen)
	}

	// Every term must match: AND, not OR.
	if r, err := st.SearchVideos("go rust", 10); err != nil {
		t.Fatal(err)
	} else if r.Total != 0 {
		t.Fatalf("AND semantics broken: want 0, got %d", r.Total)
	}

	// Quoted phrases stay together.
	if r, err := st.SearchVideos(`"ownership explained"`, 10); err != nil {
		t.Fatal(err)
	} else if r.Total != 1 {
		t.Fatalf("phrase search: want 1, got %d", r.Total)
	}

	// Empty query is not an error and matches nothing.
	if r, err := st.SearchVideos("   ", 10); err != nil {
		t.Fatal(err)
	} else if r.Total != 0 || len(r.Hits) != 0 {
		t.Fatalf("empty query should match nothing, got %d", r.Total)
	}
}

func TestSuggestEntropyAndBlocklist(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// One seed rail: a popular video at slot 0 and a niche one at slot 1.
	seed := "seedvideo01"
	put := func(page, vid, title, ch string, slot int, views float64) {
		t.Helper()
		v := views
		if _, err := st.PutImpressions([]bridge.Impression{{
			PageLoadID: page, ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
			ContextVideoID: &seed, SlotIndex: slot,
			VideoID: vid, Title: title, ChannelID: &ch, ViewCount: &v,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	put("11111111-1111-4111-8111-111111111111", "popularvid1", "Popular", "UCpopnnnnnnnnnnnnnnnnnnn", 0, 5_000_000)
	put("11111111-1111-4111-8111-111111111111", "nichevideo1", "Niche", "UCnichennnnnnnnnnnnnnnnn", 1, 12)

	// Focus (entropy 0) ranks by walk mass, so the slot-0 popular video leads.
	focus, err := st.Suggest(seed, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(focus.Suggestions) != 2 || focus.Suggestions[0].VideoID != "popularvid1" {
		t.Fatalf("focus should lead with the slot-0 popular video, got %+v", focus.Suggestions)
	}

	// Serendipity (entropy 100) damps by popularity, flipping the order.
	ser, err := st.Suggest(seed, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ser.Suggestions) == 0 || ser.Suggestions[0].VideoID != "nichevideo1" {
		t.Fatalf("serendipity should surface the niche video first, got %+v", ser.Suggestions)
	}

	// The seed itself is never suggested back.
	for _, s := range ser.Suggestions {
		if s.VideoID == seed {
			t.Fatal("seed returned as its own suggestion")
		}
	}

	// Blocked channels are excluded from suggestions.
	if err := st.BlockChannel("UCnichennnnnnnnnnnnnnnnn"); err != nil {
		t.Fatal(err)
	}
	blocked, err := st.Suggest(seed, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range blocked.Suggestions {
		if s.VideoID == "nichevideo1" {
			t.Fatal("blocked channel still suggested")
		}
	}
}

// TestSuggestRailCompetesForTopSlots is the regression for the user's complaint
// that the panel "stays the same" — the rail (what YouTube actually showed
// alongside the watched video) was being held out of the leading slots by a
// novelty reservation, so the top of the panel never reordered to the rail.
// Per the user's explicit call, the rail must compete for the top slots on its
// own walk mass. This test builds a seed whose rail contains a high-mass
// neighbour R and at least five non-rail (second-hop) candidates, then asserts
// R is allowed to lead — not forced to slot 6+ behind a novelty reservation.
func TestSuggestRailCompetesForTopSlots(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	seed := "seedvideo01"
	put := func(page, vid, title, ch string, slot int, views float64) {
		t.Helper()
		v := views
		if _, err := st.PutImpressions([]bridge.Impression{{
			PageLoadID: page, ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
			ContextVideoID: &seed, SlotIndex: slot,
			VideoID: vid, Title: title, ChannelID: &ch, ViewCount: &v,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// R is the seed's slot-0 rail neighbour: it holds the most walk mass.
	put("11111111-1111-4111-8111-111111111111", "railvideo01", "Rail", "UCrailnnnnnnnnnnnnnnnnnn", 0, 5_000_000)

	// Five non-rail second-hop candidates via a peer, so there are well over
	// five candidates and a novelty reservation would otherwise bury R.
	secondHops := []string{"secondhop01", "secondhop02", "secondhop03", "secondhop04", "secondhop05"}
	edges := make([]bridge.EdgeObservation, 0, len(secondHops))
	cats := make([]bridge.CatalogueEntry, 0, len(secondHops))
	for _, to := range secondHops {
		edges = append(edges, bridge.EdgeObservation{
			From: seed, To: to, Surface: "WATCH_NEXT",
			SlotBucket: "1", DayBucket: "2026-08-03", Cohort: "unknown", Count: 1,
		})
		cats = append(cats, bridge.CatalogueEntry{VideoID: to, Title: "Second " + to})
	}
	if _, _, err := st.ImportEdges("peer-a", edges, cats); err != nil {
		t.Fatal(err)
	}

	// Entropy 0: focus, so walk mass dominates and R (direct neighbour) ranks
	// first. With the rail allowed to compete, R leads. A novelty reservation
	// would have pushed it to slot 5+.
	res, err := st.Suggest(seed, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suggestions) == 0 {
		t.Fatal("no suggestions produced")
	}
	if res.Suggestions[0].VideoID != "railvideo01" {
		t.Fatalf("rail video should be allowed to lead, got top=%q (full=%+v)",
			res.Suggestions[0].VideoID, res.Suggestions)
	}
}

func TestEdgeObservationsBucketing(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Same day, same pair, slots 3 and 5 — both land in the "3-5" bucket and
	// must collapse to one observation with count 2. That collapse is the whole
	// point: exact slots are identifying, the bucket is the measurement.
	day := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC).UnixMilli()
	ctx := "ctxvideo0001"
	put := func(page string, slot int, at int64, vid string) {
		t.Helper()
		if _, err := st.PutImpressions([]bridge.Impression{{
			PageLoadID: page, ObservedAt: at, Surface: "WATCH_NEXT",
			ContextVideoID: &ctx, SlotIndex: slot, VideoID: vid, Title: "T",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	put("11111111-1111-4111-8111-111111111111", 3, day, "targetvid01")
	put("22222222-2222-4222-8222-222222222222", 5, day, "targetvid01")
	// Slot 0 stays exact — the top positions are kept separate on purpose.
	put("33333333-3333-4333-8333-333333333333", 0, day, "targetvid01")

	obs, err := st.EdgeObservations("GB-en")
	if err != nil {
		t.Fatal(err)
	}
	byBucket := map[string]int64{}
	for _, o := range obs {
		if o.From != ctx || o.To != "targetvid01" {
			continue
		}
		if o.Cohort != "GB-en" {
			t.Fatalf("cohort not carried: %q", o.Cohort)
		}
		if o.DayBucket != "2026-08-03" {
			t.Fatalf("day bucket wrong: %q", o.DayBucket)
		}
		byBucket[o.SlotBucket] += o.Count
	}
	if byBucket["3-5"] != 2 {
		t.Fatalf("slots 3 and 5 should collapse to one 3-5 bucket with count 2, got %d", byBucket["3-5"])
	}
	if byBucket["0"] != 1 {
		t.Fatalf("slot 0 should stay its own bucket, got %d", byBucket["0"])
	}

	// No raw timestamp or page_load_id survives aggregation.
	for _, o := range obs {
		if o.DayBucket == "" || len(o.DayBucket) != len("2026-08-03") {
			t.Fatalf("day bucket must be a UTC date, got %q", o.DayBucket)
		}
	}

	// Cohort defaults rather than inventing something identifying.
	def, err := st.EdgeObservations("")
	if err != nil {
		t.Fatal(err)
	}
	if len(def) > 0 && def[0].Cohort != "unknown" {
		t.Fatalf("empty cohort should default to unknown, got %q", def[0].Cohort)
	}

	// Aggregation must actually reduce the data.
	sum, err := st.AggregateSummary("GB-en")
	if err != nil {
		t.Fatal(err)
	}
	if sum.EdgeObservations >= sum.RawImpressions {
		t.Fatalf("aggregation did not reduce: %d edges from %d impressions",
			sum.EdgeObservations, sum.RawImpressions)
	}
	if sum.CatalogueEntries != 1 {
		t.Fatalf("one distinct video expected, got %d", sum.CatalogueEntries)
	}
}

func TestPeerImportWidensGraph(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Local corpus: one root with one child. A star, which is what one
	// person's watching produces.
	seed := "seedvideo01"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &seed, SlotIndex: 0, VideoID: "localchild1", Title: "Local child",
	}}); err != nil {
		t.Fatal(err)
	}

	before, err := st.Suggest(seed, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Suggestions) != 1 {
		t.Fatalf("expected the single local child, got %d", len(before.Suggestions))
	}

	// A peer contributes a second hop the local corpus cannot reach.
	edges := []bridge.EdgeObservation{
		{From: seed, To: "peerchild01", Surface: "WATCH_NEXT", SlotBucket: "1", DayBucket: "2026-08-03", Cohort: "unknown", Count: 4},
		{From: "localchild1", To: "grandchild1", Surface: "WATCH_NEXT", SlotBucket: "0", DayBucket: "2026-08-03", Cohort: "unknown", Count: 3},
	}
	cat := []bridge.CatalogueEntry{
		{VideoID: "peerchild01", Title: "Peer child"},
		{VideoID: "grandchild1", Title: "Grandchild"},
	}
	if _, _, err := st.ImportEdges("peer-a", edges, cat); err != nil {
		t.Fatal(err)
	}

	after, err := st.Suggest(seed, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Suggestions) <= len(before.Suggestions) {
		t.Fatalf("peer import should widen the graph: before=%d after=%d",
			len(before.Suggestions), len(after.Suggestions))
	}
	// The second hop is only reachable through the peer's edge.
	var sawGrandchild bool
	for _, s := range after.Suggestions {
		if s.VideoID == "grandchild1" {
			sawGrandchild = true
		}
	}
	if !sawGrandchild {
		t.Fatal("peer edge did not give the walk a second hop")
	}

	// Re-importing replaces rather than accumulating — counts inside a bundle
	// are already cumulative, so adding would double-count.
	if _, _, err := st.ImportEdges("peer-a", edges, cat); err != nil {
		t.Fatal(err)
	}
	peers, err := st.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Edges != int64(len(edges)) {
		t.Fatalf("re-import should replace: %+v", peers)
	}

	// Forgetting a peer removes its contribution entirely.
	if _, err := st.ForgetPeer("peer-a"); err != nil {
		t.Fatal(err)
	}
	back, err := st.Suggest(seed, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Suggestions) != len(before.Suggestions) {
		t.Fatalf("forgetting a peer should restore the local-only graph: %d vs %d",
			len(back.Suggestions), len(before.Suggestions))
	}

	// Imported rows must never appear in the local corpus.
	var n int64
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("peer import leaked into impressions: %d rows", n)
	}
}

func TestBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(filepath.Join(dir, "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	seed := "seedvideo01"
	if _, err := a.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &seed, SlotIndex: 2, VideoID: "sharedvid01", Title: "Shared",
	}}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "bundle.json")
	res, err := a.ExportBundle(path, "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	if res.Edges == 0 || res.Catalogue == 0 {
		t.Fatalf("empty bundle: %+v", res)
	}

	// A bundle must not carry anything that reconstructs a timeline.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"page_load_id", "observed_at"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Fatalf("bundle leaks %q — that turns counts back into a browsing timeline", banned)
		}
	}

	// A second install imports it and gains the edge.
	b, err := Open(filepath.Join(dir, "b.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	in, err := b.ImportBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if in.NodeID == "" || in.Edges == 0 {
		t.Fatalf("import produced nothing: %+v", in)
	}
	sug, err := b.Suggest(seed, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sug.Suggestions) == 0 || sug.Suggestions[0].VideoID != "sharedvid01" {
		t.Fatalf("imported edge did not reach the walk: %+v", sug.Suggestions)
	}
	// Title came from the peer catalogue, not from thin air.
	if sug.Suggestions[0].Title != "Shared" {
		t.Fatalf("peer catalogue title missing, got %q", sug.Suggestions[0].Title)
	}

	// Importing your own bundle is refused — it would double your own counts.
	if _, err := a.ImportBundle(path); err == nil {
		t.Fatal("importing own bundle should be refused")
	}

	// Garbage is rejected with a readable error rather than a panic.
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ImportBundle(bad); err == nil {
		t.Fatal("garbage bundle should be refused")
	}
}

func TestCohortNormalisation(t *testing.T) {
	cases := map[string]string{
		"en-GB":      "GB-en",
		"en_US":      "US-en",
		"pt-BR":      "BR-pt",
		"en":         "unknown", // no region: do not invent one
		"":           "unknown",
		"en-Latn-US": "US-en", // script subtag ignored, region still found
		"garbage":    "unknown",
	}
	for in, want := range cases {
		if got := NormalizeCohort(in); got != want {
			t.Errorf("NormalizeCohort(%q) = %q, want %q", in, got, want)
		}
	}

	// Anything richer than country+language must not survive — §6.3 forbids it.
	if got := NormalizeCohort("en-GB-x-interest-tech"); got != "GB-en" {
		t.Errorf("extra subtags must be dropped, got %q", got)
	}
}

func TestSearchIncludesPeerCatalogue(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := "contextvid01"
	ch := "UClocalnnnnnnnnnnnnnnnnn"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, SlotIndex: 0,
		VideoID: "localvideo1", Title: "Kestrel local footage", ChannelID: &ch,
	}}); err != nil {
		t.Fatal(err)
	}

	// A video known only from a bundle must still be findable.
	if _, _, err := st.ImportEdges("peer-a",
		[]bridge.EdgeObservation{{
			From: "localvideo1", To: "peervideo01", Surface: "WATCH_NEXT",
			SlotBucket: "1", DayBucket: "2026-08-04", Cohort: "unknown", Count: 2,
		}},
		[]bridge.CatalogueEntry{{VideoID: "peervideo01", Title: "Kestrel peer footage"}},
	); err != nil {
		t.Fatal(err)
	}

	r, err := st.SearchVideos("kestrel", 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 {
		t.Fatalf("peer catalogue not searchable: want 2, got %d", r.Total)
	}
	var local, peer bool
	for _, h := range r.Hits {
		switch h.VideoID {
		case "localvideo1":
			local = true
			if h.Seen == 0 {
				t.Fatal("locally observed video should report seen > 0")
			}
		case "peervideo01":
			peer = true
			// seen = 0 distinguishes "catalogued here" from "observed here".
			if h.Seen != 0 {
				t.Fatalf("peer-only video should report seen=0, got %d", h.Seen)
			}
		}
	}
	if !local || !peer {
		t.Fatalf("expected both local and peer hits, got %+v", r.Hits)
	}
	// Local observations outrank peer-only entries.
	if r.Hits[0].VideoID != "localvideo1" {
		t.Fatalf("locally observed video should rank first, got %q", r.Hits[0].VideoID)
	}

	// Forgetting the peer removes it from search too.
	if _, err := st.ForgetPeer("peer-a"); err != nil {
		t.Fatal(err)
	}
	if r2, err := st.SearchVideos("kestrel", 10); err != nil {
		t.Fatal(err)
	} else if r2.Total != 1 {
		t.Fatalf("forgotten peer still searchable: %d", r2.Total)
	}
}

func TestBundleDigestDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(filepath.Join(dir, "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	seed := "seedvideo01"
	if _, err := a.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &seed, SlotIndex: 1, VideoID: "sharedvid01", Title: "Shared",
	}}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "b.json")
	if _, err := a.ExportBundle(path, "GB-en"); err != nil {
		t.Fatal(err)
	}

	// The digest must cover the data only, so re-exporting an unchanged corpus
	// produces the same hash even though created_day is written fresh.
	path2 := filepath.Join(dir, "b2.json")
	if _, err := a.ExportBundle(path2, "GB-en"); err != nil {
		t.Fatal(err)
	}
	read := func(p string) Bundle {
		t.Helper()
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var b Bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if read(path).ContentSHA256 != read(path2).ContentSHA256 {
		t.Fatal("same corpus must produce the same digest")
	}

	// Altering a title in transit must be caught rather than merged.
	b := read(path)
	if len(b.Catalogue) == 0 {
		t.Fatal("bundle had no catalogue to tamper with")
	}
	b.Catalogue[0].Title = "Altered in transit"
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "tampered.json")
	if err := os.WriteFile(bad, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Open(filepath.Join(dir, "c.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ImportBundle(bad); err == nil {
		t.Fatal("tampered bundle was imported — the digest is not being checked")
	}
	// And nothing from it landed.
	peers, err := c.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Fatalf("rejected bundle still wrote rows: %+v", peers)
	}
}

// TestAnalysisTopChannelsUseName verifies the analysis view shows channel
// names, not just the channel_id hash. Regression for the long-standing bug
// where "Channels seen most" listed hashes (channel_id) because the query
// selected MAX(channel_id) for the label column and never read channel_name.
func TestAnalysisTopChannelsUseName(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ch := "@realchannel"
	name := "Real Channel Name"
	ctx := "ctxvid001"
	// Same channel, two impressions, name present on both.
	base := bridge.Impression{
		PageLoadID:     "11111111-1111-4111-8111-111111111111",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		VideoID:        "vidA",
		ChannelID:      &ch,
		ChannelName:    &name,
		Title:          "A video",
		Badges:         []string{},
	}
	other := base
	other.PageLoadID = "22222222-2222-4222-8222-222222222222"
	other.VideoID = "vidB"
	if _, err := st.PutImpressions([]bridge.Impression{base, other}); err != nil {
		t.Fatal(err)
	}

	a, err := st.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.TopChannels) == 0 {
		t.Fatal("expected at least one channel in TopChannels")
	}
	row := a.TopChannels[0]
	if row.Key != ch {
		t.Errorf("Key should be the channel_id %q, got %q", ch, row.Key)
	}
	if row.Label == nil {
		t.Fatalf("Label is nil — channel name not surfaced (got hash-only row)")
	}
	if *row.Label != name {
		t.Errorf("Label should be the channel name %q, got %q (hash shown instead)", name, *row.Label)
	}
}

// TestAnalysisTopChannelsNameFallback covers the case where channel_name is
// null (only recorded for first-paint cards): the label falls back to the
// channel_id hash rather than crashing or showing empty.
func TestAnalysisTopChannelsNameFallback(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ch := "@norecordname"
	ctx := "ctxvid002"
	imp := bridge.Impression{
		PageLoadID:     "11111111-1111-4111-8111-111111111111",
		ObservedAt:     time.Now().UnixMilli(),
		Surface:        "WATCH_NEXT",
		ContextVideoID: &ctx,
		VideoID:        "vidX",
		ChannelID:      &ch,
		ChannelName:    nil, // not a first-paint card
		Title:          "A video",
		Badges:         []string{},
	}
	if _, err := st.PutImpressions([]bridge.Impression{imp}); err != nil {
		t.Fatal(err)
	}
	a, err := st.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.TopChannels) == 0 {
		t.Fatal("expected one channel in TopChannels")
	}
	row := a.TopChannels[0]
	// Label may be nil (page falls back to Key) but Key must still be the id.
	if row.Key != ch {
		t.Errorf("Key should be channel_id %q, got %q", ch, row.Key)
	}
}

// TestOpenCreatesTheDatabaseDirectory is the WO-091 QA failure, at its root.
//
// SQLite does not create directories: it reports a missing parent as "unable to
// open database file" and names neither the file nor the reason. Only the
// default-path branch created the directory, so any explicit path — KEEL_DB, or
// a first run on Windows where %AppData%\keel does not exist yet — died here.
// The owner exited during startup, the proxy saw an unexplained EOF, and the
// panel said "not running".
func TestOpenCreatesTheDatabaseDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "does", "not", "exist", "keel.sqlite")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open with a missing parent directory: %v", err)
	}
	defer st.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was not created at %s: %v", path, err)
	}
	// It must be a working database, not just a file.
	if _, err := st.Stats(); err != nil {
		t.Fatalf("Stats on the new database: %v", err)
	}
}

// TestOpenErrorNamesTheFile: "unable to open database file" with no path is
// unactionable, and it is what a user sees in the panel.
func TestOpenErrorNamesTheFile(t *testing.T) {
	root := t.TempDir()
	// A regular file where the directory needs to be: MkdirAll cannot win.
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "keel.sqlite")

	_, err := Open(path)
	if err == nil {
		t.Fatal("want an error when the parent cannot be created")
	}
	if !strings.Contains(err.Error(), blocker) {
		t.Errorf("error does not name the path: %v", err)
	}
}
