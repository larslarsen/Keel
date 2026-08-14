// SPDX-License-Identifier: Apache-2.0
// How one Keel node finds another (WO-094).
//
// Everything else in this package is content-addressed: a node publishes
// provider records for the buckets it holds, and looks one up when it wants
// that bucket. That answers "who has 0a3f", and it is the right mechanism for
// fetching data.
//
// It cannot answer "who else is running this software". Two nodes with
// different viewing histories share no bucket, so neither ever looks the other
// up, and they stay invisible to each other indefinitely — both healthy, both
// announcing, both reporting zero peers. That is exactly what live QA saw
// across a full day with two consented Level-2 nodes.
//
// So there is one more key: a constant of the software that every node provides
// and periodically looks up. It carries no user data — it is derived from the
// protocol version alone, so nodes that could not talk to each other do not
// find each other either.
package swarm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mh "github.com/multiformats/go-multihash"

	"github.com/keel-app/keel/daemon/store"
)

// rendezvousDomain is the string the rendezvous key is derived from.
//
// Versioned with the block protocol and the key scheme on purpose: a node
// speaking a different revision is not a peer this one can use, and finding it
// would only produce connections that fail at the first request (WO-060).
const rendezvousDomain = "keel/rendezvous/1"

// rendezvousProviderCandidates is deliberately wider than the connection
// budget. Serving levels use an ephemeral peer identity, while provider
// records live beyond one daemon process. A release restart therefore leaves
// a dead record on this shared key; enough QA restarts can otherwise fill the
// first result page entirely with dead identities and hide the live peer.
//
// This bounds DHT results, not successful connections. FindPeers still stops
// at its caller-supplied connection budget.
const rendezvousProviderCandidates = 32

// A dead identity can take the full per-peer dial deadline to fail. Trying
// stale providers serially would still let eight old records consume the
// entire 90-second rendezvous round even after widening the candidate page.
// Reserve at most the connection budget's eight slots concurrently: failed
// attempts release slots for later candidates, while connected+in-flight can
// never exceed the caller's ceiling.
const rendezvousDialConcurrency = 8

// RendezvousCID is the key every node provides and looks up.
//
// Identical on every install by construction — no per-user input goes into it.
// That is the point: it means "a Keel node is here", nothing more, and it is
// the only thing about a node that is discoverable without already knowing
// which content it holds.
func RendezvousCID() (cid.Cid, error) {
	sum, err := mh.Sum(
		[]byte(fmt.Sprintf("%s/ks%d/%s", rendezvousDomain, store.KeySchemeVersion, BlockProtocol)),
		mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

// errAnnounceForbidden means the outbound gate or the contribution policy shut
// between the presence loop's check and this call. Not a network failure, so it
// must not be counted as one — see runPresence, which re-checks the gate.
var errAnnounceForbidden = errors.New("announcing is not permitted")

// publishPresence publishes the rendezvous key, once, bounded.
//
// This is the single measurement the network-health record is built on: it
// answers "can another Keel node find this one?" and nothing else (WO-093 §1).
// Content provider records answer a different question and are published by
// Announce on their own schedule.
//
// Gate-aware like every other outbound action: a node that may not announce may
// not advertise its existence either.
func (n *Node) publishPresence(ctx context.Context) error {
	if !n.mayAnnounce() {
		return errAnnounceForbidden
	}
	c, err := RendezvousCID()
	if err != nil {
		return err
	}
	// Bounded rather than inheriting the node's lifetime: a Provide that never
	// returns would hold the loop open forever and report `starting` for the
	// life of the process — a spinner, which is the outcome WO-093 forbids.
	ctx, cancel := context.WithTimeout(ctx, presenceProvideTimeout)
	defer cancel()
	return n.dht.Provide(ctx, c, true)
}

// FindPeers looks up the rendezvous key and connects to what it finds.
//
// Returns how many peers were newly connected. Bounded by max: this is the
// node's only unprompted outbound lookup and it must stay small.
//
// Connecting is all this does. What may then be served or fetched is decided by
// the contribution level and the outbound gate exactly as before — rendezvous
// must not become a way around either.
func (n *Node) FindPeers(ctx context.Context, max int) (int, error) {
	if !n.mayAnnounce() {
		// Level 1 has nothing to offer and does not advertise; it does not go
		// looking for company either.
		return 0, nil
	}
	if max <= 0 {
		return 0, nil
	}
	c, err := RendezvousCID()
	if err != nil {
		return 0, err
	}
	self := n.host.ID()
	connected := 0
	seen := map[peer.ID]bool{self: true}
	lookupCtx, cancelLookup := context.WithCancel(ctx)
	defer cancelLookup()
	providers := n.findProviders(lookupCtx, c, rendezvousProviderCandidates)
	type dialResult struct{ connected bool }
	results := make(chan dialResult, rendezvousDialConcurrency)
	active := 0
	lookupOpen := true
	// Recorded whatever the round found, including nothing: "looked and found
	// nobody" is the honest quiet-network state, and it is only distinguishable
	// from "has not looked yet" because this runs (WO-093 §3).
	defer n.health.lookedUp(time.Now())
	for lookupOpen || active > 0 {
		var next <-chan peer.AddrInfo
		if lookupOpen && active < rendezvousDialConcurrency && connected+active < max {
			next = providers
		}
		select {
		case <-ctx.Done():
			return connected, nil
		case p, ok := <-next:
			if !ok {
				lookupOpen = false
				continue
			}
			if seen[p.ID] || len(p.Addrs) == 0 {
				continue
			}
			seen[p.ID] = true
			if n.host.Network().Connectedness(p.ID) == network.Connected {
				continue
			}
			active++
			go func(p peer.AddrInfo) {
				dialCtx, cancel := context.WithTimeout(lookupCtx, 15*time.Second)
				err := n.peerConnect(dialCtx, p)
				cancel()
				select {
				case results <- dialResult{connected: err == nil}:
				case <-lookupCtx.Done():
				}
			}(p)
		case result := <-results:
			active--
			if result.connected {
				connected++
				if connected >= max {
					return connected, nil
				}
			}
		}
	}
	// The public DHT can be censored or degraded so the rendezvous walk returns
	// nothing even though a compatible peer is already remembered from earlier
	// verified service (WO-052 §7.4a). A degraded lookup must cost latency, not
	// the connection: fall back to remembered peers, dialing them directly.
	// Exact protocol negotiation still fails closed if the remembered peer now
	// speaks a different scheme, and a successful dial must not be mistaken for
	// public discoverability — the presence loop alone measures that (WO-093).
	if connected == 0 && ctx.Err() == nil {
		exclude := map[peer.ID]bool{self: true}
		for _, p := range n.rememberedProviders(exclude, max) {
			if n.host.Network().Connectedness(p.ID) == network.Connected {
				continue
			}
			dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := n.peerConnect(dialCtx, p)
			cancel()
			if err != nil {
				continue
			}
			connected++
		}
	}
	return connected, nil
}
