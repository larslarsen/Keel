// SPDX-License-Identifier: Apache-2.0
// The canonical query plan, the title-window rule, and the final matcher
// (WO-097). Key scheme 2.
//
// # Why the query and the index tokenize differently
//
// Scheme 1 had one function for both sides and cut k-grams *per word*, so a
// word's tokens were the same wherever it appeared. That reads like the
// property matching needs, and it is not: a query is a continuous string, and
// the substring a person types can begin at any character offset in a title.
// `world` cut per word is [wor][ld ], and `the world today` cut per word is
// [the][wor][ld ][tod][ay ] — those happen to agree only because `world`
// starts a word. Move it one character and the two sides never meet again,
// which is the live failure this scheme exists to repair.
//
// So the two sides are deliberately asymmetric:
//
//   - **The query** is normalized once, as a whole, padded at its tail to a
//     multiple of ShardK, and cut into fixed non-overlapping chunks at offsets
//     0, 3, 6, … Spaces are consumed characters like any other. The tokenizer
//     never restarts at a word boundary and never slides. One query therefore
//     produces exactly ceil(len/ShardK) token occurrences — the count of the
//     bars a person sees, and the count of the peer requests made.
//
//   - **The index** normalizes a title the same way, appends ShardK-1 spaces,
//     and generates *every* overlapping ShardK window whose start lies in the
//     unpadded title — equivalently, all ShardK fixed-grid alignments at once.
//     A query chunk can then be found at whatever alignment it happens to sit
//     at.
//
// The extra alignments are inverted-index coverage and nothing else. They must
// never become extra query tokens, peer requests, colors or bars: a title
// generating three times as many windows costs index size, while a query
// generating three times as many chunks would triple the network work and
// triple what the interface claims is happening.
//
// # Why stopwords are filtered by occurrence, not by token string
//
// A shard holding every window that touches `is` or `the` is enormous and
// answers nothing anybody searched for. But a *banned token list* is the wrong
// instrument: `the` is also the first three characters of `theory`, and
// `theory` is exactly the kind of word search exists for. So the rule is about
// where a window's letters came from, not what the window spells — a window is
// kept when its non-space characters overlap at least one occurrence of a
// non-stopword word. `the` in `theory` is kept; `the` in `the world` is not;
// `e w` spanning `the world`'s boundary is kept, because it touches `world`.
//
// The same test decides which query chunks are worth a peer request. A chunk
// covering only stopword letters is not fetched, so a stopword-only query does
// local work and visibly no distributed work. Stopwords stay in the matcher
// below regardless: discovery is not the semantic test.
package store

import (
	"sort"
	"strings"
)

// NormalizeSearchText is scheme 2's one normalization rule, applied to a whole
// query and to a whole title identically: lowercase, every run of non-a-z
// characters becomes one space, leading and trailing separators trimmed.
//
// One rule for both sides is what makes a query chunk and a title window
// comparable at all. Non-ASCII letters are dropped rather than folded, which
// matches what splitWords/NormalizeWord have always done — changing that is a
// key-scheme decision, not a tidy-up.
func NormalizeSearchText(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
			continue
		}
		pendingSpace = true
	}
	return b.String()
}

// tokenSpan is one ShardK window and the character range it covers in the
// normalized string it came from. End may exceed the string's length: the
// tail of the last chunk is padding, which is a real part of the token value
// but not a real part of the text.
type tokenSpan struct {
	Token string
	Start int
	End   int
}

// wordSpan is one whitespace-delimited word occurrence and its character
// range. Occurrences, not values: `red red` is two spans.
type wordSpan struct {
	Word  string
	Start int
	End   int
}

// wordSpans splits an already-normalized string into its word occurrences.
// Cheap byte scanning rather than strings.Fields because the character offsets
// are the point — everything downstream intersects ranges.
func wordSpans(normalized string) []wordSpan {
	var out []wordSpan
	i := 0
	for i < len(normalized) {
		if normalized[i] == ' ' {
			i++
			continue
		}
		j := i
		for j < len(normalized) && normalized[j] != ' ' {
			j++
		}
		out = append(out, wordSpan{Word: normalized[i:j], Start: i, End: j})
		i = j
	}
	return out
}

