// SPDX-License-Identifier: Apache-2.0
package store

import (
	"strings"
	"testing"
)

// TestQueryGridPadsOnlyTheWholeTail is WO-097 §1's first acceptance line.
//
// Scheme 1 padded every word's tail, so `a big` produced [a  ][big] and the
// space between words was never a token character. Scheme 2 pads once, at the
// end of the whole normalized string, which is what lets a chunk straddle a
// space — and what makes the chunk count equal ceil(len/ShardK) rather than a
// sum over words.
func TestQueryGridPadsOnlyTheWholeTail(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"world", []string{"wor", "ld "}},
		{"a big", []string{"a b", "ig "}},
		{"red world", []string{"red", " wo", "rld"}},
	} {
		plan := BuildQueryPlan(tc.query)
		if len(plan.Tokens) != len(tc.want) {
			t.Errorf("plan(%q) has %d chunks, want %d", tc.query, len(plan.Tokens), len(tc.want))
			continue
		}
		for i, want := range tc.want {
			if plan.Tokens[i].Token != want {
				t.Errorf("plan(%q) chunk %d = %q, want %q", tc.query, i, plan.Tokens[i].Token, want)
			}
		}
		// Chunk ranges must tile the query with no gaps and no overlap: that is
		// what "fixed, non-overlapping" means, and it is what stops one query
		// producing more network work than the bars a person sees.
		for i, tok := range plan.Tokens {
			if tok.Start != i*ShardK || tok.End != (i+1)*ShardK {
				t.Errorf("plan(%q) chunk %d covers [%d,%d), want [%d,%d)",
					tc.query, i, tok.Start, tok.End, i*ShardK, (i+1)*ShardK)
			}
		}
	}
}

// TestTitleWindowsCoverEveryAlignment is the repair itself (WO-097 §2): a
// query chunk must be findable wherever the substring sits in a title, not
// only when it happens to start on the offset-zero grid.
func TestTitleWindowsCoverEveryAlignment(t *testing.T) {
	// `world` sits at offset 4 of this title — not a multiple of ShardK — which
	// is exactly the case scheme 1 could not find.
	index := map[string]bool{}
	for _, tok := range TitleTokens("the world today") {
		index[tok] = true
	}
	for _, tok := range TokenizeQuery("world") {
		if !index[tok] {
			t.Errorf("query token %q is missing from the index of %q", tok, "the world today")
		}
	}

	// The extra alignments are index coverage and must never leak into the
	// query side. One query, one fixed pass, however many alignments a title
	// generates.
	if got := len(BuildQueryPlan("world").Tokens); got != 2 {
		t.Errorf("the query %q produced %d chunks, want exactly 2 — title "+
			"alignments must not become query tokens, requests, colors or bars",
			"world", got)
	}
}

// TestStopwordOccurrenceRuleIsNotABannedTokenList is WO-097 §3.
//
// Every clause here is a case a banned-token-string list gets wrong.
func TestStopwordOccurrenceRuleIsNotABannedTokenList(t *testing.T) {
	has := func(title, token string) bool {
		for _, tok := range TitleTokens(title) {
			if tok == token {
				return true
			}
		}
		return false
	}

	// Stopword-only text generates nothing at all: this is the giant-shard
	// problem, and it is an indexing problem rather than a reason to build a
	// special structure for searching `is is`.
	if got := TitleTokens("is is"); len(got) != 0 {
		t.Errorf("TitleTokens(%q) = %v, want nothing — stopword-only occurrences "+
			"must generate no shard entries", "is is", got)
	}
	if got := TitleTokens("the a of and to"); len(got) != 0 {
		t.Errorf("TitleTokens(%q) = %v, want nothing", "the a of and to", got)
	}

	// `the` inside `theory` survives. A blacklist keyed on the token string
	// would delete it and make a real word unsearchable at its first three
	// letters.
	if !has("theory tutorial", "the") {
		t.Errorf("TitleTokens(%q) dropped %q — the rule is about which occurrence "+
			"a window's letters came from, not what the window spells",
			"theory tutorial", "the")
	}

	// A boundary window touching a meaningful word survives even though it also
	// touches a stopword and a space.
	if !has("the world", "e w") {
		t.Errorf("TitleTokens(%q) dropped the boundary window %q, which touches %q",
			"the world", "e w", "world")
	}

	// Windows wholly inside the stopword do not.
	if has("the world", "the") {
		t.Errorf("TitleTokens(%q) kept %q, whose letters are all from the stopword occurrence",
			"the world", "the")
	}

	// A meaningful word is indexed at every alignment regardless of what any
	// particular client would choose to fetch for a particular query.
	windows := TitleTokens("the world today")
	if len(windows) < 3 {
		t.Fatalf("TitleTokens(%q) = %v, too few to test", "the world today", windows)
	}
	for _, tok := range windows {
		if len(tok) != ShardK {
			t.Errorf("index window %q has length %d, want ShardK=%d", tok, len(tok), ShardK)
		}
	}
	// `tod`/`oda`/`day` all come from `today`, which this query never asks for.
	// They must be indexed anyway — the index is not built per query.
	for _, want := range []string{"tod", "oda", "day"} {
		if !has("the world today", want) {
			t.Errorf("TitleTokens(%q) is missing %q — meaningful windows must be "+
				"indexed even when no current query selects them", "the world today", want)
		}
	}
}

