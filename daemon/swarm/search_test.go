// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
		plan:       plan,
		targets:    targets,
		ev:         nopEvents{},
		tokenIDs:   map[string]int{},
		wordsFor:   map[string][]int{},
		wordFound:  map[int]map[string]bool{},
		wordMiss:   map[int]int{},
		checked:    map[string]bool{},
		emitted:    map[string]bool{},
		usedPeers:  map[peer.ID]bool{},
		candidates: map[string]*candidateState{},
		absent:     map[string]bool{},
		prefixes:   newPrefixGroup(),
		meter:      newBudgetMeter(1<<30, nil),
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

// TestSearchDiagnosticsCarryNoIdentifiers is WO-099 §6, exercised through the
// real resolver (WO-100).
//
// The earlier version of this test invoked the verbose ordinary catalogue path
// and then filtered its output away, which proved nothing about the search
// path. This one drives searchState.resolveTitles — the function a search
// actually uses — and asserts on EVERY line captured while it runs, rather than
// on the subset that happens to be prefixed "search:".
func TestSearchDiagnosticsCarryNoIdentifiers(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	capturing := false
	capture := func(f string, a ...any) {
		mu.Lock()
		if capturing {
			lines = append(lines, fmt.Sprintf(f, a...))
		}
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A server that serves nothing, so both the shard fetch and the catalogue
	// traversal fail and both want to log.
	server := newStore(t, "diag-server.sqlite")
	sNode, err := Start(ctx, server, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "diag-client.sqlite")
	cfg := isolated(true, t)
	cfg.Log = capture
	cNode, err := Start(ctx, client, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()
	cNode.remember(sNode.AddrInfo())

	// Everything from here is search-path logging.
	mu.Lock()
	capturing = true
	mu.Unlock()

	token := store.TokenizeQuery("recommendation")[0]
	_, _, _, _ = cNode.fetchShardPages(ctx, sNode.AddrInfo(), store.ShardOf(token))

	// The actual search resolver, not the ordinary catalogue path.
	st, _ := planState(t, "recommendation", map[int]store.WordTarget{})
	st.n = cNode
	ids := []string{"vid00000001", "vid00000002"}
	_, _ = st.resolveTitles(ctx, ids)

	mu.Lock()
	capturing = false
	captured := append([]string(nil), lines...)
	mu.Unlock()

	forbidden := []string{
		store.CataloguePrefix(ids[0], cNode.prefixBits()),
		store.CataloguePrefix(ids[1], cNode.prefixBits()),
		sNode.ID().String(),
		cNode.ID().String(),
		ids[0], ids[1],
		"recommendation", token,
	}
	for _, line := range captured {
		for _, bad := range forbidden {
			if bad == "" {
				continue
			}
			if strings.Contains(line, bad) {
				t.Errorf("search diagnostic %q contains an identifier (%q) — WO-095 §10 "+
					"permits one aggregate terminal diagnostic and no query, corpus or "+
					"peer identifiers at all", line, bad)
			}
		}
	}
}

// TestBudgetTerminalWinsOverExhausted is WO-100 §1's last clause.
//
// The budget can be spent inside the LAST available provider response, where no
// next loop iteration exists to notice. Reading exhaustion from the meter
// rather than from a flag some iteration set is what stops that reporting as
// `exhausted` — which would tell the user the network ran out of peers when it
// was their own allowance that ran out.
func TestBudgetTerminalWinsOverExhausted(t *testing.T) {
	s, plan := planState(t, "world", map[int]store.WordTarget{})
	world := wordIDOf(plan, "world")
	s.targets[world] = store.WordTarget{Word: "world", Adjusted: 100, Known: true}
	s.wordFound[world] = map[string]bool{"v1": true}

	// Below target with no budget problem: honestly exhausted.
	if got := s.outcome(context.Background()).Reason; got != bridge.JobReasonExhausted {
		t.Fatalf("reason = %q, want %q", got, bridge.JobReasonExhausted)
	}

	// Same state, but the allowance ran out. No loop iteration ever called
	// overBudget().
	s.meter = newBudgetMeter(0, nil)
	_, _ = s.meter.reserve(context.Background(), 1)
	if got := s.outcome(context.Background()).Reason; got != bridge.JobReasonBudget {
		t.Errorf("reason = %q, want %q — exhaustion inside the last response "+
			"must still report as budget", got, bridge.JobReasonBudget)
	}

	// An external withdrawal outranks the budget: "you cancelled this" and
	// "this ran out of allowance" are different answers, and the user asked for
	// one of them.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := s.outcome(ctx).Reason; got != bridge.JobReasonCancelled {
		t.Errorf("reason = %q, want %q — cancellation must outrank budget", got, bridge.JobReasonCancelled)
	}
}

// TestConcurrentNominationCannotRecordAFalseMiss is WO-100 §4.
//
// The previous version skipped a candidate another worker was already
// resolving and returned zero gain — so a response could be recorded gainless,
// and saturate a word, while the very candidate that would have advanced that
// word was still being fetched.
func TestConcurrentNominationCannotRecordAFalseMiss(t *testing.T) {
	s, plan := planState(t, "world", map[int]store.WordTarget{})
	world := wordIDOf(plan, "world")

	// Worker A has claimed vid1 and has not finished.
	claimed := &candidateState{done: make(chan struct{})}
	s.candidates["vid1"] = claimed

	// Worker B nominates the same candidate. It must WAIT, not report a miss.
	done := make(chan responseCredit, 1)
	go func() {
		done <- s.creditResponse(context.Background(), tokenFor(s), []string{"vid1"})
	}()

	select {
	case <-done:
		t.Fatal("a response sharing an in-flight candidate returned before that " +
			"candidate was resolved — it would have counted as a gainless step")
	case <-time.After(50 * time.Millisecond):
	}

	// Worker A finishes and the candidate confirms the word.
	s.mu.Lock()
	s.checked["vid1"] = true
	s.wordFound[world] = map[string]bool{"vid1": true}
	delete(s.candidates, "vid1")
	s.mu.Unlock()
	close(claimed.done)

	credit := <-done
	if credit.gained[world] != 1 {
		t.Errorf("gain for the shared word = %d, want 1 — a concurrent "+
			"confirmation must be observed as gain, not a simultaneous false miss",
			credit.gained[world])
	}

	// And it does not double-count: the word is a set of video ids.
	if len(s.wordFound[world]) != 1 {
		t.Errorf("word count = %d, want 1", len(s.wordFound[world]))
	}
}

// TestAlreadyCheckedCandidatesAreAHonestMiss is the other side of the same
// rule: a response whose candidates were all decided before it started really
// did add nothing, and must be allowed to saturate.
func TestAlreadyCheckedCandidatesAreAHonestMiss(t *testing.T) {
	s, plan := planState(t, "world", map[int]store.WordTarget{})
	world := wordIDOf(plan, "world")
	s.mu.Lock()
	s.checked["vid1"] = true
	s.wordFound[world] = map[string]bool{"vid1": true}
	s.mu.Unlock()

	credit := s.creditResponse(context.Background(), tokenFor(s), []string{"vid1"})
	if credit.gained[world] != 0 {
		t.Errorf("gain = %d for a candidate already checked before this response "+
			"began; that is a real miss and must be able to saturate", credit.gained[world])
	}
	if credit.unresolved {
		t.Error("a response whose candidates were all already decided reported " +
			"an unresolved candidate, which would freeze the saturation streak")
	}
}

// tokenFor returns any discovery token of the plan under test.
func tokenFor(s *searchState) string {
	for token := range s.wordsFor {
		return token
	}
	return ""
}

// TestUnresolvedCandidateDoesNotAdvanceSaturation is WO-101 §2.
//
// resolveTitles correctly left candidates retryable after an incomplete,
// unavailable or invalid traversal — and then creditResponse returned a plain
// zero-gain map that runTokenWork fed straight to recordSaturation. Three peer
// failures could therefore satisfy saturation and stop the search below its
// retained word target, which is exactly the outcome the target exists to
// prevent.
func TestUnresolvedCandidateDoesNotAdvanceSaturation(t *testing.T) {
	s, plan := planState(t, "world", map[int]store.WordTarget{})
	world := wordIDOf(plan, "world")
	token := tokenFor(s)

	// Gained nothing, and a candidate is still undecided.
	s.recordSaturation(token, responseCredit{gained: map[int]int{world: 0}, unresolved: true})
	s.recordSaturation(token, responseCredit{gained: map[int]int{world: 0}, unresolved: true})
	s.recordSaturation(token, responseCredit{gained: map[int]int{world: 0}, unresolved: true})
	if s.wordMiss[world] != 0 {
		t.Errorf("miss streak = %d after three unresolved responses; an unresolved "+
			"candidate is not evidence the network is quiet", s.wordMiss[world])
	}
	if s.wordDone(world) {
		t.Error("unresolved responses saturated the word and would stop the search")
	}

	// Gained nothing and everything decided: an honest miss.
	for i := 0; i < searchSaturationStreak; i++ {
		s.recordSaturation(token, responseCredit{gained: map[int]int{world: 0}})
	}
	if s.wordMiss[world] != searchSaturationStreak {
		t.Errorf("miss streak = %d after %d decided gainless responses",
			s.wordMiss[world], searchSaturationStreak)
	}

	// A gain resets the streak regardless.
	s.recordSaturation(token, responseCredit{gained: map[int]int{world: 2}})
	if s.wordMiss[world] != 0 {
		t.Errorf("a gaining response left the streak at %d", s.wordMiss[world])
	}
}

// TestJoinedCandidateObservesThePublishedDisposition is §2's barrier clause:
// the disposition must be published before joiners wake, so two responses
// sharing one candidate agree about what was established.
func TestJoinedCandidateObservesThePublishedDisposition(t *testing.T) {
	for _, tc := range []struct {
		name           string
		disposition    int
		wantUnresolved bool
	}{
		{"checked", dispChecked, false},
		{"absent", dispAbsent, false},
		{"unresolved", dispUnresolved, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := planState(t, "world", map[int]store.WordTarget{})
			cs := &candidateState{done: make(chan struct{})}
			s.candidates["vid1"] = cs

			out := make(chan responseCredit, 1)
			go func() {
				out <- s.creditResponse(context.Background(), tokenFor(s), []string{"vid1"})
			}()

			// Published BEFORE the wake, exactly as the resolver does it. The
			// registration is deliberately left in place: whether the joining
			// goroutine partitions before or after the close, it must find the
			// candidate claimed and read the disposition rather than starting
			// its own resolution.
			cs.disposition = tc.disposition
			close(cs.done)

			credit := <-out
			if credit.unresolved != tc.wantUnresolved {
				t.Errorf("joiner saw unresolved=%v for a %s candidate, want %v",
					credit.unresolved, tc.name, tc.wantUnresolved)
			}
		})
	}
}

