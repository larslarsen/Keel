// SPDX-License-Identifier: Apache-2.0
// Parallel planning, peer selection and saturation for a streaming
// distributed search (WO-095 §5–§8).
//
// # What this replaces, and why parallelism alone would not have been enough
//
// PeerSearch walked its tokens one at a time under a single six-second
// deadline covering the whole query. On the live owner, `world` spent that
// budget inside `wor`'s provider walk and `ld ` never started — it reported
// zero rows, and nothing distinguished "this token found nothing" from "this
// token never ran". The daemon then sent one reply and the page caught the
// failure silently.
//
// Running the tokens concurrently fixes the starvation and would still leave
// the product wrong: one atomic reply makes every bar, count and result appear
// at the end, so a search that takes twenty seconds looks identical to a search
// that took none until it suddenly doesn't. So this file owns the *work* and
// reports it as it happens, through a SearchEvents sink the daemon turns into
// protocol frames. Nothing here formats an envelope, and nothing here knows
// what a session is.
//
// # The three rules that shape the loop
//
//  1. **Every response gets its own deadline.** Never one deadline over the
//     query, and no token inherits time another token already spent. The
//     aggregate backstop is a byte budget, not a clock, because a clock is what
//     produced the original starvation.
//
//  2. **Candidate sets are unioned, never intersected.** A shard not mentioning
//     a video is not evidence the video fails the query — it is evidence that
//     peer's corpus does not hold it in that shard. Discovery nominates; the
//     local full-query matcher decides. Intersecting would silently drop every
//     title whose other tokens happen to live on peers nobody asked.
//
//  3. **A response is not finished until its candidates are resolved and
//     checked.** Saturation measured before string resolution would read
//     "titles still in flight" as "this peer gained nothing", and stop a search
//     that was about to produce results.
package swarm

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

const (
	// MaxDiscoveryTokens bounds how many distinct tokens one search may take to
	// the network. Refused *before* peer contact (WO-095 §5): a query long
	// enough to exceed this is a query whose bars nobody could read anyway, and
	// the point of the limit is to cap what one submission can ask of the
	// serving population, which means deciding it before anything is asked.
	MaxDiscoveryTokens = 16

	// MaxConcurrentResponses bounds logical peer responses in flight across the
	// whole job. Named rather than inlined so later measurement can tune it —
	// four is the starting point WO-095 §5 specifies, not a measured optimum.
	MaxConcurrentResponses = 4

	// searchResponseDeadline bounds ONE logical peer response. Per response,
	// never per query: a token that starts late gets the same budget as one
	// that started first, which is the whole repair.
	searchResponseDeadline = 20 * time.Second

	// searchSaturationStreak is how many consecutive gainless responses count
	// as saturation for a word. Same value FetchShard has used since WO-067.
	searchSaturationStreak = 3

	// maxProvidersPerToken bounds the DHT walk for one token.
	maxProvidersPerToken = 20
)

// SearchEvents is what the orchestrator reports as work happens.
//
// An interface rather than a channel because the three signals are genuinely
// different shapes with different delivery rules — token phases coalesce,
// results must not — and because the daemon needs to attach a session and a
// sequence number to each, which is not this package's business.
//
// Implementations must be safe to call from several goroutines: token work
// runs concurrently by design.
type SearchEvents interface {
	// TokenPhase reports one token bar's state. cycle counts logical peer
	// responses for that token, so a reset is distinguishable from a redraw.
	TokenPhase(tokenID, cycle int, phase, reason string)
	// WordProgress reports a word's confirmed distinct-candidate count.
	WordProgress(wordID, found int)
	// Result reports one candidate whose resolved title satisfies the COMPLETE
	// query. Definitive: never speculative, never retracted.
	Result(hit bridge.SearchHit)
}

// SearchOutcome is how a job ended.
type SearchOutcome struct {
	Reason    string
	Results   int
	TargetMet bool
}