// meaningfulMask marks every character position belonging to a non-stopword
// word occurrence.
//
// A mask over positions, not a set of strings — that is the whole difference
// between this and a banned-token list, and it is why `the` survives inside
// `theory`. See the file comment.
func meaningfulMask(normalized string) []bool {
	mask := make([]bool, len(normalized))
	for _, w := range wordSpans(normalized) {
		if IsStopword(w.Word) {
			continue
		}
		for i := w.Start; i < w.End; i++ {
			mask[i] = true
		}
	}
	return mask
}

// spanIsMeaningful reports whether [start,end) touches any meaningful
// position. Positions past the end of the mask are padding — spaces that
// belong to no word occurrence — so they contribute nothing either way.
func spanIsMeaningful(mask []bool, start, end int) bool {
	for i := start; i < end && i < len(mask); i++ {
		if i >= 0 && mask[i] {
			return true
		}
	}
	return false
}

// queryGrid cuts a normalized query into fixed, non-overlapping ShardK chunks
// at offsets 0, ShardK, 2*ShardK, … padding only the whole string's tail.
//
//	world  -> [wor] [ld ]
//	a big  -> [a b] [ig ]
//
// Padding the *whole* tail once, rather than each word's tail, is the
// difference from scheme 1 and the reason spaces are ordinary characters here.
func queryGrid(normalized string) []tokenSpan {
	if normalized == "" {
		return nil
	}
	padded := normalized
	if rem := len(padded) % ShardK; rem != 0 {
		padded += strings.Repeat(" ", ShardK-rem)
	}
	out := make([]tokenSpan, 0, len(padded)/ShardK)
	for i := 0; i+ShardK <= len(padded); i += ShardK {
		out = append(out, tokenSpan{Token: padded[i : i+ShardK], Start: i, End: i + ShardK})
	}
	return out
}

// titleWindows generates every overlapping ShardK window whose start lies in
// the unpadded normalized title, with ShardK-1 spaces appended so the final
// characters still produce a full-width window.
//
// Index coverage only. A title of length L yields L windows before
// deduplication, against ceil(L/ShardK) for the query grid — deliberately, so
// a query chunk is findable at every alignment. See the file comment.
func titleWindows(normalized string) []tokenSpan {
	if normalized == "" {
		return nil
	}
	padded := normalized + strings.Repeat(" ", ShardK-1)
	out := make([]tokenSpan, 0, len(normalized))
	for i := 0; i < len(normalized); i++ {
		out = append(out, tokenSpan{Token: padded[i : i+ShardK], Start: i, End: i + ShardK})
	}
	return out
}

// TitleTokens is the inverted-index entry set for one title: every alignment,
// stopword-only occurrences dropped, deduplicated and sorted.
//
// Deduplicating (video_id, token) membership here rather than at shard
// assignment keeps the sorted result directly usable as a ShardEntry's token
// list, which is what the pack digest is computed over.
func TitleTokens(title string) []string {
	normalized := NormalizeSearchText(title)
	if normalized == "" {
		return nil
	}
	mask := meaningfulMask(normalized)
	seen := make(map[string]bool, len(normalized))
	out := make([]string, 0, len(normalized))
	for _, w := range titleWindows(normalized) {
		if !spanIsMeaningful(mask, w.Start, w.End) {
			continue
		}
		if seen[w.Token] {
			continue
		}
		seen[w.Token] = true
		out = append(out, w.Token)
	}
	sort.Strings(out)
	return out
}

// PlanWord is one display-word occurrence in the normalized query.
//
// WordID identifies the word *value*, so two occurrences of `red` share one
// id, one target and one cumulative count (WO-095 §1). The occurrence list
// stays separate because rendering needs character ranges per occurrence.
type PlanWord struct {
	WordID   int
	Word     string
	Start    int
	End      int
	Stopword bool
}

// PlanFragment is the part of one display word that a token occurrence covers
// — the intersection of a token's character range with a word's.
//
// Token ranges are constructed before word layout, so a token spanning a space
// produces two fragments and colors parts of both words. That is a property of
// the fixed grid, not a special case.
type PlanFragment struct {
	WordID int
	Start  int
	End    int
}

