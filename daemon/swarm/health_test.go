// SPDX-License-Identifier: Apache-2.0
// WO-093: a node that cannot join the network must say so, not count forever.
//
// The states this machine exists to distinguish are minutes and hours apart, so
// none of these tests waits for one. The clock and the shared-key publisher are
// injected through presenceHooks and the `wait` hook advances the fake clock
// instead of sleeping — a test that sat through a 15-minute backoff is a test
// nobody would run, which is how the six-hour retry survived in the first place.
package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakePresence drives runPresence with a controllable clock and publisher.
type fakePresence struct {
	t        *testing.T
	now      time.Time
	routable bool
	// results is consumed one per publish attempt; the loop stops when it runs
	// out, which is what bounds every test here.
	results []error
	// waits records each interval the loop asked to sleep for, in order. This
	// is the backoff schedule, observed rather than asserted from constants.
	waits    []time.Duration
	attempts int
}

func (f *fakePresence) hooks() presenceHooks {
	return presenceHooks{
		now: func() time.Time { return f.now },
		publish: func(context.Context) error {
			if f.attempts >= len(f.results) {
				f.t.Fatalf("publish called %d times, only %d results queued",
					f.attempts+1, len(f.results))
			}
			err := f.results[f.attempts]
			f.attempts++
			return err
		},
		routable: func() bool { return f.routable },
		wait: func(_ context.Context, d time.Duration) bool {
			f.waits = append(f.waits, d)
			f.now = f.now.Add(d)
			// Out of queued outcomes means the scenario is over; ending the
			// loop here is what keeps these tests from running forever.
			return f.attempts < len(f.results)
		},
	}
}

func presenceNode(t *testing.T, announce bool) *Node {
	t.Helper()
	n := &Node{
		cfg:    Config{Policy: Policy{AnnounceProviders: announce}},
		health: newNetworkHealth(announce),
	}
	n.outbound.Store(true)
	return n
}

// A Level-1 node's zero is a policy outcome, not a fault and not a quiet
// network — and it is settled before any loop runs, because nothing that could
// change it exists short of replacing the node.
func TestLevelOneReportsOffAndNeverPublishesOrLooks(t *testing.T) {
	n := presenceNode(t, false)

	s := n.health.snapshot()
	if s.State != NetworkOff || s.Reason != ReasonLevelPolicy {
		t.Fatalf("level 1 should be off/level_policy, got %s/%s", s.State, s.Reason)
	}
	if s.AnnouncePermitted {
		t.Error("level 1 must not report announcing as permitted")
	}

	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true, results: []error{nil}}
	n.runPresence(context.Background(), f.hooks())
	if f.attempts != 0 {
		t.Errorf("level 1 published the shared key %d time(s)", f.attempts)
	}
	// And the state did not drift into "starting" on the way out.
	if got := n.health.snapshot().State; got != NetworkOff {
		t.Errorf("state after loop = %s, want off", got)
	}

	select {
	case <-n.Published():
		t.Error("Published closed at level 1; the lookup loop would start")
	default:
	}
}

// A permitted node that has not finished an attempt is `starting`, not `ready`
// with a zero and not a fault.
func TestPermittedNodeStartsInStarting(t *testing.T) {
	n := presenceNode(t, true)
	s := n.health.snapshot()
	if s.State != NetworkStarting || s.Reason != ReasonNone {
		t.Fatalf("got %s/%s, want starting/none", s.State, s.Reason)
	}
	if !s.AnnouncePermitted {
		t.Error("announce_permitted should be true at level 2+")
	}
	if s.Published || s.LookupCompleted || s.ConsecutiveFailures != 0 {
		t.Errorf("fresh node claims work it has not done: %+v", s)
	}
}

// Two failures retry; the third is a stated fault. The node keeps trying — the
// fault is a report, not a decision to stop.
func TestThirdConsecutiveFailureIsAFault(t *testing.T) {
	n := presenceNode(t, true)
	boom := errors.New("failed to find any peer in table")
	f := &fakePresence{
		t: t, now: time.Unix(0, 0), routable: true,
		results: []error{boom, boom, boom},
	}

	// Sampled between rounds rather than at the end: the point of the machine
	// is the sequence, and a test that only checked the final state would pass
	// on an implementation that jumped straight to `fault`.
	var states []string
	h := f.hooks()
	inner := h.wait
	h.wait = func(ctx context.Context, d time.Duration) bool {
		states = append(states, n.health.snapshot().State)
		return inner(ctx, d)
	}
	n.runPresence(context.Background(), h)

	want := []string{NetworkRetrying, NetworkRetrying, NetworkFault}
	if len(states) != len(want) {
		t.Fatalf("sampled %d states, want %d: %v", len(states), len(want), states)
	}
	for i, w := range want {
		if states[i] != w {
			t.Errorf("after failure %d: state %s, want %s", i+1, states[i], w)
		}
	}
	s := n.health.snapshot()
	if s.ConsecutiveFailures != 3 {
		t.Errorf("consecutive_failures = %d, want 3", s.ConsecutiveFailures)
	}
	if s.Reason != ReasonPublishFailed {
		t.Errorf("reason = %s, want publish_failed", s.Reason)
	}
	if s.Published {
		t.Error("published must be false while every attempt is failing")
	}
	if s.NextRetryAt <= s.LastAttemptAt {
		t.Error("a fault still promises a retry; next_retry_at must be in the future")
	}
}

