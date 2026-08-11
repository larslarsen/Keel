// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/store"
	"github.com/libp2p/go-libp2p/core/peer"
)

// TestWordTelemetryDirectFetch is WO-068's multi-node contract: a serving
// peer with titled videos answers WordTelemetryProtocol; the client merges
// and reports a non-zero distinct-word estimate plus a per-word % for a
// word present on the server. Transport is direct fetch, not gossip.
func TestWordTelemetryDirectFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore := newStore(t, "word-server.sqlite")
	putTitle(t, serverStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	putTitle(t, serverStore, "findmevideo2", "Sourdough starter tips")
	server, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "word-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Direct connect — word telemetry is on-demand between already-known
	// peers, not DHT provider discovery. Mirror live/yield test harness.
	connect(t, client, server)
	if !waitUntil(15*time.Second, func() bool {
		return client.Peers() > 0 && server.Peers() > 0
	}) {
		t.Fatal("client and server never became peers")
	}

	stats, err := client.FetchWordStats(ctx, "sourdough trading")
	if err != nil {
		t.Fatal(err)
	}
	ok := stats.Available && stats.Peers > 0
	if !ok {
		t.Fatalf("FetchWordStats got no peer pack (available=%v peers=%d words=%d)",
			stats.Available, stats.Peers, stats.DistinctWords)
	}
	if stats.DistinctWords == 0 {
		t.Error("distinct_words = 0 after merging a peer with titled videos")
	}
	found := false
	for _, w := range stats.Words {
		if w.Word == "sourdough" {
			found = true
			if w.Pct == nil || *w.Pct <= 0 {
				t.Errorf("sourdough pct = %v, want > 0", w.Pct)
			}
			if len(w.Tokens) == 0 {
				t.Error("sourdough should carry char-token sub-bars")
			}
		}
		if w.Word == "the" {
			t.Error("stopword 'the' must not appear in Words")
		}
	}
	if !found {
		t.Errorf("Words = %+v, missing sourdough", stats.Words)
	}
}

func TestMedianFilterWordPacks(t *testing.T) {
	mk := func(n uint64) wordPeerPack {
		p := store.NewWordTelemetry()
		// DistinctWords won't match n exactly — we set words field directly.
		return wordPeerPack{words: n, pack: p}
	}
	// <3 peers: keep all
	two := []wordPeerPack{mk(10), mk(10_000)}
	if got := medianFilterWordPacks(two); len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	// Outlier inflated pack dropped
	three := []wordPeerPack{mk(100), mk(110), mk(100_000)}
	got := medianFilterWordPacks(three)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (outlier dropped), got words %v", len(got),
			func() []uint64 {
				var v []uint64
				for _, g := range got {
					v = append(v, g.words)
				}
				return v
			}())
	}
}
