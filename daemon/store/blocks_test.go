// SPDX-License-Identifier: Apache-2.0
package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// pageLoadSeq gives each seeded impression its own page load.
//
// impressions is keyed on (page_load_id, surface, video_id), so reusing one id
// across calls silently collapses repeats into a single row — which quietly
// breaks any test that depends on how often something was recommended.
var pageLoadSeq int

func nextPageLoad() string {
	pageLoadSeq++
	return fmt.Sprintf("%08d-0000-4000-8000-000000000000", pageLoadSeq)
}

// seedEdge records one impression of `to` recommended from `from`.
func seedEdge(t *testing.T, st *Store, from, to string, slot int) {
	t.Helper()
	ctx := from
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: nextPageLoad(),
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, SlotIndex: slot, VideoID: to,
		Title: "Title " + to,
	}}); err != nil {
		t.Fatal(err)
	}
}

// testing.TB rather than *testing.T so fuzz targets can use it too — *testing.F
// satisfies the same interface.
func openStore(t testing.TB, name string) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestBlockHoldsOneNeighbourhood is the structural claim from §5d: a block is
// the edges out of one video, not a slice of the whole graph.
func TestBlockHoldsOneNeighbourhood(t *testing.T) {
	st := openStore(t, "a.sqlite")
	seedEdge(t, st, "seedaaaaaaa", "targetaaaa1", 0)
	seedEdge(t, st, "seedaaaaaaa", "targetaaaa2", 1)
	seedEdge(t, st, "otheraaaaaa", "targetaaaa3", 0)

	b, err := st.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Edges) != 2 {
		t.Fatalf("block has %d edges, want 2", len(b.Edges))
	}
	for _, e := range b.Edges {
		if e.From != "seedaaaaaaa" {
			t.Errorf("block contains an edge from %q", e.From)
		}
		if e.To == "targetaaaa3" {
			t.Error("block leaked an edge belonging to another neighbourhood")
		}
	}
	// Blocks are stringless: titles travel in the catalogue dataset, not here.
	// Re-marshalling must not reintroduce a catalogue field.
	raw, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if containsSub(string(raw), "catalogue") || containsSub(string(raw), "Title ") {
		t.Errorf("block payload carries strings: %s", raw)
	}
}

// TestBlockRoundTripAcrossNodes is the path the transport will drive: build on
// one node, verify and merge on another, and the walk sees new edges.
func TestBlockRoundTripAcrossNodes(t *testing.T) {
	a := openStore(t, "a.sqlite")
	seedEdge(t, a, "seedaaaaaaa", "targetaaaa1", 0)
	seedEdge(t, a, "seedaaaaaaa", "targetaaaa2", 3)

	b, err := a.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}

	receiver := openStore(t, "b.sqlite")
	got, n, err := receiver.ImportBlock(raw)
	if err != nil {
		t.Fatalf("valid block refused: %v", err)
	}
	if n != 2 {
		t.Errorf("imported %d edges, want 2", n)
	}
	if got.Key != "seedaaaaaaa" {
		t.Errorf("key = %q", got.Key)
	}

	// The receiver's walk must actually gain the neighbourhood.
	g, count, err := receiver.peerGraph()
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 || len(g["seedaaaaaaa"]) != 2 {
		t.Errorf("peer graph has %d rows and %d neighbours for the seed, want 2 neighbours",
			count, len(g["seedaaaaaaa"]))
	}
}

// TestImportBlockReplacesOnlyItsOwnKey is the difference from bundle import.
// A peer publishes many blocks; importing one must not delete the others.
func TestImportBlockReplacesOnlyItsOwnKey(t *testing.T) {
	a := openStore(t, "a.sqlite")
	seedEdge(t, a, "seedaaaaaaa", "targetaaaa1", 0)
	seedEdge(t, a, "seedbbbbbbb", "targetbbbb1", 0)

	receiver := openStore(t, "b.sqlite")
	for _, key := range []string{"seedaaaaaaa", "seedbbbbbbb"} {
		blk, err := a.BuildBlock(key, "GB-en")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := blk.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := receiver.ImportBlock(raw); err != nil {
			t.Fatal(err)
		}
	}

	// Re-import the first block; the second must survive.
	blk, err := a.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := receiver.ImportBlock(raw); err != nil {
		t.Fatal(err)
	}

	g, _, err := receiver.peerGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(g["seedbbbbbbb"]) != 1 {
		t.Fatalf("re-importing one block deleted another block from the same peer")
	}
}

