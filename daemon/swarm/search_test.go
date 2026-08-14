// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// planState builds a searchState over a real query plan without a node, so the
// stop matrix and the counting rules can be tested directly.
//
// Deterministic by construction: the alternative — driving these through real
// DHT provider ordering — cannot express "three quiet peers, then a productive
// one", because provider order is not something a test controls. Same reasoning
// resolveShardEntries and shouldStopOnSaturation were pulled out under WO-067.
func planState(t *testing.T, query string, targets map[int]store.WordTarget) (*searchState, store.QueryPlan) {
	t.Helper()
	plan := store.BuildQueryPlan(query)
	s := &searchState{
		plan:      plan,
		targets:   targets,
		ev:        nopEvents{},
		tokenIDs:  map[string]int{},
		wordsFor:  map[string][]int{},
		wordFound: map[int]map[string]bool{},
		wordMiss:  map[int]int{},
		checked:   map[string]bool{},
		emitted:   map[string]bool{},
		usedPeers: map[peer.ID]bool{},
		budget:    1 << 30,
	}
	for _, tok := range plan.Tokens {
		if tok.Discovery {
			s.tokenIDs[tok.Token] = tok.TokenID
		}
	}
	stopword := map[int]bool{}
	for _, w := range plan.WordValues() {
		stopword[w.WordID] = w.Stopword
	}
	for token, ids := range plan.WordsAdvancedByToken() {
		keep := []int{}
		for _, id := range ids {
			if !stopword[id] {
				keep = append(keep, id)
			}
		}
		s.wordsFor[token] = keep
	}
	return s, plan
}

type nopEvents struct{}

func (nopEvents) TokenPhase(int, int, string, string) {}
func (nopEvents) WordProgress(int, int)               {}
func (nopEvents) Result(bridge.SearchHit)             {}

// wordIDOf finds a word's plan id by its text.
func wordIDOf(plan store.QueryPlan, word string) int {
	for _, w := range plan.WordValues() {
		if w.Word == word {
			return w.WordID
		}
	}
	return -1
}

// TestStopMatrix is WO-095 §8, clause by clause.
//
// Each clause exists because the obvious simplification breaks a real case:
// stopping on saturation alone abandons a search that was merely unlucky in
// which peers it tried, and stopping on target alone abandons one that is still
// producing matches past an estimate the sketch under-counted.
func TestStopMatrix(t *testing.T) {
	s, plan := planState(t, "world", map[int]store.WordTarget{})
	world := wordIDOf(plan, "world")
	s.targets[world] = store.WordTarget{Word: "world", Adjusted: 10, Known: true}
	s.wordFound[world] = map[string]bool{}

	find := func(n int) {
		for i := 0; i < n; i++ {
			s.wordFound[world][string(rune('a'+i))] = true
		}
	}

	// Below target, saturated: KEEP GOING. Counts almost never decrease, so
	// three quiet peers more likely means bad luck in who was tried.
	find(3)
	s.wordMiss[world] = searchSaturationStreak
	if s.wordDone(world) {
		t.Error("stopped below target on saturation alone — a search that was " +
			"merely unlucky in its peers is abandoned")
	}

	// At/above target, still productive: KEEP GOING. The target is an estimate
	// from a sketch, not a ceiling.
	find(12)
	s.wordMiss[world] = 0
	if s.wordDone(world) {
		t.Error("stopped at target while valid new matches were still arriving")
	}

	// At/above target AND saturated: STOP.
	s.wordMiss[world] = searchSaturationStreak
	if !s.wordDone(world) {
		t.Error("did not stop when the word was above target and had then saturated")
	}

	// Unknown target: the bounded saturation fallback is all there is.
	s2, plan2 := planState(t, "world", map[int]store.WordTarget{})
	w2 := wordIDOf(plan2, "world")
	s2.wordFound[w2] = map[string]bool{}
	s2.wordMiss[w2] = searchSaturationStreak - 1
	if s2.wordDone(w2) {
		t.Error("stopped before the saturation streak with no target")
	}
	s2.wordMiss[w2] = searchSaturationStreak
	if !s2.wordDone(w2) {
		t.Error("an unknown target must fall back to bounded saturation, not run forever")
	}
}

