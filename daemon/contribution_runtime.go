// SPDX-License-Identifier: Apache-2.0
// The contribution supervisor: making a level change take effect (WO-077).
//
// The bug this exists to fix is small to describe and serious to have: the
// swarm read the contribution level once, at process start. Choosing Level 1
// wrote a row to SQLite and changed nothing on the network, so a node kept
// serving blocks and announcing itself as their provider until the user
// happened to restart the daemon. The interface said "Strictly Personal" while
// the node mirrored. That is a consent failure, not stale UI, and "restart to
// apply" is not an acceptable privacy mechanism.
//
// Handlers, provider loops and pubsub topics are all bound during swarm.Start
// and cannot be re-gated afterwards, so the level change is implemented as
// node *replacement* under a supervisor rather than mutation of a live node.
// Two things follow, and both matter more than the replacement itself:
//
//   - Stopping a libp2p host is not instant, and requests keep arriving while
//     it winds down. So the old node's outbound permission gate is shut
//     synchronously *before* teardown begins (swarm.Node.CloseOutbound), and
//     every serve path re-checks it per request. The gate, not the teardown,
//     is what makes the downgrade immediate.
//   - A crash mid-transition must never resolve upward. That is what the
//     second persisted value (startup_level) is for; see store/contribution.go.
//
// Ordering is deliberately asymmetric between the two directions, because the
// unsafe outcome is different in each. Going down, persist first and never
// resurrect the higher node. Going up, run first and commit the durable
// escalation only once the higher policy is actually effective.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

// Transition states reported to the interface alongside the two levels.
const (
	transitionIdle     = "idle"
	transitionStarting = "starting"
	transitionStopping = "stopping"
	transitionFailed   = "failed"
	// transitionConsentRequired means no node exists and none will be built
	// until the user accepts the current network disclosure (WO-089).
	//
	// Its own state rather than a failure: nothing went wrong, and the
	// interface's response is to ask, not to retry or report a fault.
	transitionConsentRequired = "consent_required"
)

// consentDetail is what the interface shows while the gate is shut. Stated
// once so the daemon and the tests cannot drift apart on the wording.
const consentDetail = "waiting for you to accept what Keel downloads and records"

// contributionState is what the bridge reports: the user's choice, the policy
// actually running, and whether the two are mid-flight or stuck.
//
// stored and effective may differ only while transition is not idle, or after
// a failure that left them apart — and in the latter case detail says why.
type contributionState struct {
	Stored     int    `json:"stored_level"`
	Effective  int    `json:"effective_level"`
	Startup    int    `json:"startup_level"`
	Transition string `json:"transition"`
	Detail     string `json:"detail,omitempty"`
	// Consent is the WO-089 network-data gate. Carried here rather than in a
	// separate status because the two are one question for the interface: a
	// level control means nothing while no network may exist, and the surfaces
	// that render the level already receive this payload on every change.
	Consent store.NetworkConsent `json:"network_consent"`
}

// contributionStatusPublisher is supplied by the owner session boundary.
// Standalone bridge tests have no publisher; owner-backed sessions use it to
// tell every connected browser which policy is now effective after a runtime
// transition. The event has its own owner-scoped id and never participates in
// request correlation.
type contributionStatusPublisher interface {
	publishContribution(contributionState)
}

type contributionPublisherContextKey struct{}

func withContributionPublisher(ctx context.Context, p contributionStatusPublisher) context.Context {
	return context.WithValue(ctx, contributionPublisherContextKey{}, p)
}

func publishContributionStatus(ctx context.Context, state contributionState) {
	p, _ := ctx.Value(contributionPublisherContextKey{}).(contributionStatusPublisher)
	if p != nil {
		p.publishContribution(state)
	}
}

// swarmSupervisor owns the node pointer and every transition of it.
//
// Callers fetch the node per operation and must not retain it across a
// transition: the pointer they hold may be a stopped node moments later. That
// is why there is no exported accessor returning the node plus a lock — the
// only supported pattern is "ask again next time".
type swarmSupervisor struct {
	// mu serializes transitions against each other. Held for the duration of
	// a replacement, which can take seconds; readers of the node pointer use
	// nodeMu instead so a status query never blocks behind a transition.
	mu sync.Mutex

	nodeMu sync.RWMutex
	node   *swarm.Node
	// stop cancels the context the current node's goroutines run on. Node.Close
	// stops the host but not the publish/consume loops started under Start's
	// context, so without this a replaced node keeps gossiping from the grave.
	stop context.CancelFunc

	stateMu    sync.RWMutex
	effective  int
	transition string
	detail     string

	// launchFn constructs one node. Replaceable so tests can inject a
	// construction failure at a chosen point in a transition: the ordering
	// guarantees this file exists for are only meaningful if the failure
	// paths are exercised, and a cancelled context does not fail libp2p host
	// construction. Nil means the real implementation.
	launchFn func(ctx context.Context, st *store.Store, level int) (*swarm.Node, context.CancelFunc, error)
}

