// SPDX-License-Identifier: Apache-2.0
// WO-089's consent boundary: no network before the user has accepted the
// current disclosure, enforced by the daemon rather than by a browser profile.
//
// The reason this is a daemon test rather than an extension one is the whole
// point of the ticket. The daemon is a separate long-lived process; it starts
// on its own schedule, with no browser attached, and it is what opens sockets.
// A permission stored in `chrome.storage` cannot gate it. So every assertion
// here is about the daemon refusing on its own, with no extension in the
// picture at all.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// callConsent drives one SET_NETWORK_CONSENT through handleRaw as the bridge
// would, and returns the decoded envelope.
func callConsent(t *testing.T, st *store.Store, payload any) *bridge.Envelope {
	t.Helper()
	env, err := bridge.NewEnvelope("c-1", "SET_NETWORK_CONSENT", payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := handleRaw(raw, &buf, st); err != nil {
		t.Fatal(err)
	}
	framed, err := bridge.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("response is not a validly framed message: %v", err)
	}
	got, err := bridge.ParseEnvelope(framed)
	if err != nil {
		t.Fatalf("response is not a valid envelope: %v", err)
	}
	return got
}

// TestNoNetworkStartsBeforeConsent is the release blocker, stated directly.
func TestNoNetworkStartsBeforeConsent(t *testing.T) {
	s := freshSupervisor(t)
	st := testStoreAwaitingConsent(t, "no-consent.sqlite")

	s.start(t.Context(), st)

	if n := s.currentNode(); n != nil {
		t.Fatal("a swarm node exists before the user accepted anything")
	}
	state := s.state(st)
	if state.Effective != 0 {
		t.Errorf("effective level = %d with no consent, want 0", state.Effective)
	}
	if state.Transition != transitionConsentRequired {
		t.Errorf("transition = %q, want %q", state.Transition, transitionConsentRequired)
	}
	if state.Consent.Current {
		t.Error("a fresh store reports current consent")
	}
	// The interface has to be able to tell "waiting to be asked" from "broken".
	// Both leave no node running; only one of them is answered by asking.
	if state.Transition == transitionFailed {
		t.Error("an unanswered consent screen is reported as a failure")
	}
	p := contributionPayload(state)
	if p["consent_required"] != true {
		t.Error("the payload does not tell the interface to ask")
	}
}

// TestAStoredLevelIsNotConsent is the migration rule: an install that had
// already chosen a level under the old disclosure must still be re-asked.
//
// This is the case the ticket is most concerned with, because it is the one a
// boolean would get wrong. Those users accepted a screen that said the default
// level published live sightings and answered the word protocol. The corrected
// default does neither, and their stored "2" is not agreement to the new
// sentence — it is agreement to an older one.
func TestAStoredLevelIsNotConsent(t *testing.T) {
	s := freshSupervisor(t)
	st := testStoreAwaitingConsent(t, "legacy-level.sqlite")
	if err := st.SetContributionAndStartupLevel(store.LevelBroad); err != nil {
		t.Fatal(err)
	}

	s.start(t.Context(), st)

	if n := s.currentNode(); n != nil {
		t.Fatal("a stored contribution level started a network with no consent behind it")
	}
	if got := s.state(st).Stored; got != store.LevelBroad {
		t.Errorf("stored level = %d; the choice must be preserved, only not acted on", got)
	}
	// And the corpus is untouched — being re-asked is not a data event.
	if _, err := st.Stats(); err != nil {
		t.Errorf("the corpus became unreadable while consent was pending: %v", err)
	}
}

