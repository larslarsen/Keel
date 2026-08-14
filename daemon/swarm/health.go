// SPDX-License-Identifier: Apache-2.0
// Whether this node can be found at all, and why not (WO-093).
//
// `keel_peers` is zero in three unrelated situations and the count cannot tell
// them apart: nothing was ever published so nobody can find us, publication
// worked and nobody else is online, or announcing is forbidden at this
// contribution level and zero is the only value the count can ever have. Only
// the middle one is a normal state. A live QA session spent a day in the first
// while reading it as the second — the number was honest and useless.
//
// So the node keeps one bounded record of its own discoverability, and the
// interface renders that instead of inferring health from a zero.
//
// What it measures is the shared discovery key from WO-094 — the one constant
// of the software every install provides and looks up — and nothing else.
// Content provider records are a different question ("who serves this bucket?")
// and must not stand in for this one in either direction: a content key can
// publish while the shared key fails, leaving a node with an unrelated corpus
// unable to find this one, and the shared key can publish long before thousands
// of content records finish, at which point this node *is* discoverable even
// though the bulk round is still running. That is why presence has its own loop
// (runPresence) rather than a line inside Announce.
//
// The record is in memory and never persisted. A fresh node starts fresh
// because its operational state — and, below Level 4, its network identity —
// is process-scoped anyway.
package swarm

import (
	"context"
	"errors"
	"sync"
	"time"
)

// The five states a node's discoverability can be in (WO-093 §2).
//
// Deliberately not a bool plus a spinner: "cannot reach the network" is an
// answer a person can act on, and a counter that never moves is not.
const (
	// NetworkOff means shared-key publication is forbidden by the effective
	// contribution policy. At Level 1, zero connected Keel nodes is expected.
	NetworkOff = "off"
	// NetworkStarting means publication is permitted but no attempt has
	// completed yet. Includes the bounded routing-table wait.
	NetworkStarting = "starting"
	// NetworkRetrying means one or two consecutive attempts failed and another
	// bounded retry is scheduled.
	NetworkRetrying = "retrying"
	// NetworkReady means the latest attempt succeeded. Only in this state does
	// a zero Keel-node count honestly mean "nobody else found yet".
	NetworkReady = "ready"
	// NetworkFault means faultAfter consecutive attempts failed. The node keeps
	// retrying; what changes is that the interface states the fault.
	NetworkFault = "fault"
)

// Why the state is what it is. A bounded enum, not a message: the daemon owns
// the user-safe explanation, and no raw libp2p error, address or peer id ever
// leaves this package. Those stay in the local daemon log.
const (
	ReasonNone               = "none"
	ReasonLevelPolicy        = "level_policy"
	ReasonRoutingUnavailable = "routing_unavailable"
	ReasonPublishFailed      = "publish_failed"
)

// faultAfter is how many consecutive failed publications make the state a
// stated fault rather than a retry. Three: enough that a single unlucky round
// on a slow join does not alarm anyone, few enough that a node which genuinely
// cannot reach the DHT says so within minutes instead of counting forever.
const faultAfter = 3

const (
	// presenceProvideTimeout bounds one shared-key Provide. A DHT call must
	// never get the node's whole lifetime as its only deadline: a Provide that
	// hangs is indistinguishable from one that is slow, and neither retries.
	presenceProvideTimeout = 30 * time.Second
	// presenceRetryBase and presenceRetryMax define the backoff after failure
	// n: min(1 minute × 2^(n-1), 15 minutes) — one, two, four, eight, fifteen.
	presenceRetryBase = time.Minute
	presenceRetryMax  = 15 * time.Minute
	// presenceRefresh is the steady-state cadence after a success. DHT provider
	// records expire in about 24 hours; refreshing well inside that keeps a
	// node findable without adding meaningful traffic.
	presenceRefresh = 6 * time.Hour
	// presenceRoutingWait is the maximum wait for a routable DHT before the
	// first attempt. Provide fails outright with an empty routing table, which
	// is exactly the state at start-up.
	presenceRoutingWait = 90 * time.Second
	presenceRoutingPoll = 500 * time.Millisecond
)