// PlanToken is one token occurrence: a chunk of the fixed grid, its character
// range, and everything the interface needs to draw it.
type PlanToken struct {
	// TokenID identifies the token *value*. Repeated occurrences of the same
	// chunk share it, so they share one color and one live fetch state.
	TokenID int
	// ColorSlot is TokenID today and kept separate anyway: a renderer may need
	// to cycle a palette shorter than the token count, and conflating the two
	// would make that change a protocol change.
	ColorSlot int
	// Token is the chunk text. Daemon-internal — it must not reach the wire,
	// a log, or persistence (DESIGN_v2 §4.2).
	Token string
	Start int
	End   int
	// Discovery is whether this chunk is worth a peer request: false when its
	// covered letters belong only to stopword occurrences.
	Discovery bool
	// BarWordID is the deterministic placement for this token's one bar: the
	// first word whose letters it covers. -1 when it covers none, which only
	// an empty query can produce. The placement carries no search meaning.
	BarWordID int
	Fragments []PlanFragment
}

// PlanPhrase is one quoted segment: words required adjacent and in order.
type PlanPhrase struct {
	Words []string
}

// QueryPlan is the daemon's whole understanding of one query — the render
// plan, the discovery set and the final matcher, derived once.
//
// It lives in bounded memory for the life of one local request or search and
// is never persisted or logged. Normalized and Token carry query text.
type QueryPlan struct {
	// Normalized is the query under NormalizeSearchText. Every Start/End in
	// this plan indexes it.
	Normalized string
	Words      []PlanWord
	Tokens     []PlanToken
	Phrases    []PlanPhrase
}

// BuildQueryPlan derives the canonical plan for a raw query string.
//
// Token ranges are constructed before display-word ranges and intersected
// afterwards (WO-097 §1), which is what makes a cross-space token color both
// words rather than being clipped to one.
func BuildQueryPlan(raw string) QueryPlan {
	normalized := NormalizeSearchText(raw)
	plan := QueryPlan{
		Normalized: normalized,
		Words:      []PlanWord{},
		Tokens:     []PlanToken{},
		Phrases:    []PlanPhrase{},
	}
	if normalized == "" {
		return plan
	}

	spans := wordSpans(normalized)
	wordIDs := map[string]int{}
	for _, s := range spans {
		id, ok := wordIDs[s.Word]
		if !ok {
			id = len(wordIDs)
			wordIDs[s.Word] = id
		}
		plan.Words = append(plan.Words, PlanWord{
			WordID:   id,
			Word:     s.Word,
			Start:    s.Start,
			End:      s.End,
			Stopword: IsStopword(s.Word),
		})
	}

	mask := meaningfulMask(normalized)
	tokenIDs := map[string]int{}
	for _, t := range queryGrid(normalized) {
		id, ok := tokenIDs[t.Token]
		if !ok {
			id = len(tokenIDs)
			tokenIDs[t.Token] = id
		}
		pt := PlanToken{
			TokenID:   id,
			ColorSlot: id,
			Token:     t.Token,
			Start:     t.Start,
			End:       t.End,
			Discovery: spanIsMeaningful(mask, t.Start, t.End),
			BarWordID: -1,
			Fragments: []PlanFragment{},
		}
		for _, w := range plan.Words {
			lo, hi := t.Start, t.End
			if w.Start > lo {
				lo = w.Start
			}
			if w.End < hi {
				hi = w.End
			}
			if lo >= hi {
				continue
			}
			pt.Fragments = append(pt.Fragments, PlanFragment{WordID: w.WordID, Start: lo, End: hi})
			if pt.BarWordID < 0 {
				pt.BarWordID = w.WordID
			}
		}
		plan.Tokens = append(plan.Tokens, pt)
	}

	for _, phrase := range quotedPhrases(raw) {
		words := strings.Fields(NormalizeSearchText(phrase))
		if len(words) == 0 {
			continue
		}
		plan.Phrases = append(plan.Phrases, PlanPhrase{Words: words})
	}
	return plan
}