var supervisor = &swarmSupervisor{transition: transitionIdle}

func (s *swarmSupervisor) currentNode() *swarm.Node {
	s.nodeMu.RLock()
	defer s.nodeMu.RUnlock()
	return s.node
}

func (s *swarmSupervisor) setNode(n *swarm.Node, stop context.CancelFunc) {
	s.nodeMu.Lock()
	s.node, s.stop = n, stop
	s.nodeMu.Unlock()
}

func (s *swarmSupervisor) setState(effective int, transition, detail string) {
	s.stateMu.Lock()
	s.effective, s.transition, s.detail = effective, transition, detail
	s.stateMu.Unlock()
}

func (s *swarmSupervisor) setTransition(transition, detail string) {
	s.stateMu.Lock()
	s.transition, s.detail = transition, detail
	s.stateMu.Unlock()
}

// state reports the current runtime state joined with what SQLite holds.
func (s *swarmSupervisor) state(st *store.Store) contributionState {
	s.stateMu.RLock()
	effective, transition, detail := s.effective, s.transition, s.detail
	s.stateMu.RUnlock()
	return contributionState{
		Stored:     st.ContributionLevel(),
		Effective:  effective,
		Startup:    st.StartupLevel(),
		Transition: transition,
		Detail:     detail,
		Consent:    st.NetworkConsent(),
	}
}

// inTransition reports whether the node pointer is currently being replaced,
// so network-dependent RPCs can answer "temporarily unavailable" rather than
// behave as though the network were simply down.
func (s *swarmSupervisor) inTransition() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.transition == transitionStarting || s.transition == transitionStopping
}

// contributionPayload is the bridge shape for contribution state.
//
// `level` is retained alongside `stored_level` for the extension's existing
// reader. It carries the *effective* level, because a control that shows the
// stored choice while a different policy runs is the misreport this ticket is
// about; when they agree — the overwhelmingly common case — nothing changes.
func contributionPayload(s contributionState) map[string]any {
	return map[string]any{
		"level":           s.Effective,
		"stored_level":    s.Stored,
		"effective_level": s.Effective,
		"startup_level":   s.Startup,
		"transition":      s.Transition,
		"detail":          s.Detail,
		"max_implemented": store.MaxImplementedLevel,
		"levels_disagree": s.Stored != s.Effective,
		// The reciprocal-search entitlement travels with contribution state
		// rather than in its own RPC (WO-085), so the one status broadcast
		// every browser session already receives after a level change is
		// enough to re-render the search control everywhere. A second RPC
		// would be a second chance for the two to disagree.
		//
		// Derived from the effective level, not the stored one: the control
		// must reflect the policy being enforced, for exactly the reason the
		// radio buttons do.
		"distributed_search":           swarm.PolicyForLevel(s.Effective).DistributedSearch,
		"distributed_search_min_level": store.LevelBroad,
		// Live and outbound word statistics moved to Level 2+ (WO-089). Sent
		// the same way and for the same reason as distributed_search: one
		// broadcast has to be able to re-render every level-dependent control,
		// or each one needs its own RPC and its own chance to disagree.
		"live":              swarm.PolicyForLevel(s.Effective).Live,
		"live_min_level":    store.LevelBroad,
		"shares_word_stats": swarm.PolicyForLevel(s.Effective).ServeWordTelemetry,
		// The gate. `network_consent.current` false means no node exists and
		// none will until the user accepts — which the interface must present
		// as a question, not as a fault.
		"network_consent":  s.Consent,
		"consent_required": !s.Consent.Current,
		"consent_revision": store.NetworkConsentRevision,
	}
}

// networkBusy reports whether a network-dependent RPC should decline right
// now because the node is being replaced.
func networkBusy() bool { return supervisor.inTransition() }

// effectiveLevel is the contribution policy currently in force. Zero means no
// node is running at all — see the failure paths below, which set it.
func (s *swarmSupervisor) effectiveLevel() int {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.effective
}

