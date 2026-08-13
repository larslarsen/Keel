// SPDX-License-Identifier: Apache-2.0
package store

import (
	"sort"
	"testing"
)

func TestWordStopwordsSorted(t *testing.T) {
	if !sort.StringsAreSorted(WordStopwords) {
		t.Fatalf("WordStopwords must stay sorted for binary search: %v", WordStopwords)
	}
}

func TestNormalizeWord(t *testing.T) {
	cases := map[string]string{
		"Trading": "trading",
		"foo-bar": "foobar",
		"123":     "",
		"":        "",
		"A":       "a",
	}
	for in, want := range cases {
		if got := NormalizeWord(in); got != want {
			t.Errorf("NormalizeWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCharTokensForWord(t *testing.T) {
	// "trading" → fixed, non-overlapping ShardK pieces cut from the front,
	// tail padded: "tra", "din", "g  ".
	toks := CharTokensForWord("trading")
	if len(toks) == 0 {
		t.Fatal("expected char tokens for trading")
	}
	// Every token must be exactly ShardK runes and dict-valid.
	for _, tok := range toks {
		if len(tok) != ShardK {
			t.Errorf("token %q length %d, want %d", tok, len(tok), ShardK)
		}
		if _, ok := TokenDictIndex(tok); !ok {
			t.Errorf("token %q rejected by TokenDictIndex", tok)
		}
	}
}

func TestWordTelemetryMergeConverges(t *testing.T) {
	a := NewWordTelemetry()
	a.addTitle("va", "sourdough baking")
	b := NewWordTelemetry()
	b.addTitle("vb", "sourdough starter")
	b.addTitle("vc", "rust programming")

	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	// Both sides of a merge of identical packs must agree on counts.
	c := NewWordTelemetry()
	c.addTitle("va", "sourdough baking")
	c.addTitle("vb", "sourdough starter")
	c.addTitle("vc", "rust programming")
	// a is union of a's original + b; c is the full set built once.
	// Distinct graph counts should match (3).
	if a.DistinctGraphs() != c.DistinctGraphs() {
		t.Errorf("graphs a=%d c=%d", a.DistinctGraphs(), c.DistinctGraphs())
	}
	// sourdough appears in 2 of 3 graphs on both.
	if a.WordGraphCount("sourdough") != c.WordGraphCount("sourdough") {
		t.Errorf("sourdough count a=%d c=%d", a.WordGraphCount("sourdough"), c.WordGraphCount("sourdough"))
	}
	pct, ok := a.WordPct("sourdough")
	if !ok || pct < 50 || pct > 80 {
		t.Errorf("WordPct(sourdough) = %v ok=%v, want ~66", pct, ok)
	}
}

func TestWordTelemetryWireRoundTrip(t *testing.T) {
	w := NewWordTelemetry()
	w.addTitle("v1", "day trading strategy")
	w.PrepareWire()
	raw := WordTelemetry{
		WordRegisters:  append([]byte(nil), w.WordRegisters...),
		GraphRegisters: append([]byte(nil), w.GraphRegisters...),
		FreqCounters:   append([]byte(nil), w.FreqCounters...),
		P:              w.P,
	}
	if err := raw.Hydrate(); err != nil {
		t.Fatal(err)
	}
	if raw.DistinctWords() != w.DistinctWords() {
		t.Errorf("words %d vs %d", raw.DistinctWords(), w.DistinctWords())
	}
	if raw.WordGraphCount("trading") != w.WordGraphCount("trading") {
		t.Errorf("trading freq mismatch")
	}
}

func TestQueryWordsAndStopwords(t *testing.T) {
	got := QueryWords("The Trading Strategy")
	want := []string{"the", "trading", "strategy"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	filtered := FilterStopwords(got)
	if len(filtered) != 2 || filtered[0] != "trading" {
		t.Errorf("FilterStopwords = %v", filtered)
	}
}

func TestIsStopword(t *testing.T) {
	if !IsStopword("the") || !IsStopword("video") {
		t.Error("expected stopwords")
	}
	if IsStopword("trading") {
		t.Error("trading must not be a stopword")
	}
}