// One, two, four, eight, then fifteen — and fifteen forever after. Doubling
// stops earning anything once the failure is structural, and a node behind a
// blocked route should still notice recovery within a quarter of an hour.
func TestBackoffDoublesThenCapsAtFifteenMinutes(t *testing.T) {
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		15 * time.Minute, 15 * time.Minute, 15 * time.Minute,
	}
	for i, w := range want {
		if got := retryBackoff(i + 1); got != w {
			t.Errorf("retryBackoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	// Defensive: a caller that has not failed yet still gets a sane interval
	// rather than zero, which would be a hot loop against the DHT.
	if got := retryBackoff(0); got != time.Minute {
		t.Errorf("retryBackoff(0) = %s, want 1m", got)
	}
}

// The observed schedule, not just the pure function: the loop must actually
// wait the interval its own state machine chose.
func TestLoopWaitsTheBackoffItChose(t *testing.T) {
	n := presenceNode(t, true)
	boom := errors.New("no route")
	f := &fakePresence{
		t: t, now: time.Unix(0, 0), routable: true,
		results: []error{boom, boom, boom, boom},
	}
	n.runPresence(context.Background(), f.hooks())
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}
	if len(f.waits) < len(want) {
		t.Fatalf("only %d waits recorded: %v", len(f.waits), f.waits)
	}
	for i, w := range want {
		if f.waits[i] != w {
			t.Errorf("wait %d = %s, want %s", i+1, f.waits[i], w)
		}
	}
}

// Success is `ready` with the reason and the failure count cleared, and the
// steady six-hour cadence resumes. Only in this state does a zero Keel-node
// count honestly mean "nobody else yet".
func TestSuccessBecomesReadyAndResumesTheRefreshCadence(t *testing.T) {
	n := presenceNode(t, true)
	f := &fakePresence{t: t, now: time.Unix(1_700_000_000, 0), routable: true,
		results: []error{nil}}
	n.runPresence(context.Background(), f.hooks())

	s := n.health.snapshot()
	if s.State != NetworkReady || s.Reason != ReasonNone {
		t.Fatalf("got %s/%s, want ready/none", s.State, s.Reason)
	}
	if !s.Published || s.ConsecutiveFailures != 0 {
		t.Errorf("ready must be published with no outstanding failures: %+v", s)
	}
	if s.LastSuccessAt != f.now.UnixMilli() && s.LastSuccessAt == 0 {
		t.Error("last_success_at not recorded")
	}
	if len(f.waits) == 0 || f.waits[0] != presenceRefresh {
		t.Errorf("post-success wait = %v, want %s", f.waits, presenceRefresh)
	}
	select {
	case <-n.Published():
	default:
		t.Error("Published did not close, so the lookup loop would never start")
	}
}

// Recovery needs no restart: a success after a fault is just a success.
func TestFaultRecoversToReadyWithoutARestart(t *testing.T) {
	n := presenceNode(t, true)
	boom := errors.New("no route")
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true,
		results: []error{boom, boom, boom, nil}}
	n.runPresence(context.Background(), f.hooks())

	s := n.health.snapshot()
	if s.State != NetworkReady {
		t.Fatalf("state = %s, want ready after recovery", s.State)
	}
	if s.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures = %d, want 0 after recovery", s.ConsecutiveFailures)
	}
	if s.Reason != ReasonNone {
		t.Errorf("reason = %s, want none after recovery", s.Reason)
	}
	if s.LastSuccessAt == 0 {
		t.Error("recovery did not record a success time")
	}
}

// "Still joining" and "cannot reach the network" are different problems with
// different remedies, so they are not one reason.
func TestRoutingUnavailableIsItsOwnReason(t *testing.T) {
	n := presenceNode(t, true)
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: false,
		results: []error{errors.New("failed to find any peer in table")}}
	n.runPresence(context.Background(), f.hooks())

	if got := n.health.snapshot().Reason; got != ReasonRoutingUnavailable {
		t.Errorf("reason = %s, want routing_unavailable", got)
	}
}