// liveAllowed answers the entitlement question for the Live feed (WO-089),
// by the same rule as distributedSearchLevel: ask the running node when there
// is one, because that is the policy actually in force, and fall back to the
// stored choice when there is not.
func liveAllowed(st *store.Store) (allowed bool, level int) {
	if n := currentSwarmNode(); n != nil {
		return n.Policy().Live, supervisor.effectiveLevel()
	}
	stored := st.ContributionLevel()
	return swarm.PolicyForLevel(stored).Live, stored
}

// distributedSearchLevel is the entitlement question answered for the RPC
// layer: may this daemon run a user-triggered distributed search right now,
// and if not, what is running instead (WO-085)?
//
// Asks the node when there is one, because the node's policy is the thing that
// would actually be enforced a layer down and asking anything else could
// disagree with it. With no node there is no effective policy to read at all,
// so it falls back to the user's stored choice: the reply for a node-less
// daemon is "unavailable" either way, and this only decides which of two true
// statements the interface is given — "you have not opted in" is the actionable
// one, and claiming it of a Level-2 user whose network failed to start would
// not be.
func distributedSearchLevel(st *store.Store) (allowed bool, level int) {
	if n := currentSwarmNode(); n != nil {
		return n.MayDistributedSearch(), supervisor.effectiveLevel()
	}
	stored := st.ContributionLevel()
	return swarm.PolicyForLevel(stored).DistributedSearch, stored
}

// contributionImpactLevel answers the entitlement question for
// GET_CONTRIBUTION_IMPACT (WO-086), by the same rule as distributedSearchLevel
// and liveAllowed: ask the running node's actual policy for the allow
// decision — the policy actually in force is what would actually be enforced
// a layer down — and supervisor.effectiveLevel() only for the level to
// report, exactly as distributedSearchLevel does with MayDistributedSearch.
func contributionImpactLevel(st *store.Store) (allowed bool, level int) {
	if n := currentSwarmNode(); n != nil {
		return n.Serving(), supervisor.effectiveLevel()
	}
	stored := st.ContributionLevel()
	return swarm.PolicyForLevel(stored).ServeBroadBuckets, stored
}

// replyNetworkBusy declines one RPC for the duration of a node replacement.
//
// Typed rather than shaped as an empty result: "the network found nothing"
// and "ask again in a moment" are different answers, and a caller that cannot
// tell them apart will cache the wrong one.
func replyNetworkBusy(out io.Writer, id string) error {
	return reply(out, id, "ERROR", bridge.ErrorPayload{
		Message: "the peer network is restarting after a contribution change; try again shortly",
		Code:    bridge.CodeNetworkBusy,
	})
}

// start brings up the first node of the process.
//
// It constructs startup_level, never the stored level when that is higher: a
// stored level above the startup level means a previous upgrade did not finish,
// and resuming it automatically would turn a crash into an escalation nobody
// confirmed. The mismatch is reported instead.
func (s *swarmSupervisor) start(ctx context.Context, st *store.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// The consent gate, ahead of everything else (WO-089). The daemon starts
	// on its own schedule with no browser attached, so this is the only place
	// that can stop a network from existing before the user has seen the
	// current disclosure. It is checked before the level is even read: an
	// unaccepted disclosure means network-off regardless of what level is
	// stored, including a level a previous build's screen sold them.
	if c := st.NetworkConsent(); !c.Current {
		log.Printf("swarm: %s (disclosure revision %d, accepted %d)",
			consentDetail, c.Required, c.Revision)
		s.setState(0, transitionConsentRequired, consentDetail)
		return
	}

	stored, startup := st.ContributionLevel(), st.StartupLevel()
	level := startup
	detail := ""
	if stored > startup {
		detail = fmt.Sprintf(
			"level %d was chosen but never finished activating; running level %d until it is chosen again",
			stored, startup)
		log.Printf("swarm: %s", detail)
	}

	n, stop, err := s.launch(ctx, st, level)
	if err != nil {
		// The local product works without the network; refusing to start the
		// daemon because a router is unhelpful would be a poor trade.
		log.Printf("swarm unavailable, continuing locally: %v", err)
		s.setState(0, transitionFailed, err.Error())
		return
	}
	s.setNode(n, stop)
	if detail != "" {
		s.setState(level, transitionFailed, detail)
		return
	}
	s.setState(level, transitionIdle, "")
}