// TestBlockRejectsTampering covers the two failure modes separately, because
// they are detected by different mechanisms.
func TestBlockRejectsTampering(t *testing.T) {
	a := openStore(t, "a.sqlite")
	seedEdge(t, a, "seedaaaaaaa", "targetaaaa1", 0)
	blk, err := a.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}

	// Edited content, digest left stale — caught by the digest.
	edited := *blk
	edited.Edges = append([]bridge.EdgeObservation{}, blk.Edges...)
	edited.Edges[0].Count = 9999
	raw, err := json.Marshal(&edited)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBlock(raw); err == nil {
		t.Error("block with an altered count was accepted")
	}

	// Edited content with the digest repaired — caught by the signature.
	repaired := edited
	if repaired.ContentSHA256, err = contentDigest(nil, repaired.Edges); err != nil {
		t.Fatal(err)
	}
	if raw, err = json.Marshal(&repaired); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBlock(raw); err == nil {
		t.Error("block with a repaired digest but broken signature was accepted")
	}

	// An edge from a different video must be refused even if perfectly signed:
	// a block is one neighbourhood, and accepting a foreign edge would let a
	// peer write anywhere in the graph by serving a block nobody asked about.
	smuggled := *blk
	smuggled.Edges = append([]bridge.EdgeObservation{}, blk.Edges...)
	smuggled.Edges[0].From = "elsewhereee"
	if smuggled.ContentSHA256, err = contentDigest(nil, smuggled.Edges); err != nil {
		t.Fatal(err)
	}
	payload, err := canonicalPayload(nil, smuggled.Edges)
	if err != nil {
		t.Fatal(err)
	}
	if smuggled.Signature, smuggled.PublicKey, err = a.signPayload(payload); err != nil {
		t.Fatal(err)
	}
	if raw, err = json.Marshal(&smuggled); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBlock(raw); err == nil {
		t.Error("a correctly signed block smuggled an edge from another neighbourhood")
	}
}

// TestEmptyBlockIsServable — "I have nothing here" is an answer, not an error.
func TestEmptyBlockIsServable(t *testing.T) {
	st := openStore(t, "a.sqlite")
	b, err := st.BuildBlock("nothingatall", "GB-en")
	if err != nil {
		t.Fatalf("building an empty block failed: %v", err)
	}
	raw, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBlock(raw); err != nil {
		t.Errorf("empty block did not verify: %v", err)
	}
}

// TestLocalBlockKeysNeverAdvertisesWatchHistory is the disclosure this call can
// cause if it is wrong: the context videos in `impressions` are the videos this
// user watched, so a mirroring node announcing them would be publishing a
// viewing history to every peer on the network.
func TestLocalBlockKeysNeverAdvertisesWatchHistory(t *testing.T) {
	st := openStore(t, "a.sqlite")
	seedEdge(t, st, "watchedvid1", "targetaaaa1", 0)
	seedEdge(t, st, "watchedvid2", "targetbbbb1", 0)

	// Mirroring (Level 2): nothing this user watched may be announced.
	mirror, err := st.LocalBlockKeys(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range mirror {
		if k == "watchedvid1" || k == "watchedvid2" {
			t.Fatalf("a mirroring node advertised a watched video: %v", mirror)
		}
	}

	// Level 3 and above, where publishing one's own edges is the opt-in.
	own, err := st.LocalBlockKeys(false)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, k := range own {
		found[k] = true
	}
	if !found["watchedvid1"] || !found["watchedvid2"] {
		t.Errorf("keys = %v, want both watched videos", own)
	}
}

// TestUnlabelledSuggestionsSurfaceUnlessBlocking covers the consequence of
// stringless blocks: a video known only from a fetched edge has no title and no
// channel yet.
//
// It must still be suggested — the walk found it, and dropping it would make
// fetched graph data useless until catalogue sync exists. But if the user has
// blocked any channel, an unlabelled video cannot be checked against that
// blocklist, and showing something they asked never to see is worse than
// briefly hiding something they did not.
func TestUnlabelledSuggestionsSurfaceUnlessBlocking(t *testing.T) {
	origin := openStore(t, "origin.sqlite")
	seedEdge(t, origin, "seedaaaaaaa", "unlabelled1", 0)
	blk, err := origin.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}

	receiver := openStore(t, "b.sqlite")
	if _, _, err := receiver.ImportBlock(raw); err != nil {
		t.Fatal(err)
	}

	// No blocklist: the unlabelled suggestion is surfaced, title empty.
	sug, err := receiver.Suggest("seedaaaaaaa", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sug.Suggestions) == 0 {
		t.Fatal("unlabelled suggestion was dropped; fetched graph data is unusable")
	}
	if sug.Suggestions[0].VideoID != "unlabelled1" {
		t.Errorf("suggested %q, want unlabelled1", sug.Suggestions[0].VideoID)
	}
	if sug.Suggestions[0].Title != "" {
		t.Errorf("title = %q, want empty — the block carries no strings", sug.Suggestions[0].Title)
	}

	// With a blocklist, an uncheckable video is withheld rather than shown.
	if err := receiver.BlockChannel("UCabcdefghijklmnopqrstuv"); err != nil {
		t.Fatal(err)
	}
	sug, err = receiver.Suggest("seedaaaaaaa", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range sug.Suggestions {
		if g.VideoID == "unlabelled1" {
			t.Error("an unlabelled video was shown to a user who has blocked channels")
		}
	}
}
