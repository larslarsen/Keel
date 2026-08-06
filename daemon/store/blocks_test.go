// SPDX-License-Identifier: Apache-2.0
package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// seedEdge records one impression of `to` recommended from `from`.
func seedEdge(t *testing.T, st *Store, from, to string, slot int) {
	t.Helper()
	ctx := from
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "22222222-2222-4222-8222-222222222222",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, SlotIndex: slot, VideoID: to,
		Title: "Title " + to,
	}}); err != nil {
		t.Fatal(err)
	}
}

func openStore(t *testing.T, name string) *Store {
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
	// Targets must carry labels, or a walk into new territory renders bare ids.
	if len(b.Catalogue) == 0 {
		t.Error("block carries no catalogue; fetched blocks would not render")
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
	if repaired.ContentSHA256, err = contentDigest(repaired.Catalogue, repaired.Edges); err != nil {
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
	if smuggled.ContentSHA256, err = contentDigest(smuggled.Catalogue, smuggled.Edges); err != nil {
		t.Fatal(err)
	}
	payload, err := canonicalPayload(smuggled.Catalogue, smuggled.Edges)
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

// TestLocalBlockKeys lists what this node can serve, local and imported alike.
func TestLocalBlockKeys(t *testing.T) {
	st := openStore(t, "a.sqlite")
	seedEdge(t, st, "seedaaaaaaa", "targetaaaa1", 0)
	seedEdge(t, st, "seedbbbbbbb", "targetbbbb1", 0)

	keys, err := st.LocalBlockKeys()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, k := range keys {
		found[k] = true
	}
	if !found["seedaaaaaaa"] || !found["seedbbbbbbb"] {
		t.Errorf("keys = %v, want both seeds", keys)
	}
}
