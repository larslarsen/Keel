// SPDX-License-Identifier: Apache-2.0
package store

import (
	"bytes"
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

// FuzzBlocksInPrefix fuzzes the bucket-serving path (WO-062 §1).
//
// The prefix in a peer's request is fully attacker-controlled: it arrives over
// the wire from a stranger and is parsed before anything else happens. A
// malformed one must produce an error, never a panic that takes the daemon down
// — a node that can be crashed by one request is a node anyone can remove from
// the network.
func FuzzBlocksInPrefix(f *testing.F) {
	f.Add("12:35f0")
	f.Add("8:35")
	f.Add("")
	f.Add(":")
	f.Add("12:")
	f.Add("0:00")
	f.Add("65:ffff")
	f.Add("-1:ff")
	f.Add("12:zzzz")
	f.Add("99999999999999999999:ff")

	st := openStore(f, "fuzz-prefix.sqlite")
	ctx := "seedaaaaaaa"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "33333333-3333-4333-8333-333333333333",
		ObservedAt: 1, Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, SlotIndex: 0, VideoID: "targetaaaa1", Title: "t",
	}}); err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, prefix string) {
		blocks, err := st.BlocksInPrefix(prefix, "", false, 8)
		if err != nil {
			return
		}
		// A prefix good enough to serve must be good enough to parse back, or
		// the node is answering under a key it could not itself request.
		if _, ok := PrefixOf(prefix); !ok {
			t.Fatalf("served %d blocks for prefix %q that PrefixOf rejects",
				len(blocks), prefix)
		}
		if len(blocks) > 8 {
			t.Fatalf("limit of 8 returned %d blocks", len(blocks))
		}
	})
}

// FuzzShardSlice fuzzes the token-shard serving path (WO-059), mirroring
// FuzzBlocksInPrefix: the shard number in a peer's request is fully
// attacker-controlled, so an out-of-range or adversarial value must produce a
// clean error, never a panic, and whatever is served must actually belong to
// the shard that was asked for.
func FuzzShardSlice(f *testing.F) {
	f.Add(0)
	f.Add(255)
	f.Add(ShardM - 1)
	f.Add(ShardM)
	f.Add(-1)
	f.Add(1 << 30)
	f.Add(-(1 << 30))

	st := openStore(f, "fuzz-shard.sqlite")
	ctx := "seedaaaaaaa"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "44444444-4444-4444-8444-444444444444",
		ObservedAt: 1, Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, SlotIndex: 0,
		VideoID: "targetaaaa1", Title: "Recommendation systems explained",
	}}); err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, shard int) {
		entries, err := st.ShardSlice(shard, false)
		if err != nil {
			if shard >= 0 && shard < ShardM {
				t.Fatalf("in-range shard %d returned an error: %v", shard, err)
			}
			return
		}
		if shard < 0 || shard >= ShardM {
			t.Fatalf("out-of-range shard %d was served instead of rejected", shard)
		}
		for _, e := range entries {
			for _, tok := range e.Tokens {
				if ShardOf(tok) != shard {
					t.Fatalf("shard %d served token %q, which hashes to shard %d", shard, tok, ShardOf(tok))
				}
			}
		}
	})
}

// FuzzImportCataloguePack fuzzes the other thing a stranger can hand us.
//
// Same contract as FuzzImportBlock: garbage in must mean an error out, and
// anything accepted must leave the store usable afterwards.
func FuzzImportCataloguePack(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("{}"))
	f.Add([]byte(`{"schema":1}`))
	f.Add([]byte(`{"schema":1,"entries":[]}`))
	f.Add([]byte(`{"schema":999,"entries":[{"video_id":"aaaaaaaaaaa"}]}`))
	f.Add(bytes.Repeat([]byte("\x00"), 64))

	// One store for the whole run, not one per input. A fresh SQLite file per
	// iteration caps throughput at a few dozen executions a second, which is far
	// too slow for fuzzing to reach anything interesting. Sharing it also tests
	// the more realistic case: importing into a store that already holds data.
	st := openStore(f, "fuzz-cat.sqlite")

	f.Fuzz(func(t *testing.T, raw []byte) {
		n, err := st.ImportCataloguePack(raw)
		if err != nil {
			return
		}
		if n < 0 {
			t.Fatalf("imported a negative number of entries: %d", n)
		}
		// Whatever was accepted, the store must still answer afterwards.
		if _, err := st.ListQueue(); err != nil {
			t.Fatalf("store unusable after importing %d entries: %v", n, err)
		}
	})
}
