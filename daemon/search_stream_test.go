// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// syncBuf is a writer several job goroutines may share, like the real
// syncWriter over stdout.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) envelopes(t *testing.T) []*bridge.Envelope {
	t.Helper()
	s.mu.Lock()
	raw := append([]byte(nil), s.buf.Bytes()...)
	s.mu.Unlock()
	r := bytes.NewReader(raw)
	var out []*bridge.Envelope
	for {
		framed, err := bridge.ReadMessage(r)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			t.Fatalf("stream is not validly framed: %v", err)
		}
		env, err := bridge.ParseEnvelope(framed)
		if err != nil {
			t.Fatalf("stream carried an invalid envelope: %v", err)
		}
		out = append(out, env)
	}
	return out
}

// streamingSession is a negotiated session at peer_search revision 3.
//
// retire() stands in for the job goroutine's deferred finishJob. Since WO-100 a
// cancelled job keeps its registration — and its place against the active-job
// ceiling — until its goroutine terminates, so a test that only cancels would
// leak registrations into the shared registry and starve later tests.
func streamingSession(t *testing.T) *bridgeSession {
	caps := bridge.DaemonCaps()
	sess := &bridgeSession{helloOK: true, caps: caps}
	t.Cleanup(func() { sess.retireAll() })
	return sess
}

// retireAll finishes every job the way a job goroutine would.
func (s *bridgeSession) retireAll() {
	s.jobMu.Lock()
	jobs := map[string]*searchJob{}
	for id, j := range s.jobs {
		jobs[id] = j
	}
	s.jobMu.Unlock()
	for id, j := range jobs {
		j.stop(cancelSession)
		s.finishJob(id, j)
	}
}

// TestEventEnvelopeIdsCannotResolveARequest is WO-095 §3's correlation rule.
//
// A job emits envelopes nobody is waiting for onto a session where every other
// envelope answers a request by id. Without a reserved prefix a job event could
// collide with a live request's correlation id and settle somebody else's
// promise with a payload of the wrong type — the THUMBNAIL/GET_CONSENT
// fall-through class of defect, arriving over the wire instead of through a
// switch.
func TestEventEnvelopeIdsCannotResolveARequest(t *testing.T) {
	job := &searchJob{id: "search-abc", out: &syncBuf{}}
	buf := &syncBuf{}
	job.out = buf

	job.emit("PEER_SEARCH_PROGRESS", func(seq uint64) any {
		return bridge.PeerSearchProgressPayload{SearchID: job.id, Seq: seq, TokenID: 1, Phase: bridge.PhaseActive}
	})

	envs := buf.envelopes(t)
	if len(envs) != 1 {
		t.Fatalf("emitted %d envelopes, want 1", len(envs))
	}
	if !strings.HasPrefix(envs[0].ID, bridge.EventIDPrefix) {
		t.Errorf("event id %q does not carry the reserved prefix %q — it could "+
			"resolve an unrelated pending request", envs[0].ID, bridge.EventIDPrefix)
	}
	if envs[0].ID == job.id {
		t.Error("the event id is the bare search id, which a client could have used as a request id")
	}
}

// TestEventSequenceIsMonotonicUnderConcurrency: the sequence number and the
// write must be one step.
//
// Token work runs concurrently by design, so several goroutines emit at once.
// If the number were assigned under one lock and written under another, two
// emitters could produce 4 then 3 on the wire — and the client's ordering guard
// would then discard a live event as stale, which is silent and permanent.
func TestEventSequenceIsMonotonicUnderConcurrency(t *testing.T) {
	buf := &syncBuf{}
	job := &searchJob{id: "search-seq", out: buf}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(tokenID int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				job.emit("PEER_SEARCH_PROGRESS", func(seq uint64) any {
					return bridge.PeerSearchProgressPayload{
						SearchID: job.id, Seq: seq, TokenID: tokenID, Phase: bridge.PhaseActive,
					}
				})
			}
		}(i)
	}
	wg.Wait()

	envs := buf.envelopes(t)
	if len(envs) != 160 {
		t.Fatalf("emitted %d envelopes, want 160", len(envs))
	}
	seen := map[uint64]bool{}
	for _, env := range envs {
		var p bridge.PeerSearchProgressPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Seq == 0 {
			t.Fatal("an event carried sequence 0; sequence numbers start at 1")
		}
		if seen[p.Seq] {
			t.Fatalf("sequence %d was issued twice — two events would be "+
				"indistinguishable to the client's ordering guard", p.Seq)
		}
		seen[p.Seq] = true
	}
	for i := uint64(1); i <= 160; i++ {
		if !seen[i] {
			t.Fatalf("sequence %d was never issued; the numbering has a hole", i)
		}
	}
}