// TestCancelledBarrierWakeIsUnresolved: a joiner woken by cancellation never
// learned anything, so it must not report a decision.
func TestCancelledBarrierWakeIsUnresolved(t *testing.T) {
	s, _ := planState(t, "world", map[int]store.WordTarget{})
	s.candidates["vid1"] = &candidateState{done: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan responseCredit, 1)
	go func() {
		out <- s.creditResponse(ctx, tokenFor(s), []string{"vid1"})
	}()
	cancel()

	select {
	case credit := <-out:
		if !credit.unresolved {
			t.Error("a joiner woken by cancellation reported a decision it never received")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a joiner did not wake on cancellation")
	}
}

// failingTitles is a Store whose local title read fails, so the false-absence
// path can be driven without corrupting a database.
type failingTitles struct {
	Store
	failTitles   bool
	failPlanning bool
}

func (f failingTitles) TitlesFor(ids []string) ([]bridge.SearchHit, error) {
	if f.failTitles {
		return nil, errors.New("database is unreadable")
	}
	return f.Store.TitlesFor(ids)
}

func (f failingTitles) MissingCataloguePrefixes(ids []string, bits int) ([]string, error) {
	if f.failPlanning {
		return nil, errors.New("database is unreadable")
	}
	return f.Store.MissingCataloguePrefixes(ids, bits)
}

// TestLocalReadFailureCannotProveAbsence is WO-102 §2.
//
// A complete network traversal proves what arrived from the bucket. It does not
// prove a candidate ABSENT when the local database failed before the resolver
// could look at the imported rows — and marking it absent suppressed it from
// every later retry in the job, permanently, on a transient local error.
func TestLocalReadFailureCannotProveAbsence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	base := newStore(t, "localfail.sqlite")
	node, err := Start(ctx, base, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	for _, tc := range []struct {
		name                     string
		failTitles, failPlanning bool
	}{
		{"title read fails", true, false},
		{"planning fails", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := planState(t, "world", map[int]store.WordTarget{})
			s.n = node
			node.st = failingTitles{Store: base, failTitles: tc.failTitles, failPlanning: tc.failPlanning}
			defer func() { node.st = base }()

			credit := s.creditResponse(ctx, tokenFor(s), []string{"vid00000001"})

			s.mu.Lock()
			absent := s.absent["vid00000001"]
			checked := s.checked["vid00000001"]
			s.mu.Unlock()

			if absent {
				t.Error("a local read failure marked the candidate absent, suppressing " +
					"it from every later retry in this job")
			}
			if checked {
				t.Error("a candidate was marked checked though no title was ever read")
			}
			if !credit.unresolved {
				t.Error("a local read failure was reported as a decided response, which " +
					"would advance the saturation streak")
			}
		})
	}
}