// TestStopwordQueriesStayLocalButStayRequired is the other half of §3: a
// stopword contributes no distributed discovery work and still has to be
// satisfied by the final matcher.
func TestStopwordQueriesStayLocalButStayRequired(t *testing.T) {
	// Stopword-only: no discovery tokens, so no peer is contacted at all and
	// the interface can show that there is visibly no network work to do.
	plan := BuildQueryPlan("the is")
	if got := plan.DiscoveryTokens(); len(got) != 0 {
		t.Errorf("the stopword-only query %q produced discovery tokens %v", "the is", got)
	}
	// It still renders: the chunks exist, they are simply not fetched.
	if len(plan.Tokens) == 0 {
		t.Error("a stopword-only query produced no render tokens at all")
	}
	for _, tok := range plan.Tokens {
		if tok.Discovery {
			t.Errorf("chunk %q of a stopword-only query is marked for discovery", tok.Token)
		}
	}

	// Mixed: the meaningful word drives discovery, and the stopword is still
	// enforced by the matcher.
	mixed := BuildQueryPlan("the world")
	if len(mixed.DiscoveryTokens()) == 0 {
		t.Fatal("the mixed query \"the world\" produced no discovery tokens")
	}
	if !mixed.MatchTitle("the world today") {
		t.Error("matcher rejected \"the world today\" for the query \"the world\"")
	}
	if mixed.MatchTitle("a world apart") {
		t.Error("matcher accepted \"a world apart\" for the query \"the world\" — " +
			"a stopword omitted from discovery is still required by the matcher")
	}
}

// TestMatcherRespectsNormalizedWordBoundaries is WO-097 §4's settled behavior,
// which this order must preserve exactly.
func TestMatcherRespectsNormalizedWordBoundaries(t *testing.T) {
	plan := BuildQueryPlan("world")
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"the world today", true},
		// The hyphen normalizes to a space, so this is two words and one of
		// them is `world`.
		{"world-star report", true},
		{"worldwide tour", false},
		{"underworld", false},
	} {
		if got := plan.MatchTitle(tc.title); got != tc.want {
			t.Errorf("MatchTitle(%q) for query %q = %v, want %v", tc.title, "world", got, tc.want)
		}
	}

	// Unquoted words are all required, in any order.
	any := BuildQueryPlan("red world")
	if !any.MatchTitle("world of red") {
		t.Error("unquoted words must match in any order")
	}
	if any.MatchTitle("red things") {
		t.Error("every unquoted word is required")
	}

	// Quoted text is an exact adjacent normalized phrase.
	phrase := BuildQueryPlan(`"red world"`)
	if !phrase.MatchTitle("a red world indeed") {
		t.Error("quoted phrase failed to match an adjacent occurrence")
	}
	if phrase.MatchTitle("world of red") {
		t.Error("quoted phrase matched a non-adjacent occurrence")
	}
}

// TestPlanFragmentsColorBothWordsAcrossASpace is WO-097 §1's render
// requirement and WO-095's cross-word coloring, decided here so the interface
// does not have to re-derive it.
func TestPlanFragmentsColorBothWordsAcrossASpace(t *testing.T) {
	plan := BuildQueryPlan("red world")
	// `red world` -> [red][ wo][rld]. The middle chunk begins in the space and
	// covers letters only in the second word.
	if len(plan.Tokens) != 3 {
		t.Fatalf("plan chunks = %v, want 3", plan.Tokens)
	}
	mid := plan.Tokens[1]
	if mid.Token != " wo" {
		t.Fatalf("middle chunk = %q, want %q", mid.Token, " wo")
	}
	if len(mid.Fragments) != 1 || mid.Fragments[0].WordID != plan.Words[1].WordID {
		t.Errorf("chunk %q colors %v, want only the second word — it begins in a "+
			"space and covers letters only in the next word", mid.Token, mid.Fragments)
	}
	if mid.BarWordID != plan.Words[1].WordID {
		t.Errorf("chunk %q placed its bar under word %d, want the next word %d",
			mid.Token, mid.BarWordID, plan.Words[1].WordID)
	}

	// A chunk genuinely straddling two words colors both and sits under the
	// first one whose letters it covers.
	straddle := BuildQueryPlan("a big")
	first := straddle.Tokens[0]
	if len(first.Fragments) != 2 {
		t.Fatalf("chunk %q colors %d fragments, want 2", first.Token, len(first.Fragments))
	}
	if first.BarWordID != straddle.Words[0].WordID {
		t.Errorf("chunk %q placed its bar under word %d, want the first word it covers, %d",
			first.Token, first.BarWordID, straddle.Words[0].WordID)
	}
}

