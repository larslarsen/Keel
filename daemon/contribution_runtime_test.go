// SPDX-License-Identifier: Apache-2.0
// WO-077: a contribution change must reconfigure the running swarm, and must
// fail in the safe direction when any step of that goes wrong.
//
// The tests here are about ordering and durability rather than transport: what
// is persisted before what, what a restart reconstructs after a crash at each
// durable step, and which way a partial failure resolves. The wire-level half
// of the ticket — that a Level-1 node genuinely answers nothing — lives in
// swarm/policy_test.go, where a real peer can try to fetch from it.
package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

// errInjected is the failure these tests inject at a chosen construction step.
var errInjected = errors.New("injected construction failure")

// adoptNodeForTest points the supervisor at a node without running a
// transition, and returns a restore func.
//
// Test-only seam. Production code may not do this: the whole point of the
// supervisor is that the node pointer changes only through apply/start/stopAll,
// so that a change of policy cannot be half-applied. Tests that want to
// exercise an RPC against a known node need to bypass that, and being explicit
// here is better than widening the production surface.
func adoptNodeForTest(n *swarm.Node) func() {
	prev := supervisor.currentNode()
	prevStop := supervisor.stop
	supervisor.setNode(n, nil)
	return func() { supervisor.setNode(prev, prevStop) }
}

// freshSupervisor isolates one test from the package-level supervisor.
func freshSupervisor(t *testing.T) *swarmSupervisor {
	t.Helper()
	s := &swarmSupervisor{transition: transitionIdle}
	t.Cleanup(s.stopAll)
	return s
}

