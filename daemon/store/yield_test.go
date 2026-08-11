// SPDX-License-Identifier: Apache-2.0
package store

import (
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
)

// TestLocalYieldVectorAboveAndBelowThreshold: a token covered by every video
// in its shard gets its bit set; a token covered by only a sliver of its
// shard does not.
func TestLocalYieldVectorAboveAndBelowThreshold(t *testing.T) {
	st := openStore(t, "yield.sqlite")

	// Two titles sharing enough letters to guarantee shared tokens is fiddly
	// to engineer by hand, so instead: seed one title, read back its own
	// tokens (which by construction cover 100% of their own shards, since
	// this is the only video), and use those as the "always above
	// threshold" case.
	seedTitle(t, st, "vid00000001", "Recommendation systems explained")

	vec, err := st.LocalYieldVector(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != YieldVectorBytes {
		t.Fatalf("LocalYieldVector returned %d bytes, want %d", len(vec), YieldVectorBytes)
	}

	toks := tokenize("recommendation systems explained", ShardK)
	if len(toks) == 0 {
		t.Fatal("test setup produced no tokens")
	}
	for _, tok := range toks {
		idx, ok := TokenDictIndex(tok)
		if !ok {
			t.Fatalf("TokenDictIndex(%q) rejected a real tokenize() output", tok)
		}
		if !YieldBitSet(vec, idx) {
			t.Errorf("token %q covers 100%% of its shard (only video present) but its bit is not set", tok)
		}
	}

	// A token this node has never seen must not be set.
	unseen := "recommendation ai video"
	for _, tok := range tokenize(unseen, ShardK) {
		found := false
		for _, t2 := range toks {
			if t2 == tok {
				found = true
			}
		}
		if found {
			continue // shares a token with the seeded title, not a useful negative case
		}
		idx, ok := TokenDictIndex(tok)
		if !ok {
			continue
		}
		if YieldBitSet(vec, idx) {
			t.Errorf("token %q, never held, has its bit set", tok)
		}
	}
}

// TestLocalYieldVectorRespectsMirrorOnly mirrors ShardSlice's own test: below
// Level 3, only peer_catalogue titles may contribute, never this node's own
// impressions.
func TestLocalYieldVectorRespectsMirrorOnly(t *testing.T) {
	st := openStore(t, "yield-mirror.sqlite")
	seedTitle(t, st, "ownvideo001", "Owner watched this exact video")

	vec, err := st.LocalYieldVector(true) // mirrorOnly=true, Level 2
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokenize("owner watched this exact video", ShardK) {
		idx, ok := TokenDictIndex(tok)
		if ok && YieldBitSet(vec, idx) {
			t.Errorf("LocalYieldVector(mirrorOnly=true) set a bit for a token derived from this node's own impressions")
		}
	}

	if _, _, err := st.ImportEdges("peer-a", nil,
		[]bridge.CatalogueEntry{{VideoID: "mirroredvid1", Title: "Owner watched this exact video"}}); err != nil {
		t.Fatal(err)
	}
	vec, err = st.LocalYieldVector(true)
	if err != nil {
		t.Fatal(err)
	}
	anySet := false
	for _, tok := range tokenize("owner watched this exact video", ShardK) {
		idx, ok := TokenDictIndex(tok)
		if ok && YieldBitSet(vec, idx) {
			anySet = true
		}
	}
	if !anySet {
		t.Error("LocalYieldVector(mirrorOnly=true) set no bits after the same title arrived via peer_catalogue")
	}
}
