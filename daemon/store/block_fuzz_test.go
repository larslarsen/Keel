// SPDX-License-Identifier: Apache-2.0
package store

import (
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
)

// ============================================================================
// SQLite-technique ports on the block wire format (see TESTING.md, WO-062).
// The block is the unit of fetch/sync across the swarm (WO-052): a node serves
// blocks built from its corpus, peers verify + merge them. The encode → wire →
// verify → import path is untrusted-input surface and is fuzzed here.
// ============================================================================

// ---- Technique: Fuzz Testing (sqlite testing.html §4) -----------------------
//
// Fuzz the block import path the way SQLite fuzzes SQL/DB files. Any bytes must
// (a) never panic, (b) either verify cleanly or return a clean error. A block
// with a forged/missing signature must be rejected, never merged.

func FuzzImportBlock(f *testing.F) {
	// Seed corpus: a well-formed block, an empty block, an unsigned one, and
	// adversarial shapes.
	good := &Block{
		SchemaVersion: blockSchemaVersion,
		Key:           "12:abc",
		Cohort:        "GB-en",
		Edges:         []bridge.EdgeObservation{{To: "video2", Count: 1}},
	}
	if raw, err := good.Encode(); err == nil {
		f.Add(raw)
	}
	f.Add([]byte{})
	f.Add([]byte("not json"))
	f.Add([]byte(`{"key":"12:abc"}`)) // no signature
	f.Add([]byte(`{"schema_version":2,"key":"12:abc","signature":"deadbeef","public_key":"x"}`))
	f.Add([]byte(`{"schema_version":999,"key":"12:abc"}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		st := openStore(t, "fuzz.sqlite")
		// ImportBlock must never panic. It either merges (returns a block) or
		// returns a clean error. VerifyBlock is the pure verifier.
		b, _, err := st.ImportBlock(raw)
		if err != nil {
			if b != nil {
				t.Errorf("ImportBlock returned block with error")
			}
			return
		}
		// On success the block must carry a key and the schema must be accepted.
		if b.Key == "" {
			t.Errorf("ImportBlock merged a block with no key")
		}
	})
}

// ---- Technique: Round-trip / equivalence (§9) -------------------------------
//
// A block built from the store, encoded, and re-imported must reproduce the same
// edges. This is the "same answer with optimization on/off" guarantee applied to
// the build→encode→verify→import pipeline.

func TestBlockBuildEncodeImportRoundTrip(t *testing.T) {
	st := openStore(t, "rt.sqlite")
	seedEdge(t, st, "fromvid001", "tovid0001", 0)
	seedEdge(t, st, "fromvid001", "tovid0002", 3)

	blk, err := st.buildBlock("fromvid001", "GB-en", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Import into a SECOND store — simulates a peer receiving the block.
	st2 := openStore(t, "rt2.sqlite")
	got, _, err := st2.ImportBlock(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Edges) != len(blk.Edges) {
		t.Fatalf("imported %d edges, built %d", len(got.Edges), len(blk.Edges))
	}
	for i := range blk.Edges {
		if got.Edges[i].To != blk.Edges[i].To || got.Edges[i].Count != blk.Edges[i].Count {
			t.Errorf("edge %d diverged after import: %+v vs %+v", i, got.Edges[i], blk.Edges[i])
		}
	}
}

// ---- Technique: Malformed / forged-input rejection (§4.2) --------------------
//
// A block with a mismatched or absent signature must be rejected, not merged.
// This mirrors SQLite feeding corrupt DB files and checking graceful rejection.

func TestImportBlockRejectsForged(t *testing.T) {
	st := openStore(t, "forge.sqlite")
	cases := [][]byte{
		[]byte(`{"schema_version":2,"key":"12:abc","edges":[{"to":"x","count":1}],"signature":"bogus","public_key":"bogus","algorithm":"ed25519"}`),
		[]byte(`{"schema_version":2,"key":"12:abc","edges":[{"to":"x","count":1}]}`), // unsigned
		[]byte(`garbage`),
		[]byte(`{}`),
		[]byte(`{"schema_version":2,"key":"12:abc","edges":[{"to":"x","count":1}],"signature":"","public_key":"","algorithm":""}`),
	}
	for i, c := range cases {
		b, _, err := st.ImportBlock(c)
		if err == nil && b != nil {
			t.Errorf("case %d: forged/unsigned block was merged", i)
		}
	}
}