func testStore(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestPolicyForLevelMatchesArchitectureTable pins ARCHITECTURE_CURRENT §3's
// capability matrix in code, so a future edit to PolicyForLevel that quietly
// re-gates a capability has to change this table too.
func TestPolicyForLevelMatchesArchitectureTable(t *testing.T) {
	one := swarm.PolicyForLevel(store.LevelPersonal)
	two := swarm.PolicyForLevel(store.LevelMirror)

	// Level 1 is a full consumer. Every one of these being on is the
	// product-boundary half of WO-077: withholding them would make privacy a
	// toll booth rather than a promise.
	for name, got := range map[string]bool{
		"live":                    one.Live,
		"fetch":                   one.Fetch,
		"exchange_word_telemetry": one.ExchangeWordTelemetry,
	} {
		if !got {
			t.Errorf("level 1 has %s off; it is a full consumer", name)
		}
	}
	// And it offers nothing.
	for name, got := range map[string]bool{
		"serve_mirrors":         one.ServeMirrors,
		"announce_providers":    one.AnnounceProviders,
		"join_search_telemetry": one.JoinSearchTelemetry,
		"publish_own":           one.PublishOwn,
	} {
		if got {
			t.Errorf("level 1 has %s on; level 1 serves nothing", name)
		}
	}

	for name, got := range map[string]bool{
		"serve_mirrors":         two.ServeMirrors,
		"announce_providers":    two.AnnounceProviders,
		"join_search_telemetry": two.JoinSearchTelemetry,
	} {
		if !got {
			t.Errorf("level 2 has %s off; a mirror serves and is findable", name)
		}
	}
	// The line that separates a mirror from a publisher.
	if two.PublishOwn {
		t.Error("level 2 publishes its own observations; that begins at level 3")
	}
	if !swarm.PolicyForLevel(store.LevelCohort).PublishOwn {
		t.Error("level 3 does not publish own observations")
	}
	// An unreadable/garbage level must read as the consumer policy, never as
	// consent to serve.
	if swarm.PolicyForLevel(0).ServeMirrors || swarm.PolicyForLevel(-3).ServeMirrors {
		t.Error("an out-of-range level produced a serving policy")
	}
}

// TestStartupLevelDefaultsToStoredForOldDatabases is the migration: a database
// written before the split has one value, and a node that has only ever had
// one value has by definition never been mid-transition.
func TestStartupLevelDefaultsToStoredForOldDatabases(t *testing.T) {
	st := testStore(t, "migrate.sqlite")
	if _, err := st.SetContributionLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	if got := st.StartupLevel(); got != store.LevelMirror {
		t.Fatalf("startup level = %d, want %d (should follow the single stored value)",
			got, store.LevelMirror)
	}
}

// TestStartupLevelNeverExceedsStored covers the corrupted/partly-written case.
// A startup level above the stored choice can only be leftover state, and the
// safe reading of leftover state is the user's choice.
func TestStartupLevelNeverExceedsStored(t *testing.T) {
	st := testStore(t, "clamp.sqlite")
	if err := st.SetStartupLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetContributionLevel(store.LevelPersonal); err != nil {
		t.Fatal(err)
	}
	if got := st.StartupLevel(); got != store.LevelPersonal {
		t.Fatalf("startup level = %d with stored = 1; a leftover higher value must not survive", got)
	}
}

// TestDowngradePersistsBothLevelsAtomically is the crash-point test for 2→1:
// after the durable step there must be no combination that reconstructs
// Level 2, because the user has already been told Level 1 is in force.
func TestDowngradeCommitsBothLevelsTogether(t *testing.T) {
	st := testStore(t, "downgrade.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}

	if err := st.SetContributionAndStartupLevel(store.LevelPersonal); err != nil {
		t.Fatal(err)
	}
	// Simulate the restart: whatever a fresh process would construct.
	if got := st.StartupLevel(); got != store.LevelPersonal {
		t.Errorf("restart would construct level %d after a downgrade to 1", got)
	}
	if got := st.ContributionLevel(); got != store.LevelPersonal {
		t.Errorf("stored level = %d after a downgrade to 1", got)
	}
}

// TestUpgradeKeepsStartupLowUntilActivation is the crash-point test for 1→2,
// and the reason the two values exist at all: between "user chose 2" and
// "2 is running", a crash must come back at 1.
func TestUpgradeKeepsStartupLowUntilActivation(t *testing.T) {
	st := testStore(t, "upgrade.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelPersonal); err != nil {
		t.Fatal(err)
	}

	// Step one of an upgrade: the explicit choice is durable...
	if _, err := st.SetContributionLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	// ...but a crash here must not escalate.
	if got := st.StartupLevel(); got != store.LevelPersonal {
		t.Fatalf("a crash mid-upgrade would construct level %d; must stay at 1 until activation", got)
	}

	// Step two: activation commits.
	if err := st.SetStartupLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	if got := st.StartupLevel(); got != store.LevelMirror {
		t.Fatalf("after activation startup level = %d, want 2", got)
	}
}

// TestStartRefusesToResumeAnUnfinishedUpgrade proves the restart half of the
// same rule: a stored level above the startup level is reported as a mismatch,
// not silently completed.
func TestStartRefusesToResumeAnUnfinishedUpgrade(t *testing.T) {
	st := testStore(t, "unfinished.sqlite")
	// Exactly the state a crash mid-upgrade leaves behind.
	if _, err := st.SetContributionLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStartupLevel(store.LevelPersonal); err != nil {
		t.Fatal(err)
	}

	s := freshSupervisor(t)
	s.start(t.Context(), st)

	got := s.state(st)
	if got.Effective != store.LevelPersonal {
		t.Errorf("effective level = %d after an unfinished upgrade, want 1", got.Effective)
	}
	if got.Stored != store.LevelMirror {
		t.Errorf("stored level = %d, want the user's unchanged choice of 2", got.Stored)
	}
	if got.Transition != transitionFailed {
		t.Errorf("transition = %q, want %q so the mismatch is visible",
			got.Transition, transitionFailed)
	}
	if got.Detail == "" {
		t.Error("a level mismatch was reported with no explanation")
	}
}

// TestDowngradeShutsTheGateBeforeAnythingElse is the ordering guarantee that
// makes a downgrade immediate rather than eventual.
//
// Teardown of a libp2p host is not instant and requests keep arriving while it
// winds down, so the promise is kept by the gate, not by the stop.
func TestDowngradeShutsTheGateBeforeAnythingElse(t *testing.T) {
	st := testStore(t, "gate.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}

	s := freshSupervisor(t)
	s.start(t.Context(), st)
	old := s.currentNode()
	if old == nil {
		t.Skip("swarm did not start in this environment")
	}
	if !old.Serving() {
		t.Fatal("a level 2 node started with its outbound gate shut")
	}

	if _, err := s.apply(t.Context(), st, store.LevelPersonal); err != nil {
		t.Fatalf("downgrade: %v", err)
	}

	// The node object still exists in this test's hand; the point is that it
	// refuses to serve regardless of how far teardown has progressed.
	if old.Serving() {
		t.Error("the replaced node would still answer a block request")
	}
	if got := s.state(st); got.Effective != store.LevelPersonal {
		t.Errorf("effective level = %d after downgrade, want 1", got.Effective)
	}
}

// stubSupervisor is a supervisor whose node construction is faked, so a
// transition can be driven deterministically and made to fail at a chosen
// step. A cancelled context is not enough: libp2p host construction succeeds
// regardless, which is why these tests inject rather than improvise.
func stubSupervisor(t *testing.T, level int, failAt map[int]error) *swarmSupervisor {
	t.Helper()
	s := &swarmSupervisor{transition: transitionIdle}
	s.launchFn = func(_ context.Context, _ *store.Store, want int) (*swarm.Node, context.CancelFunc, error) {
		if err := failAt[want]; err != nil {
			return nil, nil, err
		}
		// A nil node with a no-op cancel is enough for these tests: they
		// assert on persisted levels and reported state, never on transport.
		return nil, func() {}, nil
	}
	s.effective = level
	t.Cleanup(s.stopAll)
	return s
}

// TestDowngradedNodeKeepsEveryConsumerCapability is the other half of the
// downgrade acceptance: withdrawing the cache must not withdraw the product.
//
// The pre-WO-077 mapping turned Fetch off with Serve, so choosing "Strictly
// Personal" silently disabled peer search and graph pre-walk. Dropping to
// Level 1 must stop what this node offers and change nothing about what it
// receives.
func TestDowngradedNodeKeepsEveryConsumerCapability(t *testing.T) {
	st := testStore(t, "downgrade-caps.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	s := freshSupervisor(t)
	s.start(t.Context(), st)
	if s.currentNode() == nil {
		t.Skip("swarm did not start in this environment")
	}

	if _, err := s.apply(t.Context(), st, store.LevelPersonal); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	n := s.currentNode()
	if n == nil {
		t.Fatal("no node running after a successful downgrade")
	}
	p := n.Policy()
	if !p.Fetch {
		t.Error("the downgraded node cannot fetch; peer search and pre-walk are gone")
	}
	if !p.Live {
		t.Error("the downgraded node left the live network")
	}
	if !p.ExchangeWordTelemetry {
		t.Error("the downgraded node stopped exchanging word telemetry")
	}
	// ...while offering nothing.
	if p.ServeMirrors || p.AnnounceProviders || p.JoinSearchTelemetry || p.PublishOwn {
		t.Errorf("the downgraded node still offers something: %+v", p)
	}
	if n.Serving() {
		t.Error("the downgraded node reports itself as serving")
	}
}

// TestUpgradeActivatesServiceButNeverOwnObservations is the 1→2 acceptance
// criterion, on a real node.
func TestUpgradeActivatesServiceButNeverOwnObservations(t *testing.T) {
	st := testStore(t, "upgrade-caps.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelPersonal); err != nil {
		t.Fatal(err)
	}
	s := freshSupervisor(t)
	s.start(t.Context(), st)
	if s.currentNode() == nil {
		t.Skip("swarm did not start in this environment")
	}
	if s.currentNode().Serving() {
		t.Fatal("a level 1 node started in a serving state")
	}

	if _, err := s.apply(t.Context(), st, store.LevelMirror); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	n := s.currentNode()
	if n == nil {
		t.Fatal("no node running after a successful upgrade")
	}
	if !n.Serving() {
		t.Error("the upgraded node is not serving cached blocks")
	}
	p := n.Policy()
	if !p.AnnounceProviders || !p.JoinSearchTelemetry {
		t.Errorf("the upgraded node is not findable: %+v", p)
	}
	// The line a mirror must not cross.
	if p.PublishOwn {
		t.Error("an upgrade to level 2 turned on own-observation service")
	}
	// And the escalation is now durable, so a restart reconstructs it.
	got := s.state(st)
	if got.Startup != store.LevelMirror || got.Stored != store.LevelMirror {
		t.Errorf("after a successful upgrade state = %+v, want both levels at 2", got)
	}
	if got.Transition != transitionIdle {
		t.Errorf("transition = %q after a successful upgrade, want idle", got.Transition)
	}
}

// TestFailedDowngradeStaysStoppedRatherThanResurrectingLevelTwo injects a
// construction failure on the replacement node.
//
// The user asked for less. Giving them none of it is the safe direction to
// fail; bringing the old node back to "restore service" would go on serving
// blocks they just withdrew.
func TestFailedDowngradeStaysStoppedRatherThanResurrectingLevelTwo(t *testing.T) {
	st := testStore(t, "faildown.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	s := stubSupervisor(t, store.LevelMirror,
		map[int]error{store.LevelPersonal: errInjected})

	if _, err := s.apply(t.Context(), st, store.LevelPersonal); err != nil {
		t.Logf("downgrade reported: %v", err)
	}

	got := s.state(st)
	// Whatever happened to the replacement, the durable choice and the running
	// policy must both be at or below Level 1 — never back at 2.
	if got.Stored != store.LevelPersonal {
		t.Errorf("stored level = %d after a failed downgrade, want the chosen 1", got.Stored)
	}
	if got.Startup > store.LevelPersonal {
		t.Errorf("startup level = %d; a restart would resurrect the level the user left", got.Startup)
	}
	if got.Effective > store.LevelPersonal {
		t.Errorf("effective level = %d after a failed downgrade, want 0 (stopped) or 1", got.Effective)
	}
	if got.Transition != transitionFailed {
		t.Errorf("transition = %q after a failed downgrade, want %q", got.Transition, transitionFailed)
	}
}

// TestFailedUpgradeRollsBackAndDoesNotEscalateOnRestart injects a construction
// failure on the higher-level node.
func TestFailedUpgradeRollsBackAndDoesNotEscalateOnRestart(t *testing.T) {
	st := testStore(t, "failup.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelPersonal); err != nil {
		t.Fatal(err)
	}
	// Level 2 refuses to start; the Level-1 rollback succeeds.
	s := stubSupervisor(t, store.LevelPersonal,
		map[int]error{store.LevelMirror: errInjected})

	if _, err := s.apply(t.Context(), st, store.LevelMirror); err == nil {
		t.Fatal("a failed upgrade reported success")
	}

	got := s.state(st)
	if got.Startup > store.LevelPersonal {
		t.Errorf("startup level = %d after a failed upgrade; a restart would escalate", got.Startup)
	}
	if got.Effective > store.LevelPersonal {
		t.Errorf("effective level = %d after a failed upgrade", got.Effective)
	}
	if got.Stored > store.LevelPersonal {
		t.Errorf("stored level = %d; the failed choice was not rolled back", got.Stored)
	}
}

// TestFailedUpgradeThatCannotRestartEitherReportsStopped is the worse case:
// the higher level fails AND the rollback fails. Nothing may be left running,
// and nothing may be left durably escalated.
func TestFailedUpgradeThatCannotRestartEitherReportsStopped(t *testing.T) {
	st := testStore(t, "failboth.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelPersonal); err != nil {
		t.Fatal(err)
	}
	s := stubSupervisor(t, store.LevelPersonal, map[int]error{
		store.LevelMirror:   errInjected,
		store.LevelPersonal: errInjected,
	})

	if _, err := s.apply(t.Context(), st, store.LevelMirror); err == nil {
		t.Fatal("a doubly-failed transition reported success")
	}
	got := s.state(st)
	if got.Effective != 0 {
		t.Errorf("effective level = %d with no node running, want 0", got.Effective)
	}
	if got.Startup > store.LevelPersonal {
		t.Errorf("startup level = %d; a restart would escalate after a failure", got.Startup)
	}
	if got.Transition != transitionFailed || got.Detail == "" {
		t.Errorf("state = %+v, want a failed transition with an explanation", got)
	}
}

// TestDowngradeWithUnwritableStoreKeepsTheGateShut injects a persistence
// failure on the downgrade's durable step.
//
// The order matters here more than the outcome: the gate is shut before the
// write is attempted, so even a database that cannot be written leaves the
// node offering nothing. Reporting failure while still serving would be the
// unsafe resolution.
func TestDowngradeWithUnwritableStoreKeepsTheGateShut(t *testing.T) {
	st := testStore(t, "failwrite.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelMirror); err != nil {
		t.Fatal(err)
	}
	s := stubSupervisor(t, store.LevelMirror, nil)

	// Closing the database is a real write failure, not a simulated one.
	st.Close()

	if _, err := s.apply(t.Context(), st, store.LevelPersonal); err == nil {
		t.Fatal("a downgrade onto a closed database reported success")
	}
	s.stateMu.RLock()
	effective, transition := s.effective, s.transition
	s.stateMu.RUnlock()
	if effective != 0 {
		t.Errorf("effective level = %d after an unwritable downgrade, want 0 (stopped)", effective)
	}
	if transition != transitionFailed {
		t.Errorf("transition = %q, want %q", transition, transitionFailed)
	}
}

// TestNetworkRPCsDeclineDuringTransition covers the typed busy response.
// "Nothing found" and "ask again shortly" are different answers and a caller
// that cannot tell them apart will cache the wrong one.
func TestNetworkRPCsDeclineDuringTransition(t *testing.T) {
	s := freshSupervisor(t)
	s.setTransition(transitionStopping, "")
	if !s.inTransition() {
		t.Fatal("a stopping supervisor does not report itself in transition")
	}
	s.setTransition(transitionStarting, "")
	if !s.inTransition() {
		t.Fatal("a starting supervisor does not report itself in transition")
	}
	// Idle and failed are both terminal: the caller should get a real answer,
	// not be told to retry forever.
	s.setTransition(transitionIdle, "")
	if s.inTransition() {
		t.Error("an idle supervisor reports itself busy")
	}
	s.setTransition(transitionFailed, "boom")
	if s.inTransition() {
		t.Error("a failed supervisor reports itself busy; the failure would never surface")
	}
}

// TestContributionPayloadReportsEffectiveNotStored is the UI contract: the
// control must never show the stored choice as though it were in force.
func TestContributionPayloadReportsEffectiveNotStored(t *testing.T) {
	p := contributionPayload(contributionState{
		Stored: store.LevelMirror, Effective: store.LevelPersonal,
		Startup: store.LevelPersonal, Transition: transitionFailed, Detail: "could not start",
	})
	if p["level"] != store.LevelPersonal {
		t.Errorf(`payload["level"] = %v, want the effective %d`, p["level"], store.LevelPersonal)
	}
	if p["effective_level"] != store.LevelPersonal || p["stored_level"] != store.LevelMirror {
		t.Errorf("payload did not carry both levels: %v", p)
	}
	if p["levels_disagree"] != true {
		t.Error("a stored/effective mismatch was not flagged for the interface")
	}
}
