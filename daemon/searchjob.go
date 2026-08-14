// SPDX-License-Identifier: Apache-2.0
// The asynchronous, session-scoped distributed-search job (WO-095 §3, §4).
//
// # Why the RPC stopped being a request/reply
//
// PEER_SEARCH used to hold a native request open while the daemon walked every
// token, then answer once. Two things followed from that shape, and neither was
// fixable inside it.
//
// The first is the failure this order came from: one deadline covered the whole
// query, so a slow first token could consume it and a later token never ran —
// and the single reply carried no way to say so. The second is a product
// problem rather than a bug. Even with the tokens running in parallel, one
// reply means every bar, every count and every result appears atomically at the
// end. A twenty-second search and an instant one look identical until the
// moment they don't.
//
// So PEER_SEARCH now only *starts* work. It validates, creates a job, and is
// acknowledged immediately with PEER_SEARCH_STARTED; the job reports itself
// through unsolicited events on the same session until it ends. The native
// request is not held open for the search, which also means the client's 8s
// request timeout no longer bounds how long a search may take.
//
// # Why events are scoped to the session that asked
//
// A job's events go to the session that started it and to no other. They are
// not published through the owner-wide contribution-status hub, which exists to
// tell every connected browser about a setting that changed — a search is one
// page's private activity, and broadcasting it would hand every other browser
// profile on the machine a live feed of what this one is looking for.
//
// Envelope ids carry bridge.EventIDPrefix so an event can never resolve a
// pending request. Without that, a job event could collide with a live
// request's correlation id and settle somebody else's promise with a payload of
// the wrong type.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

// maxSearchIDLen bounds the client-minted job id. It is a crypto.randomUUID()
// in practice; the bound exists because it is client input that gets echoed
// back on every event.
const maxSearchIDLen = 64

// searchJob is one running distributed search on one session.
type searchJob struct {
	id     string
	out    io.Writer
	cancel context.CancelFunc
	done   chan struct{}

	// mu serializes (assign seq, write envelope) as one step. The two cannot
	// be separated: a sequence number assigned under one lock and written under
	// another would let two goroutines emit 4 then 3, and the client's ordering
	// guard would then discard a live event as stale.
	mu  sync.Mutex
	seq uint64

	// streamed counts results actually sent, so a terminal frame can report
	// what the client should have received.
	streamed int
}

// emit assigns a sequence number and writes the event as one step.
//
// Both halves are under the same lock deliberately. The client discards any
// event whose sequence is not ahead of the last one it applied — that is what
// makes a replaced job's late events harmless — so an event that reaches the
// wire out of sequence order is not merely reordered, it is *dropped*. Two
// goroutines assigning under one lock and writing outside it would do exactly
// that under load, and the symptom would be a bar that silently stops moving.
//
// Nothing called here re-enters the job, so holding the lock across the write
// cannot deadlock; a slow consumer blocks this job's other emitters, which is
// the intended backpressure.
func (j *searchJob) emit(typ string, build func(seq uint64) any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	env, err := bridge.NewEnvelope(bridge.EventIDPrefix+j.id, typ, build(j.seq))
	if err != nil {
		return
	}
	if err := writeEnv(j.out, env); err != nil {
		log.Printf("search event write failed: %v", err)
	}
}

// jobEvents adapts swarm's orchestrator callbacks onto the wire protocol.
//
// Deliberately thin: the swarm package decides what happened, this decides how
// to say it, and neither knows about the other's constraints.
type jobEvents struct {
	job *searchJob
}

func (e *jobEvents) TokenPhase(tokenID, cycle int, phase, reason string) {
	e.job.emit("PEER_SEARCH_PROGRESS", func(seq uint64) any {
		return bridge.PeerSearchProgressPayload{
			SearchID: e.job.id, Seq: seq,
			TokenID: tokenID, Cycle: cycle, Phase: phase, Reason: reason,
		}
	})
}

func (e *jobEvents) WordProgress(wordID, found int) {
	e.job.emit("PEER_SEARCH_WORD_PROGRESS", func(seq uint64) any {
		return bridge.PeerSearchWordProgressPayload{
			SearchID: e.job.id, Seq: seq, WordID: wordID, Found: found,
		}
	})
}

func (e *jobEvents) Result(hit bridge.SearchHit) {
	e.job.mu.Lock()
	e.job.streamed++
	e.job.mu.Unlock()
	e.job.emit("PEER_SEARCH_RESULT", func(seq uint64) any {
		return bridge.PeerSearchStreamResultPayload{
			SearchID: e.job.id, Seq: seq, Hit: hit,
		}
	})
}

