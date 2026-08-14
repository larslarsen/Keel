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
	c, err := RendezvousCID()
	if err != nil {
		return 0, err
	}
	self := n.host.ID()
	connected := 0
	// Recorded whatever the round found, including nothing: "looked and found
	// nobody" is the honest quiet-network state, and it is only distinguishable
	// from "has not looked yet" because this runs (WO-093 §3).
	defer n.health.lookedUp(time.Now())
	for p := range n.dht.FindProvidersAsync(ctx, c, max) {
		if p.ID == self || len(p.Addrs) == 0 {
			continue
		}
		if n.host.Network().Connectedness(p.ID) == network.Connected {
			continue
		}
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := n.host.Connect(dialCtx, peer.AddrInfo{ID: p.ID, Addrs: p.Addrs})
		cancel()
		if err != nil {
			continue
		}
		connected++
	}
	return connected, nil
}