// TestConsentStartsAndWithdrawalStopsTheNetworkWithoutRestart pairs the two
// directions, because each is only meaningful if the other works: a gate that
// never opens is indistinguishable from a broken daemon, and one that never
// closes is not a gate.
func TestConsentStartsAndWithdrawalStopsTheNetworkWithoutRestart(t *testing.T) {
	s := freshSupervisor(t)
	old := supervisor
	supervisor = s
	t.Cleanup(func() { supervisor = old })

	st := testStoreAwaitingConsent(t, "grant-withdraw.sqlite")
	s.start(t.Context(), st)
	if s.currentNode() != nil {
		t.Fatal("node exists before consent")
	}

	got := callConsent(t, st, map[string]any{
		"accepted": true, "revision": store.NetworkConsentRevision,
	})
	if got.Type != "NETWORK_CONSENT_RESULT" {
		t.Fatalf("grant answered %s: %s", got.Type, got.Payload)
	}
	if !st.NetworkConsent().Current {
		t.Fatal("consent was not persisted")
	}
	if s.currentNode() == nil {
		// A grant that only wrote a row would leave the user looking at
		// "accepted" with no network until they restarted the daemon — the
		// exact failure WO-077 fixed for contribution levels.
		t.Fatal("the network did not start on consent; a restart should not be needed")
	}
	if lvl := s.state(st).Effective; lvl != store.LevelPersonal {
		t.Errorf("effective level after consent = %d, want the default %d", lvl, store.LevelPersonal)
	}

	got = callConsent(t, st, map[string]any{"accepted": false})
	if got.Type != "NETWORK_CONSENT_RESULT" {
		t.Fatalf("withdrawal answered %s: %s", got.Type, got.Payload)
	}
	if s.currentNode() != nil {
		t.Error("the node survived a withdrawal of consent")
	}
	if st.NetworkConsent().Current {
		t.Error("consent survived its own withdrawal")
	}
	// Withdrawing permission is not a request to delete anything.
	if _, err := st.Stats(); err != nil {
		t.Errorf("withdrawing consent damaged the corpus: %v", err)
	}
}

// TestNoLevelMayBeChosenBeforeConsent covers the other way in: a client that
// skips the screen and sets a level directly must not get a network either.
func TestNoLevelMayBeChosenBeforeConsent(t *testing.T) {
	s := freshSupervisor(t)
	st := testStoreAwaitingConsent(t, "level-before-consent.sqlite")
	s.start(t.Context(), st)

	if _, err := s.apply(t.Context(), st, store.LevelBroad); err == nil {
		t.Fatal("a level was applied with no consent behind it")
	}
	if s.currentNode() != nil {
		t.Error("a node was constructed by a refused level change")
	}
	if got := st.ContributionLevel(); got != store.LevelPersonal {
		t.Errorf("stored level = %d after a refused change; nothing should have been written", got)
	}
}

// TestConsentRevisionMustMatchWhatWasShown stops a stale client from accepting
// wording it never rendered, in either direction.
func TestConsentRevisionMustMatchWhatWasShown(t *testing.T) {
	st := testStoreAwaitingConsent(t, "revisions.sqlite")

	// A client claiming a disclosure this build does not have is a version
	// mismatch. Storing it would satisfy every future gate on this database.
	if _, err := st.GrantNetworkConsent(store.NetworkConsentRevision + 5); err == nil {
		t.Error("a revision newer than this build understands was accepted")
	}
	if st.NetworkConsent().Current {
		t.Error("a rejected grant still opened the gate")
	}
	if _, err := st.GrantNetworkConsent(0); err == nil {
		t.Error("revision 0 was accepted")
	}

	if _, err := st.GrantNetworkConsent(store.NetworkConsentRevision); err != nil {
		t.Fatal(err)
	}
	c := st.NetworkConsent()
	if !c.Current || c.Revision != store.NetworkConsentRevision {
		t.Fatalf("consent = %+v, want the current revision accepted", c)
	}
	if c.AcceptedAt == 0 {
		t.Error("no acceptance timestamp was recorded")
	}

	// An old client re-sending a stale revision must be refused even once the
	// gate is already current (WO-110): the client still displayed obsolete
	// wording, and a silent "no-op success" is exactly the reply that let a
	// stale extension believe it had consented when it never named revision 2.
	if store.NetworkConsentRevision > 1 {
		if _, err := st.GrantNetworkConsent(1); err == nil {
			t.Error("an obsolete revision was accepted against an already-current gate")
		}
		if got := st.NetworkConsent().Revision; got != store.NetworkConsentRevision {
			t.Errorf("a refused stale grant changed the stored revision to %d", got)
		}
	}
}

