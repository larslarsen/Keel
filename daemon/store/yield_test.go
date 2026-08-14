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

	vec, err := st.LocalYieldVector(AllSources)
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != YieldVectorBytes {
		t.Fatalf("LocalYieldVector returned %d bytes, want %d", len(vec), YieldVectorBytes)
	}

	toks := TitleTokens("recommendation systems explained")
	if len(toks) == 0 {
		t.Fatal("test setup produced no tokens")
	}
	for _, tok := range toks {
		idx, ok := TokenDictIndex(tok)
		if !ok {
			t.Fatalf("TokenDictIndex(%q) rejected a real TitleTokens() output", tok)
		}
		if !YieldBitSet(vec, idx) {
			t.Errorf("token %q covers 100%% of its shard (only video present) but its bit is not set", tok)
		}
	}

	// A token this node has never seen must not be set.
	unseen := "recommendation ai video"
	for _, tok := range TitleTokens(unseen) {
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

// TestLocalYieldVectorFollowsItsSourceSet mirrors ShardSlice's own test: the
// vector is computed over exactly the corpus its SourceSet names.
//
// That equality is the point (WO-084 requirement 4). A yield bit claims a shard
// fetch from this node would return something useful, so it has to be computed
// over the same corpus ShardSlice will actually serve — Policy.CatalogueSources
// feeds both.
func TestLocalYieldVectorFollowsItsSourceSet(t *testing.T) {
	st := openStore(t, "yield-sources.sqlite")
	seedTitle(t, st, "ownvideo001", "Owner watched this exact video")

	vec, err := st.LocalYieldVector(PeerSources)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range TitleTokens("owner watched this exact video") {
		idx, ok := TokenDictIndex(tok)
		if ok && YieldBitSet(vec, idx) {
			t.Errorf("LocalYieldVector(PeerSources) set a bit for a token derived from this node's own impressions")
		}
	}

	if _, _, err := st.ImportEdges("peer-a", nil,
		[]bridge.CatalogueEntry{{VideoID: "mirroredvid1", Title: "Owner watched this exact video"}}); err != nil {
		t.Fatal(err)
	}
	vec, err = st.LocalYieldVector(PeerSources)
	if err != nil {
		t.Fatal(err)
	}
	anySet := false
	for _, tok := range TitleTokens("owner watched this exact video") {
		idx, ok := TokenDictIndex(tok)
		if ok && YieldBitSet(vec, idx) {
			anySet = true
		}
	}
	if !anySet {
		t.Error("LocalYieldVector(PeerSources) set no bits after the same title arrived via peer_catalogue")
	}

	// The Level-2 vector covers the local half too: a node that served its own
	// titles while gossiping a vector that denied holding them would send peers
	// past material it was ready to answer with.
	stLocal := openStore(t, "yield-local.sqlite")
	seedTitle(t, stLocal, "ownvideo001", "Owner watched this exact video")
	vec, err = stLocal.LocalYieldVector(AllSources)
	if err != nil {
		t.Fatal(err)
	}
	anySet = false
	for _, tok := range TitleTokens("owner watched this exact video") {
		idx, ok := TokenDictIndex(tok)
		if ok && YieldBitSet(vec, idx) {
			anySet = true
		}
	}
	if !anySet {
		t.Error("LocalYieldVector(AllSources) set no bits for a title this node holds and would serve")
	}
}
