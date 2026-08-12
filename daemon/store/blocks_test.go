// SPDX-License-Identifier: Apache-2.0
package store

import (
	"crypto/ed25519"
	"encoding/base64"
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
	if repaired.ContentSHA256, err = claimDigest(repaired.Key, repaired.Cohort, repaired.Edges); err != nil {
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
	resignClaim(t, a, &smuggled)
	if raw, err = json.Marshal(&smuggled); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBlock(raw); err == nil {
		t.Error("a correctly signed block smuggled an edge from another neighbourhood")
	}

	// The cohort is inside the claim payload at schema 3, unlike schema 2 where
	// only the edges were covered. A relay could otherwise re-label whose
	// cohort a neighbourhood belongs to without breaking a thing.
	relabelled := *blk
	relabelled.Cohort = "XX-zz"
	if raw, err = json.Marshal(&relabelled); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBlock(raw); err == nil {
		t.Error("a block's cohort was re-labelled in flight and still verified")
	}

	// So is the revision, which decides which version of a claim wins
	// replacement at every holder.
	promoted := *blk
	promoted.Revision = blk.Revision + 500
	if raw, err = json.Marshal(&promoted); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBlock(raw); err == nil {
		t.Error("a block's revision was raised in flight and still verified")
	}
}

// resignClaim re-signs a block with st's claim key for its own graph key, so a
// test can produce a block that is internally consistent and still wrong.
func resignClaim(t *testing.T, st *Store, b *Block) {
	t.Helper()
	var err error
	if b.ContentSHA256, err = claimDigest(b.Key, b.Cohort, b.Edges); err != nil {
		t.Fatal(err)
	}
	priv, pub, err := st.claimKey(b.Key)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := claimPayload(b.Key, b.Cohort, b.Revision, b.Edges)
	if err != nil {
		t.Fatal(err)
	}
	b.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	b.PublicKey = pub
	b.Algorithm = signAlgorithm
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

// TestLocalBlockKeysFollowsItsSourceSet holds the seam WO-084 replaced.
//
// This used to assert the opposite — that a Level-2 node must never list a
// video its user watched — because the flag it took could only choose one
// corpus. A SourceSet cannot express "own instead of imported", so the property
// worth holding now is that each selector returns exactly its own corpus and
// the union returns both. What keeps the watched-video keys from being a
// viewing history on the wire is LocalPrefixes hashing them into shared 12-bit
// buckets, which TestLocalPrefixesDoNotNameVideos holds.
func TestLocalBlockKeysFollowsItsSourceSet(t *testing.T) {
	st := openStore(t, "a.sqlite")
	seedEdge(t, st, "watchedvid1", "targetaaaa1", 0)
	seedEdge(t, st, "watchedvid2", "targetbbbb1", 0)

	origin := openStore(t, "origin.sqlite")
	seedEdge(t, origin, "peerseedaa1", "targetcccc1", 0)
	blk, err := origin.BuildBlock("peerseedaa1", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ImportBlock(raw); err != nil {
		t.Fatal(err)
	}

	set := func(sources SourceSet) map[string]bool {
		t.Helper()
		keys, err := st.LocalBlockKeys(sources)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, k := range keys {
			out[k] = true
		}
		return out
	}

	if got := set(SourceSet{}); len(got) != 0 {
		t.Errorf("the empty source set listed %v; a Level-1 node advertises nothing", got)
	}
	peers := set(PeerSources)
	if !peers["peerseedaa1"] {
		t.Error("the peer source set dropped an imported neighbourhood")
	}
	if peers["watchedvid1"] || peers["watchedvid2"] {
		t.Errorf("the peer source set listed local material: %v", peers)
	}
	local := set(LocalSources)
	if !local["watchedvid1"] || !local["watchedvid2"] {
		t.Errorf("the local source set dropped a local neighbourhood: %v", local)
	}
	if local["peerseedaa1"] {
		t.Errorf("the local source set listed imported material: %v", local)
	}
	all := set(AllSources)
	for _, want := range []string{"watchedvid1", "watchedvid2", "peerseedaa1"} {
		if !all[want] {
			t.Errorf("the union dropped %q — this is the mirror-only bug WO-084 corrected", want)
		}
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
