// SPDX-License-Identifier: Apache-2.0
package store

import "testing"

// scheme1TokenizerRecord is what deployed scheme 1 produced, kept verbatim.
//
// Scheme 1's tokenizer cut fixed k-grams *per word*. That function is gone —
// keeping dead production code to satisfy a test would be worse than useless —
// but its outputs are the deployed record and are preserved here, because the
// whole argument for bumping KeySchemeVersion is that scheme 2 generates
// different token data for the same input. A reader who wants to know what a
// scheme-1 peer holds reads this table, and
// TestSchemeTwoDoesNotReproduceSchemeOneTokens proves the two cannot be mixed.
var scheme1TokenizerRecord = []struct {
	text string
	want []string
}{
	{"eclipse", []string{"ecl", "ips", "e  "}},
	{"recommendation ai", []string{"rec", "omm", "end", "ati", "on ", "ai "}},
	{"men", []string{"men"}},
	{"a", []string{"a  "}},
	// The failure that started WO-097: scheme 1 tokenized `world` to
	// [wor][ld ] and `the world today` to [the][wor][ld ][tod][ay ]. Those
	// agree only because `world` happens to start a word — which is why
	// `wor` fetched 18 rows and `ld ` fetched 0 in live QA.
	{"world", []string{"wor", "ld "}},
	{"the world today", []string{"the", "wor", "ld ", "tod", "ay "}},
}

// TestKeySchemeGoldenVectors pins every key derivation to a literal digest.
//
// This is the only mechanism that can catch the failure WO-060 is about. The
// compiler is no help: changing a domain string or a bucket width is valid Go
// and every test that round-trips through one node keeps passing, because that
// node agrees with itself. The damage only shows up between two nodes on
// different builds, as an empty network.
//
// The expected values were captured from this implementation, not derived
// independently — they record what the scheme has always produced, which is the
// property that matters: every node on a scheme must agree with them.
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

	// The query grid: one fixed, non-overlapping pass over the WHOLE
	// normalized query, tail-padded once. Spaces are consumed characters, the
	// tokenizer never restarts at a word boundary, and it never slides.
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"world", []string{"wor", "ld "}},
		{"a big", []string{"a b", "ig "}},
		{"eclipse", []string{"ecl", "ips", "e  "}},
		// Only the whole normalized tail is padded — `a` does not become
		// `a  ` followed by a second word's own padding.
		{"a bigger thing", []string{"a b", "igg", "er ", "thi", "ng "}},
		{"men", []string{"men"}},
		{"a", []string{"a  "}},
		{"", nil},
		// Normalization runs first: punctuation collapses to one space and the
		// ends are trimmed, so these are the same query.
		{"  World--Star!! ", []string{"wor", "ld ", "sta", "r  "}},
	} {
		got := queryGridTokens(tc.query)
		if len(got) != len(tc.want) {
			t.Errorf("query grid %q = %v, want %v", tc.query, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("query grid %q [%d] = %q, want %q", tc.query, i, got[i], tc.want[i])
			}
		}
	}

	// The index side: every alignment, so a query chunk is findable wherever
	// it sits. `world` starts at offset 4 of `the world today`, off the
	// offset-zero grid, and both of its chunks must still be present.
	windows := map[string]bool{}
	for _, w := range TitleTokens("the world today") {
		windows[w] = true
	}
	for _, want := range []string{"wor", "ld "} {
		if !windows[want] {
			t.Errorf("TitleTokens(%q) is missing %q — a query chunk off the "+
				"offset-zero grid is unfindable, which is the scheme-1 defect",
				"the world today", want)
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
		t.Errorf("TokenDictSize = %d, want 19683 (27^3) for key scheme 2", TokenDictSize)
	}
	if YieldVectorBytes != 2461 {
		t.Errorf("YieldVectorBytes = %d, want 2461 (ceil(19683/8)) for key scheme 2", YieldVectorBytes)
	}
	if YieldThreshold != 0.10 {
		t.Errorf("YieldThreshold = %v, want 0.10 for key scheme 2", YieldThreshold)
	}
	if TokenSketchP != 8 {
		t.Errorf("TokenSketchP = %d, want 8 for key scheme 2", TokenSketchP)
	}
}

// queryGridTokens is the test's view of the query side: the chunk values in
// character order, before the stopword-occurrence filter picks discovery
// tokens out of them.
func queryGridTokens(query string) []string {
	spans := queryGrid(NormalizeSearchText(query))
	if len(spans) == 0 {
		return nil
	}
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Token)
	}
	return out
}

// TestSchemeTwoDoesNotReproduceSchemeOneTokens is the compatibility fence's
// justification, asserted rather than asserted-in-prose (WO-097 §5).
//
// If scheme 2 happened to generate the same token data as scheme 1, the
// KeySchemeVersion bump would be gratuitous and the temporary network
// partition it causes would be unjustified. It does not, and the divergence
// below is exactly the live failure: scheme 1 indexed `the world today` under
// tokens that a `world` query can only find when the word starts at a grid
// offset.
func TestSchemeTwoDoesNotReproduceSchemeOneTokens(t *testing.T) {
	diverged := false
	for _, tc := range scheme1TokenizerRecord {
		scheme2 := map[string]bool{}
		for _, tok := range TitleTokens(tc.text) {
			scheme2[tok] = true
		}
		for _, old := range tc.want {
			if !scheme2[old] {
				diverged = true
			}
		}
		if len(TitleTokens(tc.text)) != len(tc.want) {
			diverged = true
		}
	}
	if !diverged {
		t.Fatal("scheme 2 reproduces scheme 1's token data exactly — if that is " +
			"really true, the KeySchemeVersion bump costs a network partition " +
			"and buys nothing, and WO-097 §5 needs revisiting")
	}

	// The specific repair: `world`'s chunks are both present in a title where
	// `world` does not start at a multiple of ShardK. Under scheme 1 they were
	// not, which is why `ld ` fetched zero rows in live QA.
	title := TitleTokens("watch the world today")
	have := map[string]bool{}
	for _, tok := range title {
		have[tok] = true
	}
	for _, tok := range TokenizeQuery("world") {
		if !have[tok] {
			t.Errorf("query token %q is absent from the index of %q — scheme 2 has "+
				"not repaired the alignment defect", tok, "watch the world today")
		}
	}
}
