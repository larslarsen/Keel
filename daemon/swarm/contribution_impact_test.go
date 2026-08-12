// SPDX-License-Identifier: Apache-2.0
// WO-086: a request refused by policy or by the serving budget must never be
// counted as answered, and a request that is actually answered must be.
package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/store"
)

// TestBlockRequestCountsOnlyWhenPolicyServesIt proves the policy-refusal half
// of the acceptance criterion, over a real libp2p connection: a Level-1 node
// holding the data refuses and records nothing, a Level-2 node holding the
// same data answers and records exactly one request.
func TestBlockRequestCountsOnlyWhenPolicyServesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	asker := newStore(t, "asker.sqlite")
	aNode, err := Start(ctx, asker, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer aNode.Close()

	refuserSt := newStore(t, "refuser.sqlite")
	seed(t, refuserSt, "seedaaaaaaa", "targetaaaa1", 0)
	refuser, err := Start(ctx, refuserSt, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer refuser.Close()

	// requestOn directly on BlockProtocol with the hashed prefix (what
	// n.request/FetchFrom compute internally), not FetchFrom itself:
	// FetchFrom also syncs catalogue metadata for whatever it learns, which is
	// a second genuine served request and would make "one block request
	// answered exactly once" a false negative.
	prefix := store.BlockPrefix("seedaaaaaaa", aNode.prefixBits())
	if _, err := aNode.requestOn(ctx, refuser.AddrInfo(), prefix, BlockProtocol); err == nil {
		t.Fatal("expected a Level 1 node to refuse the block request")
	}
	if answered, bytesServed, _, err := refuserSt.ContributionActivity(); err != nil || answered != 0 || bytesServed != 0 {
		t.Fatalf("a refused request must not be counted as answered: answered=%d bytesServed=%d err=%v",
			answered, bytesServed, err)
	}

	serverSt := newStore(t, "server.sqlite")
	seed(t, serverSt, "seedaaaaaaa", "targetaaaa1", 0)
	server, err := Start(ctx, serverSt, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if _, err := aNode.requestOn(ctx, server.AddrInfo(), prefix, BlockProtocol); err != nil {
		t.Fatalf("expected a Level 2 node to serve the block request: %v", err)
	}
	answered, bytesServed, since, err := serverSt.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if answered != 1 {
		t.Errorf("a served request must be counted as answered exactly once, got %d", answered)
	}
	if bytesServed <= 0 {
		t.Errorf("a served request must record the bytes actually written, got %d", bytesServed)
	}
	if since == "" {
		t.Error("the first serve must set since_day")
	}
}

// TestWordTelemetryRequestCountsOnlyWhenPolicyServesIt is the second
// handler the ticket asks be proven independently — a different serve path,
// same rule.
func TestWordTelemetryRequestCountsOnlyWhenPolicyServesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	asker := newStore(t, "asker.sqlite")
	aNode, err := Start(ctx, asker, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer aNode.Close()

	refuserSt := newStore(t, "refuser.sqlite")
	seed(t, refuserSt, "seedaaaaaaa", "targetaaaa1", 0)
	refuser, err := Start(ctx, refuserSt, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer refuser.Close()

	if err := aNode.host.Connect(ctx, refuser.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := aNode.requestOn(ctx, refuser.AddrInfo(), "", WordTelemetryProtocol); err == nil {
		t.Fatal("expected a Level 1 node to refuse the word-telemetry request")
	}
	if answered, _, _, err := refuserSt.ContributionActivity(); err != nil || answered != 0 {
		t.Fatalf("a refused word-telemetry request must not be counted: answered=%d err=%v", answered, err)
	}

	serverSt := newStore(t, "server.sqlite")
	seed(t, serverSt, "seedaaaaaaa", "targetaaaa1", 0)
	server, err := Start(ctx, serverSt, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if err := aNode.host.Connect(ctx, server.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := aNode.requestOn(ctx, server.AddrInfo(), "", WordTelemetryProtocol); err != nil {
		t.Fatalf("expected a Level 2 node to serve the word-telemetry request: %v", err)
	}
	answered, bytesServed, _, err := serverSt.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	if answered != 1 {
		t.Errorf("a served word-telemetry request must be counted exactly once, got %d", answered)
	}
	if bytesServed <= 0 {
		t.Errorf("a served word-telemetry request must record its bytes, got %d", bytesServed)
	}
}

// TestBlockRequestOverBudgetIsNotCounted proves the third refusal path: a
// reply that was built but dropped by the serving byte budget must not be
// counted either, distinct from a policy refusal.
func TestBlockRequestOverBudgetIsNotCounted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	asker := newStore(t, "asker.sqlite")
	aNode, err := Start(ctx, asker, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer aNode.Close()

	serverSt := newStore(t, "server.sqlite")
	seed(t, serverSt, "seedaaaaaaa", "targetaaaa1", 0)
	server, err := Start(ctx, serverSt, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// Exhaust this window's byte budget up front so the real request's reply
	// is guaranteed to be dropped by chargeBytes, deterministically rather
	// than by racing a slow test against a real 64 MiB transfer.
	if !server.serve.chargeBytes(serveByteBudget) {
		t.Fatal("setup: could not pre-fill the byte budget")
	}

	prefix := store.BlockPrefix("seedaaaaaaa", aNode.prefixBits())
	if _, err := aNode.requestOn(ctx, server.AddrInfo(), prefix, BlockProtocol); err == nil {
		t.Fatal("expected the reply to be dropped once the serving byte budget is exhausted")
	}
	if answered, bytesServed, _, err := serverSt.ContributionActivity(); err != nil || answered != 0 || bytesServed != 0 {
		t.Fatalf("a budget-dropped reply must not be counted as answered: answered=%d bytesServed=%d err=%v",
			answered, bytesServed, err)
	}
}