// quotedPhrases extracts the contents of double-quoted segments from a raw
// query. An unterminated quote takes the rest of the string, which is what a
// person typing a phrase and not yet having closed it means.
func quotedPhrases(raw string) []string {
	var out []string
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		end := strings.IndexByte(raw[i+1:], '"')
		if end < 0 {
			out = append(out, raw[i+1:])
			break
		}
		out = append(out, raw[i+1:i+1+end])
		i += end + 1
	}
	return out
}

// WordValues lists the distinct display words in first-occurrence order.
func (p QueryPlan) WordValues() []PlanWord {
	seen := map[int]bool{}
	out := make([]PlanWord, 0, len(p.Words))
	for _, w := range p.Words {
		if seen[w.WordID] {
			continue
		}
		seen[w.WordID] = true
		out = append(out, w)
	}
	return out
}

// DiscoveryTokens lists the distinct token values worth fetching from peers,
// in first-occurrence order.
//
// Order is query order rather than sorted, so the caller's bounded parallel
// work starts with the tokens a person sees first. Repeated occurrences
// collapse to one value, and so to one fetch.
func (p QueryPlan) DiscoveryTokens() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range p.Tokens {
		if !t.Discovery || seen[t.Token] {
			continue
		}
		seen[t.Token] = true
		out = append(out, t.Token)
	}
	return out
}

// WordsAdvancedByToken maps each discovery token value to the distinct word
// ids its characters touch — what a result found through that token can
// advance (WO-097 §9).
//
// A cross-space token legitimately advances two words. Its single bar sits
// under one of them; that placement is presentation, and this is not.
func (p QueryPlan) WordsAdvancedByToken() map[string][]int {
	out := map[string][]int{}
	for _, t := range p.Tokens {
		if !t.Discovery {
			continue
		}
		seen := map[int]bool{}
		for _, id := range out[t.Token] {
			seen[id] = true
		}
		ids := out[t.Token]
		for _, f := range t.Fragments {
			if seen[f.WordID] {
				continue
			}
			seen[f.WordID] = true
			ids = append(ids, f.WordID)
		}
		out[t.Token] = ids
	}
	return out
}

// Empty reports whether the plan can match anything at all.
func (p QueryPlan) Empty() bool { return len(p.Words) == 0 }

// MatchTitle applies the complete query semantics to one title — the final
// test, and the only one.
//
// Token-shard membership discovers candidates; it never decides them. A
// candidate found through one token still has to satisfy every word and every
// phrase here, and a candidate no shard mentioned would still match if its
// title were resolved some other way.
//
// The settled behavior (WO-095 §2), unchanged by this order:
//
//   - unquoted normalized words are all required, in any order;
//   - quoted text is required as an exact adjacent normalized phrase; and
//   - normalized word boundaries are respected, so `world` matches
//     `the world today` and `world-star` (the hyphen normalizes to a space)
//     but not `worldwide`.
//
// Stopwords are required here even when WO-097 omitted them from discovery.
func (p QueryPlan) MatchTitle(title string) bool {
	if p.Empty() {
		return false
	}
	titleWords := strings.Fields(NormalizeSearchText(title))
	present := make(map[string]bool, len(titleWords))
	for _, w := range titleWords {
		present[w] = true
	}
	for _, w := range p.WordValues() {
		if !present[w.Word] {
			return false
		}
	}
	for _, phrase := range p.Phrases {
		if !containsPhrase(titleWords, phrase.Words) {
			return false
		}
	}
	return true
}

// WordIDsInTitle reports which distinct query words the title confirms.
//
// This is the numerator source for WO-095's word bars: a candidate that
// contains only one query word advances only that word, without becoming a
// result. Word ids, counted once per video however often the title repeats
// the word.
func (p QueryPlan) WordIDsInTitle(title string) []int {
	titleWords := strings.Fields(NormalizeSearchText(title))
	if len(titleWords) == 0 {
		return nil
	}
	present := make(map[string]bool, len(titleWords))
	for _, w := range titleWords {
		present[w] = true
	}
	var out []int
	for _, w := range p.WordValues() {
		if present[w.Word] {
			out = append(out, w.WordID)
		}
	}
	return out
}

// containsPhrase reports whether want appears as a consecutive run in have.
func containsPhrase(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	if len(want) > len(have) {
		return false
	}
	for i := 0; i+len(want) <= len(have); i++ {
		match := true
		for j := range want {
			if have[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
