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
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

// maxActiveSearchJobs bounds how many searches this daemon runs at once, across
// every session and every page (WO-099 §1).
//
// A bound is needed because jobs are no longer one-per-session: several pages
// in one profile, and several profiles sharing one owner, can each hold a live
// search. What is NOT an acceptable bound is cancelling somebody else's search
// to make room — that is what the first implementation did, and it meant
// opening a search in one tab silently killed the one in another. So excess
// starts are refused, visibly, and the caller decides what to do about it.
const maxActiveSearchJobs = 8

// searchIDPattern is the canonical UUID shape crypto.randomUUID() produces.
//
// Revision 3 requires it (WO-099 §7). A length check alone accepted an empty
// id, whitespace, and anything else a modified client cared to send — and the
// id is echoed on every event and used as a routing key, so a malformed one is
// not merely untidy: it is a routing key nobody can claim.
var searchIDPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Cancellation reasons, bounded and machine-readable (WO-099 §3).
//
// The distinction they carry is the point: only the first four are
// cancellation. Budget exhaustion is a visibly incomplete *success* and a
// verification error is a *failure*, and collapsing any of them into the others
// tells the user something untrue about why their search stopped.
const (
	cancelReplaced  = "replaced"
	cancelSession   = "session_closed"
	cancelDowngrade = "contribution_downgrade"
	cancelConsent   = "consent_withdrawn"
	cancelShutdown  = "shutdown"
)

// errSearchBusy refuses a start rather than displacing a running search.
var errSearchBusy = errors.New("too many searches are already running")

// errDuplicateSearchID refuses a start that would silently take over another
// page's route (WO-099 §7).
var errDuplicateSearchID = errors.New("that search id is already running")

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

	// reasonMu guards the cancellation reason, which is written by whoever
	// stops the job and read by the goroutine that emits its terminal frame.
	reasonMu sync.Mutex
	reason   string
}

// stop cancels the job, recording why. First reason wins: a downgrade that
// races a page's own cancel should report the downgrade, because that is the
// one the user did not ask for and may need explaining.
func (j *searchJob) stop(reason string) {
	j.reasonMu.Lock()
	if j.reason == "" {
		j.reason = reason
	}
	j.reasonMu.Unlock()
	j.cancel()
}

func (j *searchJob) stopReason() string {
	j.reasonMu.Lock()
	defer j.reasonMu.Unlock()
	return j.reason
}

// liveSearches is every running job on this daemon, across every session.
//
// Session-scoped registration alone cannot serve a contribution downgrade: the
// gate shuts on the swarm supervisor, which knows nothing about bridge
// sessions, and it must be able to stop distributed-search work everywhere
// promptly rather than waiting for each worker to poll (WO-099 §3).
var liveSearches = &searchRegistry{jobs: map[*searchJob]struct{}{}}

type searchRegistry struct {
	mu   sync.Mutex
	jobs map[*searchJob]struct{}
}

// add registers a job, or reports that the daemon is already at its ceiling.
// Checked and inserted under one lock so two simultaneous starts cannot both
// see room for one more.
func (r *searchRegistry) add(j *searchJob) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.jobs) >= maxActiveSearchJobs {
		return false
	}
	r.jobs[j] = struct{}{}
	return true
}

func (r *searchRegistry) remove(j *searchJob) {
	r.mu.Lock()
	delete(r.jobs, j)
	r.mu.Unlock()
}

// stopAll cancels every running search. Called when the entitlement to run one
// goes away — a downgrade, a consent withdrawal, process shutdown.
func (r *searchRegistry) stopAll(reason string) {
	r.mu.Lock()
	jobs := make([]*searchJob, 0, len(r.jobs))
	for j := range r.jobs {
		jobs = append(jobs, j)
	}
	r.jobs = map[*searchJob]struct{}{}
	r.mu.Unlock()
	for _, j := range jobs {
		j.stop(reason)
	}
}

