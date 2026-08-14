// SPDX-License-Identifier: Apache-2.0
// WO-086: the persisted counters must hold nothing beyond a coarse running
// total — proven directly against the schema, not by inspection — and the
// live snapshot must agree with the corpus the serve handlers actually use.
package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestContributionActivitySchemaHasNoIdentifiers is the direct proof WO-086
// asks for: introspect the table itself rather than trust a source-text
// search, so a future column added without reading this file's package
// comment fails a test instead of silently widening what gets persisted.
func TestContributionActivitySchemaHasNoIdentifiers(t *testing.T) {
	st := openTestStore(t)

	rows, err := st.db.Query(`PRAGMA table_info(contribution_activity)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// singleton is a schema constant (always 1) that makes the one-row
	// upsert targetable. It is not a user, peer, query, prefix or request
	// identity (WO-092).
	want := map[string]bool{"singleton": true, "requests_answered": true, "bytes_served": true, "since_day": true}
	if len(got) != len(want) {
		t.Fatalf("contribution_activity columns = %v, want exactly %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("contribution_activity is missing column %q", name)
		}
	}

	var singleton, pk int
	if err := st.db.QueryRow(
		`SELECT "notnull", pk FROM pragma_table_info('contribution_activity') WHERE name = 'singleton'`,
	).Scan(&singleton, &pk); err != nil {
		t.Fatalf("singleton column: %v", err)
	}
	if pk != 1 {
		t.Errorf("singleton pk = %d, want 1 (schema constant, not an identity)", pk)
	}
	if _, err := st.db.Exec(`INSERT INTO contribution_activity(singleton, requests_answered, bytes_served, since_day) VALUES(2, 0, 0, '')`); err == nil {
		t.Fatal("singleton CHECK must reject any value other than 1")
	}
}

func TestContributionActivityRowCountNeverExceedsOne(t *testing.T) {
	st := openTestStore(t)

	for i := 0; i < 5; i++ {
		if err := st.RecordContributionServe(100); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ResetContributionActivity(); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordContributionServe(50); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM contribution_activity`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("contribution_activity must never hold more than one row, got %d", n)
	}
}

func TestRecordContributionServeAccumulates(t *testing.T) {
	st := openTestStore(t)

	if answered, bytesServed, since, err := st.ContributionActivity(); err != nil || answered != 0 || bytesServed != 0 || since != "" {
		t.Fatalf("a fresh store must report zero, not an error: answered=%d bytesServed=%d since=%q err=%v",
			answered, bytesServed, since, err)
	}

	if err := st.RecordContributionServe(1000); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordContributionServe(2500); err != nil {
		t.Fatal(err)
	}

	answered, bytesServed, since, err := st.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if answered != 2 {
		t.Errorf("requests_answered = %d, want 2", answered)
	}
	if bytesServed != 3500 {
		t.Errorf("bytes_served = %d, want 3500", bytesServed)
	}
	if since == "" {
		t.Error("since_day must be set once anything has been served")
	}
}

// TestResetContributionActivityDoesNotTouchCorpus is the reset half of the
// privacy boundary: a reset must zero the counters and nothing else,
// exactly like Wipe in reverse — a data-scoped action and a counter-scoped
// action must not be conflated into a single button.
func TestResetContributionActivityDoesNotTouchCorpus(t *testing.T) {
	st := openTestStore(t)

	ctx := "seedaaaaaaa"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, SlotIndex: 0, VideoID: "targetaaaa1", Title: "T",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordContributionServe(1234); err != nil {
		t.Fatal(err)
	}

	if err := st.ResetContributionActivity(); err != nil {
		t.Fatal(err)
	}

	answered, bytesServed, since, err := st.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if answered != 0 || bytesServed != 0 {
		t.Fatalf("reset must zero the counters, got answered=%d bytesServed=%d", answered, bytesServed)
	}
	if since == "" {
		t.Error("reset must still set since_day — it starts a new counting window, not an empty one")
	}

	var impressions int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&impressions); err != nil {
		t.Fatal(err)
	}
	if impressions != 1 {
		t.Errorf("reset must not touch the observation corpus, impressions = %d, want 1", impressions)
	}
}