// TestRepeatedWordsAndTokensShareIdentity is WO-095 §1's shared-state rule,
// decided in the plan: two occurrences of one word share a target and a count,
// and two occurrences of one chunk share a color and a fetch.
func TestRepeatedWordsAndTokensShareIdentity(t *testing.T) {
	plan := BuildQueryPlan("red red")
	if len(plan.Words) != 2 {
		t.Fatalf("expected two word occurrences, got %d", len(plan.Words))
	}
	if plan.Words[0].WordID != plan.Words[1].WordID {
		t.Error("two occurrences of the same word got different word ids, so they " +
			"would draw two bars against two copies of one target")
	}
	if got := len(plan.WordValues()); got != 1 {
		t.Errorf("WordValues() = %d distinct words, want 1", got)
	}

	// `red red` -> [red][ re][d  ]. Only one distinct chunk repeats here in
	// general, so assert the identity rule directly on a query that does.
	repeat := BuildQueryPlan("abcabc")
	ids := map[string]int{}
	for _, tok := range repeat.Tokens {
		if prev, seen := ids[tok.Token]; seen && prev != tok.TokenID {
			t.Errorf("chunk %q got two ids (%d, %d) — repeated occurrences must "+
				"share one color and one fetch state", tok.Token, prev, tok.TokenID)
		}
		ids[tok.Token] = tok.TokenID
		if tok.ColorSlot != tok.TokenID {
			t.Errorf("chunk %q has color slot %d against id %d", tok.Token, tok.ColorSlot, tok.TokenID)
		}
	}
	if got := len(repeat.DiscoveryTokens()); got != 1 {
		t.Errorf("query %q produced %d distinct discovery tokens, want 1 — "+
			"a repeated chunk is one fetch", "abcabc", got)
	}
}

// TestWordsAdvancedByTokenCrossesWords backs WO-095's word bars: a candidate
// found through a cross-space token may advance either of the words it covers.
func TestWordsAdvancedByTokenCrossesWords(t *testing.T) {
	plan := BuildQueryPlan("a big")
	advanced := plan.WordsAdvancedByToken()
	got := advanced["a b"]
	if len(got) != 2 {
		t.Errorf("token %q advances %v, want both words it covers", "a b", got)
	}
}

// TestWordIDsInTitleCountsOncePerVideo is the numerator rule for word bars:
// a repeated word in one title advances that word once, not twice.
func TestWordIDsInTitleCountsOncePerVideo(t *testing.T) {
	plan := BuildQueryPlan("red world")
	ids := plan.WordIDsInTitle("red red red world")
	if len(ids) != 2 {
		t.Errorf("WordIDsInTitle returned %v, want one entry per distinct query word", ids)
	}
	// A candidate containing only one query word advances only that word,
	// without becoming a result.
	partial := plan.WordIDsInTitle("red things")
	if len(partial) != 1 {
		t.Errorf("a title with one query word advanced %v, want exactly one word", partial)
	}
	if plan.MatchTitle("red things") {
		t.Error("a partial match became a result")
	}
}

// TestNormalizationIsOneSharedRule: query and title normalize identically, or
// nothing downstream can be compared at all.
func TestNormalizationIsOneSharedRule(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  World--Star!! ", "world star"},
		// Non-ASCII letters are dropped and, like any other non-a-z run, act as
		// separators — so `Ünïcode` splits rather than contracting. That is
		// what splitWords/NormalizeWord have always done; folding accents
		// instead would be a key-scheme decision, not a tidy-up.
		{"Ünïcode text", "n code text"},
		{"a\t\nb", "a b"},
		{"!!!", ""},
	} {
		if got := NormalizeSearchText(tc.in); got != tc.want {
			t.Errorf("NormalizeSearchText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPlanCarriesNoQueryBeyondItself guards the privacy boundary from the
// inside: the plan is allowed to hold the query, and everything derived from
// it that leaves the daemon must not hold token text.
//
// This cannot test the wire (that is bridge's job) but it can pin the shape
// the wire converter reads from: token text lives on PlanToken.Token and
// nowhere else, so a converter that copies fields wholesale would have to name
// it explicitly to leak it.
func TestPlanCarriesNoQueryBeyondItself(t *testing.T) {
	plan := BuildQueryPlan("secret phrase")
	for _, f := range plan.Tokens[0].Fragments {
		if f.Start < 0 || f.End > len(plan.Normalized) {
			t.Errorf("fragment [%d,%d) escapes the normalized query of length %d",
				f.Start, f.End, len(plan.Normalized))
		}
	}
	// Every range indexes Normalized, so a renderer slices the string it was
	// given rather than being sent token text separately.
	for _, tok := range plan.Tokens {
		if tok.Start > len(plan.Normalized) {
			t.Errorf("chunk range [%d,%d) starts past the normalized query", tok.Start, tok.End)
		}
		lo, hi := tok.Start, tok.End
		if hi > len(plan.Normalized) {
			hi = len(plan.Normalized)
		}
		if !strings.HasPrefix(tok.Token, plan.Normalized[lo:hi]) {
			t.Errorf("chunk %q does not match the text it claims to cover, %q",
				tok.Token, plan.Normalized[lo:hi])
		}
	}
}
