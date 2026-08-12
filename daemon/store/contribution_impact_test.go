// SPDX-License-Identifier: Apache-2.0
// WO-086: the persisted counters must hold nothing beyond a coarse running
// total — proven directly against the schema, not by inspection — and the
// live snapshot must agree with the corpus the serve handlers actually use.
package store

import (
	"path/filepath"
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

	want := map[string]bool{"requests_answered": true, "bytes_served": true, "since_day": true}
	if len(got) != len(want) {
		t.Fatalf("contribution_activity columns = %v, want exactly %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("contribution_activity is missing column %q", name)
		}
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