// TestJobsAreScopedToTheirSession is WO-095 §3's isolation requirement: two
// sessions must never receive one another's events.
func TestJobsAreScopedToTheirSession(t *testing.T) {
	a, b := streamingSession(t), streamingSession(t)
	bufA, bufB := &syncBuf{}, &syncBuf{}

	jobA, err := a.startJob("11111111-1111-4111-8111-111111111111", bufA, func() {})
	if err != nil {
		t.Fatal(err)
	}
	jobB, err := b.startJob("22222222-2222-4222-8222-222222222222", bufB, func() {})
	if err != nil {
		t.Fatal(err)
	}

	jobA.emit("PEER_SEARCH_RESULT", func(seq uint64) any {
		return bridge.PeerSearchStreamResultPayload{SearchID: jobA.id, Seq: seq}
	})

	if got := len(bufB.envelopes(t)); got != 0 {
		t.Errorf("session B received %d envelopes from session A's job", got)
	}
	if got := len(bufA.envelopes(t)); got != 1 {
		t.Errorf("session A received %d envelopes from its own job, want 1", got)
	}

	// Cancelling one session's job leaves the other's alone.
	if !a.cancelJob(jobA.id, cancelReplaced) {
		t.Error("cancelling a live job reported it did not exist")
	}
	if b.cancelJob(jobA.id, cancelReplaced) {
		t.Error("session B cancelled a job belonging to session A")
	}
	_ = jobB
}

// TestConcurrentPagesOnOneSessionDoNotCancelEachOther is WO-099 §1, and it
// records a corrected mistake.
//
// The first implementation cancelled every job in the native session before
// registering a new one, reading WO-095 §4's "a page owns at most one active
// job" as a session rule. It is not: every search page in one browser profile
// shares the service worker's single native connection, so that reading meant
// opening a search in one tab silently killed the one in another — with no
// error anywhere, because cancellation is a legitimate terminal state.
//
// Replacement is a page decision, expressed by the page cancelling its own id.
func TestConcurrentPagesOnOneSessionDoNotCancelEachOther(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}

	pageACancelled := false
	pageA, err := sess.startJob("11111111-1111-4111-8111-111111111111", buf,
		func() { pageACancelled = true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.startJob("22222222-2222-4222-8222-222222222222", buf, func() {}); err != nil {
		t.Fatal(err)
	}

	if pageACancelled {
		t.Error("starting a search in a second page cancelled the first page's search")
	}
	if sess.cancelJob(pageA.id, cancelReplaced) != true {
		t.Error("the first page's job is no longer registered")
	}
}

// TestDuplicateLiveSearchIDIsRefused is WO-099 §7: a duplicate id would
// silently take over another page's route rather than starting a search.
func TestDuplicateLiveSearchIDIsRefused(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}
	const id = "33333333-3333-4333-8333-333333333333"

	if _, err := sess.startJob(id, buf, func() {}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.startJob(id, buf, func() {}); !errors.Is(err, errDuplicateSearchID) {
		t.Errorf("a duplicate live search id was accepted (err=%v)", err)
	}
}

// TestActiveJobCeilingRefusesRatherThanDisplacing is WO-099 §1's bound. Making
// room by cancelling somebody else's search is invisible to whoever was using
// it, so excess starts are refused instead.
func TestActiveJobCeilingRefusesRatherThanDisplacing(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}

	cancelled := 0
	for i := 0; i < maxActiveSearchJobs; i++ {
		id := fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)
		if _, err := sess.startJob(id, buf, func() { cancelled++ }); err != nil {
			t.Fatalf("start %d refused early: %v", i, err)
		}
	}
	_, err := sess.startJob("99999999-9999-4999-8999-999999999999", buf, func() {})
	if !errors.Is(err, errSearchBusy) {
		t.Errorf("the ceiling did not refuse an excess start (err=%v)", err)
	}
	if cancelled != 0 {
		t.Errorf("%d running searches were displaced to make room", cancelled)
	}
}