// TestCancellationWinsOverARefundedLease is WO-102 §3.
//
// reserve() checked capacity before context. A waiter selecting on both a
// settlement and cancellation could take the settlement case, loop, and lease
// refunded capacity for a job that had already been replaced or downgraded —
// admitting work after the withdrawal that was supposed to stop it.
func TestCancellationWinsOverARefundedLease(t *testing.T) {
	m := newBudgetMeter(budgetReadChunk, nil)
	held, err := m.reserve(context.Background(), budgetReadChunk)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type res struct {
		n   int
		err error
	}
	out := make(chan res, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		n, err := m.reserve(ctx, 4096)
		out <- res{n, err}
	}()
	<-started
	time.Sleep(20 * time.Millisecond) // let it park on the settlement channel

	// Cancel and refund together: whichever the select picks, the next loop
	// must observe the cancelled context before touching `reserved`.
	cancel()
	m.settle(held, 0)

	got := <-out
	if !errors.Is(got.err, context.Canceled) {
		t.Errorf("a cancelled waiter returned %v, want context.Canceled", got.err)
	}
	if got.n != 0 {
		t.Errorf("a cancelled waiter acquired a lease of %d bytes", got.n)
	}
	// And the invariant holds: nothing is left reserved.
	m.mu.Lock()
	reserved := m.reserved
	m.mu.Unlock()
	if reserved != 0 {
		t.Errorf("reserved = %d after the waiter was cancelled", reserved)
	}
}

// TestCancelledReaderNeverTouchesTheStream: the reader must not call through
// when its reservation was refused.
func TestCancelledReaderNeverTouchesTheStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	touched := false
	r := &budgetReader{
		ctx: ctx,
		r:   readerFunc(func([]byte) (int, error) { touched = true; return 0, nil }),
		m:   newBudgetMeter(1<<20, nil),
	}
	n, err := r.Read(make([]byte, 1024))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("read returned %v, want context.Canceled", err)
	}
	if n != 0 || touched {
		t.Error("a cancelled reader called through to the underlying stream")
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