// resumeAfterConsent brings the network up once the user accepts (WO-089).
//
// Consent arrives while the process is already running — the daemon started,
// found no acceptance, and stopped at the gate — so something has to open it
// without a restart. This is that something, and it is deliberately just
// `start` again: the gate check lives inside it, so a call that arrives with
// consent still missing is a no-op that re-reports the same state rather than
// a second code path that could disagree with the first.
//
// Idempotent. A node already running means the gate was open, and start's own
// mutex plus its consent check make a redundant call harmless.
func (s *swarmSupervisor) resumeAfterConsent(ctx context.Context, st *store.Store) contributionState {
	if s.currentNode() != nil {
		return s.state(st)
	}
	s.start(ctx, st)
	return s.state(st)
}

// stopForWithdrawnConsent tears the network down when consent is withdrawn.
//
// Same asymmetry as a downgrade, for the same reason: the gate shuts
// synchronously before teardown begins, so from the first instruction nothing
// further is offered — regardless of how long stopping the host takes.
func (s *swarmSupervisor) stopForWithdrawnConsent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeMu.Lock()
	n, stop := s.node, s.stop
	s.node, s.stop = nil, nil
	s.nodeMu.Unlock()
	if n != nil {
		n.CloseOutbound()
	}
	s.teardown(n, stop)
	s.setState(0, transitionConsentRequired, consentDetail)
}

// launch constructs and starts one node at the given level, plus the announce
// loop that keeps its provider records alive.
//
// The returned cancel belongs to the node: calling it stops every goroutine
// started under this node's context, which Node.Close alone does not do.
func (s *swarmSupervisor) launch(
	ctx context.Context, st *store.Store, level int,
) (*swarm.Node, context.CancelFunc, error) {
	if s.launchFn != nil {
		return s.launchFn(ctx, st, level)
	}
	nodeCtx, cancel := context.WithCancel(ctx)
	cfg := swarmConfigFor(level)
	n, err := swarm.Start(nodeCtx, st, cfg)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if cfg.Policy.AnnounceProviders {
		go announceLoop(nodeCtx, n)
		// Same gate: a node that may not advertise itself does not go looking
		// for others either (WO-094).
		go rendezvousLoop(nodeCtx, n)
	}
	return n, cancel, nil
}

// teardown shuts a node's gate, cancels its goroutines and closes its host.
//
// Gate first, always. Everything after it is best-effort cleanup; the promise
// to the user is kept by the first line.
func (s *swarmSupervisor) teardown(n *swarm.Node, stop context.CancelFunc) {
	if n != nil {
		n.CloseOutbound()
	}
	if stop != nil {
		stop()
	}
	if n != nil {
		if err := n.Close(); err != nil {
			log.Printf("swarm: closing replaced node: %v", err)
		}
	}
}

// stopAll tears the current node down for good, for process shutdown.
func (s *swarmSupervisor) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeMu.Lock()
	n, stop := s.node, s.stop
	s.node, s.stop = nil, nil
	s.nodeMu.Unlock()
	s.teardown(n, stop)
	s.setState(0, transitionIdle, "")
}