// NetworkStatus is the whole record, and it is the payload the interface reads.
//
// Operational fields only. No observation, query, prefix, bucket, peer id, IP
// address or raw error appears here — this travels to a browser.
type NetworkStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	// AnnouncePermitted is the policy answer, separate from the outcome: it is
	// what distinguishes "not allowed to be discoverable" from "allowed and
	// failing", which is the whole point of this record.
	AnnouncePermitted bool `json:"announce_permitted"`
	// Published describes the LATEST publication outcome. Not a sticky
	// "succeeded once in this process" bit — a node that published an hour ago
	// and has failed three times since is not discoverable now.
	Published           bool  `json:"published"`
	ConsecutiveFailures int   `json:"consecutive_failures"`
	LastAttemptAt       int64 `json:"last_attempt_at,omitempty"`
	// LastSuccessAt survives failures, so a fault can be diagnosed against when
	// this node last actually worked.
	LastSuccessAt int64 `json:"last_success_at,omitempty"`
	NextRetryAt   int64 `json:"next_retry_at,omitempty"`
	// LookupCompleted separates "discoverable and still looking" from
	// "discoverable, looked, nobody there". Without it the first lookup's
	// duration renders as an established empty network.
	LookupCompleted bool  `json:"lookup_completed"`
	LastLookupAt    int64 `json:"last_lookup_at,omitempty"`
	// KeelPeers travels inside this object as well as beside it, so the count
	// and the state a reader interprets it with are one snapshot taken under
	// one lock rather than two reads that can disagree.
	KeelPeers int `json:"keel_peers"`
}

// networkHealth is the lock-protected state machine. One per running node.
type networkHealth struct {
	mu sync.Mutex
	s  NetworkStatus

	// published closes on the first successful shared-key publication, so the
	// lookup loop can start promptly at that moment instead of on a timer that
	// has no idea whether this node is findable yet.
	published chan struct{}
	once      sync.Once
}

// newNetworkHealth builds the record for a node whose policy does or does not
// permit announcing.
//
// Level 1 derives `off` here, at construction, rather than by starting a loop
// that immediately declines to do anything: there is nothing to wait for and
// nothing that could change it without replacing the node.
func newNetworkHealth(permitted bool) *networkHealth {
	h := &networkHealth{published: make(chan struct{})}
	if permitted {
		h.s = NetworkStatus{State: NetworkStarting, Reason: ReasonNone, AnnouncePermitted: true}
	} else {
		h.s = NetworkStatus{State: NetworkOff, Reason: ReasonLevelPolicy}
	}
	return h
}

func (h *networkHealth) snapshot() NetworkStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.s
}

// attempted records that a publication is being tried. The state does not move
// here: `starting` stays `starting` and `retrying` stays `retrying` until the
// attempt has an outcome, so a reader never sees an in-flight attempt as a
// resolved one.
func (h *networkHealth) attempted(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.s.LastAttemptAt = now.UnixMilli()
}

// failed records one failed publication and returns how long to wait.
func (h *networkHealth) failed(now time.Time, reason string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.s.ConsecutiveFailures++
	h.s.Published = false
	h.s.Reason = reason
	if h.s.ConsecutiveFailures >= faultAfter {
		h.s.State = NetworkFault
	} else {
		h.s.State = NetworkRetrying
	}
	wait := retryBackoff(h.s.ConsecutiveFailures)
	h.s.NextRetryAt = now.Add(wait).UnixMilli()
	return wait
}

// succeeded records a successful publication and returns the refresh interval.
//
// Recovery from `fault` needs no restart and no separate path: a success is a
// success whatever preceded it.
func (h *networkHealth) succeeded(now time.Time) time.Duration {
	h.mu.Lock()
	h.s.State = NetworkReady
	h.s.Reason = ReasonNone
	h.s.Published = true
	h.s.ConsecutiveFailures = 0
	h.s.LastSuccessAt = now.UnixMilli()
	h.s.NextRetryAt = now.Add(presenceRefresh).UnixMilli()
	h.mu.Unlock()
	h.once.Do(func() { close(h.published) })
	return presenceRefresh
}

// lookedUp records that one FindPeers round finished.
func (h *networkHealth) lookedUp(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.s.LookupCompleted = true
	h.s.LastLookupAt = now.UnixMilli()
}

// turnOff records that publication is no longer permitted.
//
// Reached when the outbound gate shuts under a running loop. The gate is the
// downgrade mechanism (WO-077) and this only reports it — no contribution
// policy is mutated here, and the Level-1 node that replaces this one derives
// `off` at construction.
func (h *networkHealth) turnOff() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.s.State = NetworkOff
	h.s.Reason = ReasonLevelPolicy
	h.s.AnnouncePermitted = false
	h.s.Published = false
	h.s.NextRetryAt = 0
}

