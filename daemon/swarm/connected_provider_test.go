// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

type resultEvents struct {
	results chan bridge.SearchHit
}

func (e *resultEvents) TokenPhase(int, int, string, string) {}
func (e *resultEvents) WordProgress(int, int)               {}
func (e *resultEvents) Result(hit bridge.SearchHit) {
	select {
	case e.results <- hit:
	default:
	}
}

func waitForProtocols(t *testing.T, n *Node, id peer.ID, protocols ...protocol.ID) {
	t.Helper()
	if !waitUntil(5*time.Second, func() bool {
		got, err := n.host.Peerstore().SupportsProtocols(id, protocols...)
		return err == nil && len(got) == len(protocols)
	}) {
		t.Fatalf("peer %s never advertised protocols %v", id, protocols)
	}
}

// TestConnectedPeerProducesAResultWhileDHTDiscoveryIsStalled is the failed
// WO-095 two-machine run in a controlled harness.
//
// The provider directory never returns. A compatible peer is already connected
// and holds both the shard candidate and its public title. The result must cross
// both broad protocols before the DHT channel is released; otherwise either the
// shard or catalogue path has put discovery back in front of direct reachability.
func TestConnectedPeerProducesAResultWhileDHTDiscoveryIsStalled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	serverStore := newStore(t, "connected-search-server.sqlite")
	putTitle(t, serverStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	server, err := Start(ctx, serverStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "connected-search-client.sqlite")
	client, err := Start(ctx, clientStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.host.Connect(ctx, server.AddrInfo()); err != nil {
		t.Fatal(err)
	}
	waitForProtocols(t, client, server.ID(), ShardProtocol, CatalogueProtocol)

	prefix := store.CataloguePrefix("findmevideo1", client.prefixBits())
	catalogueCID, err := prefixCID("catalogue/" + prefix)
	if err != nil {
		t.Fatal(err)
	}
	var catalogueLookups atomic.Int32
	lookupStarted := make(chan struct{}, 1)
	client.providerLookup = func(lookupCtx context.Context, c cid.Cid, _ int) <-chan peer.AddrInfo {
		if c.Equals(catalogueCID) {
			catalogueLookups.Add(1)
		}
		select {
		case lookupStarted <- struct{}{}:
		default:
		}
		out := make(chan peer.AddrInfo)
		go func() {
			defer close(out)
			<-lookupCtx.Done()
		}()
		return out
	}

	plan := store.BuildQueryPlan("sourdough")
	searchCtx, stopSearch := context.WithCancel(ctx)
	events := &resultEvents{results: make(chan bridge.SearchHit, 4)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.StreamingSearch(searchCtx, plan, map[int]store.WordTarget{}, 10, events)
	}()

	select {
	case hit := <-events.results:
		if hit.VideoID != "findmevideo1" || hit.Title == "" {
			t.Fatalf("direct result = %+v, want the connected peer's titled candidate", hit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connected peer produced no result before stalled DHT discovery")
	}
	if got := catalogueLookups.Load(); got != 0 {
		t.Fatalf("catalogue provider discovery ran %d time(s) before using a connected complete responder", got)
	}

	// At least one token is still expanding after its direct response, which
	// proves the directory really is held open rather than replaced by an empty
	// fast path. The streamed result above did not wait for this expansion.
	select {
	case <-lookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("test never reached the deliberately stalled provider lookup")
	}

	stopSearch()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling a search did not release its stalled provider lookup")
	}
}

// TestDirectCatalogueFailureExpandsThroughDHT proves connected-first is an
// ordering rule, not a replacement for provider discovery. A malformed direct
// responder remains `invalid`; a new DHT provider can still resolve the same
// complete broad prefix.
func TestDirectCatalogueFailureExpandsThroughDHT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bad, err := Start(ctx, newStore(t, "direct-bad.sqlite"), isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	var badRequests atomic.Int32
	bad.host.SetStreamHandler(CatalogueProtocol, func(s network.Stream) {
		badRequests.Add(1)
		garbageHandler(s)
	})

	goodStore := newStore(t, "dht-good.sqlite")
	putTitle(t, goodStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	good, err := Start(ctx, goodStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()

	client, err := Start(ctx, newStore(t, "direct-expand-client.sqlite"), isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.host.Connect(ctx, bad.AddrInfo()); err != nil {
		t.Fatal(err)
	}
	waitForProtocols(t, client, bad.ID(), CatalogueProtocol)

	var lookups atomic.Int32
	client.providerLookup = func(context.Context, cid.Cid, int) <-chan peer.AddrInfo {
		lookups.Add(1)
		out := make(chan peer.AddrInfo, 2)
		// Repeating the direct peer proves source deduplication on the prefix
		// path; the valid new provider after it proves DHT expansion remains.
		out <- bad.AddrInfo()
		out <- good.AddrInfo()
		close(out)
		return out
	}

	prefix := store.CataloguePrefix("findmevideo1", client.prefixBits())
	res, err := client.fetchCataloguePrefixQuiet(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != catalogueComplete || res.Rows == 0 {
		t.Fatalf("DHT expansion after invalid direct peer = %+v, want complete rows", res)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("provider discovery ran %d times, want one expansion after direct failure", got)
	}
	if got := badRequests.Load(); got != 1 {
		t.Fatalf("catalogue peer repeated by connected and DHT sources received %d requests, want 1", got)
	}
}

// TestConnectedProviderMustAdvertiseTheExactProtocol prevents "connected"
// from becoming a synonym for "ask every public-IPFS routing peer".
func TestConnectedProviderMustAdvertiseTheExactProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Start(ctx, newStore(t, "protocol-client.sqlite"), isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	unrelated, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer unrelated.Close()
	if err := client.host.Connect(ctx, peer.AddrInfo{ID: unrelated.ID(), Addrs: unrelated.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(5*time.Second, func() bool {
		_, err := client.host.Peerstore().Get(unrelated.ID(), "AgentVersion")
		return err == nil
	}) {
		t.Fatal("identify did not complete for the unrelated connected peer")
	}
	if client.Peers() == 0 {
		t.Fatal("test setup: unrelated peer is not connected")
	}
	if got := client.connectedProviders(ShardProtocol, maxProvidersPerToken); len(got) != 0 {
		t.Errorf("unrelated connected peer nominated for shard requests: %+v", got)
	}
	if got := client.connectedProviders(CatalogueProtocol, maxCatalogueDHTProviders); len(got) != 0 {
		t.Errorf("unrelated connected peer nominated for catalogue requests: %+v", got)
	}
}

// TestProviderSourcesAreDeduplicatedPerToken puts one server in every source:
// connected, remembered and DHT. Its broad shard handler must run once.
func TestProviderSourcesAreDeduplicatedPerToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	serverStore := newStore(t, "dedupe-server.sqlite")
	putTitle(t, serverStore, "worldvideo1", "The world today")
	server, err := Start(ctx, serverStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	var shardRequests atomic.Int32
	server.host.SetStreamHandler(ShardProtocol, func(s network.Stream) {
		shardRequests.Add(1)
		server.handleShardRequest(s)
	})

	client, err := Start(ctx, newStore(t, "dedupe-client.sqlite"), isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.host.Connect(ctx, server.AddrInfo()); err != nil {
		t.Fatal(err)
	}
	waitForProtocols(t, client, server.ID(), ShardProtocol, CatalogueProtocol)
	client.remember(server.AddrInfo())
	var lookupMax atomic.Int32
	client.providerLookup = func(_ context.Context, _ cid.Cid, max int) <-chan peer.AddrInfo {
		lookupMax.Store(int32(max))
		out := make(chan peer.AddrInfo, 1)
		out <- server.AddrInfo()
		close(out)
		return out
	}

	st, _ := planState(t, "world", map[int]store.WordTarget{})
	st.n = client
	client.runTokenWork(ctx, st, tokenFor(st), client.searchSem)
	if got := shardRequests.Load(); got != 1 {
		t.Fatalf("one peer nominated by connected, remembered and DHT sources received %d shard requests, want 1", got)
	}
	if got := lookupMax.Load(); got != maxProvidersPerToken-1 {
		t.Fatalf("DHT expansion received provider cap %d after one direct candidate, want %d",
			got, maxProvidersPerToken-1)
	}
}