// apply moves the running policy to the requested level.
//
// Returns the state to report. An error result still carries a state: the
// caller shows the user what is actually running, which after a failed
// transition is the thing they most need to know.
func (s *swarmSupervisor) apply(
	ctx context.Context, st *store.Store, level int,
) (contributionState, error) {
	if err := store.ValidateContributionLevel(level); err != nil {
		return s.state(st), err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stateMu.RLock()
	effective := s.effective
	s.stateMu.RUnlock()

	if level == effective && st.ContributionLevel() == level && st.StartupLevel() == level {
		return s.state(st), nil
	}
	// Asking for *less* never needs permission, and must never be blocked by
	// the consent gate (WO-089). The whole point of the downgrade path is that
	// it shuts the outbound gate before anything else can fail, so a check in
	// front of it would leave a higher-level node running exactly when the
	// user asked it to stop — including on an unreadable database, where the
	// consent read itself fails closed. Removing capability is always allowed.
	if level < effective {
		return s.downgrade(ctx, st, level)
	}
	// Raising one does need it. A level is a choice about what to send, and it
	// cannot be exercised before the user has accepted what Keel does at all.
	// Refused rather than stored-and-deferred: storing a 2 here would leave a
	// database whose setting says "sharing" while nothing has been agreed, and
	// the next start would honour it the moment consent arrived.
	if c := st.NetworkConsent(); !c.Current {
		return s.state(st), fmt.Errorf("%s", consentDetail)
	}
	return s.upgrade(ctx, st, level)
}

// downgrade removes capabilities. Persist first, then replace.
//
// The gate closes before anything else and never reopens on this node, so from
// the first instruction of this function the old policy is no longer being
// offered — regardless of how long teardown or the new node's construction
// takes, or whether either fails.
func (s *swarmSupervisor) downgrade(
	ctx context.Context, st *store.Store, level int,
) (contributionState, error) {
	old, oldStop := s.detachNode()
	if old != nil {
		old.CloseOutbound()
	}
	s.setTransition(transitionStopping, "")

	// Durable before effective: if the process dies immediately after this
	// commit, restart constructs the lower level. The reverse order could
	// restart into the level the user just left.
	if err := st.SetContributionAndStartupLevel(level); err != nil {
		s.teardown(old, oldStop)
		detail := fmt.Sprintf("could not save level %d: %v; network stopped", level, err)
		s.setState(0, transitionFailed, detail)
		return s.state(st), fmt.Errorf("%s", detail)
	}

	s.teardown(old, oldStop)
	s.setTransition(transitionStarting, "")

	n, stop, err := s.launch(ctx, st, level)
	if err != nil {
		// Stay stopped. The user asked for less; giving them none of it is
		// the safe direction to fail, and resurrecting the higher-level node
		// to "restore service" would serve blocks they just withdrew.
		detail := fmt.Sprintf("level %d saved, but the network could not restart: %v", level, err)
		s.setState(0, transitionFailed, detail)
		return s.state(st), nil
	}
	s.setNode(n, stop)
	s.setState(level, transitionIdle, "")
	return s.state(st), nil
}

// upgrade adds capabilities. Run first, commit the escalation last.
//
// stored_level records the explicit choice immediately so the interface can
// reflect it, but startup_level — the value a restart would reconstruct —
// stays low until the higher policy is genuinely running.
func (s *swarmSupervisor) upgrade(
	ctx context.Context, st *store.Store, level int,
) (contributionState, error) {
	prevStored := st.ContributionLevel()
	prevStartup := st.StartupLevel()

	if _, err := st.SetContributionLevel(level); err != nil {
		return s.state(st), err
	}

	old, oldStop := s.detachNode()
	s.setTransition(transitionStopping, "")
	s.teardown(old, oldStop)
	s.setTransition(transitionStarting, "")

	n, stop, err := s.launch(ctx, st, level)
	if err != nil {
		return s.rollback(ctx, st, prevStored, prevStartup,
			fmt.Sprintf("level %d could not start: %v", level, err))
	}
	s.setNode(n, stop)

	// Activation commit. Only now may a restart reconstruct the higher policy.
	if err := st.SetStartupLevel(level); err != nil {
		s.detachAndTeardown()
		return s.rollback(ctx, st, prevStored, prevStartup,
			fmt.Sprintf("level %d started but could not be saved: %v", level, err))
	}

	s.setState(level, transitionIdle, "")
	return s.state(st), nil
}

// rollback returns to the previous policy after a failed upgrade.
//
// Restoring stored_level is best-effort and deliberately not fatal: the
// retained lower startup_level is the durable guarantee, so even a rollback
// that cannot write still cannot produce an automatic escalation on restart.
func (s *swarmSupervisor) rollback(
	ctx context.Context, st *store.Store, stored, startup int, reason string,
) (contributionState, error) {
	log.Printf("swarm: %s; rolling back to level %d", reason, startup)
	if _, err := st.SetContributionLevel(stored); err != nil {
		log.Printf("swarm: could not restore stored level %d: %v", stored, err)
	}
	if err := st.SetStartupLevel(startup); err != nil {
		log.Printf("swarm: could not restore startup level %d: %v", startup, err)
	}

	n, stop, err := s.launch(ctx, st, startup)
	if err != nil {
		detail := fmt.Sprintf("%s; could not restart level %d either: %v", reason, startup, err)
		s.setState(0, transitionFailed, detail)
		return s.state(st), fmt.Errorf("%s", detail)
	}
	s.setNode(n, stop)
	s.setState(startup, transitionFailed, reason)
	return s.state(st), fmt.Errorf("%s", reason)
}

// detachNode removes the node from service and returns it for teardown.
func (s *swarmSupervisor) detachNode() (*swarm.Node, context.CancelFunc) {
	s.nodeMu.Lock()
	n, stop := s.node, s.stop
	s.node, s.stop = nil, nil
	s.nodeMu.Unlock()
	return n, stop
}

func (s *swarmSupervisor) detachAndTeardown() {
	n, stop := s.detachNode()
	s.teardown(n, stop)
}