// TestContributionImpactSnapshotMatchesServedCorpus proves the acceptance
// criterion that the panel's counts come from the same local-plus-cached
// corpus WO-084's serve handlers actually answer from, not a value that
// could drift from it — by comparing directly against LocalGraphKeys,
// PeerGraphKeys and heldCatalogue rather than a hand-computed expectation.
func TestContributionImpactSnapshotMatchesServedCorpus(t *testing.T) {
	st := openTestStore(t)

	ctx := "seedaaaaaaa"
	if _, err := st.PutImpressions([]bridge.Impression{
		{PageLoadID: "11111111-1111-4111-8111-111111111111", ObservedAt: time.Now().UnixMilli(),
			Surface: "WATCH_NEXT", ContextVideoID: &ctx, SlotIndex: 0, VideoID: "targetaaaa1", Title: "T1"},
		{PageLoadID: "22222222-2222-4222-8222-222222222222", ObservedAt: time.Now().UnixMilli(),
			Surface: "WATCH_NEXT", ContextVideoID: &ctx, SlotIndex: 1, VideoID: "targetaaaa2", Title: "T2"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(
		`INSERT INTO peer_blocks(claim_id, graph_key, revision, block_json, updated_at)
		 VALUES(?, ?, 0, ?, ?)`,
		"claim1", "peerkeyaaaa1", []byte(`{}`), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(
		`INSERT INTO peer_catalogue(video_id, title, channel_id, duration_s, view_count, published_at, source)
		 VALUES(?, ?, NULL, NULL, NULL, NULL, ?)`,
		"peervid0001", "Peer video", "peer1"); err != nil {
		t.Fatal(err)
	}

	snap, err := st.ContributionImpactSnapshot(DefaultPrefixBits, AllSources, AllSources)
	if err != nil {
		t.Fatal(err)
	}

	localKeys, err := st.LocalGraphKeys()
	if err != nil {
		t.Fatal(err)
	}
	peerKeys, err := st.PeerGraphKeys()
	if err != nil {
		t.Fatal(err)
	}
	if snap.GraphClaimsLocal != len(localKeys) {
		t.Errorf("GraphClaimsLocal = %d, want %d (LocalGraphKeys)", snap.GraphClaimsLocal, len(localKeys))
	}
	if snap.GraphClaimsPeerCached != len(peerKeys) {
		t.Errorf("GraphClaimsPeerCached = %d, want %d (PeerGraphKeys)", snap.GraphClaimsPeerCached, len(peerKeys))
	}
	if snap.GraphClaimsPeerCached == 0 {
		t.Error("expected the seeded peer_blocks row to be counted")
	}

	localCat, err := st.heldCatalogue(SourceSet{Local: true})
	if err != nil {
		t.Fatal(err)
	}
	peerCat, err := st.heldCatalogue(SourceSet{Peers: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.CatalogueLocal != len(localCat) {
		t.Errorf("CatalogueLocal = %d, want %d", snap.CatalogueLocal, len(localCat))
	}
	if snap.CataloguePeerCached != len(peerCat) {
		t.Errorf("CataloguePeerCached = %d, want %d", snap.CataloguePeerCached, len(peerCat))
	}

	prefixes, err := st.LocalPrefixes(DefaultPrefixBits, AllSources)
	if err != nil {
		t.Fatal(err)
	}
	if snap.BucketsAnnounced != len(prefixes) {
		t.Errorf("BucketsAnnounced = %d, want %d", snap.BucketsAnnounced, len(prefixes))
	}

	shards, err := st.LocalShards(AllSources)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ShardsAnnounced != len(shards) {
		t.Errorf("ShardsAnnounced = %d, want %d", snap.ShardsAnnounced, len(shards))
	}
}

// TestContributionImpactSnapshotNeverInventsAnUnselectedSource proves a
// source whose SourceSet flag is false contributes an honest zero because it
// was never queried, not because the corpus happens to be empty.
func TestContributionImpactSnapshotNeverInventsAnUnselectedSource(t *testing.T) {
	st := openTestStore(t)

	ctx := "seedaaaaaaa"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111", ObservedAt: time.Now().UnixMilli(),
		Surface: "WATCH_NEXT", ContextVideoID: &ctx, SlotIndex: 0, VideoID: "targetaaaa1", Title: "T1",
	}}); err != nil {
		t.Fatal(err)
	}

	snap, err := st.ContributionImpactSnapshot(DefaultPrefixBits, LocalSources, PeerSources)
	if err != nil {
		t.Fatal(err)
	}
	if snap.GraphClaimsPeerCached != 0 {
		t.Errorf("GraphClaimsPeerCached = %d, want 0 (Peers not selected for graph)", snap.GraphClaimsPeerCached)
	}
	if snap.CatalogueLocal != 0 {
		t.Errorf("CatalogueLocal = %d, want 0 (Local not selected for catalogue)", snap.CatalogueLocal)
	}
	if snap.GraphClaimsLocal == 0 {
		t.Error("expected the seeded local impression to be counted for the selected source")
	}
}

// TestConcurrentFirstIncrementsLeaveOneRow is the race WO-092 names: two
// first calls must not both INSERT. Every successful increment and its
// bytes are preserved on the single remaining row.
func TestConcurrentFirstIncrementsLeaveOneRow(t *testing.T) {
	st := openTestStore(t)
	const n = 40
	var wg sync.WaitGroup
	errc := make(chan error, n)
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(bytes int) {
			defer wg.Done()
			errc <- st.RecordContributionServe(bytes)
		}(i)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM contribution_activity`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("concurrent first increments left %d rows, want 1", rows)
	}
	answered, bytesServed, _, err := st.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if answered != n {
		t.Errorf("requests_answered = %d, want %d", answered, n)
	}
	wantBytes := int64(n * (n + 1) / 2)
	if bytesServed != wantBytes {
		t.Errorf("bytes_served = %d, want %d", bytesServed, wantBytes)
	}
}

// TestConcurrentResetAndIncrement documents the last-commit-wins result of
// two atomic upserts on the singleton row. Every increment writes 100 bytes,
// so bytes_served == 100 * requests_answered always holds; the table never
// has more than one row.
func TestConcurrentResetAndIncrement(t *testing.T) {
	st := openTestStore(t)
	if err := st.RecordContributionServe(100); err != nil {
		t.Fatal(err)
	}

	const n = 30
	var wg sync.WaitGroup
	errc := make(chan error, n*2)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errc <- st.RecordContributionServe(100)
		}()
		go func() {
			defer wg.Done()
			errc <- st.ResetContributionActivity()
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}

	var rows int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM contribution_activity`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("concurrent reset/increment left %d rows, want 1", rows)
	}
	answered, bytesServed, since, err := st.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if since == "" {
		t.Error("a reset or first increment must set since_day")
	}
	if bytesServed != answered*100 {
		t.Fatalf("counters drifted under concurrent reset/increment: answered=%d bytes=%d", answered, bytesServed)
	}
}

func TestContributionActivityPropagatesNonNoRowsErrors(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.db.Exec(`DROP TABLE contribution_activity`); err != nil {
		t.Fatal(err)
	}
	answered, bytesServed, since, err := st.ContributionActivity()
	if err == nil {
		t.Fatal("a missing table must not be reported as zero activity")
	}
	if answered != 0 || bytesServed != 0 || since != "" {
		t.Fatalf("error path leaked values: answered=%d bytes=%d since=%q", answered, bytesServed, since)
	}
}

// TestContributionActivityRepairsDuplicateRows collapses pre-WO-092 rows
// without discarding the summed counters, keeping the earliest since_day.
func TestContributionActivityRepairsDuplicateRows(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.db.Exec(`DROP TABLE contribution_activity`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TABLE contribution_activity (
		requests_answered INTEGER NOT NULL DEFAULT 0,
		bytes_served INTEGER NOT NULL DEFAULT 0,
		since_day TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(
		`INSERT INTO contribution_activity(requests_answered, bytes_served, since_day) VALUES(3, 300, '2026-08-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(
		`INSERT INTO contribution_activity(requests_answered, bytes_served, since_day) VALUES(2, 200, '2026-08-03')`); err != nil {
		t.Fatal(err)
	}
	if err := st.ensureContributionActivitySingleton(); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM contribution_activity`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("repair left %d rows, want 1", rows)
	}
	answered, bytesServed, since, err := st.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if answered != 5 || bytesServed != 500 {
		t.Fatalf("repair discarded counters: answered=%d bytes=%d", answered, bytesServed)
	}
	if since != "2026-08-01" {
		t.Errorf("since_day = %q, want the earliest day 2026-08-01", since)
	}
}