// searchByteBudget derives one job's hard aggregate ceiling from the user's
// disk/network budget (WO-095 §5).
//
// A fraction rather than the whole thing: the budget is what the user devoted
// to Keel's evictable cache, and one search consuming all of it in a single
// query is already pathological. An eighth leaves a large search comfortable
// room while keeping a runaway one bounded. Deliberately NOT a wall-clock
// cutoff — WO-095 forbids a fixed whole-query timeout, because that is exactly
// the mechanism that starved `ld ` in the first place.
func searchByteBudget(diskBudget int64) int64 {
	b := diskBudget / 8
	const floor = 8 << 20
	if b < floor {
		b = floor
	}
	return b
}

// StreamingSearch runs one distributed search, reporting progress as it goes.
//
// Blocks until the work concludes or ctx is cancelled; the caller owns the
// goroutine and the cancellation. Returns how the job ended so the caller can
// emit the right terminal frame — an exhausted or budget-stopped search is a
// visibly incomplete success, never an empty one.
func (n *Node) StreamingSearch(
	ctx context.Context,
	plan store.QueryPlan,
	targets map[int]store.WordTarget,
	limit int,
	ev SearchEvents,
) (SearchOutcome, error) {
	if !n.MayDistributedSearch() {
		return SearchOutcome{}, ErrDistributedSearchNotPermitted
	}
	tokens := plan.DiscoveryTokens()
	if len(tokens) == 0 {
		// A stopword-only query (WO-097 §3). Not a failure and not an empty
		// network — there is simply no distributed work this query implies, and
		// the interface says so rather than showing bars that never move.
		return SearchOutcome{Reason: bridge.JobReasonLocalOnly, TargetMet: true}, nil
	}
	if len(tokens) > MaxDiscoveryTokens {
		return SearchOutcome{}, fmt.Errorf(
			"a search may take at most %d distinct tokens to the network; this query has %d",
			MaxDiscoveryTokens, len(tokens))
	}
	if n.Peers() == 0 {
		return SearchOutcome{Reason: bridge.JobReasonNoPeers}, nil
	}

	st := newSearchState(n, plan, targets, limit, ev)
	sem := make(chan struct{}, MaxConcurrentResponses)
	var wg sync.WaitGroup
	for _, tok := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			n.runTokenWork(ctx, st, token, sem)
		}(tok)
	}
	wg.Wait()
	return st.outcome(ctx), nil
}

// searchState is everything the concurrent token workers share.
type searchState struct {
	n       *Node
	plan    store.QueryPlan
	ev      SearchEvents
	targets map[int]store.WordTarget

	// tokenIDs maps a token value to its render-plan id, so an event names the
	// bar the interface already drew rather than anything about the token.
	tokenIDs map[string]int
	// wordsFor is the non-stopword words each token can advance. Stopwords are
	// excluded: they have no bar and no target (WO-095 §7), so letting one gate
	// the stop condition would be invisible to the person watching.
	wordsFor map[string][]int

	mu sync.Mutex
	// wordFound is the numerator: distinct candidate ids whose resolved title
	// locally confirms the word. A set, so one video counts once however often
	// its title repeats the word.
	wordFound map[int]map[string]bool
	wordMiss  map[int]int
	// checked is every candidate already resolved and matched, so a video
	// nominated by three tokens is resolved once.
	checked map[string]bool
	// emitted guards against streaming one video twice.
	emitted map[string]bool
	// matched is every verified full-query result; streamed is how many of
	// them actually went out. They differ once the presentation cap is
	// reached, which bounds display and nothing else (WO-095 §8).
	matched  int
	streamed int
	limit    int
	// usedPeers backs the diversity preference. Not a correctness requirement:
	// broad token-prefix hashing is what supplies privacy, and one peer
	// answering several tokens is allowed when alternatives are insufficient.
	usedPeers map[peer.ID]bool
	bytes     int64
	budget    int64
	stopped   string
}