// startJob registers a job on the session, cancelling whatever it replaces.
//
// One active job per session is the contract (WO-095 §4: a page owns at most
// one). Replacement cancels rather than queues: the previous submission's
// results are no longer wanted, and letting two jobs run would double the peer
// load for a query nobody is looking at.
func (s *bridgeSession) startJob(id string, out io.Writer, cancel context.CancelFunc) *searchJob {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]*searchJob{}
	}
	for prevID, prev := range s.jobs {
		prev.cancel()
		delete(s.jobs, prevID)
	}
	j := &searchJob{id: id, out: out, cancel: cancel, done: make(chan struct{})}
	s.jobs[id] = j
	return j
}

// finishJob deregisters a job. Safe to call for a job already replaced.
func (s *bridgeSession) finishJob(id string) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	delete(s.jobs, id)
}

// cancelJob stops one job by id and reports whether it existed.
func (s *bridgeSession) cancelJob(id string) bool {
	s.jobMu.Lock()
	j, ok := s.jobs[id]
	if ok {
		delete(s.jobs, id)
	}
	s.jobMu.Unlock()
	if !ok {
		return false
	}
	j.cancel()
	return true
}

// cancelAllJobs stops every job on this session.
//
// Called when the session ends — a native disconnect, an owner shutdown, a
// browser going away. Work nobody can receive the results of must not continue
// to consume peers' serving budget (WO-095 §4).
func (s *bridgeSession) cancelAllJobs() {
	s.jobMu.Lock()
	jobs := make([]*searchJob, 0, len(s.jobs))
	for id, j := range s.jobs {
		jobs = append(jobs, j)
		delete(s.jobs, id)
	}
	s.jobMu.Unlock()
	for _, j := range jobs {
		j.cancel()
	}
}

// handlePeerSearchStart is the revision-3 entry point (WO-095 §3).
//
// Everything it validates, it validates before creating anything: bounds, the
// effective Level-2+ entitlement, the negotiated capability revision, the
// search id and the presentation limit. A refusal here is a normal reply to the
// request, not an event — the job does not exist yet, so there is nothing for
// an event to be about.
func handlePeerSearchStart(
	ctx context.Context, env *bridge.Envelope, out io.Writer, st *store.Store, sess *bridgeSession,
) error {
	var p bridge.SearchPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid PEER_SEARCH payload", Code: "bad_payload",
		})
	}
	if len(p.SearchID) > maxSearchIDLen {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "search_id too long", Code: "bad_payload",
		})
	}
	if networkBusy() {
		return replyNetworkBusy(out, env.ID)
	}
	if allowed, level := distributedSearchLevel(st); !allowed {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: contributionRequiredMessage,
			Code:    bridge.CodeContributionRequired,
			Detail: bridge.ContributionRequiredDetail{
				Capability:     bridge.CapDistributedSearch,
				RequiredLevel:  store.LevelBroad,
				EffectiveLevel: level,
			},
		})
	}

	plan := store.BuildQueryPlan(p.Query)
	if plan.Empty() {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "the query has nothing searchable in it", Code: "bad_payload",
		})
	}
	if got := len(plan.DiscoveryTokens()); got > swarm.MaxDiscoveryTokens {
		// Refused before peer contact, which is the point of the bound.
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: fmt.Sprintf(
				"that query needs %d network lookups; the limit is %d. Try fewer or shorter words.",
				got, swarm.MaxDiscoveryTokens),
			Code: "query_too_broad",
		})
	}

	// Frozen at start, from the retained snapshot, with no network I/O and no
	// wait for a refresh (WO-097 §7.4). A refresh landing mid-search affects
	// the NEXT search only — a denominator that moved under a bar already
	// filling would make the bar meaningless.
	targets := map[int]store.WordTarget{}
	wire := []bridge.WordTargetWire{}
	words := plan.WordValues()
	names := make([]string, 0, len(words))
	for _, w := range words {
		if !w.Stopword {
			names = append(names, w.Word)
		}
	}
	found, err := st.WordTargets(names)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	byWord := map[string]store.WordTarget{}
	for _, t := range found {
		byWord[t.Word] = t
	}
	for _, w := range words {
		if w.Stopword {
			continue
		}
		t := byWord[w.Word]
		targets[w.WordID] = t
		wire = append(wire, bridge.WordTargetWire{
			WordID: w.WordID, Word: w.Word,
			Target: t.Adjusted, Raw: t.Raw,
			Known: t.Known, Uncertain: t.Uncertain,
			SnapshotAgeMS: t.SnapshotAgeMS,
		})
	}

	n := currentSwarmNode()
	if n == nil {
		// No swarm running is a machine state, not an empty result. Answered as
		// a started-then-immediately-complete job so the page's state machine
		// has one shape to handle rather than two.
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "the peer network is not running", Code: "swarm_unavailable",
		})
	}

	jobCtx, cancel := context.WithCancel(ctx)
	job := sess.startJob(p.SearchID, out, cancel)

	if err := reply(out, env.ID, "PEER_SEARCH_STARTED", bridge.PeerSearchStartedPayload{
		SearchID: p.SearchID,
		Tokens:   len(plan.DiscoveryTokens()),
		Words:    wire,
	}); err != nil {
		cancel()
		sess.finishJob(p.SearchID)
		return err
	}

	go func() {
		defer close(job.done)
		defer cancel()
		defer sess.finishJob(p.SearchID)
		started := time.Now()

		outcome, err := n.StreamingSearch(jobCtx, plan, targets, p.Limit, &jobEvents{job: job})

		job.mu.Lock()
		streamed := job.streamed
		job.mu.Unlock()

		switch {
		case jobCtx.Err() != nil:
			job.emit("PEER_SEARCH_CANCELLED", func(seq uint64) any {
				return bridge.PeerSearchCancelledPayload{
					SearchID: job.id, Seq: seq, Results: streamed,
				}
			})
		case err != nil:
			// A typed job failure, never an unsolicited generic ERROR: an ERROR
			// with no matching request id is how a client learns the host is
			// dying, and a failed search must not read as that.
			job.emit("PEER_SEARCH_FAILED", func(seq uint64) any {
				return bridge.PeerSearchFailedPayload{
					SearchID: job.id, Seq: seq,
					Message: err.Error(), Code: "peer_search_failed",
					Results: streamed,
				}
			})
		default:
			job.emit("PEER_SEARCH_COMPLETE", func(seq uint64) any {
				return bridge.PeerSearchCompletePayload{
					SearchID: job.id, Seq: seq,
					Reason: outcome.Reason, Results: streamed,
					TargetMet: outcome.TargetMet,
					ElapsedMS: time.Since(started).Milliseconds(),
				}
			})
		}
		// The one aggregate diagnostic WO-095 §10 permits: search id, elapsed
		// milliseconds, counts and terminal state. No query, no token, no
		// title, no result id, no peer.
		log.Printf("peer search %s: %s in %dms, %d results",
			job.id, outcome.Reason, time.Since(started).Milliseconds(), streamed)
	}()
	return nil
}