// TestStaleConsentGrantFailsClosedAtTheBridge is WO-110's live defect,
// reproduced end to end through handleRaw rather than the Store directly: a
// browser still rendering an old disclosure sends an affirmative grant naming
// a revision behind what this daemon requires, and must get an explicit
// refusal carrying the authoritative gate state — never a reply an extension
// reading "not an ERROR" would treat as acceptance — and the network must not
// start behind it.
func TestStaleConsentGrantFailsClosedAtTheBridge(t *testing.T) {
	if store.NetworkConsentRevision < 2 {
		t.Skip("no revision below the current one exists to test against")
	}
	s := freshSupervisor(t)
	old := supervisor
	supervisor = s
	t.Cleanup(func() { supervisor = old })

	st := testStoreAwaitingConsent(t, "stale-bridge.sqlite")
	s.start(t.Context(), st)

	got := callConsent(t, st, map[string]any{
		"accepted": true, "revision": store.NetworkConsentRevision - 1,
	})
	if got.Type != "ERROR" {
		t.Fatalf("stale grant answered %s: %s", got.Type, got.Payload)
	}
	var p bridge.ErrorPayload
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != "consent_rejected" {
		t.Errorf("error code = %q, want consent_rejected", p.Code)
	}
	detail, ok := p.Detail.(map[string]any)
	if !ok {
		t.Fatalf("detail is not a structured object: %#v", p.Detail)
	}
	if detail["consent_required"] != true {
		t.Errorf("detail does not report consent still required: %#v", detail)
	}
	nc, ok := detail["network_consent"].(map[string]any)
	if !ok {
		t.Fatalf("detail has no network_consent state: %#v", detail)
	}
	if nc["current"] != false {
		t.Errorf("detail claims consent is current: %#v", nc)
	}
	if got := nc["required"]; got != float64(store.NetworkConsentRevision) {
		t.Errorf("detail required revision = %v, want %d", got, store.NetworkConsentRevision)
	}

	if s.currentNode() != nil {
		t.Fatal("a stale grant started the network")
	}
	if st.NetworkConsent().Current {
		t.Fatal("a stale grant was persisted as consent")
	}
}

// TestUnreadableConsentIsNotConsent is the fail-closed rule every other stored
// permission in this daemon follows.
func TestUnreadableConsentIsNotConsent(t *testing.T) {
	s := freshSupervisor(t)
	st := testStore(t, "unreadable.sqlite") // starts consented
	if !st.NetworkConsent().Current {
		t.Fatal("test assumption broken")
	}
	st.Close()

	if c := st.NetworkConsent(); c.Current {
		t.Error("an unreadable database reported current consent")
	}
	s.start(t.Context(), st)
	if s.currentNode() != nil {
		t.Error("a node started against an unreadable consent record")
	}
}

// TestNetworkConsentRPCsAreCapabilityGated keeps an old extension from being
// silently treated as consenting — it cannot even name the RPC.
func TestNetworkConsentRPCsAreCapabilityGated(t *testing.T) {
	for _, typ := range []string{"GET_NETWORK_CONSENT", "SET_NETWORK_CONSENT"} {
		if got := bridge.RPCCapability(typ); got != bridge.CapNetworkConsent {
			t.Errorf("RPCCapability(%s) = %q, want %q", typ, got, bridge.CapNetworkConsent)
		}
	}
	if got := bridge.DaemonCaps()[bridge.CapNetworkConsent]; got != 1 {
		t.Errorf("daemon offers network_consent:%d, want 1", got)
	}
}

// TestGetNetworkConsentReportsTheGate is the read side the consent screen and
// the migration banner both use.
func TestGetNetworkConsentReportsTheGate(t *testing.T) {
	st := testStoreAwaitingConsent(t, "read-gate.sqlite")

	env, err := bridge.NewEnvelope("g-1", "GET_NETWORK_CONSENT", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := env.Encode()
	var buf bytes.Buffer
	if err := handleRaw(raw, &buf, st); err != nil {
		t.Fatal(err)
	}
	framed, err := bridge.ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridge.ParseEnvelope(framed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "NETWORK_CONSENT_RESULT" {
		t.Fatalf("type = %q", got.Type)
	}
	var p struct {
		ConsentRequired bool `json:"consent_required"`
		ConsentRevision int  `json:"consent_revision"`
		Consent         struct {
			Current  bool `json:"current"`
			Required int  `json:"required"`
		} `json:"network_consent"`
	}
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if !p.ConsentRequired || p.Consent.Current {
		t.Errorf("payload does not report a pending consent: %s", got.Payload)
	}
	if p.ConsentRevision != store.NetworkConsentRevision ||
		p.Consent.Required != store.NetworkConsentRevision {
		t.Errorf("payload names revision %d/%d, want %d",
			p.ConsentRevision, p.Consent.Required, store.NetworkConsentRevision)
	}
}

// TestConsentRevisionMatchesExtension is the cross-language pin: the number
// the consent screen sends and the number the daemon records are one fact.
func TestConsentRevisionMatchesExtension(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "extension", "lib", "protocol.js"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`export const CONSENT_REVISION = (\d+)`)
	m := re.FindSubmatch(body)
	if m == nil {
		t.Fatal("CONSENT_REVISION not found in extension/lib/protocol.js")
	}
	got, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	if got != store.NetworkConsentRevision {
		t.Errorf("extension CONSENT_REVISION = %d, daemon NetworkConsentRevision = %d",
			got, store.NetworkConsentRevision)
	}
}
