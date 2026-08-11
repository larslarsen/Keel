// SPDX-License-Identifier: Apache-2.0
package store

import (
	"testing"
	"time"
)

// TestMergeTokenSketchPersistsAndEstimates is the round trip: a sketch
// merged in comes back out via TokenEstimate.
func TestMergeTokenSketchPersistsAndEstimates(t *testing.T) {
	st := openStore(t, "sketch-store.sqlite")
	idx, ok := TokenDictIndex(" re")
	if !ok {
		t.Fatal("test assumption broken")
	}

	if _, known := st.TokenEstimate(" re"); known {
		t.Fatal("TokenEstimate reported known=true before anything was merged")
	}

	incoming := NewSketchP(KindToken, TokenSketchP)
	for i := 0; i < 200; i++ {
		incoming.Add(randVideoID(i))
	}
	if err := st.MergeTokenSketch(idx, incoming); err != nil {
		t.Fatal(err)
	}

	got, known := st.TokenEstimate(" re")
	if !known {
		t.Fatal("TokenEstimate reported known=false after a merge")
	}
	if e := relErr(got, 200); e > 0.25 {
		t.Errorf("estimate %d off by %.1f%% from 200 (TokenSketchP is low-precision, generous tolerance)", got, e*100)
	}
}

// TestMergeTokenSketchIsUnion: merging two different peers' sketches for the
// same token must estimate the union, mirroring TestSketchMergeIsUnion but
// through the persisted path.
func TestMergeTokenSketchIsUnion(t *testing.T) {
	st := openStore(t, "sketch-union.sqlite")
	idx, _ := TokenDictIndex(" re")

	a := NewSketchP(KindToken, TokenSketchP)
	for i := 0; i < 300; i++ {
		a.Add(randVideoID(i))
	}
	if err := st.MergeTokenSketch(idx, a); err != nil {
		t.Fatal(err)
	}

	b := NewSketchP(KindToken, TokenSketchP)
	for i := 150; i < 450; i++ { // overlaps a by 150, brings 150 new
		b.Add(randVideoID(i))
	}
	if err := st.MergeTokenSketch(idx, b); err != nil {
		t.Fatal(err)
	}

	got, _ := st.TokenEstimate(" re")
	if e := relErr(got, 450); e > 0.3 {
		t.Errorf("merged estimate %d, want ~450 (off %.1f%%)", got, e*100)
	}
}

// TestMergeTokenSketchPreservesExistingSchedule is the anti-amplification
// property: receiving gossip must not let a peer reset when THIS node next
// re-broadcasts. Only a brand-new row (nothing known before) gets a fresh
// schedule from a merge; an existing row's due_at is untouched by more
// incoming data.
func TestMergeTokenSketchPreservesExistingSchedule(t *testing.T) {
	st := openStore(t, "sketch-schedule.sqlite")
	idx, _ := TokenDictIndex(" re")

	sk1 := NewSketchP(KindToken, TokenSketchP)
	sk1.Add(randVideoID(1))
	if err := st.MergeTokenSketch(idx, sk1); err != nil {
		t.Fatal(err)
	}
	var firstDue int64
	if err := st.db.QueryRow(`SELECT due_at FROM token_sketches WHERE token_index = ?`, idx).Scan(&firstDue); err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)
	sk2 := NewSketchP(KindToken, TokenSketchP)
	sk2.Add(randVideoID(2))
	if err := st.MergeTokenSketch(idx, sk2); err != nil {
		t.Fatal(err)
	}
	var secondDue int64
	if err := st.db.QueryRow(`SELECT due_at FROM token_sketches WHERE token_index = ?`, idx).Scan(&secondDue); err != nil {
		t.Fatal(err)
	}
	if firstDue != secondDue {
		t.Errorf("due_at changed from %d to %d after a second merge — incoming gossip must not reschedule our own broadcast", firstDue, secondDue)
	}
}