// The bounded routing-table wait keeps the state at `starting`: nothing has
// failed yet, and reporting a fault for a node that has not finished joining
// is the same lie in the other direction.
func TestRoutingWaitIsBoundedAndStaysStarting(t *testing.T) {
	n := presenceNode(t, true)
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: false,
		results: []error{nil}}
	h := f.hooks()

	n.waitRoutable(context.Background(), h)
	if got := n.health.snapshot().State; got != NetworkStarting {
		t.Errorf("state during the routing wait = %s, want starting", got)
	}
	// It gave up rather than blocking forever, and it did so within the bound.
	elapsed := f.now.Sub(time.Unix(0, 0))
	if elapsed > presenceRoutingWait+presenceRoutingPoll {
		t.Errorf("routing wait ran for %s, bound is %s", elapsed, presenceRoutingWait)
	}
	if elapsed == 0 {
		t.Error("routing wait did not wait at all")
	}
}

// A downgrade shuts the outbound gate before the node is replaced. The loop
// must notice and report `off` rather than keep publishing under the old
// policy — and it must not mutate the policy itself to get there.
func TestGateShutStopsPublishingAndReportsOff(t *testing.T) {
	n := presenceNode(t, true)
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true,
		results: []error{nil, nil}}
	h := f.hooks()
	h.wait = func(context.Context, time.Duration) bool {
		n.closeOutbound() // the downgrade, mid-loop
		return true
	}
	n.runPresence(context.Background(), h)

	if f.attempts != 1 {
		t.Errorf("published %d times after the gate shut; want 1 (the round before)", f.attempts)
	}
	s := n.health.snapshot()
	if s.State != NetworkOff || s.Reason != ReasonLevelPolicy {
		t.Errorf("got %s/%s, want off/level_policy", s.State, s.Reason)
	}
	if s.Published {
		t.Error("published must be false once the gate is shut")
	}
	if !n.cfg.Policy.AnnounceProviders {
		t.Error("the health machine mutated the contribution policy")
	}
}

// A gate that shuts mid-attempt is a withdrawn permission, not a network
// failure. Counting it as one would leave a node the user just downgraded
// reporting a fault it never had.
func TestGateShutMidAttemptIsNotCountedAsAFailure(t *testing.T) {
	n := presenceNode(t, true)
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true}
	h := f.hooks()
	h.publish = func(ctx context.Context) error {
		n.closeOutbound()
		return n.publishPresence(ctx) // the real gate check, on a real gate
	}
	n.runPresence(context.Background(), h)

	s := n.health.snapshot()
	if s.ConsecutiveFailures != 0 {
		t.Errorf("a withdrawn permission was counted as a failure: %+v", s)
	}
	if s.State != NetworkOff || s.Reason != ReasonLevelPolicy {
		t.Errorf("got %s/%s, want off/level_policy", s.State, s.Reason)
	}
}

// A cancelled context ends the loop without inventing an outcome for the
// attempt that was in flight.
func TestCancelledContextDoesNotRecordAFailure(t *testing.T) {
	n := presenceNode(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true}
	h := f.hooks()
	h.publish = func(context.Context) error {
		cancel()
		return errors.New("context canceled")
	}
	n.runPresence(ctx, h)

	s := n.health.snapshot()
	if s.ConsecutiveFailures != 0 {
		t.Errorf("shutdown counted as a network failure: %+v", s)
	}
	if s.State != NetworkStarting {
		t.Errorf("state = %s, want starting", s.State)
	}
}

// Nothing in the snapshot may identify a peer, a request or the user. It
// travels to a browser; raw libp2p errors stay in the local daemon log.
func TestStatusCarriesNoRawErrorOrIdentity(t *testing.T) {
	n := presenceNode(t, true)
	secret := "12D3KooWSECRETPEERID at 203.0.113.9 for prefix 0a3f"
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true,
		results: []error{errors.New(secret)}}
	n.runPresence(context.Background(), f.hooks())
	n.health.lookedUp(f.now)

	blob, err := json.Marshal(n.health.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"12D3KooW", "203.0.113.9", "0a3f", "prefix"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("status leaked %q: %s", leak, blob)
		}
	}

	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"state": true, "reason": true, "announce_permitted": true,
		"published": true, "consecutive_failures": true, "last_attempt_at": true,
		"last_success_at": true, "next_retry_at": true, "lookup_completed": true,
		"last_lookup_at": true, "keel_peers": true,
	}
	for k := range got {
		if !allowed[k] {
			t.Errorf("unexpected field %q in the status snapshot", k)
		}
	}
	// The reason stays inside the bounded enum whatever libp2p said.
	switch got["reason"] {
	case ReasonNone, ReasonLevelPolicy, ReasonRoutingUnavailable, ReasonPublishFailed:
	default:
		t.Errorf("reason %q is outside the enum", got["reason"])
	}
}