// retryBackoff is min(1 minute × 2^(n-1), 15 minutes).
//
// Doubling loses its value once the failure is structural — no route out, a
// blocked port — and a node in that state should still notice recovery within
// a quarter of an hour rather than a quarter of a day.
func retryBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := presenceRetryBase
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= presenceRetryMax {
			return presenceRetryMax
		}
	}
	return d
}

// NetworkStatus reports discoverability plus the count it explains.
//
// The count is read here rather than stored by the loops so it cannot go stale,
// and it is attached to the state in one call so the interface never renders a
// fresh zero against a stale `ready`.
func (n *Node) NetworkStatus() NetworkStatus {
	s := n.health.snapshot()
	s.KeelPeers = n.KeelPeers()
	return s
}

// Published closes once this node has successfully published the shared
// discovery key. Before then there is nothing for another node to find, so
// looking for company is premature.
func (n *Node) Published() <-chan struct{} { return n.health.published }

// presenceHooks are the loop's dependencies on time and the network.
//
// Injected so the transition and backoff decisions can be tested without
// sleeping for production intervals: the states this machine exists to
// distinguish are minutes and hours apart, and a test that waited for them
// would be a test nobody runs.
type presenceHooks struct {
	now      func() time.Time
	publish  func(context.Context) error
	routable func() bool
	// wait blocks for d and reports false if the context ended first.
	wait func(context.Context, time.Duration) bool
}

// RunPresence owns shared-key publication for the life of this node.
//
// Separate from announceLoop on purpose — see this file's package comment. The
// bulk loop publishes provider records for data this node can serve and takes
// minutes on a real corpus; being findable at all must not queue behind it.
func (n *Node) RunPresence(ctx context.Context) {
	n.runPresence(ctx, presenceHooks{
		now:      time.Now,
		publish:  n.publishPresence,
		routable: func() bool { return n.RoutingTableSize() > 0 },
		wait:     sleepUntil,
	})
}

func (n *Node) runPresence(ctx context.Context, h presenceHooks) {
	if !n.mayAnnounce() {
		// Level 1 neither advertises nor searches, so there is no loop to run.
		// turnOff rather than a bare return because the gate can also be shut
		// between construction and this goroutine being scheduled, and then the
		// record would still read `starting` for a node that will never try.
		n.health.turnOff()
		return
	}
	n.waitRoutable(ctx, h)
	for {
		if !n.mayAnnounce() {
			n.health.turnOff()
			return
		}
		n.health.attempted(h.now())
		err := h.publish(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errAnnounceForbidden) {
			// The gate shut between the check above and the call. That is a
			// withdrawn permission, not a network failure, and counting it as
			// one would leave a downgraded node reporting a fault it never had.
			n.health.turnOff()
			return
		}
		var wait time.Duration
		if err != nil {
			// An empty routing table and a failed walk are different problems
			// with different remedies — "still joining" versus "this node
			// cannot reach the network" — so they are not one reason.
			reason := ReasonPublishFailed
			if !h.routable() {
				reason = ReasonRoutingUnavailable
			}
			wait = n.health.failed(h.now(), reason)
			n.logf("presence: this node is NOT discoverable (%s): %v; retrying in %s",
				reason, err, wait)
		} else {
			wait = n.health.succeeded(h.now())
			n.logf("presence: shared discovery key published; other Keel nodes can find this one")
		}
		if !h.wait(ctx, wait) {
			return
		}
	}
}

// waitRoutable blocks until the DHT can route, bounded.
//
// The state stays `starting` throughout: nothing has failed yet, and reporting
// a failure for a node that has not finished joining would be the same lie in
// the other direction.
func (n *Node) waitRoutable(ctx context.Context, h presenceHooks) {
	deadline := h.now().Add(presenceRoutingWait)
	for !h.routable() {
		if !h.now().Before(deadline) {
			// Try anyway. A failure carrying `routing_unavailable` is a stated
			// answer; never attempting produces the silent zero this whole
			// mechanism exists to abolish.
			n.logf("presence: DHT routing table still empty after %s; publishing anyway",
				presenceRoutingWait)
			return
		}
		if !h.wait(ctx, presenceRoutingPoll) {
			return
		}
	}
}

// sleepUntil sleeps for d, or returns false as soon as ctx ends.
func sleepUntil(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
