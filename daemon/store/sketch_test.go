// SPDX-License-Identifier: Apache-2.0
package store

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"testing"
)

// relErr is the fraction by which an estimate misses the truth.
func relErr(got uint64, want int) float64 {
	return math.Abs(float64(got)-float64(want)) / float64(want)
}

// TestSketchAccuracy checks the estimator across the range the project will
// actually use — a few hundred edges on a new install up to a large corpus.
func TestSketchAccuracy(t *testing.T) {
	for _, n := range []int{100, 1000, 10000, 200000} {
		sk := NewSketch(KindEdge)
		for i := 0; i < n; i++ {
			sk.Add(fmt.Sprintf("vid%08d\x00vid%08d", i, i*7+1))
		}
		if e := relErr(sk.Count(), n); e > 0.03 {
			t.Errorf("n=%d: estimated %d, off by %.1f%% (want <=3%%)", n, sk.Count(), e*100)
		}
	}
}

// TestSketchMergeIsUnion is the property the whole measurement rests on: the
// merged sketch must estimate the union, not the sum.
func TestSketchMergeIsUnion(t *testing.T) {
	a, b := NewSketch(KindEdge), NewSketch(KindEdge)
	for i := 0; i < 10000; i++ {
		a.Add(fmt.Sprintf("edge%06d", i))
	}
	// b shares half of a's keys and brings 5000 of its own.
	for i := 5000; i < 15000; i++ {
		b.Add(fmt.Sprintf("edge%06d", i))
	}

	rep, err := Overlap(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if e := relErr(rep.Union, 15000); e > 0.03 {
		t.Errorf("union estimated %d, want ~15000 (off %.1f%%)", rep.Union, e*100)
	}
	if e := relErr(rep.Intersection, 5000); e > 0.15 {
		t.Errorf("intersection estimated %d, want ~5000 (off %.1f%%)", rep.Intersection, e*100)
	}
	// The figure that decides the scaling question.
	if e := relErr(rep.NewPerNode, 5000); e > 0.15 {
		t.Errorf("new-from-second-node estimated %d, want ~5000 (off %.1f%%)", rep.NewPerNode, e*100)
	}
}

// TestSketchDisjointAndIdentical pins the two ends of the scale, which are the
// two outcomes §5d cares about: no cross-user dedup, or total dedup.
func TestSketchDisjointAndIdentical(t *testing.T) {
	a, disjoint, same := NewSketch(KindEdge), NewSketch(KindEdge), NewSketch(KindEdge)
	for i := 0; i < 10000; i++ {
		a.Add(fmt.Sprintf("a%06d", i))
		same.Add(fmt.Sprintf("a%06d", i))
		disjoint.Add(fmt.Sprintf("z%06d", i))
	}

	// Disjoint: the network gains a whole corpus per user. Aggregate grows
	// linearly and the free-channel plan does not survive.
	rep, err := Overlap(a, disjoint)
	if err != nil {
		t.Fatal(err)
	}
	if e := relErr(rep.Union, 20000); e > 0.03 {
		t.Errorf("disjoint union %d, want ~20000", rep.Union)
	}
	if float64(rep.Intersection) > 0.05*float64(rep.B) {
		t.Errorf("disjoint sets reported %d overlap, want ~0", rep.Intersection)
	}

	// Identical: a second user adds nothing.
	rep, err = Overlap(a, same)
	if err != nil {
		t.Fatal(err)
	}
	if e := relErr(rep.Union, 10000); e > 0.03 {
		t.Errorf("identical union %d, want ~10000", rep.Union)
	}
	if float64(rep.NewPerNode) > 0.05*float64(rep.A) {
		t.Errorf("identical sets reported %d new keys, want ~0", rep.NewPerNode)
	}
}

// TestSketchRoundTrip covers the wire format — a sketch is useless if it cannot
// survive being sent to another node.
func TestSketchRoundTrip(t *testing.T) {
	sk := NewSketch(KindTuple)
	for i := 0; i < 5000; i++ {
		sk.Add(fmt.Sprintf("k%06d", i))
	}
	raw, err := json.Marshal(sk)
	if err != nil {
		t.Fatal(err)
	}

	var back Sketch
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Kind != KindTuple {
		t.Errorf("kind = %q, want %q", back.Kind, KindTuple)
	}
	if back.Count() != sk.Count() {
		t.Errorf("count changed across the wire: %d -> %d", sk.Count(), back.Count())
	}
	if len(raw) > 32*1024 {
		t.Errorf("sketch is %d bytes; too large to exchange casually", len(raw))
	}
}

// TestSketchRejectsMismatch stops an edge-keyed sketch being compared against a
// tuple-keyed one, which would silently produce a meaningless overlap.
func TestSketchRejectsMismatch(t *testing.T) {
	if _, err := Overlap(NewSketch(KindEdge), NewSketch(KindTuple)); err == nil {
		t.Fatal("merging different key kinds was allowed")
	}
}

// TestSketchRevealsNoMembership is the privacy claim, asserted rather than
// asserted-in-a-comment: a populated sketch must not contain any key it was
// given, so possessing one cannot leak what a node watched.
func TestSketchRevealsNoMembership(t *testing.T) {
	sk := NewSketch(KindEdge)
	secret := "dQw4w9WgXcQ\x00oHg5SJYRHA0"
	sk.Add(secret)
	for i := 0; i < 1000; i++ {
		sk.Add(fmt.Sprintf("filler%06d", i))
	}
	raw, err := json.Marshal(sk)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"dQw4w9WgXcQ", "oHg5SJYRHA0"} {
		if containsSub(string(raw), part) {
			t.Fatalf("sketch payload contains the video id %q", part)
		}
	}
	// Registers are bounded small integers, never identifiers.
	for i, r := range sk.Registers {
		if r > 64 {
			t.Fatalf("register %d holds %d, which is not a leading-zero rank", i, r)
		}
	}
}

func containsSub(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestEdgeSketchFromStore checks the store seam against real aggregated rows.
func TestEdgeSketchFromStore(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedOne(t, st, "sketchvid01", "One")
	seedOne(t, st, "sketchvid02", "Two")

	for _, kind := range []SketchKind{KindEdge, KindTuple} {
		sk, err := st.EdgeSketch(kind, "GB-en")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if sk.Count() == 0 {
			t.Errorf("%s: sketch of a seeded store is empty", kind)
		}
	}
}