func newSearchState(n *Node, plan store.QueryPlan, targets map[int]store.WordTarget, limit int, ev SearchEvents) *searchState {
	s := &searchState{
		limit:     limit,
		n:         n,
		plan:      plan,
		ev:        ev,
		targets:   targets,
		tokenIDs:  map[string]int{},
		wordsFor:  map[string][]int{},
		wordFound: map[int]map[string]bool{},
		wordMiss:  map[int]int{},
		checked:   map[string]bool{},
		emitted:   map[string]bool{},
		usedPeers: map[peer.ID]bool{},
		budget:    searchByteBudget(n.st.DiskBudget()),
	}
	for _, t := range plan.Tokens {
		if t.Discovery {
			s.tokenIDs[t.Token] = t.TokenID
		}
	}
	stopword := map[int]bool{}
	for _, w := range plan.WordValues() {
		stopword[w.WordID] = w.Stopword
	}
	for token, ids := range plan.WordsAdvancedByToken() {
		keep := make([]int, 0, len(ids))
		for _, id := range ids {
			if !stopword[id] {
				keep = append(keep, id)
			}
		}
		s.wordsFor[token] = keep
	}
	return s
}

// runTokenWork drives one token's peer walk to completion.
//
// One goroutine per token, so no token waits on another's provider lookup.
// Concurrency is bounded on *responses* rather than on tokens: sixteen tokens
// may be in their DHT walks at once while only four downloads are in flight,
// which is the shape that keeps a slow peer from idling the other fifteen.
func (n *Node) runTokenWork(ctx context.Context, st *searchState, token string, sem chan struct{}) {
	tokenID := st.tokenIDs[token]
	st.ev.TokenPhase(tokenID, 0, bridge.PhaseQueued, "")

	shard := store.ShardOf(token)
	acc := newTokenAccumulator(token)
	cycle := 0
	reason := bridge.JobReasonComplete

	defer func() {
		// Every real search feeds WO-067's drift scheduling, however it ended,
		// unless nothing was found and nothing was expected — that carries no
		// signal worth recording.
		ids := acc.ids()
		if _, haveTarget := n.st.TokenEstimate(token); len(ids) > 0 || haveTarget {
			if err := n.st.RecordTokenSearch(token, ids); err != nil {
				n.logf("search: record token search: %v", err)
			}
		}
		st.ev.TokenPhase(tokenID, cycle, bridge.PhaseDone, reason)
	}()

	providers, err := n.shardProviderList(ctx, token, shard)
	if err != nil || len(providers) == 0 {
		reason = bridge.JobReasonNoPeers
		return
	}

	for _, p := range st.orderPeers(providers) {
		if ctx.Err() != nil {
			reason = bridge.JobReasonComplete
			st.ev.TokenPhase(tokenID, cycle, bridge.PhaseCancelled, "")
			return
		}
		if st.tokenSatisfied(token) {
			// Every word this token could advance is done. Work useful only to
			// a finished word stops; a token relevant to two words stays
			// eligible while either is incomplete (WO-095 §8).
			reason = bridge.JobReasonSaturated
			return
		}
		if st.overBudget() {
			reason = bridge.JobReasonBudget
			return
		}
		if !n.MayDistributedSearch() {
			// A contribution downgrade mid-search (WO-095 §4). Checked in the
			// loop rather than only at the start, because the gate shuts before
			// the node is torn down and a job started at Level 2 must not keep
			// reaching peers after the user has said stop.
			reason = bridge.JobReasonCancelled
			st.ev.TokenPhase(tokenID, cycle, bridge.PhaseCancelled, "contribution_downgrade")
			return
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			st.ev.TokenPhase(tokenID, cycle, bridge.PhaseCancelled, "")
			return
		}

		cycle++
		st.claimPeer(p.ID)
		st.ev.TokenPhase(tokenID, cycle, bridge.PhaseActive, "")

		// This response's own deadline. Nothing here inherits elapsed time from
		// another token, and there is no clock over the query as a whole.
		rctx, cancel := context.WithTimeout(ctx, searchResponseDeadline)
		entries, signed, complete, nbytes, err := n.fetchShardPagesCounted(rctx, p, shard)
		cancel()
		<-sem

		st.addBytes(int64(nbytes))
		if err != nil {
			// Another peer may still answer for this token, which starts a new
			// cycle and resets the bar. A failed response is visible rather
			// than silently skipped.
			st.ev.TokenPhase(tokenID, cycle, bridge.PhaseFailed, "unavailable")
			continue
		}

		acc.add(entries, signed)

		// Resolve and check BEFORE the cycle completes. A token-shard response
		// is not zero-gain merely because its strings are still in flight
		// (WO-095 §8), so the response is not finished until they are not.
		gained := st.creditResponse(ctx, token, acc.take())

		terminal := ""
		if !complete {
			terminal = "incomplete"
		}
		st.ev.TokenPhase(tokenID, cycle, bridge.PhaseComplete, terminal)
		st.recordSaturation(token, gained)
	}
	// The provider list ran out. Whether that is "complete" or "exhausted"
	// depends on whether the words this token feeds actually reached their
	// targets — a search that ran out of peers below target is visibly
	// incomplete, never an empty success.
	if st.tokenSatisfied(token) {
		reason = bridge.JobReasonSaturated
	} else {
		reason = bridge.JobReasonExhausted
	}
}