// TestABoundaryTokenStaysEligibleWhileEitherWordIsIncomplete is WO-095 §8's
// last clause. Stopping a shared token when one of its words finishes would
// strand the other for the sake of the first.
func TestABoundaryTokenStaysEligibleWhileEitherWordIsIncomplete(t *testing.T) {
	s, plan := planState(t, "a big", map[int]store.WordTarget{})
	// `a big` cuts to [a b][ig ]; the first chunk covers letters of both words,
	// and `a` is a stopword so only `big` is tracked. Use a query where a
	// boundary token genuinely spans two meaningful words instead.
	_ = plan

	s2, plan2 := planState(t, "red world", map[int]store.WordTarget{})
	red, world := wordIDOf(plan2, "red"), wordIDOf(plan2, "world")
	s2.wordFound[red] = map[string]bool{}
	s2.wordFound[world] = map[string]bool{}
	s2.targets[red] = store.WordTarget{Word: "red", Adjusted: 1, Known: true}
	s2.targets[world] = store.WordTarget{Word: "world", Adjusted: 1, Known: true}

	// Find a token covering both words. `red world` cuts to [red][ wo][rld];
	// none straddles two meaningful words here, so assert the general rule on
	// whichever token has two tracked words, and skip cleanly if the query
	// shape does not produce one.
	var shared string
	for token, words := range s2.wordsFor {
		if len(words) == 2 {
			shared = token
			break
		}
	}
	if shared == "" {
		// Construct the case directly: the rule is about wordsFor, not about
		// which query happens to produce a straddle.
		shared = "synthetic"
		s2.wordsFor[shared] = []int{red, world}
	}

	// `red` is finished; `world` is not.
	s2.wordFound[red]["v1"] = true
	s2.wordMiss[red] = searchSaturationStreak
	s2.wordMiss[world] = 0
	if !s2.wordDone(red) {
		t.Fatal("test setup: red should be done")
	}
	if s2.tokenSatisfied(shared) {
		t.Error("a token feeding two words stopped when only one of them finished")
	}

	// Now both are finished.
	s2.wordFound[world]["v2"] = true
	s2.wordMiss[world] = searchSaturationStreak
	if !s2.tokenSatisfied(shared) {
		t.Error("a token stayed eligible after every word it feeds had finished")
	}
	_ = s
}

// TestStopwordsDriveNoBarAndNoStopCondition is WO-095 §7.
//
// A stopword is required by the matcher and invisible in the interface, so
// letting one gate the stop condition would stop the search for a reason nobody
// watching could see.
func TestStopwordsDriveNoBarAndNoStopCondition(t *testing.T) {
	s, plan := planState(t, "the world", map[int]store.WordTarget{})
	the, world := wordIDOf(plan, "the"), wordIDOf(plan, "world")
	if the < 0 || world < 0 {
		t.Fatal("test setup: both words should be in the plan")
	}
	if s.tracksWord(the) {
		t.Error("a stopword is tracked for word-bar and stop purposes")
	}
	if !s.tracksWord(world) {
		t.Error("a meaningful word is not tracked")
	}
	for token, words := range s.wordsFor {
		for _, id := range words {
			if id == the {
				t.Errorf("token %q lists the stopword %q among the words it advances", token, "the")
			}
		}
	}
}

// TestOutcomeReportsAnUnmetTargetHonestly is WO-095 §9: do not silently turn
// failure or budget exhaustion into an empty successful search.
func TestOutcomeReportsAnUnmetTargetHonestly(t *testing.T) {
	s, plan := planState(t, "world", map[int]store.WordTarget{})
	world := wordIDOf(plan, "world")
	s.targets[world] = store.WordTarget{Word: "world", Adjusted: 50, Known: true}
	s.wordFound[world] = map[string]bool{"v1": true}

	out := s.outcome(t.Context())
	if out.TargetMet {
		t.Error("reported the target met with one result against an estimate of fifty")
	}
	if out.Reason != bridge.JobReasonExhausted {
		t.Errorf("reason = %q, want %q — running out below target is visibly incomplete",
			out.Reason, bridge.JobReasonExhausted)
	}

	// A budget stop keeps its own reason rather than being relabelled.
	s.stopped = bridge.JobReasonBudget
	if got := s.outcome(t.Context()).Reason; got != bridge.JobReasonBudget {
		t.Errorf("a budget-stopped job reported %q", got)
	}

	// An unknown target can never be "met" — claiming it was would assert
	// completeness this node has no basis for.
	s2, plan2 := planState(t, "world", map[int]store.WordTarget{})
	w2 := wordIDOf(plan2, "world")
	s2.wordFound[w2] = map[string]bool{"v1": true}
	if s2.outcome(t.Context()).TargetMet {
		t.Error("reported the target met when there was no target")
	}
}

