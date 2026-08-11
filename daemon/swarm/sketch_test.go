// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/store"
	"github.com/libp2p/go-libp2p/core/peer"
)

func sketchCfg(t *testing.T, serve bool) Config {
	c := isolated(serve, t)
	c.Fetch = true
	return c
}

// TestSketchGossipPropagates mirrors TestYieldVectorPropagates: one node
// searches locally (which is what schedules a token for gossip via
// RecordTokenSearch), the other's TokenEstimate reflects it once the
// publish tick fires and gossip settles.
func TestSketchGossipPropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pubStore := newStore(t, "sketch-pub.sqlite")
	// A real search is what RecordTokenSearch needs — simulate what
	// FetchShard does after fetching, without needing a full network fetch
	// for this test's purpose (that path is covered by
	// TestFetchShardTagSelfFilter and the stop-condition tests).
	if err := pubStore.RecordTokenSearch(" re", []string{"vid00000001", "vid00000002"}); err != nil {
		t.Fatal(err)
	}
	pub, err := Start(ctx, pubStore, sketchCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	sub, err := Start(ctx, newStore(t, "sketch-sub.sqlite"), sketchCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	connect(t, sub, pub)

	waitFor(t, "topic mesh", func() bool { return pub.Peers() > 0 && sub.Peers() > 0 })

	waitFor(t, "sketch to arrive", func() bool {
		_, known := sub.st.TokenEstimate(" re")
		return known
	})
	got, known := sub.st.TokenEstimate(" re")
	if !known {
		t.Fatal("estimate never arrived")
	}
	if got == 0 {
		t.Error("estimate arrived but reads as 0 for a token with 2 real videos recorded")
	}
}

// TestValidateSketchMessageRejectsMalformed covers the untrusted-input
// checks a gossip peer's message must pass before merge: size, JSON shape,
// index range, and precision (P must equal store.TokenSketchP exactly, or
// Merge would simply fail later — reject before that wasted attempt).
func TestValidateSketchMessageRejectsMalformed(t *testing.T) {
	valid := sketchGossipMsg{I: 5, P: store.TokenSketchP, R: make([]byte, 1<<store.TokenSketchP)}
	validRaw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if !validateSketchMessage(context.Background(), "", newMsg(validRaw)) {
		t.Fatal("validateSketchMessage rejected a well-formed message")
	}

	cases := map[string][]byte{
		"empty":      {},
		"not json":   []byte("not json"),
		"oversized":  make([]byte, maxSketchMessageBytes+1),
		"index -1":   mustJSON(t, sketchGossipMsg{I: -1, P: store.TokenSketchP, R: valid.R}),
		"index huge": mustJSON(t, sketchGossipMsg{I: store.TokenDictSize, P: store.TokenSketchP, R: valid.R}),
		"wrong P":    mustJSON(t, sketchGossipMsg{I: 5, P: store.TokenSketchP + 1, R: valid.R}),
		"short regs": mustJSON(t, sketchGossipMsg{I: 5, P: store.TokenSketchP, R: make([]byte, 4)}),
	}
	for name, raw := range cases {
		if validateSketchMessage(context.Background(), "", newMsg(raw)) {
			t.Errorf("validateSketchMessage accepted case %q", name)
		}
	}
}

// TestSketchOverLimitCapsPerPeer is the anti-flood rate limit: once a peer
// crosses maxSketchMessagesPerPeerPerTick within a tick window, further
// messages from it are rejected until resetLimits runs.
func TestSketchOverLimitCapsPerPeer(t *testing.T) {
	si := &SketchIndex{perPeer: map[peer.ID]int{}}
	p := peer.ID("test-peer")
	for i := 0; i < maxSketchMessagesPerPeerPerTick; i++ {
		if si.overLimit(p) {
			t.Fatalf("overLimit tripped early at message %d of %d", i+1, maxSketchMessagesPerPeerPerTick)
		}
	}
	if !si.overLimit(p) {
		t.Errorf("overLimit did not trip after %d messages", maxSketchMessagesPerPeerPerTick+1)
	}
	si.resetLimits()
	if si.overLimit(p) {
		t.Error("overLimit still tripped immediately after resetLimits")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