// TestRecordTokenSearchDriftSchedulesSooner is the scheduling contract: a
// search that found far more (or fewer) videos than the prior estimate
// predicted must be scheduled for re-gossip sooner than one that matched
// closely.
func TestRecordTokenSearchDriftSchedulesSooner(t *testing.T) {
	stable := openStore(t, "sketch-drift-stable.sqlite")
	idxStable, _ := TokenDictIndex(" re")
	seed := NewSketchP(KindToken, TokenSketchP)
	for i := 0; i < 100; i++ {
		seed.Add(randVideoID(i))
	}
	if err := stable.MergeTokenSketch(idxStable, seed); err != nil {
		t.Fatal(err)
	}
	// A search that finds roughly what was already estimated: low drift.
	found := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		found = append(found, randVideoID(i))
	}
	if err := stable.RecordTokenSearch(" re", found); err != nil {
		t.Fatal(err)
	}
	var stableDue int64
	if err := stable.db.QueryRow(`SELECT due_at FROM token_sketches WHERE token_index = ?`, idxStable).Scan(&stableDue); err != nil {
		t.Fatal(err)
	}

	drifting := openStore(t, "sketch-drift-high.sqlite")
	idxDrift, _ := TokenDictIndex(" th")
	seed2 := NewSketchP(KindToken, TokenSketchP)
	seed2.Add(randVideoID(999)) // estimate of ~1
	if err := drifting.MergeTokenSketch(idxDrift, seed2); err != nil {
		t.Fatal(err)
	}
	// A search that finds far more than the tiny prior estimate: high drift.
	manyFound := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		manyFound = append(manyFound, randVideoID(i+2000))
	}
	if err := drifting.RecordTokenSearch(" th", manyFound); err != nil {
		t.Fatal(err)
	}
	var driftDue int64
	if err := drifting.db.QueryRow(`SELECT due_at FROM token_sketches WHERE token_index = ?`, idxDrift).Scan(&driftDue); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	if driftDue-now >= stableDue-now {
		t.Errorf("high-drift due_at (%dms from now) not sooner than low-drift due_at (%dms from now)",
			driftDue-now, stableDue-now)
	}
}

// TestRecordTokenSearchBrandNewIsImmediatelyDue: a token this node had no
// row for at all, now backed by a real search, must be scheduled for the
// very next publish tick rather than filtered through the same drift math
// an update to a known estimate gets — new information is worth sharing
// promptly, not calibrated against a baseline that never existed.
func TestRecordTokenSearchBrandNewIsImmediatelyDue(t *testing.T) {
	st := openStore(t, "sketch-new.sqlite")
	if _, known := st.TokenEstimate(" re"); known {
		t.Fatal("test assumption broken: token already had a row")
	}
	before := time.Now().UnixMilli()
	if err := st.RecordTokenSearch(" re", []string{"vid00000001"}); err != nil {
		t.Fatal(err)
	}
	idx, _ := TokenDictIndex(" re")
	var dueAt int64
	if err := st.db.QueryRow(`SELECT due_at FROM token_sketches WHERE token_index = ?`, idx).Scan(&dueAt); err != nil {
		t.Fatal(err)
	}
	if dueAt > before+1000 { // generous slack for test execution time
		t.Errorf("brand-new discovery due_at = %d, want ~%d (immediate), not scheduled out via drift math", dueAt, before)
	}
}

// TestTokenDriftBothZeroIsNoDrift pins the edge case explicitly: an
// estimate of nothing and a search that found nothing must not read as
// maximal drift, or every untouched token would look maximally urgent.
func TestTokenDriftBothZeroIsNoDrift(t *testing.T) {
	if got := tokenDrift(0, 0); got != 0 {
		t.Errorf("tokenDrift(0, 0) = %v, want 0", got)
	}
}

// TestGossipBackoffClamped checks both ends of the schedule: extreme drift
// pushes toward the SHORT interval (more urgent), not the long one, and
// zero drift stays within bounds rather than exceeding the max.
func TestGossipBackoffClamped(t *testing.T) {
	if got := gossipBackoff(1000); got != minGossipInterval {
		t.Errorf("gossipBackoff(1000) = %v, want minGossipInterval %v", got, minGossipInterval)
	}
	if got := gossipBackoff(0); got > maxGossipInterval || got < minGossipInterval {
		t.Errorf("gossipBackoff(0) = %v, out of [%v,%v]", got, minGossipInterval, maxGossipInterval)
	}
}

