// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// privateDHT starts one server-mode DHT node for the other test nodes to
// bootstrap from.
//
// A real DHT is the whole point. Every other multi-node test in this package
// hands one node the other's address and calls Connect, which proves the
// transport works and says nothing about whether two nodes can *find* each
// other — the step WO-058 flags as never having been confirmed end to end.
//
// The two Keel nodes stay in their normal auto mode. On loopback with no
// AutoNAT they will settle as DHT clients, which is the interesting case:
// clients still publish and look up provider records, they just do it through
// a server. If discovery works here it works for a node behind a NAT.
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

// bootstrappedTo builds a config that joins only our private DHT.
//
// Fetch is set explicitly. isolated() leaves it false — that is Level 1, where
// a node asks nobody anything — and Fetch() then returns (0, nil) without
// touching the network. A discovery test written against that config passes its
// own polling loop and proves nothing, which is the same "silently zero"
// failure shape the tests here exist to catch.
func bootstrappedTo(boot peer.AddrInfo, serve bool, t *testing.T) Config {
	cfg := isolated(serve, t)
	cfg.Policy.Fetch = true
	cfg.Bootstrap = []peer.AddrInfo{boot}
	return cfg
}

// waitUntil polls until cond holds, so a test never depends on how long the DHT
// takes to settle. Unlike live_test.go's waitFor it returns rather than fatals,
// because a DHT step needs a longer budget than gossip and its failure message
// has to explain which link of the chain broke.
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

// TestFetchViaDiscoveryNotManualDial is WO-062 §4's named gap and the loopback
// proof WO-058 asks for: the full announce → provider lookup → dial → transfer
// chain, with the fetching node never told where the serving node is.
//
// The distinction matters because the two halves fail independently. Every
// existing transfer test would still pass if Announce published nothing at all,
// or if prefixCID derived a different key on each node — the manual Connect
// hides both. Here, the only way A can reach B is a provider record B put in
// the DHT under a key A derived independently.
func TestFetchViaDiscoveryNotManualDial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	// B holds a neighbourhood and serves it.
	serverStore := newStore(t, "disc-server.sqlite")
	seed(t, serverStore, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, serverStore, "seedaaaaaaa", "targetaaaa2", 1)
	b, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// A holds nothing and does not serve.
	clientStore := newStore(t, "disc-client.sqlite")
	a, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// Both must have found the bootstrap node before anything can be published
	// or looked up through it.
	if !waitUntil(30*time.Second, func() bool {
		return len(b.host.Network().Peers()) > 0 && len(a.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}

	// B publishes provider records for the buckets it holds. Retried because a
	// freshly bootstrapped routing table may have nowhere to put them yet.
	if !waitUntil(45*time.Second, func() bool {
		return b.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce")
	}

	// A has never been given B's address. Everything it needs — which bucket
	// the key falls in, which DHT key that bucket is announced under, who
	// provides it — it derives or looks up itself.
	if a.host.Network().Connectedness(b.host.ID()).String() == "Connected" {
		t.Fatal("nodes were already connected; this test would prove nothing")
	}

	var edges int64
	ok := waitUntil(45*time.Second, func() bool {
		n, err := a.Fetch(ctx, "seedaaaaaaa")
		if err != nil {
			t.Logf("fetch attempt: %v", err)
			return false
		}
		edges = n
		return n > 0
	})
	if !ok {
		t.Fatalf("fetched %d edges via discovery alone — the announce → provider "+
			"lookup → dial chain did not complete", edges)
	}
	if edges != 2 {
		t.Errorf("gained %d edges, want 2", edges)
	}

	// And the data actually landed, rather than the fetch merely reporting a
	// number.
	sug, err := clientStore.Suggest("seedaaaaaaa", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sug.Suggestions) == 0 {
		t.Errorf("discovery reported %d edges but the walk returns nothing "+
			"(graph: %d nodes, %d edges)", edges, sug.GraphNodes, sug.GraphEdges)
	}
}

// TestConvergesAfterOneNodeDies covers WO-062 §4's other half: several nodes,
// one of them removed, and the rest still get the data.
//
// The failure this guards against is a fetch that gives up on the first
// provider it cannot reach. Provider records outlive the node that published
// them — a peer that has quit is still listed for its lease — so "the first
// provider is dead" is the normal case in a live network, not an edge case.
func TestConvergesAfterOneNodeDies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	// Two independent holders of the same neighbourhood.
	var servers []*Node
	for _, name := range []string{"conv-a.sqlite", "conv-b.sqlite"} {
		st := newStore(t, name)
		seed(t, st, "seedaaaaaaa", "targetaaaa1", 0)
		seed(t, st, "seedaaaaaaa", "targetaaaa2", 1)
		n, err := Start(ctx, st, bootstrappedTo(bootInfo, true, t))
		if err != nil {
			t.Fatal(err)
		}
		defer n.Close()
		servers = append(servers, n)
	}

	clientStore := newStore(t, "conv-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !waitUntil(30*time.Second, func() bool {
		for _, n := range servers {
			if len(n.host.Network().Peers()) == 0 {
				return false
			}
		}
		return len(client.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never joined the private DHT")
	}
	for _, n := range servers {
		if !waitUntil(45*time.Second, func() bool { return n.Announce(ctx) == nil }) {
			t.Fatal("a serving node could not announce")
		}
	}

	// One holder goes away, its provider record still in the DHT.
	if err := servers[0].Close(); err != nil {
		t.Fatal(err)
	}

	var edges int64
	ok := waitUntil(60*time.Second, func() bool {
		n, err := client.Fetch(ctx, "seedaaaaaaa")
		if err != nil {
			t.Logf("fetch attempt: %v", err)
			return false
		}
		edges = n
		return n > 0
	})
	if !ok {
		t.Fatal("a dead provider stopped the fetch; the live one was never tried")
	}
	if edges != 2 {
		t.Errorf("gained %d edges from the surviving node, want 2", edges)
	}
}
