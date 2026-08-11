// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// TestParseShard covers the untrusted-input parser handleShardRequest reads
// straight off the wire from a stranger — same reasoning as PrefixOf's own
// rejection tests: a peer can send anything on that line.
func TestParseShard(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  int
		valid bool
	}{
		{"0", 0, true},
		{"255", 255, true},
		{"", 0, false},
		{"-1", 0, false},
		{"1x", 0, false},
		{"x1", 0, false},
		{" 1", 0, false},
		{"99999999999999999999", 0, false}, // overflow-shaped, must not panic
	} {
		got, ok := parseShard(tc.in)
		if ok != tc.valid {
			t.Errorf("parseShard(%q) valid=%v, want %v", tc.in, ok, tc.valid)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parseShard(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// store.ShardM itself and anything above must be rejected — parseShard is
	// the one place that keeps an out-of-range shard from ever reaching
	// ShardSlice.
	if _, ok := parseShard(strconv.Itoa(store.ShardM)); ok {
		t.Errorf("parseShard(%d) accepted store.ShardM, which is out of range", store.ShardM)
	}
}

// TestFetchShardTagSelfFilter is WO-059's tag-self-filter requirement: a
// shard reply can legitimately hold a video that is in the shard only because
// some OTHER token of its title hashes there too. FetchShard must keep only
// entries actually tagged with the requested token, never everything the
// peer happened to send.
//
// Uses the same private-DHT discovery harness as TestPeerSearchViaDiscovery:
// FetchShard is DHT-discovery-based (like Fetch), so a direct host.Connect
// alone gives it no provider record to find.
func TestFetchShardTagSelfFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore := newStore(t, "shard-server.sqlite")
	// Two distinct titles. We don't control which shard each token lands in,
	// so the test doesn't assume they collide — it only asserts that
	// whichever shard is fetched, only genuinely-tagged videos come back.
	putTitle(t, serverStore, "recvideo001", "Recommendation systems explained")
	putTitle(t, serverStore, "pianovideo1", "Ambient piano for studying")
	server, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "shard-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !waitUntil(30*time.Second, func() bool {
		return len(server.host.Network().Peers()) > 0 && len(client.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}
	if !waitUntil(45*time.Second, func() bool {
		return server.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce its shards")
	}

	token := store.TokenizeQuery("recommendation")[0] // " rec", anchored
	var got map[string][]string
	ok := waitUntil(45*time.Second, func() bool {
		got, err = client.FetchShard(ctx, token)
		return err == nil && len(got) > 0
	})
	if !ok {
		t.Fatal("FetchShard found nothing for a token its only peer holds")
	}
	if _, ok := got["recvideo001"]; !ok {
		t.Errorf("FetchShard(%q) = %v, missing the video that actually carries it", token, got)
	}
	if _, ok := got["pianovideo1"]; ok {
		t.Errorf("FetchShard(%q) returned pianovideo1, which does not carry this token — "+
			"tag-self-filter did not run", token)
	}
	for _, tokens := range got {
		found := false
		for _, tk := range tokens {
			if tk == token {
				found = true
			}
		}
		if !found {
			t.Errorf("returned entry's own token list %v does not contain the fetched token %q", tokens, token)
		}
	}
}

// TestPeerSearchViaDiscovery mirrors TestFetchViaDiscoveryNotManualDial: the
// client never learns the server's address directly, only through the DHT
// provider record the server's Announce publishes for its shards. This is
// the falsifiable form — a search that only worked because the test wired
// the nodes together would prove nothing about whether a stranger can be
// found.
func TestPeerSearchViaDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore := newStore(t, "psearch-server.sqlite")
	putTitle(t, serverStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	server, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "psearch-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !waitUntil(30*time.Second, func() bool {
		return len(server.host.Network().Peers()) > 0 && len(client.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}
	if !waitUntil(45*time.Second, func() bool {
		return server.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce its shards")
	}
	if client.host.Network().Connectedness(server.host.ID()).String() == "Connected" {
		t.Fatal("nodes were already connected; this test would prove nothing")
	}

	var ids []string
	ok := waitUntil(45*time.Second, func() bool {
		ids, err = client.PeerSearch(ctx, "sourdough")
		if err != nil {
			t.Logf("search attempt: %v", err)
			return false
		}
		return len(ids) > 0
	})
	if !ok {
		t.Fatalf("PeerSearch found nothing via discovery alone (got %v)", ids)
	}
	found := false
	for _, id := range ids {
		if id == "findmevideo1" {
			found = true
		}
	}
	if !found {
		t.Errorf("PeerSearch(\"sourdough\") = %v, missing findmevideo1", ids)
	}
}

// putTitle writes an impression whose title is exactly what's given, unlike
// seed() (block-transfer tests) which hardcodes "Title <id>". Shard tests care
// about the title text itself, not the graph edge.
func putTitle(t *testing.T, st *store.Store, videoID, title string) {
	t.Helper()
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: videoID + "-load-4444-8444-000000000000",
		ObservedAt: time.Now().UnixMilli(), Surface: "HOME",
		SlotIndex: 0, VideoID: videoID, Title: title,
	}}); err != nil {
		t.Fatal(err)
	}
}
