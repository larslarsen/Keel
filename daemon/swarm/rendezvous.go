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

// rendezvousState is what this node knows about its own discoverability.
//
// Reported to the interface because zero peers has three unrelated causes —
// never published, published and nobody there, or not permitted at this level —
// and they are indistinguishable from the count alone (WO-093).
type rendezvousState struct {
	Published  bool  `json:"published"`
	LastLookAt int64 `json:"last_look_at,omitempty"`
	LastFound  int   `json:"last_found"`
	Looks      int   `json:"looks"`
}

// RendezvousState reports discoverability for the interface.
func (n *Node) RendezvousState() rendezvousState {
	n.rvMu.Lock()
	defer n.rvMu.Unlock()
	return n.rv
}

// announceRendezvous publishes the rendezvous key.
//
// Gate-aware like every other outbound action: a node that may not announce may
// not advertise its existence either.
func (n *Node) announceRendezvous(ctx context.Context) error {
	if !n.mayAnnounce() {
		return nil
	}
	c, err := RendezvousCID()
	if err != nil {
		return err
	}
	if err := n.dht.Provide(ctx, c, true); err != nil {
		return err
	}
	n.rvMu.Lock()
	n.rv.Published = true
	n.rvMu.Unlock()
	return nil
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
	defer func() {
		n.rvMu.Lock()
		n.rv.LastLookAt = time.Now().UnixMilli()
		n.rv.LastFound = connected
		n.rv.Looks++
		n.rvMu.Unlock()
	}()
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