// TestDowngradeCancelsEverySearchPromptly is WO-099 §3. A Level-2-to-Level-1
// transition must not wait for a worker to poll, and must not let a job report
// COMPLETE.
func TestDowngradeCancelsEverySearchPromptly(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}

	job, err := sess.startJob("44444444-4444-4444-8444-444444444444", buf, func() {})
	if err != nil {
		t.Fatal(err)
	}
	stopDistributedSearches(cancelDowngrade)

	if got := job.stopReason(); got != cancelDowngrade {
		t.Errorf("job stop reason = %q, want %q — a cancellation the user did "+
			"not ask for has to be distinguishable from one they did",
			got, cancelDowngrade)
	}
}

// TestSessionTeardownCancelsEveryJob covers native disconnect, owner shutdown
// and a browser going away (WO-095 §4). Work whose results nobody can receive
// must not keep spending peers' serving budget.
func TestSessionTeardownCancelsEveryJob(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}
	n := 0
	if _, err := sess.startJob("55555555-5555-4555-8555-555555555555", buf, func() { n++ }); err != nil {
		t.Fatal(err)
	}
	sess.cancelAllJobs()
	if n != 1 {
		t.Errorf("session teardown cancelled %d jobs, want 1", n)
	}
	// The id stays reserved until the job's goroutine retires it (WO-100 §5),
	// so cancelling again is still idempotent and still finds it.
	sess.retireAll()
	if sess.cancelJob("55555555-5555-4555-8555-555555555555", cancelReplaced) {
		t.Error("a job survived being retired")
	}
}

// TestCancelIsIdempotent: a page that cancels a search which just finished on
// its own has not made an error, and telling it so would be noise it cannot act
// on.
func TestCancelIsIdempotent(t *testing.T) {
	sess := streamingSession(t)
	env, err := bridge.NewEnvelope("req-1", "PEER_SEARCH_CANCEL",
		bridge.PeerSearchCancelPayload{SearchID: "never-existed"})
	if err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	if err := handlePeerSearchCancel(env, buf, sess); err != nil {
		t.Fatal(err)
	}
	envs := buf.envelopes(t)
	if len(envs) != 1 || envs[0].Type != "PEER_SEARCH_CANCEL_RESULT" {
		t.Fatalf("cancelling an unknown search answered %+v, want a plain result", envs)
	}
}

