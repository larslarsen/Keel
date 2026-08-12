// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

// callPeerSearch drives handleRaw exactly as the bridge would: a framed
// PEER_SEARCH envelope in, a parsed PEER_SEARCH_RESULT decoded out.
func callPeerSearch(t *testing.T, st *store.Store, query string) bridge.PeerSearchResultPayload {
	t.Helper()
	env, err := bridge.NewEnvelope("req-1", "PEER_SEARCH", bridge.SearchPayload{Query: query, Limit: 50})
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
	if got.Type != "PEER_SEARCH_RESULT" {
		t.Fatalf("got envelope type %q, want PEER_SEARCH_RESULT (payload: %s)", got.Type, got.Payload)
	}
	var p bridge.PeerSearchResultPayload
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatalf("PEER_SEARCH_RESULT payload did not decode: %v", err)
	}
	return p
}

// TestPeerSearchUnavailableWhenSwarmNil mirrors handleLiveSearch's contract:
// no running swarm must answer "unavailable", never a bare empty result that
// reads as "the network has nothing for this query."
func TestPeerSearchUnavailableWhenSwarmNil(t *testing.T) {
	restore := adoptNodeForTest(nil)
	t.Cleanup(restore)

	st, err := store.Open(filepath.Join(t.TempDir(), "peer-search-nil.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// WO-085: with no node there is no effective policy to read, so the
	// entitlement falls back to the stored choice. This test is about the
	// nil-swarm answer, so the store must be at a level that would be allowed
	// to search — otherwise the refusal, not the unavailability, is what gets
	// tested.
	if _, err := st.SetContributionLevel(store.LevelBroad); err != nil {
		t.Fatal(err)
	}

	got := callPeerSearch(t, st, "anything")
	if got.Available {
		t.Fatal("Available = true with no swarm running")
	}
	if len(got.Hits) != 0 {
		t.Errorf("Hits = %v, want empty when unavailable", got.Hits)
	}
}

// callPeerSearchErr drives handleRaw the same way but expects the typed
// refusal rather than a result.
func callPeerSearchErr(t *testing.T, st *store.Store, query string) bridge.ErrorPayload {
	t.Helper()
	env, err := bridge.NewEnvelope("req-1", "PEER_SEARCH", bridge.SearchPayload{Query: query, Limit: 50})
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
	if got.Type != "ERROR" {
		t.Fatalf("got envelope type %q, want ERROR (payload: %s)", got.Type, got.Payload)
	}
	var p bridge.ErrorPayload
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatalf("ERROR payload did not decode: %v", err)
	}
	return p
}

// TestLevelOnePeerSearchIsRefusedNotEmptied is WO-085's RPC contract.
//
// The three answers a client can get here are different and must stay
// distinguishable: no results, no network, and no entitlement. This asserts the
// third — a running Level-1 node, which could reach peers, refuses with a code
// naming the setting that would change it. An empty PEER_SEARCH_RESULT would
// be a lie about the network, and a bare ERROR would leave the interface
// nothing to offer the user.
func TestLevelOnePeerSearchIsRefusedNotEmptied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(filepath.Join(t.TempDir(), "peer-search-level1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	n, err := swarm.Start(ctx, st, swarm.Config{
		Policy:    swarm.PolicyForLevel(store.LevelPersonal),
		Bootstrap: []peer.AddrInfo{}, ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	t.Cleanup(adoptNodeForTest(n))

	got := callPeerSearchErr(t, st, "sourdough")
	if got.Code != bridge.CodeContributionRequired {
		t.Errorf("code = %q, want %q", got.Code, bridge.CodeContributionRequired)
	}
	if got.Message == "" {
		t.Error("the refusal carried no message; an old client would show nothing")
	}
	// The detail is what the interface disables a control from and routes the
	// user with, so its absence is a UI failure even when the code is right.
	raw, err := json.Marshal(got.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var d bridge.ContributionRequiredDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("detail did not decode as ContributionRequiredDetail: %v", err)
	}
	if d.Capability != bridge.CapDistributedSearch {
		t.Errorf("detail capability = %q, want %q", d.Capability, bridge.CapDistributedSearch)
	}
	if d.RequiredLevel != store.LevelBroad {
		t.Errorf("detail required_level = %d, want %d", d.RequiredLevel, store.LevelBroad)
	}

	// Local search over this device is explicitly untouched by the boundary.
	if _, err := st.SearchVideos("sourdough", 10); err != nil {
		t.Errorf("local search broke at Level 1: %v", err)
	}
}

// privateDHT starts one server-mode DHT for the client/server pair to
// bootstrap from, so discovery goes through a real announce → provider-lookup
// chain rather than a direct dial — mirrors daemon/swarm's discovery_test.go,
// duplicated here rather than shared because it is a handful of lines and the
// two packages have no test-helper package between them.
func privateDHT(t *testing.T, ctx context.Context) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	d, err := dht.New(h, dht.Mode(dht.ModeServer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// TestPeerSearchZeroPeersRespondsUnderClientCap is WO-070's regression: a
// running swarm with zero connected peers (distinct from no swarm at all,
// covered above) must reply well under the extension's 8s client-side
// request timeout, not fall into the slow per-token shard-fetch path only
// to time out there.
func TestPeerSearchZeroPeersRespondsUnderClientCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(filepath.Join(t.TempDir(), "peer-search-zero.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// A Level-2 policy since WO-085: this test is about the transport's
	// zero-peer fast path, so the node must be entitled to search at all —
	// otherwise it would answer instantly for the wrong reason.
	n, err := swarm.Start(ctx, st, swarm.Config{
		Policy:    swarm.PolicyForLevel(store.LevelBroad),
		Bootstrap: []peer.AddrInfo{}, ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	restore := adoptNodeForTest(n)
	t.Cleanup(restore)

	const clientCap = 8 * time.Second
	start := time.Now()
	got := callPeerSearch(t, st, "machine learning")
	elapsed := time.Since(start)

	if elapsed >= clientCap {
		t.Fatalf("PEER_SEARCH with zero peers took %v, want well under the %v client cap", elapsed, clientCap)
	}
	if !got.Available {
		t.Error("Available = false with a running swarm (just zero peers)")
	}
	if len(got.Hits) != 0 {
		t.Errorf("Hits = %v, want empty with zero peers", got.Hits)
	}
	if len(got.Progress) != 0 {
		t.Errorf("Progress = %v, want empty — a phantom progress bar with no peers to fetch from is the bug this fixes", got.Progress)
	}
}

// TestPeerSearchRoundTrip is the RPC-layer half of WO-059: a real (private
// DHT, loopback) peer holds a video, this daemon's handleRaw dispatches
// PEER_SEARCH into swarmNode.PeerSearch + st.TitlesFor exactly as the
// extension's request does, and the titled hit comes back through
// PEER_SEARCH_RESULT.
func TestPeerSearchRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore, err := store.Open(filepath.Join(t.TempDir(), "peer-search-server.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer serverStore.Close()
	if _, err := serverStore.PutImpressions([]bridge.Impression{{
		PageLoadID: "peer-search-load-0001", ObservedAt: time.Now().UnixMilli(),
		Surface: "HOME", SlotIndex: 0, VideoID: "findmeviaday1",
		Title: "A distinctive sourdough baking tutorial",
	}}); err != nil {
		t.Fatal(err)
	}
	server, err := swarm.Start(ctx, serverStore, swarm.Config{
		Policy:    swarm.PolicyForLevel(store.LevelCohort),
		Bootstrap: []peer.AddrInfo{bootInfo}, ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore, err := store.Open(filepath.Join(t.TempDir(), "peer-search-client.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer clientStore.Close()
	// Level 2, not Level 1: distributed search is reciprocal (WO-085), and a
	// Level-1 client would be refused before the RPC reached the swarm at all.
	client, err := swarm.Start(ctx, clientStore, swarm.Config{
		Policy:    swarm.PolicyForLevel(store.LevelBroad),
		Bootstrap: []peer.AddrInfo{bootInfo}, ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	restore := adoptNodeForTest(client)
	t.Cleanup(restore)

	if !waitUntil(30*time.Second, func() bool {
		return server.Peers() > 0 && client.Peers() > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}
	if !waitUntil(45*time.Second, func() bool {
		return server.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce its shards")
	}

	var got bridge.PeerSearchResultPayload
	ok := waitUntil(45*time.Second, func() bool {
		got = callPeerSearch(t, clientStore, "sourdough")
		return len(got.Hits) > 0
	})
	if !ok {
		t.Fatalf("PEER_SEARCH found nothing via discovery alone (available=%v hits=%v)", got.Available, got.Hits)
	}
	if !got.Available {
		t.Error("Available = false despite a running swarm")
	}
	// The title itself does NOT travel over the shard protocol (only tokens
	// do — see store.ShardEntry), and TitlesFor deliberately does not
	// backfill it with a live catalogue fetch (shard.go's TitlesFor doc
	// comment explains why: fetching catalogue buckets for exactly a search
	// result's ids would bind that fetch to the query). So a genuinely novel
	// find comes back titled or not depending only on what this node already
	// knew — here, nothing — and the hit must still be present rather than
	// dropped. store.TestTitlesForReturnsEveryIDTitledOrNot covers title
	// resolution itself; this test is about the RPC wiring around it.
	found := false
	for _, h := range got.Hits {
		if h.VideoID == "findmeviaday1" {
			found = true
			if h.Title != "" {
				t.Errorf("hit title = %q, want empty — this client never learned it", h.Title)
			}
		}
	}
	if !found {
		t.Errorf("PEER_SEARCH_RESULT hits = %v, missing findmeviaday1", got.Hits)
	}
	// Build 4's wire contract: PEER_SEARCH_RESULT carries one progress entry
	// per distinct query token so the search UI can draw coverage without
	// receiving the token text itself.
	wantTokens := len(store.TokenizeQuery("sourdough"))
	if len(got.Progress) != wantTokens {
		t.Errorf("progress has %d entries, want %d (one per distinct token)", len(got.Progress), wantTokens)
	}
	for _, p := range got.Progress {
		if p.Known {
			t.Errorf("token_index %d reports known=true on a client's first-ever search", p.TokenIndex)
		}
	}
}
