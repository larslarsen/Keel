// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/store"
)

func yieldCfg(t *testing.T, serve bool) Config {
	c := isolated(serve, t)
	c.Fetch = true
	return c
}

// TestYieldVectorPropagates mirrors TestLiveRecordPropagates: one node
// publishes its own vector (an immediate first publish, not waiting for
// yieldPublishInterval — see publishYieldLoop), the other's yieldGet
// reflects it once gossip settles.
func TestYieldVectorPropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pubStore := newStore(t, "yield-pub.sqlite")
	putTitle(t, pubStore, "recvideo001", "Recommendation systems explained")
	pub, err := Start(ctx, pubStore, yieldCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	sub, err := Start(ctx, newStore(t, "yield-sub.sqlite"), yieldCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	connect(t, sub, pub)

	waitFor(t, "topic mesh", func() bool { return pub.Peers() > 0 && sub.Peers() > 0 })

	toks := store.TokenizeQuery("recommendation")
	if len(toks) == 0 {
		t.Fatal("test setup produced no tokens")
	}
	token := toks[0]

	waitFor(t, "yield vector to arrive", func() bool {
		yield, known := sub.yieldGet(pub.host.ID(), token)
		return known && yield
	})

	// A token this publisher never held should read as low-yield once the
	// vector has arrived (known=true, yield=false), not "unknown".
	neverHeld := "zzz"
	if _, ok := store.TokenDictIndex(neverHeld); !ok {
		t.Fatal("test assumption broken: \"zzz\" is not a valid dictionary token")
	}
	yield, known := sub.yieldGet(pub.host.ID(), neverHeld)
	if !known {
		t.Error("vector arrived but an untouched token still reads as unknown")
	}
	if yield {
		t.Error("a token the publisher never held reads as high-yield")
	}
}

// TestYieldGetUnknownBeforeAnyGossip is the fallback behavior: before any
// vector has been received from a peer, yieldGet must say "unknown", not
// "low yield" — the screen in FetchShard only removes candidates it has
// positive evidence against.
func TestYieldGetUnknownBeforeAnyGossip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	n, err := Start(ctx, newStore(t, "yield-unknown.sqlite"), yieldCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	yield, known := n.yieldGet(n.host.ID(), " rec")
	if known {
		t.Error("yieldGet reported known=true for a peer that never gossiped anything")
	}
	if yield {
		t.Error("yieldGet reported yield=true alongside known=false")
	}
}

// TestValidateYieldMessageRejectsWrongSize mirrors TestLiveValidatorRejectsJunk.
func TestValidateYieldMessageRejectsWrongSize(t *testing.T) {
	cases := [][]byte{
		{},
		make([]byte, store.YieldVectorBytes-1),
		make([]byte, store.YieldVectorBytes+1),
		make([]byte, 1),
	}
	for _, c := range cases {
		if validateYieldMessage(context.Background(), "", newMsg(c)) {
			t.Errorf("validateYieldMessage accepted a %d-byte payload, want %d", len(c), store.YieldVectorBytes)
		}
	}
	if !validateYieldMessage(context.Background(), "", newMsg(make([]byte, store.YieldVectorBytes))) {
		t.Error("validateYieldMessage rejected a correctly-sized payload")
	}
}
