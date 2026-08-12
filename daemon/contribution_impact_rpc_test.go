// SPDX-License-Identifier: Apache-2.0
// WO-086: GET_CONTRIBUTION_IMPACT/RESET_CONTRIBUTION_IMPACT over the bridge.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

func callContributionImpact(t *testing.T, st *store.Store, rpcType string) (*bridge.ContributionImpactPayload, *bridge.ErrorPayload) {
	t.Helper()
	env, err := bridge.NewEnvelope("req-1", rpcType, map[string]any{})
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
	switch got.Type {
	case "CONTRIBUTION_IMPACT_RESULT":
		var p bridge.ContributionImpactPayload
		if err := json.Unmarshal(got.Payload, &p); err != nil {
			t.Fatalf("CONTRIBUTION_IMPACT_RESULT payload did not decode: %v", err)
		}
		return &p, nil
	case "ERROR":
		var e bridge.ErrorPayload
		if err := json.Unmarshal(got.Payload, &e); err != nil {
			t.Fatalf("ERROR payload did not decode: %v", err)
		}
		return nil, &e
	default:
		t.Fatalf("got envelope type %q, want CONTRIBUTION_IMPACT_RESULT or ERROR", got.Type)
		return nil, nil
	}
}

// TestContributionImpactRefusedAtLevelOne is the server-side defense-in-depth
// half of WO-086: even though the extension gates this client-side off
// effective_level (mirroring the Live tab), a running Level-1 node must still
// refuse with the same typed, actionable refusal PEER_SEARCH uses.
func TestContributionImpactRefusedAtLevelOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(filepath.Join(t.TempDir(), "impact-level1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	n, err := swarm.Start(ctx, st, swarm.Config{
		Policy: swarm.PolicyForLevel(store.LevelPersonal), Bootstrap: []peer.AddrInfo{},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	t.Cleanup(adoptNodeForTest(n))

	_, gotErr := callContributionImpact(t, st, "GET_CONTRIBUTION_IMPACT")
	if gotErr == nil {
		t.Fatal("expected a refusal at Level 1")
	}
	if gotErr.Code != bridge.CodeContributionRequired {
		t.Errorf("code = %q, want %q", gotErr.Code, bridge.CodeContributionRequired)
	}
	raw, err := json.Marshal(gotErr.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var d bridge.ContributionRequiredDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("detail did not decode as ContributionRequiredDetail: %v", err)
	}
	if d.Capability != bridge.CapContributionImpact {
		t.Errorf("detail capability = %q, want %q", d.Capability, bridge.CapContributionImpact)
	}
	if d.RequiredLevel != store.LevelBroad {
		t.Errorf("detail required_level = %d, want %d", d.RequiredLevel, store.LevelBroad)
	}
}

// TestContributionImpactUnavailableWithNoSwarm mirrors PEER_SEARCH's contract
// (TestPeerSearchUnavailableWhenSwarmNil): no running swarm answers
// Available:false, never an error and never an invented zero read as real.
func TestContributionImpactUnavailableWithNoSwarm(t *testing.T) {
	restore := adoptNodeForTest(nil)
	t.Cleanup(restore)

	st, err := store.Open(filepath.Join(t.TempDir(), "impact-no-swarm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.SetContributionLevel(store.LevelBroad); err != nil {
		t.Fatal(err)
	}

	got, gotErr := callContributionImpact(t, st, "GET_CONTRIBUTION_IMPACT")
	if gotErr != nil {
		t.Fatalf("unexpected refusal with no swarm running: %+v", gotErr)
	}
	if got.Available {
		t.Fatal("Available = true with no swarm running")
	}
}

// TestContributionImpactPopulatedAtLevelTwoAndResettable is the round trip:
// a real Level-2 node answers with populated numbers, and resetting zeroes
// the persisted counters that a follow-up GET reflects.
func TestContributionImpactPopulatedAtLevelTwoAndResettable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(filepath.Join(t.TempDir(), "impact-level2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctxVid := "seedaaaaaaa"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111", ObservedAt: time.Now().UnixMilli(),
		Surface: "WATCH_NEXT", ContextVideoID: &ctxVid, SlotIndex: 0, VideoID: "targetaaaa1", Title: "T1",
	}}); err != nil {
		t.Fatal(err)
	}

	n, err := swarm.Start(ctx, st, swarm.Config{
		Policy: swarm.PolicyForLevel(store.LevelBroad), Bootstrap: []peer.AddrInfo{},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	t.Cleanup(adoptNodeForTest(n))

	if err := st.RecordContributionServe(4096); err != nil {
		t.Fatal(err)
	}

	got, gotErr := callContributionImpact(t, st, "GET_CONTRIBUTION_IMPACT")
	if gotErr != nil {
		t.Fatalf("unexpected refusal at Level 2: %+v", gotErr)
	}
	if !got.Available {
		t.Fatal("Available = false with a running Level 2 node")
	}
	if got.RequestsAnswered != 1 || got.BytesServed != 4096 {
		t.Errorf("RequestsAnswered=%d BytesServed=%d, want 1 and 4096", got.RequestsAnswered, got.BytesServed)
	}
	if got.GraphClaimsLocal == 0 {
		t.Error("expected the seeded impression to be counted as an eligible local claim")
	}

	reset, resetErr := callContributionImpact(t, st, "RESET_CONTRIBUTION_IMPACT")
	if resetErr != nil {
		t.Fatalf("unexpected refusal resetting at Level 2: %+v", resetErr)
	}
	if reset.RequestsAnswered != 0 || reset.BytesServed != 0 {
		t.Errorf("reset did not zero the counters: RequestsAnswered=%d BytesServed=%d",
			reset.RequestsAnswered, reset.BytesServed)
	}
	// The corpus-state half must be unaffected by a reset scoped to the
	// activity counters — the two are unrelated data.
	if reset.GraphClaimsLocal != got.GraphClaimsLocal {
		t.Errorf("reset changed GraphClaimsLocal: got %d, want %d", reset.GraphClaimsLocal, got.GraphClaimsLocal)
	}

	again, againErr := callContributionImpact(t, st, "GET_CONTRIBUTION_IMPACT")
	if againErr != nil {
		t.Fatalf("unexpected refusal on the follow-up GET: %+v", againErr)
	}
	if again.RequestsAnswered != 0 || again.BytesServed != 0 {
		t.Errorf("follow-up GET after reset = %+v, want zeroed counters", again)
	}
}