// shardProviderList collects eligible providers for one token.
//
// Yield screening (WO-067) still applies and still only ever removes peers
// there is positive evidence against: a peer that has gossiped nothing is
// unknown, and unknown is tried.
func (n *Node) shardProviderList(ctx context.Context, token string, shard int) ([]peer.AddrInfo, error) {
	c, err := shardCID(shard)
	if err != nil {
		return nil, err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, searchResponseDeadline)
	defer cancel()

	out := make([]peer.AddrInfo, 0, maxProvidersPerToken)
	for p := range n.dht.FindProvidersAsync(lookupCtx, c, maxProvidersPerToken) {
		if p.ID == n.host.ID() || len(p.Addrs) == 0 {
			continue
		}
		if yield, known := n.yieldGet(p.ID, token); known && !yield {
			continue
		}
		out = append(out, p)
		if len(out) >= maxProvidersPerToken {
			break
		}
	}
	return out, nil
}

// orderPeers shuffles the eligible set and puts peers no other token has used
// first (WO-095 §5).
//
// Shuffled rather than sorted by yield: always choosing the largest-yield peer
// concentrates every search in the swarm on the same few nodes, which is a load
// problem and a correlation one. The unused-first pass is a preference, not a
// rule — a token whose only providers are already busy still asks them, because
// peer diversity is load spreading and lack of a second peer must never break
// correctness.
func (s *searchState) orderPeers(in []peer.AddrInfo) []peer.AddrInfo {
	shuffled := append([]peer.AddrInfo(nil), in...)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	s.mu.Lock()
	fresh := make([]peer.AddrInfo, 0, len(shuffled))
	reused := make([]peer.AddrInfo, 0, len(shuffled))
	for _, p := range shuffled {
		if s.usedPeers[p.ID] {
			reused = append(reused, p)
		} else {
			fresh = append(fresh, p)
		}
	}
	s.mu.Unlock()
	return append(fresh, reused...)
}

func (s *searchState) claimPeer(id peer.ID) {
	s.mu.Lock()
	s.usedPeers[id] = true
	s.mu.Unlock()
}

func (s *searchState) addBytes(n int64) {
	s.mu.Lock()
	s.bytes += n
	s.mu.Unlock()
}

func (s *searchState) overBudget() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bytes >= s.budget {
		s.stopped = bridge.JobReasonBudget
		return true
	}
	return false
}