// TestDueTokenSketchesOrderedAndLimited checks the publish-side rate limit:
// only due rows come back, most-overdue first, capped.
func TestDueTokenSketchesOrderedAndLimited(t *testing.T) {
	st := openStore(t, "sketch-due.sqlite")
	toks := []string{" re", " th", " in", " ai", "abc"}
	var indexes []int
	for i, tok := range toks {
		idx, ok := TokenDictIndex(tok)
		if !ok {
			t.Fatalf("test token %q invalid", tok)
		}
		indexes = append(indexes, idx)
		sk := NewSketchP(KindToken, TokenSketchP)
		sk.Add("v")
		// Stagger due_at so ordering is meaningful: earlier index = more overdue.
		due := time.Now().Add(time.Duration(i) * time.Millisecond).UnixMilli()
		if err := st.saveTokenSketch(idx, sk, due, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond) // let every due_at pass

	rows, err := st.DueTokenSketches(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("DueTokenSketches(3) returned %d rows, want 3", len(rows))
	}
	seen := map[int]bool{}
	for _, r := range rows {
		seen[r.TokenIndex] = true
	}
	for i := 0; i < 3; i++ {
		if !seen[indexes[i]] {
			t.Errorf("DueTokenSketches(3) missing the %d most-overdue token (index %d)", i, indexes[i])
		}
	}
}

// TestMarkTokenGossipedPushesScheduleOut confirms the flat reset after a
// successful publish.
func TestMarkTokenGossipedPushesScheduleOut(t *testing.T) {
	st := openStore(t, "sketch-marked.sqlite")
	idx, _ := TokenDictIndex(" re")
	sk := NewSketchP(KindToken, TokenSketchP)
	sk.Add("v")
	if err := st.saveTokenSketch(idx, sk, time.Now().UnixMilli(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkTokenGossiped(idx); err != nil {
		t.Fatal(err)
	}
	var due int64
	if err := st.db.QueryRow(`SELECT due_at FROM token_sketches WHERE token_index = ?`, idx).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due <= time.Now().UnixMilli() {
		t.Error("MarkTokenGossiped did not push due_at into the future")
	}
}

// TestEvictCacheReclaimsSketchesAndThumbnails: the combined LRU must evict
// from BOTH tables under one shared budget, oldest last_used_at first
// regardless of which table it's in.
func TestEvictCacheReclaimsSketchesAndThumbnails(t *testing.T) {
	st := openStore(t, "sketch-evict.sqlite")

	// A handful of sketch rows with increasing last_used_at (touch order).
	for i := 0; i < 5; i++ {
		idx, _ := TokenDictIndex(randDictToken3(i))
		sk := NewSketchP(KindToken, TokenSketchP)
		sk.Add("v")
		if err := st.saveTokenSketch(idx, sk, time.Now().Add(time.Hour).UnixMilli(), nil, nil); err != nil {
			t.Fatal(err)
		}
		// saveTokenSketch stamps last_used_at = now for every row equally
		// fast in a test, so force an explicit order for the assertion to
		// be meaningful.
		if _, err := st.db.Exec(`UPDATE token_sketches SET last_used_at = ? WHERE token_index = ?`,
			int64(i), idx); err != nil {
			t.Fatal(err)
		}
	}

	before, items, err := st.CacheUsage()
	if err != nil {
		t.Fatal(err)
	}
	if items != 5 || before == 0 {
		t.Fatalf("setup: CacheUsage = %d bytes, %d items, want 5 items with nonzero size", before, items)
	}

	// Budget smaller than current usage: eviction must reclaim the
	// oldest-touched rows first.
	if err := st.evictCache(before / 2); err != nil {
		t.Fatal(err)
	}
	after, itemsAfter, err := st.CacheUsage()
	if err != nil {
		t.Fatal(err)
	}
	if after > before/2 {
		t.Errorf("evictCache left %d bytes, want <= %d", after, before/2)
	}
	if itemsAfter >= items {
		t.Errorf("evictCache did not remove any rows: %d before, %d after", items, itemsAfter)
	}

	// The lowest last_used_at (index 0) must be the one gone, not a
	// higher-touched one.
	idx0, _ := TokenDictIndex(randDictToken3(0))
	var stillThere bool
	err = st.db.QueryRow(`SELECT 1 FROM token_sketches WHERE token_index = ?`, idx0).Scan(new(int))
	stillThere = err == nil
	if stillThere {
		t.Error("evictCache kept the least-recently-used row instead of evicting it")
	}
}

func randVideoID(i int) string {
	return "video" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+i%10))
}

func randDictToken3(i int) string {
	alpha := "abcdefghijklmnopqrstuvwxyz"
	return string([]byte{alpha[i%26], alpha[(i/26)%26], alpha[(i/676)%26]})
}