// stopDistributedSearches is the supervisor-facing entry point.
func stopDistributedSearches(reason string) { liveSearches.stopAll(reason) }

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

// startJob registers a job on the session without disturbing any other.
//
// It used to cancel every job in the session first, on the reading that a page
// owns at most one search. That reading was right about pages and wrong about
// sessions: every search page in one browser profile shares the service
// worker's single native connection, so "one job per session" meant opening a
// search in one tab silently cancelled the one in another (WO-099 §1).
//
// Replacement is a page decision, expressed by that page cancelling its own id
// before claiming another. What this enforces instead is a finite ceiling and
// no duplicate live ids.
func (s *bridgeSession) startJob(id string, out io.Writer, cancel context.CancelFunc) (*searchJob, error) {
	s.jobMu.Lock()
	if s.jobs == nil {
		s.jobs = map[string]*searchJob{}
	}
	if _, running := s.jobs[id]; running {
		s.jobMu.Unlock()
		return nil, errDuplicateSearchID
	}
	j := &searchJob{id: id, out: out, cancel: cancel, done: make(chan struct{})}
	s.jobs[id] = j
	s.jobMu.Unlock()

	if !liveSearches.add(j) {
		s.jobMu.Lock()
		delete(s.jobs, id)
		s.jobMu.Unlock()
		return nil, errSearchBusy
	}
	return j, nil
}

// finishJob deregisters a job. Safe to call for a job already replaced.
func (s *bridgeSession) finishJob(id string) {
	s.jobMu.Lock()
	j := s.jobs[id]
	delete(s.jobs, id)
	s.jobMu.Unlock()
	if j != nil {
		liveSearches.remove(j)
	}
}

// cancelJob stops one job by id and reports whether it existed.
func (s *bridgeSession) cancelJob(id, reason string) bool {
	s.jobMu.Lock()
	j, ok := s.jobs[id]
	if ok {
		delete(s.jobs, id)
	}
	s.jobMu.Unlock()
	if !ok {
		return false
	}
	liveSearches.remove(j)
	j.stop(reason)
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
		liveSearches.remove(j)
		j.stop(cancelSession)
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
	// Validated completely, and before target lookup or peer contact (WO-099
	// §7): the id is a routing key the page has already claimed, so a malformed
	// one produces events nobody can receive.
	if !searchIDPattern.MatchString(p.SearchID) {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "search_id must be a UUID", Code: "bad_payload",
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
	job, err := sess.startJob(p.SearchID, out, cancel)
	if err != nil {
		cancel()
		code := "search_busy"
		if errors.Is(err, errDuplicateSearchID) {
			code = "bad_payload"
		}
		// Refused, never resolved by displacing somebody else's search: this
		// daemon serves several pages and several profiles, and making room by
		// cancelling one of them is invisible to whoever was using it.
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: err.Error(), Code: code,
		})
	}

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

		// The terminal mapper (WO-099 §3). Cancellation, incomplete success and
		// failure are three different things and the user needs them told
		// apart: only a withdrawal — replacement, session loss, downgrade,
		// consent, shutdown — is cancellation. Budget exhaustion is a visibly
		// incomplete SUCCESS, and a verification error is a failure.
		cancelledBy := job.stopReason()
		if cancelledBy == "" && outcome.Reason == bridge.JobReasonCancelled {
			// The orchestrator's own in-loop entitlement backstop noticed a
			// downgrade before the coordinator reached this job.
			cancelledBy = cancelDowngrade
		}
		switch {
		case cancelledBy != "" || jobCtx.Err() != nil:
			if cancelledBy == "" {
				cancelledBy = cancelReplaced
			}
			job.emit("PEER_SEARCH_CANCELLED", func(seq uint64) any {
				return bridge.PeerSearchCancelledPayload{
					SearchID: job.id, Seq: seq, Results: streamed, Reason: cancelledBy,
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
	sess.cancelJob(p.SearchID, cancelReplaced)
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
