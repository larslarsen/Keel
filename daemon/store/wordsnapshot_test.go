// SPDX-License-Identifier: Apache-2.0
package store

import (
	"fmt"
	"testing"
	"time"
)

// packOf builds a word-telemetry pack over a synthetic corpus, standing in for
// one peer's contribution to a refresh round.
func packOf(videos map[string]string) *WordTelemetry {
	w := NewWordTelemetry()
	for id, title := range videos {
		w.addTitle(id, title)
	}
	w.PrepareWire()
	return w
}

func corpus(prefix string, n int, title string) map[string]string {
	out := map[string]string{}
	for i := 0; i < n; i++ {
		out[fmt.Sprintf("%s%06d", prefix, i)] = title
	}
	return out
}

// TestRefreshRoundReplacesRatherThanAccumulates is WO-097 §7's central
// correctness claim, and the bug it prevents.
//
// Count-Min merging is element-wise saturating *sum*, so folding the same pack
// in twice doubles every counter it touches. Nothing about identical registers
// says "you already have this". A node that refreshed on a timer and merged
// into what it already held would inflate every word count without bound while
// looking perfectly healthy — so a refresh has to rebuild from zero.
func TestRefreshRoundReplacesRatherThanAccumulates(t *testing.T) {
	local := packOf(corpus("own", 40, "recommendation systems"))
	peer := packOf(corpus("peer", 60, "recommendation systems"))

	first, err := BuildWordSnapshot(local, []*WordTelemetry{peer}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	firstCount := first.Telemetry.WordGraphCount("recommendation")
	if firstCount == 0 {
		t.Fatal("the first round counted nothing; the property below would be vacuous")
	}

	// The same unchanged packs, fetched again. This is the ordinary case: a
	// peer whose corpus has not moved answers with byte-identical registers.
	second, err := BuildWordSnapshot(local, []*WordTelemetry{peer}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Telemetry.WordGraphCount("recommendation"); got != firstCount {
		t.Errorf("re-fetching unchanged packs changed the count from %d to %d — "+
			"the round is accumulating instead of replacing, and CMS addition is "+
			"not idempotent", firstCount, got)
	}

	// Ten rounds, still the same. A drift that only shows up after many
	// refreshes is exactly the failure mode this guards.
	for i := 0; i < 10; i++ {
		s, err := BuildWordSnapshot(local, []*WordTelemetry{peer}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Telemetry.WordGraphCount("recommendation"); got != firstCount {
			t.Fatalf("round %d drifted to %d from %d", i+2, got, firstCount)
		}
	}
}

// TestSnapshotPersistsAndReplacesAtomically covers the store half: the
// retained round survives a reopen with its age, and saving a new round
// replaces rather than adds to it.
func TestSnapshotPersistsAndReplacesAtomically(t *testing.T) {
	st := openStore(t, "wordsnap.sqlite")

	local := packOf(corpus("own", 30, "recommendation systems"))
	snap, err := BuildWordSnapshot(local, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWordSnapshot(snap); err != nil {
		t.Fatal(err)
	}

	loaded, ok, err := st.LoadWordSnapshot()
	if err != nil || !ok {
		t.Fatalf("LoadWordSnapshot: ok=%v err=%v", ok, err)
	}
	if got, want := loaded.Telemetry.WordGraphCount("recommendation"),
		snap.Telemetry.WordGraphCount("recommendation"); got != want {
		t.Errorf("retained count %d, want %d", got, want)
	}
	if loaded.Sources != 1 {
		t.Errorf("retained sources = %d, want 1", loaded.Sources)
	}

	// A second round with a bigger corpus replaces the first outright.
	bigger := packOf(corpus("own", 300, "recommendation systems"))
	snap2, err := BuildWordSnapshot(bigger, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWordSnapshot(snap2); err != nil {
		t.Fatal(err)
	}
	loaded2, _, err := st.LoadWordSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loaded2.Telemetry.WordGraphCount("recommendation"),
		snap2.Telemetry.WordGraphCount("recommendation"); got != want {
		t.Errorf("after replacement the retained count is %d, want %d (the new "+
			"round alone, not the sum of both)", got, want)
	}
}

// TestOverlapAdjustmentOnDisjointAndMirroredCorpora is WO-097 §8.
//
// Summing per-peer CMS counters counts a video once per peer holding it. On
// disjoint corpora that is right; on mirrors it produces a target no search
// could ever reach, leaving a word bar permanently short of a denominator that
// was never real.
func TestOverlapAdjustmentOnDisjointAndMirroredCorpora(t *testing.T) {
	const each = 200

	// Disjoint: three peers, no shared video ids. The union is the sum, so the
	// duplication factor should sit near 1 and barely move the estimate.
	disjoint, err := BuildWordSnapshot(
		packOf(corpus("aaa", each, "recommendation systems")),
		[]*WordTelemetry{
			packOf(corpus("bbb", each, "recommendation systems")),
			packOf(corpus("ccc", each, "recommendation systems")),
		}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !disjoint.HaveFactor {
		t.Fatal("disjoint round produced no duplication factor")
	}
	if disjoint.DuplicationFactor > 1.35 {
		t.Errorf("disjoint corpora produced a duplication factor of %.2f — a "+
			"correction this large would deflate an honest target",
			disjoint.DuplicationFactor)
	}
	dTarget := disjoint.Target("recommendation", time.Now())
	if !dTarget.Known {
		t.Fatal("disjoint target is unknown")
	}

	// Mirrored: three peers holding the SAME video ids. The union is one
	// corpus, so the factor should approach the number of sources and pull the
	// raw sum back down toward what a search can actually find.
	mirror := corpus("mmm", each, "recommendation systems")
	mirrored, err := BuildWordSnapshot(
		packOf(mirror),
		[]*WordTelemetry{packOf(mirror), packOf(mirror)},
		time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !mirrored.HaveFactor {
		t.Fatal("mirrored round produced no duplication factor")
	}
	if mirrored.DuplicationFactor < 2 {
		t.Errorf("three identical mirrors produced a duplication factor of %.2f, "+
			"want near 3 — the raw sum is triple-counting one corpus",
			mirrored.DuplicationFactor)
	}

	mTarget := mirrored.Target("recommendation", time.Now())
	if !mTarget.Known {
		t.Fatal("mirrored target is unknown")
	}
	if mTarget.Adjusted >= mTarget.Raw {
		t.Errorf("mirrored adjusted target %d is not below the raw estimate %d — "+
			"the overlap correction did nothing", mTarget.Adjusted, mTarget.Raw)
	}
	// The whole point: the adjusted target must be reachable. A search of a
	// three-way mirror can find at most `each` distinct videos.
	if mTarget.Adjusted > uint64(float64(each)*1.5) {
		t.Errorf("mirrored adjusted target is %d against a reachable corpus of %d — "+
			"a mirrored swarm must not create an unreachable search target",
			mTarget.Adjusted, each)
	}
	// The raw estimate is retained for diagnostics, not discarded.
	if mTarget.Raw == 0 {
		t.Error("the raw estimate was not retained alongside the adjusted one")
	}
}

// TestTargetsAreAvailableOfflineUnknownAndAged is §7.4 and §8's presentation
// contract, which WO-095 renders directly.
func TestTargetsAreAvailableOfflineUnknownAndAged(t *testing.T) {
	st := openStore(t, "wordtarget.sqlite")

	// No retained round yet: a target is unknown, and that is a normal answer
	// rather than an error. A node that has never refreshed still searches.
	targets, err := st.WordTargets([]string{"recommendation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Known {
		t.Errorf("with no snapshot, targets = %+v, want one unknown target", targets)
	}

	// After a round, the target is readable with no network involved and
	// carries the snapshot's age.
	past := time.Now().Add(-90 * time.Minute)
	snap, err := BuildWordSnapshot(packOf(corpus("own", 50, "recommendation systems")), nil, past)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWordSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	targets, err = st.WordTargets([]string{"recommendation", "neverseenword"})
	if err != nil {
		t.Fatal(err)
	}
	if !targets[0].Known {
		t.Error("a retained snapshot did not produce a known target")
	}
	if targets[0].Adjusted == 0 {
		t.Error("a known target of zero for a word the corpus contains")
	}
	if targets[0].SnapshotAgeMS < int64(80*time.Minute/time.Millisecond) {
		t.Errorf("snapshot age = %dms, want roughly 90 minutes — a stale target is "+
			"usable, but hiding its age is not", targets[0].SnapshotAgeMS)
	}
	// A word the corpus has never seen still gets a known-but-small target
	// rather than an unknown one: the snapshot exists, the count is simply low.
	if !targets[1].Known {
		t.Error("an unseen word produced an unknown target even though a snapshot exists")
	}

	// Freezing is the caller's job, and it works because reading is a pure
	// function of a value already in hand: taking targets, then replacing the
	// snapshot, must not move the numbers already taken.
	frozen := targets[0]
	newer, err := BuildWordSnapshot(packOf(corpus("own", 5000, "recommendation systems")), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWordSnapshot(newer); err != nil {
		t.Fatal(err)
	}
	if frozen.Adjusted != targets[0].Adjusted {
		t.Error("a target already taken changed when a later refresh landed")
	}
	refreshed, err := st.WordTargets([]string{"recommendation"})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed[0].Adjusted == frozen.Adjusted {
		t.Error("the refresh did not change the target for the NEXT search, so this " +
			"test proves nothing about freezing")
	}
}

// TestSnapshotHoldsNoWordStrings is the privacy property the whole design
// rests on: there is no vocabulary anywhere, only sketches.
func TestSnapshotHoldsNoWordStrings(t *testing.T) {
	st := openStore(t, "wordprivacy.sqlite")
	snap, err := BuildWordSnapshot(packOf(map[string]string{
		"vid00000001": "distinctivephrase about kittens",
	}), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWordSnapshot(snap); err != nil {
		t.Fatal(err)
	}

	var wordReg, graphReg, freq []byte
	if err := st.db.QueryRow(
		`SELECT word_registers, graph_registers, freq FROM word_snapshot WHERE id = 1`).
		Scan(&wordReg, &graphReg, &freq); err != nil {
		t.Fatal(err)
	}
	for name, blob := range map[string][]byte{
		"word_registers": wordReg, "graph_registers": graphReg, "freq": freq,
	} {
		if containsSubstring(blob, "distinctivephrase") || containsSubstring(blob, "kittens") {
			t.Errorf("%s contains a plaintext word — the retained snapshot must be "+
				"sketches only, never a dictionary", name)
		}
	}

	// The sketch can still answer a supplied word, which is the whole point:
	// no dictionary, but a usable count for a word the caller already has.
	if snap.Telemetry.WordGraphCount("distinctivephrase") == 0 {
		t.Error("the CMS could not answer for a word it was given")
	}
}

func containsSubstring(haystack []byte, needle string) bool {
	n := []byte(needle)
	if len(n) == 0 || len(haystack) < len(n) {
		return false
	}
	for i := 0; i+len(n) <= len(haystack); i++ {
		match := true
		for j := range n {
			if haystack[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