// handlePeerSearchCancel stops a job by id.
//
// Answered the same way whether or not the job was still running: a page that
// cancels a search which just completed on its own has not made an error, and
// telling it so would be noise it cannot act on.
func handlePeerSearchCancel(env *bridge.Envelope, out io.Writer, sess *bridgeSession) error {
	var p bridge.PeerSearchCancelPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid PEER_SEARCH_CANCEL payload", Code: "bad_payload",
		})
	}
	sess.cancelJob(p.SearchID)
	return reply(out, env.ID, "PEER_SEARCH_CANCEL_RESULT", map[string]any{
		"search_id": p.SearchID,
	})
}

// planWire converts a query plan for the interface (WO-097 §1, WO-095 §1).
//
// The one field it deliberately does not copy is PlanToken.Token. A token's
// text is daemon-internal: the interface draws a token by slicing the
// normalized query it already has, using the character range, so sending the
// substring separately would put query content in an event payload for no
// rendering benefit (DESIGN_v2 §4.2).
func planWire(plan store.QueryPlan) *bridge.QueryPlanWire {
	if plan.Empty() {
		return nil
	}
	out := &bridge.QueryPlanWire{
		Normalized: plan.Normalized,
		Words:      make([]bridge.PlanWordWire, 0, len(plan.Words)),
		Tokens:     make([]bridge.PlanTokenWire, 0, len(plan.Tokens)),
	}
	for _, w := range plan.Words {
		out.Words = append(out.Words, bridge.PlanWordWire{
			WordID: w.WordID, Word: w.Word,
			Start: w.Start, End: w.End, Stopword: w.Stopword,
		})
	}
	for _, t := range plan.Tokens {
		tw := bridge.PlanTokenWire{
			TokenID: t.TokenID, ColorSlot: t.ColorSlot,
			Start: t.Start, End: t.End,
			Discovery: t.Discovery, BarWordID: t.BarWordID,
			Fragments: make([]bridge.PlanFragmentWire, 0, len(t.Fragments)),
		}
		for _, f := range t.Fragments {
			tw.Fragments = append(tw.Fragments, bridge.PlanFragmentWire{
				WordID: f.WordID, Start: f.Start, End: f.End,
			})
		}
		out.Tokens = append(out.Tokens, tw)
	}
	return out
}
