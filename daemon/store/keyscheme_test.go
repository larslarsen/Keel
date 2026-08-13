// SPDX-License-Identifier: Apache-2.0
package store

import "testing"

// TestKeySchemeGoldenVectors pins every key derivation to a literal digest.
//
// This is the only mechanism that can catch the failure WO-060 is about. The
// compiler is no help: changing a domain string or a bucket width is valid Go
// and every test that round-trips through one node keeps passing, because that
// node agrees with itself. The damage only shows up between two nodes on
// different builds, as an empty network.
//
// The expected values were captured from this implementation, not derived
// independently — they record what scheme 1 has always produced, which is the
// property that matters: every node on scheme 1 must agree with them.
//
// So the expected values below are deliberately literal — not recomputed from
// the constants, which would make the test agree with any change. If one fails,
// a key-deriving constant moved, and the fix is to decide whether that was
// intended and then bump KeySchemeVersion. Do not edit the expectation to match
// the new output without doing that.
func TestKeySchemeGoldenVectors(t *testing.T) {
	if KeySchemeVersion != 2 {
		t.Fatalf("KeySchemeVersion is %d — the vectors below describe scheme 2. "+
			"Add vectors for the new scheme rather than changing these.", KeySchemeVersion)
	}

	// Scheme 2 changed exactly one thing: tokens are fixed, non-overlapping
	// k-grams cut per word from the front with the tail padded, instead of a
	// sliding window. Everything else below is unchanged from scheme 1 and is
	// still checked, because a bump is not licence to move the rest.
	for _, tc := range []struct {
		word string
		want []string
	}{
		{"eclipse", []string{"ecl", "ips", "e  "}},
		{"recommendation ai", []string{"rec", "omm", "end", "ati", "on ", "ai "}},
		{"men", []string{"men"}},
		{"a", []string{"a  "}},
		{"", nil},
	} {
		got := tokenize(tc.word, ShardK)
		if len(got) != len(tc.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tc.word, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tc.word, i, got[i], tc.want[i])
			}
		}
	}

	// Bucket width. Everything downstream of this changes if it moves, so it is
	// checked on its own as well as through the vectors.
	if DefaultPrefixBits != 12 {
		t.Errorf("DefaultPrefixBits = %d, want 12 for key scheme 2", DefaultPrefixBits)
	}

	for _, tc := range []struct {
		name, videoID, want string
		bits                int
	}{
		{"block bucket, default width", "dQw4w9WgXcQ", "12:35f0", DefaultPrefixBits},
		{"block bucket, another id", "seedaaaaaaa", "12:1590", DefaultPrefixBits},
		// A narrower width must be a genuine prefix of the wider one, not a
		// separate hash — otherwise a node cannot widen its own view without
		// refetching everything.
		{"block bucket, 8 bits", "dQw4w9WgXcQ", "8:35", 8},
	} {
		if got := BlockPrefix(tc.videoID, tc.bits); got != tc.want {
			t.Errorf("%s: BlockPrefix(%q, %d) = %q, want %q",
				tc.name, tc.videoID, tc.bits, got, tc.want)
		}
	}

	// The catalogue is bucketed by the same width but a different domain, so
	// the same video lands in unrelated buckets in the two systems. That is
	// what stops one being used to probe the other.
	if got, want := CataloguePrefix("dQw4w9WgXcQ", DefaultPrefixBits), BlockPrefix("dQw4w9WgXcQ", DefaultPrefixBits); got == want {
		t.Errorf("catalogue and block prefixes collide at %q — the domain separator is not doing its job", got)
	}

	if PrefixDomain != "keel/prefix/1/" {
		t.Errorf("PrefixDomain = %q — DHT provider records would move; bump KeySchemeVersion", PrefixDomain)
	}
	if blockDomain != "keel/block/1/" {
		t.Errorf("blockDomain = %q — every block bucket moves; bump KeySchemeVersion", blockDomain)
	}
	if catalogueDomain != "keel/catalogue/1/" {
		t.Errorf("catalogueDomain = %q — every catalogue bucket moves; bump KeySchemeVersion", catalogueDomain)
	}
	if shardDomain != "keel/shard/1/" {
		t.Errorf("shardDomain = %q — every shard bucket moves; bump KeySchemeVersion", shardDomain)
	}
	if ShardK != 3 {
		t.Errorf("ShardK = %d, want 3 for key scheme 2", ShardK)
	}
	if ShardM != 256 {
		t.Errorf("ShardM = %d, want 256 for key scheme 2", ShardM)
	}
	if TokenDictAlphabet != " abcdefghijklmnopqrstuvwxyz" {
		t.Errorf("TokenDictAlphabet = %q — every token index moves; bump KeySchemeVersion", TokenDictAlphabet)
	}
	if TokenDictSize != 19683 {
		t.Errorf("TokenDictSize = %d, want 19683 (27^3) for key scheme 1", TokenDictSize)
	}
	if YieldVectorBytes != 2461 {
		t.Errorf("YieldVectorBytes = %d, want 2461 (ceil(19683/8)) for key scheme 1", YieldVectorBytes)
	}
	if YieldThreshold != 0.10 {
		t.Errorf("YieldThreshold = %v, want 0.10 for key scheme 1", YieldThreshold)
	}
	if TokenSketchP != 8 {
		t.Errorf("TokenSketchP = %d, want 8 for key scheme 1", TokenSketchP)
	}
}