// Level 1 is a full consumer that advertises nothing. Reporting `off` must not
// have cost it anything it had before (WO-093 §5).
func TestLevelOneKeepsItsConsumerCapabilitiesAndNeverLooks(t *testing.T) {
	st := newStore(t, "level1.db")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := Start(ctx, st, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	s := n.NetworkStatus()
	if s.State != NetworkOff || s.Reason != ReasonLevelPolicy {
		t.Fatalf("level 1 = %s/%s, want off/level_policy", s.State, s.Reason)
	}

	// No lookup happens, and none is recorded: at Level 1 "found nobody" would
	// be a statement about a search this node never ran.
	found, err := n.FindPeers(ctx, 8)
	if err != nil || found != 0 {
		t.Errorf("FindPeers at level 1 = %d, %v; want 0, nil", found, err)
	}
	if after := n.NetworkStatus(); after.LookupCompleted || after.LastLookupAt != 0 {
		t.Errorf("level 1 recorded a lookup it did not perform: %+v", after)
	}

	// The consumer half is untouched — withholding the cache must not withhold
	// the product (see policy.go).
	p := n.Policy()
	if !p.Fetch {
		t.Error("level 1 lost block fetch")
	}
	if !p.FetchWordTelemetry {
		t.Error("level 1 lost the word-telemetry download")
	}
	if p.AnnounceProviders || p.ServeBroadBuckets {
		t.Error("level 1 gained an outbound capability")
	}
}

// The bulk content round and the shared discovery key answer different
// questions, and neither may stand in for the other (WO-093 §1).
//
// Against a real node this asserts the strong form: Announce has no write path
// to the health record at all, so no content outcome — thousands published or
// nothing published — can move it in either direction. The isolated node here
// has an empty routing table, so its content round genuinely fails; the point
// is that the record is unchanged by it both before and after a shared-key
// success.
func TestContentAnnounceCannotMoveNetworkHealth(t *testing.T) {
	st := newStore(t, "presence.db")
	seed(t, st, "aaaaaaaaaaa", "bbbbbbbbbbb", 0)
	seed(t, st, "aaaaaaaaaaa", "ccccccccccc", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := Start(ctx, st, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	before := n.health.snapshot()
	if before.State != NetworkStarting {
		t.Fatalf("fresh serving node = %s, want starting", before.State)
	}
	_ = n.Announce(ctx)
	if after := n.health.snapshot(); after != before {
		t.Errorf("the bulk content round rewrote network health:\n before %+v\n after  %+v",
			before, after)
	}

	// Now the other direction: once the shared key is published this node IS
	// discoverable, and a failing content round must not take that away.
	n.health.succeeded(time.Now())
	ready := n.health.snapshot()
	_ = n.Announce(ctx)
	after := n.health.snapshot()
	if after.State != NetworkReady || !after.Published {
		t.Errorf("a failed content round un-published the shared key: %+v", after)
	}
	if after != ready {
		t.Errorf("content round changed health after success:\n ready %+v\n after %+v", ready, after)
	}
}

// The shared key does not queue behind the corpus: publishing it takes no
// dependency on the store at all, so a node with thousands of content keys is
// findable in the time one Provide takes rather than the time the round takes.
func TestSharedKeyPublicationDoesNotWaitForTheCorpus(t *testing.T) {
	n := presenceNode(t, true) // no store, no host, no corpus
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true, results: []error{nil}}
	n.runPresence(context.Background(), f.hooks())
	if got := n.health.snapshot().State; got != NetworkReady {
		t.Fatalf("state = %s, want ready", got)
	}
	if f.attempts != 1 {
		t.Errorf("shared key published %d times, want exactly 1 per round", f.attempts)
	}
}

// An unfinished first lookup is "looking"; only a completed one lets `ready`
// plus zero mean a quiet network.
func TestLookupCompletionIsSeparateFromReadiness(t *testing.T) {
	n := presenceNode(t, true)
	f := &fakePresence{t: t, now: time.Unix(0, 0), routable: true, results: []error{nil}}
	n.runPresence(context.Background(), f.hooks())

	if n.health.snapshot().LookupCompleted {
		t.Fatal("ready must not imply a completed lookup")
	}
	n.health.lookedUp(f.now)
	s := n.health.snapshot()
	if !s.LookupCompleted || s.LastLookupAt == 0 {
		t.Errorf("completed lookup not recorded: %+v", s)
	}
}