// creditResponse resolves the candidates one response newly produced, checks
// them against the complete query, and reports what changed.
//
// Returns the per-word gain so the caller can update saturation — and it
// returns it only after every title is resolved and checked, which is what
// makes "saturated" mean "this peer really added nothing" rather than "this
// peer's titles have not arrived yet".
func (s *searchState) creditResponse(ctx context.Context, token string, candidates []string) map[int]int {
	gained := map[int]int{}
	for _, id := range s.wordsFor[token] {
		gained[id] = 0
	}

	s.mu.Lock()
	fresh := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if id == "" || s.checked[id] {
			continue
		}
		s.checked[id] = true
		fresh = append(fresh, id)
	}
	s.mu.Unlock()
	if len(fresh) == 0 {
		return gained
	}

	// Whole broad string prefixes, coalesced (WO-097 §6). Cover rows may be
	// cached under the public-catalogue rules; only the ids this search
	// produced go any further, which is why the matcher below iterates `fresh`
	// and not whatever arrived.
	if _, err := s.n.ResolveCandidateTitles(ctx, fresh); err != nil && ctx.Err() == nil {
		s.n.logf("search: resolving candidate strings: %v", err)
	}
	hits, err := s.n.st.TitlesFor(fresh)
	if err != nil {
		s.n.logf("search: reading resolved titles: %v", err)
		return gained
	}

	touched := map[int]bool{}
	var results []bridge.SearchHit

	s.mu.Lock()
	for _, h := range hits {
		if h.Title == "" {
			// Unresolved: no title means nothing can be confirmed about it. It
			// is not a match and it is not a miss for any word — it is simply
			// not evidence either way.
			continue
		}
		for _, wordID := range s.plan.WordIDsInTitle(h.Title) {
			if !s.tracksWord(wordID) {
				continue
			}
			if s.wordFound[wordID] == nil {
				s.wordFound[wordID] = map[string]bool{}
			}
			if s.wordFound[wordID][h.VideoID] {
				continue
			}
			s.wordFound[wordID][h.VideoID] = true
			gained[wordID]++
			touched[wordID] = true
		}
		if s.plan.MatchTitle(h.Title) && !s.emitted[h.VideoID] {
			s.emitted[h.VideoID] = true
			s.matched++
			// The presentation cap bounds what is STREAMED, never what is
			// discovered or counted (WO-095 §8): a page showing a hundred rows
			// does not mean the word bars should stop moving, and the resource
			// budget is what ends the work.
			if s.limit <= 0 || s.streamed < s.limit {
				s.streamed++
				results = append(results, h)
			}
		}
	}
	// One update per word per response, carrying the final count, rather than
	// one per candidate: a single response can confirm hundreds of candidates,
	// and a bar cannot render hundreds of frames anyway. The resolution of one
	// response IS the unit "immediately after each title is resolved and
	// checked" (WO-095 §7) describes — nothing is deferred past it.
	updates := make(map[int]int, len(touched))
	for wordID := range touched {
		updates[wordID] = len(s.wordFound[wordID])
	}
	s.mu.Unlock()

	// Emitted outside the lock: a slow consumer must not block other tokens'
	// workers, and a result is definitive the moment the matcher proved it.
	for wordID, found := range updates {
		s.ev.WordProgress(wordID, found)
	}
	for _, h := range results {
		s.ev.Result(h)
	}
	return gained
}

// tracksWord reports whether a word gets a bar and a stop condition at all.
// Stopwords do not (WO-095 §7): they are required by the matcher and invisible
// in the interface, so counting them would drive a stop nobody could see.
func (s *searchState) tracksWord(wordID int) bool {
	for _, w := range s.plan.WordValues() {
		if w.WordID == wordID {
			return !w.Stopword
		}
	}
	return false
}

// recordSaturation folds one completed response into the saturation streak of
// every word the token could have advanced.
func (s *searchState) recordSaturation(token string, gained map[int]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, wordID := range s.wordsFor[token] {
		if gained[wordID] > 0 {
			s.wordMiss[wordID] = 0
			continue
		}
		s.wordMiss[wordID]++
	}
}

