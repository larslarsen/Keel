// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// TestRendezvousCIDIsAConstantOfTheSoftware.
//
// The key must be identical on every install — that is what makes it a meeting
// point — and must contain nothing about the person running it. A key derived
// from anything local would give every node its own rendezvous, which is the
// same as having none.
func TestRendezvousCIDIsAConstantOfTheSoftware(t *testing.T) {
	a, err := RendezvousCID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := RendezvousCID()
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equals(b) {
		t.Fatalf("not stable: %s then %s", a, b)
	}
	if a.String() == "" {
		t.Fatal("empty CID")
	}
	// It is derived from the protocol identity, so a node speaking a different
	// revision lands on a different key and the two never meet — which is
	// correct: they could not talk anyway (WO-060).
	if !strings.Contains(rendezvousDomain, "keel/rendezvous") {
		t.Errorf("unexpected domain %q", rendezvousDomain)
	}
}

// TestRendezvousDiffersFromContentKeys: it must not collide with any bucket, or
// looking for nodes would return whoever holds one specific slice of data.
func TestRendezvousDiffersFromContentKeys(t *testing.T) {
	r, err := RendezvousCID()
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "0", "0a3f", "ffff"} {
		c, err := prefixCID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if c.Equals(r) {
			t.Errorf("rendezvous key collides with the bucket key for %q", prefix)
		}
	}
	for _, shard := range []int{0, 1, 255} {
		c, err := shardCID(shard)
		if err != nil {
			t.Fatal(err)
		}
		if c.Equals(r) {
			t.Errorf("rendezvous key collides with shard %d", shard)
		}
	}
}

// TestFindPeersFallsBackToRememberedPeersWhenTheDHTWalkIsQuiet is the
// two-machine failure: both nodes discoverable, both on the same LAN, and the
// rendezvous lookup returns nobody because the public DHT is degraded. The node
// already remembers the peer from earlier verified service, so the round must
// dial it directly — a degraded lookup costs latency, not the connection.
func TestFindPeersFallsBackToRememberedPeersWhenTheDHTWalkIsQuiet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	serverStore := newStore(t, "findpeers-server.sqlite")
	server, err := Start(ctx, serverStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "findpeers-client.sqlite")
	client, err := Start(ctx, clientStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// The remembered peer is dialed by address, never through the DHT: the
	// walk must see nothing so only the fallback can succeed.
	client.remember(server.AddrInfo())
	client.providerLookup = func(context.Context, cid.Cid, int) <-chan peer.AddrInfo {
		empty := make(chan peer.AddrInfo)
		close(empty)
		return empty
	}

	got, err := client.FindPeers(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("FindPeers returned %d new connections with a remembered peer and an empty DHT walk, want 1", got)
	}
	if !waitUntil(2*time.Second, func() bool {
		return client.host.Network().Connectedness(server.ID()) == network.Connected
	}) {
		t.Fatalf("remembered peer %s was never connected after FindPeers", server.ID())
	}
}

// TestFindPeersSkipsRememberedPeersAlreadyConnected: the fallback must not
// redial a peer the walk (or an earlier round) already connected.
func TestFindPeersSkipsRememberedPeersAlreadyConnected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	serverStore := newStore(t, "findpeers-already-server.sqlite")
	server, err := Start(ctx, serverStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "findpeers-already-client.sqlite")
	client, err := Start(ctx, clientStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.host.Connect(ctx, server.AddrInfo()); err != nil {
		t.Fatal(err)
	}

	client.remember(server.AddrInfo())
	client.providerLookup = func(context.Context, cid.Cid, int) <-chan peer.AddrInfo {
		empty := make(chan peer.AddrInfo)
		close(empty)
		return empty
	}

	got, err := client.FindPeers(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("FindPeers reported %d new connections for an already-connected remembered peer, want 0", got)
	}
}

// TestFindPeersScansPastStaleEphemeralProviders is the release-restart
// failure from two-machine QA. Level 2 deliberately gets a fresh peer ID on
// every daemon start, while DHT provider records outlive the process. After
// enough restarts, the first eight records for the shared rendezvous key can
// all name dead identities and hide the two identities which are alive now.
//
// The connection budget must remain eight; only the candidate scan needs to
// be wider. Otherwise restarting both machines to install a release makes
// each one truthfully discoverable while neither can find the other.
func TestFindPeersScansPastStaleEphemeralProviders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	serverStore := newStore(t, "findpeers-live-server.sqlite")
	server, err := Start(ctx, serverStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "findpeers-stale-client.sqlite")
	client, err := Start(ctx, clientStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	deadAddr, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/1")
	if err != nil {
		t.Fatal(err)
	}
	const connectionBudget = 8
	candidates := make([]peer.AddrInfo, 0, rendezvousProviderCandidates)
	for i := 0; i < connectionBudget; i++ {
		priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		id, err := peer.IDFromPrivateKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, peer.AddrInfo{ID: id, Addrs: []ma.Multiaddr{deadAddr}})
	}
	candidates = append(candidates, server.AddrInfo()) // live, but ninth
	realConnect := client.peerConnect
	releaseDead := make(chan struct{})
	var deadStarted atomic.Int32
	client.peerConnect = func(ctx context.Context, p peer.AddrInfo) error {
		if p.ID == server.ID() {
			return realConnect(ctx, p)
		}
		if deadStarted.Add(1) == connectionBudget {
			close(releaseDead)
		}
		select {
		case <-releaseDead:
			return errors.New("stale ephemeral identity")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	client.providerLookup = func(_ context.Context, _ cid.Cid, count int) <-chan peer.AddrInfo {
		out := make(chan peer.AddrInfo, count)
		for i, p := range candidates {
			if i >= count {
				break
			}
			out <- p
		}
		close(out)
		return out
	}

	got, err := client.FindPeers(ctx, connectionBudget)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("FindPeers connected to %d live peers behind stale identities, want 1", got)
	}
	if client.host.Network().Connectedness(server.ID()) != network.Connected {
		t.Fatalf("live ninth provider %s was hidden behind eight stale identities", server.ID())
	}
}
