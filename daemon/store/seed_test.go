// SPDX-License-Identifier: Apache-2.0
package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestSeedPackRemovesTheQuery is the privacy property the pack exists for: a
// video covered by the pack must be answerable locally, so it never becomes a
// network request for anyone to observe.
func TestSeedPackRemovesTheQuery(t *testing.T) {
	origin := openStore(t, "origin.sqlite")
	// A seedable video needs both: inbound recommendations to make it popular,
	// and outbound edges to have a neighbourhood worth serving. Videos that are
	// only ever a destination have empty blocks and are skipped.
	seedEdge(t, origin, "othervid001", "popularvid1", 0)
	seedEdge(t, origin, "othervid002", "popularvid1", 1)
	seedEdge(t, origin, "popularvid1", "targetaaaa1", 0)
	seedEdge(t, origin, "popularvid1", "targetaaaa2", 1)
	seedEdge(t, origin, "othervid001", "popularvid2", 2)
	seedEdge(t, origin, "popularvid2", "targetaaaa3", 0)

	pack, err := origin.BuildSeedPack(100, "GB-en", AllSources)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Blocks) < 2 {
		t.Fatalf("pack has %d blocks, want at least 2", len(pack.Blocks))
	}

	fresh := openStore(t, "fresh.sqlite")
	// Before: a fresh node holds nothing, so a lookup would have to hit the net.
	if have, err := fresh.HaveBlock("popularvid1"); err != nil || have {
		t.Fatalf("fresh node already claims to hold the block (have=%v err=%v)", have, err)
	}

	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	loaded, edges, err := fresh.ImportSeedPack(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded < 2 || edges < 3 {
		t.Errorf("loaded %d blocks / %d edges, want >=2 / >=3", loaded, edges)
	}

	// After: the lookup is answered locally. No request, nothing to observe.
	if have, err := fresh.HaveBlock("popularvid1"); err != nil || !have {
		t.Errorf("seeded video still needs a network fetch (have=%v err=%v)", have, err)
	}
	// And the walk can actually use it.
	sug, err := fresh.Suggest("popularvid1", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sug.Suggestions) == 0 {
		t.Error("seeded node gained edges but its walk returns nothing")
	}
}

// TestSeedPackRejectsForgery — a pack is a delivery mechanism, not a trust
// shortcut. Editing a block inside must not survive.
func TestSeedPackRejectsForgery(t *testing.T) {
	origin := openStore(t, "origin.sqlite")
	seedEdge(t, origin, "othervid001", "popularvid1", 0)
	seedEdge(t, origin, "popularvid1", "targetaaaa1", 0)
	pack, err := origin.BuildSeedPack(100, "GB-en", AllSources)
	if err != nil {
		t.Fatal(err)
	}

	pack.Blocks[0].Edges[0].Count = 9999
	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	fresh := openStore(t, "fresh.sqlite")
	if _, _, err := fresh.ImportSeedPack(raw); err == nil {
		t.Fatal("a seed pack with an altered edge count was accepted")
	}
}

// TestMirrorSeedPackExcludesOwnObservations pins the Level 2 boundary for packs,
// the same way it is pinned for served blocks.
func TestMirrorSeedPackExcludesOwnObservations(t *testing.T) {
	st := openStore(t, "mirror.sqlite")
	seedEdge(t, st, "privateseed", "privatevid1", 0)

	if _, err := st.BuildSeedPack(100, "GB-en", PeerSources); err == nil {
		t.Fatal("a mirror pack was built out of this node's own observations")
	}
}

// TestPopularBlockKeysExcludesLeaves is the lesson from the live corpus: a
// video that is only ever a destination has no neighbourhood, so seeding it
// would produce an empty block. Ranking must be filtered to what is answerable.
func TestPopularBlockKeysExcludesLeaves(t *testing.T) {
	st := openStore(t, "s.sqlite")
	// hubvideo001 is recommended twice AND has its own neighbourhood.
	seedEdge(t, st, "seedaaaaaaa", "hubvideo001", 0)
	seedEdge(t, st, "seedbbbbbbb", "hubvideo001", 1)
	seedEdge(t, st, "hubvideo001", "onwardvid01", 0)
	// leafvideo01 is recommended three times but never watched — no edges out.
	seedEdge(t, st, "seedaaaaaaa", "leafvideo01", 2)
	seedEdge(t, st, "seedbbbbbbb", "leafvideo01", 3)
	seedEdge(t, st, "hubvideo001", "leafvideo01", 1)

	keys, err := st.PopularBlockKeys(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k == "leafvideo01" {
			t.Errorf("keys include leafvideo01, which has no neighbourhood: %v", keys)
		}
	}
	if len(keys) == 0 || keys[0] != "hubvideo001" {
		t.Errorf("keys = %v, want hubvideo001 first", keys)
	}
}

var _ = filepath.Join