// wordDone is WO-095 §8's stop matrix for one word, and every clause matters:
//
//   - below target and saturated: NOT done. "Counts almost never decrease"
//     (WO-059's own design), so three quiet peers more likely means bad luck in
//     who was tried than an empty rest-of-network.
//   - at or above target while valid new matches still arrive: NOT done. The
//     target is an estimate from a sketch, not a ceiling.
//   - at or above target and then saturated: done.
//
// With no known target the bounded saturation fallback is all there is, which
// is the pre-WO-097 behaviour and the honest one: a node that cannot estimate
// the corpus cannot say how much of it it has seen.
func (s *searchState) wordDone(wordID int) bool {
	saturated := s.wordMiss[wordID] >= searchSaturationStreak
	if !saturated {
		return false
	}
	t, ok := s.targets[wordID]
	if !ok || !t.Known {
		return true
	}
	return uint64(len(s.wordFound[wordID])) >= t.Adjusted
}

// tokenSatisfied reports whether every word this token could advance is done.
//
// A boundary token covering two words stays eligible while either is
// incomplete — stopping it early would strand the incomplete word for the sake
// of the finished one.
func (s *searchState) tokenSatisfied(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	words := s.wordsFor[token]
	if len(words) == 0 {
		return false
	}
	for _, wordID := range words {
		if !s.wordDone(wordID) {
			return false
		}
	}
	return true
}

func (s *searchState) outcome(ctx context.Context) SearchOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SearchOutcome{Results: s.streamed, TargetMet: true, Reason: bridge.JobReasonComplete}
	if s.stopped != "" {
		out.Reason = s.stopped
	}
	for _, w := range s.plan.WordValues() {
		if w.Stopword {
			continue
		}
		t, ok := s.targets[w.WordID]
		if !ok || !t.Known {
			// An unknown target cannot be "met", and reporting it as met would
			// claim completeness this node has no basis for.
			out.TargetMet = false
			continue
		}
		if uint64(len(s.wordFound[w.WordID])) < t.Adjusted {
			out.TargetMet = false
			if out.Reason == bridge.JobReasonComplete {
				out.Reason = bridge.JobReasonExhausted
			}
		}
	}
	if ctx.Err() != nil && s.stopped == "" {
		out.Reason = bridge.JobReasonComplete
	}
	return out
}

// tokenAccumulator folds successive peer responses for ONE token, preserving
// WO-067's cross-peer poison resolution.
//
// A video's tag set is a deterministic function of its title, so two peers
// claiming to hold the same video in the same shard must agree about whether it
// carries the token. The disagreement rules live in resolveShardEntries; this
// type is the per-token state they need, lifted out of FetchShard's loop so the
// orchestrator can drive the loop instead.
type tokenAccumulator struct {
	token    string
	claims   map[string]shardClaim
	poisoned map[string]bool
	out      map[string][]string
	// pending is what has arrived since the last take(), so a response is
	// credited with exactly the candidates it newly produced.
	pending []string
}

func newTokenAccumulator(token string) *tokenAccumulator {
	return &tokenAccumulator{
		token:    token,
		claims:   map[string]shardClaim{},
		poisoned: map[string]bool{},
		out:      map[string][]string{},
	}
}

func (a *tokenAccumulator) add(entries []store.ShardEntry, signed bool) int {
	before := make(map[string]bool, len(a.out))
	for id := range a.out {
		before[id] = true
	}
	gained := resolveShardEntries(entries, a.token, signed, a.claims, a.poisoned, a.out)
	for id := range a.out {
		if !before[id] {
			a.pending = append(a.pending, id)
		}
	}
	return gained
}

// take returns and clears the candidates added since the last call.
func (a *tokenAccumulator) take() []string {
	out := a.pending
	a.pending = nil
	return out
}

func (a *tokenAccumulator) ids() []string {
	out := make([]string, 0, len(a.out))
	for id := range a.out {
		out = append(out, id)
	}
	return out
}