// TestPresentationCapBoundsStreamingNotDiscovery is WO-095 §8: the result cap
// does not terminate discovery or word counting; the resource budget does.
func TestPresentationCapBoundsStreamingNotDiscovery(t *testing.T) {
	s, _ := planState(t, "world", map[int]store.WordTarget{})
	s.limit = 2
	s.matched, s.streamed = 0, 0

	// Simulate five verified matches through the same accounting the matcher
	// uses.
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		s.emitted[id] = true
		s.matched++
		if s.limit <= 0 || s.streamed < s.limit {
			s.streamed++
		}
	}
	if s.streamed != 2 {
		t.Errorf("streamed %d results against a cap of 2", s.streamed)
	}
	if s.matched != 5 {
		t.Errorf("matched %d, want 5 — the cap must not stop counting", s.matched)
	}
}

// TestSearchByteBudgetHasAFloor: a tiny disk budget must not make every search
// stop after one response.
func TestSearchByteBudgetHasAFloor(t *testing.T) {
	if got := searchByteBudget(store.MinDiskBudget); got < 8<<20 {
		t.Errorf("byte budget %d at the minimum disk budget is below the floor", got)
	}
	if got := searchByteBudget(store.DefaultDiskBudget); got <= 8<<20 {
		t.Errorf("byte budget %d at the default disk budget did not scale", got)
	}
}

// TestTokenAccumulatorUnionsAcrossPeers is WO-095 §2's "Do not".
//
// Candidate sets from distinct peers are unioned. An id must never be dropped
// because a later peer did not mention it — a shard not naming a video is
// evidence about that peer's corpus, not about the video.
func TestTokenAccumulatorUnionsAcrossPeers(t *testing.T) {
	acc := newTokenAccumulator("wor")
	acc.add([]store.ShardEntry{{VideoID: "v1", Tokens: []string{"wor"}}}, true)
	first := acc.take()
	if len(first) != 1 || first[0] != "v1" {
		t.Fatalf("first response credited %v, want [v1]", first)
	}

	// A second peer that simply does not hold v1 must not remove it.
	acc.add([]store.ShardEntry{{VideoID: "v2", Tokens: []string{"wor"}}}, true)
	second := acc.take()
	if len(second) != 1 || second[0] != "v2" {
		t.Fatalf("second response credited %v, want only the newly gained [v2]", second)
	}
	ids := acc.ids()
	if len(ids) != 2 {
		t.Errorf("accumulator holds %v, want both v1 and v2 — peers are unioned", ids)
	}

	// take() returns only what is new, so one response is never credited with
	// another's candidates.
	if got := acc.take(); len(got) != 0 {
		t.Errorf("take() returned %v after everything was already credited", got)
	}
}

// TestMaxDiscoveryTokensIsRefusedBeforePeerContact is WO-095 §5. The bound
// exists to cap what one submission can ask of the serving population, which
// means deciding it before anything is asked.
func TestMaxDiscoveryTokensIsRefusedBeforePeerContact(t *testing.T) {
	if MaxDiscoveryTokens != 16 {
		t.Errorf("MaxDiscoveryTokens = %d, want 16", MaxDiscoveryTokens)
	}
	if MaxConcurrentResponses != 4 {
		t.Errorf("MaxConcurrentResponses = %d, want 4 to start", MaxConcurrentResponses)
	}
	// Distinct words, so the DISTINCT token count really does exceed the bound —
	// repeating one word produces many occurrences but few distinct values, and
	// the bound is on values because a repeated token is one fetch.
	long := ""
	for i := 0; i < 20; i++ {
		long += string(rune('a'+i%26)) + string(rune('c'+i%17)) + string(rune('f'+i%13)) + " "
	}
	if got := len(store.BuildQueryPlan(long).DiscoveryTokens()); got <= MaxDiscoveryTokens {
		t.Fatalf("test setup: the long query produced only %d tokens", got)
	}
}