// TestRevisionTwoSessionsGetTheAtomicReply is WO-095 §3's compatibility rule.
//
// An extension that negotiated revision 2 must keep its one-shot reply and
// receive no revision-3 events — an unsolicited envelope is something an old
// client logs as an error, and it would arrive on every search.
func TestRevisionTwoSessionsGetTheAtomicReply(t *testing.T) {
	restore := adoptNodeForTest(nil)
	defer restore()

	dir := t.TempDir()
	st, err := store.Open(dir + "/rev2.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	caps := bridge.DaemonCaps()
	caps[bridge.CapPeerSearch] = bridge.PeerSearchRevReciprocal
	sess := &bridgeSession{helloOK: true, caps: caps}

	// A search_id present in the payload must NOT select the streaming path
	// when the session negotiated revision 2.
	env, err := bridge.NewEnvelope("req-1", "PEER_SEARCH", bridge.SearchPayload{
		Query: "world", Limit: 10, SearchID: "should-be-ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	if err := handleRawContext(t.Context(), raw, buf, st, sess); err != nil {
		t.Fatal(err)
	}
	envs := buf.envelopes(t)
	if len(envs) != 1 {
		t.Fatalf("a revision-2 session got %d envelopes, want exactly one reply", len(envs))
	}
	if envs[0].Type == "PEER_SEARCH_STARTED" {
		t.Error("a revision-2 session was handed a streaming job it cannot consume")
	}
	if strings.HasPrefix(envs[0].ID, bridge.EventIDPrefix) {
		t.Error("a revision-2 session received an unsolicited event envelope")
	}
}

// TestSearchResultCarriesTheRenderPlan is WO-095 §1: the plan travels on every
// local search, including when peer search is off or unentitled, because it is
// how the interface knows what the words and tokens are. The extension never
// retokenizes.
func TestSearchResultCarriesTheRenderPlan(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/plan.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	env, err := bridge.NewEnvelope("req-1", "SEARCH", bridge.SearchPayload{Query: "a big", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	if err := handleRaw(raw, buf, st); err != nil {
		t.Fatal(err)
	}
	envs := buf.envelopes(t)
	if len(envs) != 1 || envs[0].Type != "SEARCH_RESULT" {
		t.Fatalf("got %+v, want one SEARCH_RESULT", envs)
	}
	var p bridge.SearchResultPayload
	if err := json.Unmarshal(envs[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Plan == nil {
		t.Fatal("SEARCH_RESULT carried no render plan")
	}
	if p.Plan.Normalized != "a big" {
		t.Errorf("plan normalized to %q, want %q", p.Plan.Normalized, "a big")
	}
	if len(p.Plan.Tokens) != 2 {
		t.Fatalf("plan has %d tokens, want the two grid chunks", len(p.Plan.Tokens))
	}
	// The cross-word chunk colours both words and gets exactly one bar.
	if len(p.Plan.Tokens[0].Fragments) != 2 {
		t.Errorf("the chunk straddling the space covers %d fragments, want 2",
			len(p.Plan.Tokens[0].Fragments))
	}
}

// TestPlanWireCarriesNoTokenText is the privacy boundary at the wire (WO-095
// §10, DESIGN_v2 §4.2).
//
// The plan legitimately carries normalized display words — there is no way to
// draw a search box's word bars without them. What it must not carry is token
// *text*: a renderer slices the normalized string using the character ranges,
// so sending the three-gram separately would put query content in a payload for
// no rendering benefit.
func TestPlanWireCarriesNoTokenText(t *testing.T) {
	plan := store.BuildQueryPlan("distinctive phrase")
	wire := planWire(plan)
	if wire == nil {
		t.Fatal("planWire returned nothing for a real query")
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	tokens, _ := generic["tokens"].([]any)
	if len(tokens) == 0 {
		t.Fatal("no tokens on the wire")
	}
	for i, tv := range tokens {
		tok, _ := tv.(map[string]any)
		for _, forbidden := range []string{"token", "text", "shard", "peer"} {
			if _, present := tok[forbidden]; present {
				t.Errorf("token %d carries a %q field — the wire must name a token "+
					"only by opaque id and character range", i, forbidden)
			}
		}
	}
}

// TestProgressEventsCarryNoQueryOrPeerIdentity walks every event payload shape
// and asserts what is absent (WO-095 §6, §10).
//
// Written as a shape check rather than a review note because the failure mode
// is additive: someone adds a field to make a bar smarter, and query content
// starts appearing in logs and screenshots forever after.
func TestProgressEventsCarryNoQueryOrPeerIdentity(t *testing.T) {
	forbidden := []string{"token", "word", "title", "query", "peer", "shard"}
	// word_id and token_id are ids, not content; video_id appears only on a
	// result, because a result IS a video.
	allowed := map[string]bool{"word_id": true, "token_id": true, "search_id": true}

	for _, payload := range []any{
		bridge.PeerSearchProgressPayload{SearchID: "s", Seq: 1, TokenID: 3, Cycle: 1, Phase: bridge.PhaseActive},
		bridge.PeerSearchWordProgressPayload{SearchID: "s", Seq: 2, WordID: 0, Found: 4, Target: 9, Known: true},
		bridge.PeerSearchCompletePayload{SearchID: "s", Seq: 3, Reason: bridge.JobReasonComplete},
		bridge.PeerSearchCancelledPayload{SearchID: "s", Seq: 4},
	} {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatal(err)
		}
		for key := range generic {
			if allowed[key] {
				continue
			}
			for _, bad := range forbidden {
				if strings.Contains(key, bad) {
					t.Errorf("%T carries field %q — progress events must name nothing "+
						"about the query, the corpus or the peer", payload, key)
				}
			}
		}
	}
}

// TestCancelledIdStaysReservedUntilTheJobRetires is WO-100 §5.
//
// Cancelling used to deregister immediately, while the goroutine was still
// winding down. The same UUID could then be accepted for a new job — and when
// the old goroutine ran its deferred finish, it deleted the NEW one, because
// the entry was keyed only by the reused string. The new search then ran
// unregistered, outside the ceiling, and its own cancel reported that it did
// not exist.
func TestCancelledIdStaysReservedUntilTheJobRetires(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}
	const id = "66666666-6666-4666-8666-666666666666"

	old, err := sess.startJob(id, buf, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if !sess.cancelJob(id, cancelReplaced) {
		t.Fatal("cancelling a live job reported it did not exist")
	}
	if got := old.stopReason(); got != cancelReplaced {
		t.Errorf("stop reason = %q, want %q", got, cancelReplaced)
	}

	// Still retiring: the id is reserved and reusing it is refused.
	if _, err := sess.startJob(id, buf, func() {}); !errors.Is(err, errDuplicateSearchID) {
		t.Errorf("a UUID was reused while its job was still retiring (err=%v)", err)
	}
	// Cancellation stays idempotent while it retires.
	if !sess.cancelJob(id, cancelReplaced) {
		t.Error("cancelling a retiring job reported it did not exist")
	}

	// The goroutine terminates.
	sess.finishJob(id, old)

	fresh, err := sess.startJob(id, buf, func() {})
	if err != nil {
		t.Fatalf("the id was not released after the job retired: %v", err)
	}

	// The OLD job's deferred finish must not touch the new registration.
	sess.finishJob(id, old)
	if !sess.cancelJob(id, cancelReplaced) {
		t.Error("the retired job's deferred finish deleted the job that reused its id")
	}
	sess.finishJob(id, fresh)
}

// TestRetiringJobsStillCountAgainstTheCeiling: releasing the slot on cancel
// would open a hole that admits unbounded retiring work.
func TestRetiringJobsStillCountAgainstTheCeiling(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}

	var jobs []*searchJob
	for i := 0; i < maxActiveSearchJobs; i++ {
		id := fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", i)
		j, err := sess.startJob(id, buf, func() {})
		if err != nil {
			t.Fatalf("start %d refused early: %v", i, err)
		}
		jobs = append(jobs, j)
		sess.cancelJob(id, cancelReplaced)
	}
	// All cancelled, none retired: the ceiling still holds.
	if _, err := sess.startJob("bbbbbbbb-0000-4000-8000-000000000000", buf, func() {}); !errors.Is(err, errSearchBusy) {
		t.Errorf("cancelled-but-retiring jobs released their slots (err=%v)", err)
	}
	for i, j := range jobs {
		sess.finishJob(fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", i), j)
	}
	// Once retired, the slots are free again.
	fresh, err := sess.startJob("bbbbbbbb-0000-4000-8000-000000000000", buf, func() {})
	if err != nil {
		t.Errorf("slots were not released after the jobs retired: %v", err)
	}
	sess.finishJob("bbbbbbbb-0000-4000-8000-000000000000", fresh)
}

// TestStopAllKeepsSlotsUntilJobsRetire is WO-101 §4.
//
// stopAll snapshotted the jobs and then replaced the registry with an empty map
// before cancelling them. Those goroutines were still retiring, but add() could
// immediately admit another full ceiling of jobs — bypassing the identity-safe
// retirement rule cancelJob and finishJob had just been changed to enforce.
func TestStopAllKeepsSlotsUntilJobsRetire(t *testing.T) {
	sess := streamingSession(t)
	buf := &syncBuf{}

	ids := make([]string, 0, maxActiveSearchJobs)
	jobs := make([]*searchJob, 0, maxActiveSearchJobs)
	for i := 0; i < maxActiveSearchJobs; i++ {
		id := fmt.Sprintf("cccccccc-0000-4000-8000-%012d", i)
		j, err := sess.startJob(id, buf, func() {})
		if err != nil {
			t.Fatalf("start %d refused early: %v", i, err)
		}
		ids = append(ids, id)
		jobs = append(jobs, j)
	}

	// Repeated stops are safe and keep the first reason.
	stopDistributedSearches(cancelDowngrade)
	stopDistributedSearches(cancelShutdown)
	if got := jobs[0].stopReason(); got != cancelDowngrade {
		t.Errorf("stop reason = %q, want the first one recorded (%q)", got, cancelDowngrade)
	}

	// The slots are still occupied: those goroutines have not retired.
	extra := "dddddddd-0000-4000-8000-000000000000"
	if _, err := sess.startJob(extra, buf, func() {}); !errors.Is(err, errSearchBusy) {
		t.Errorf("stopAll freed the global ceiling before its jobs retired (err=%v)", err)
	}

	// Retiring by identity frees them, and repeated removal is safe.
	for i, j := range jobs {
		sess.finishJob(ids[i], j)
		sess.finishJob(ids[i], j)
	}
	fresh, err := sess.startJob(extra, buf, func() {})
	if err != nil {
		t.Errorf("slots were not released after the jobs retired: %v", err)
	}
	sess.finishJob(extra, fresh)
}
