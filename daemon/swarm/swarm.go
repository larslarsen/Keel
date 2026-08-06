// SPDX-License-Identifier: Apache-2.0
// The swarm: peer discovery and block transfer (WO-052).
//
// This is the transport DESIGN_BOOTSTRAP §5d assumes and §5b constrains. The
// constraint from §5b is the important one and it shapes everything here:
//
//	"a DHT does not store anything"
//
// So the DHT is used as a *directory* and nothing else. A node announces "I can
// serve the block for video V" as a provider record, and serves the actual
// bytes itself over a Keel protocol stream. No block content is ever written
// into the DHT, and no Keel-operated server exists — discovery rides the public
// IPFS DHT, which is free, already populated, and run by nobody in particular.
//
// That last point matters for more than cost. PRIVACY.md states that the daemon
// never contacts a Keel-operated server because none exists. Any design that
// needed one to bootstrap would make that sentence false.
//
// What a peer learns by watching this: that a node exists at an IP, and which
// block keys it advertises. It does not learn what the node watched — an
// advertised block is one the node holds, which after any peer fetching is
// mostly other people's data. Advertising is nonetheless gated on contribution
// level by the caller, because at Level 1 nothing should be offered at all.
package swarm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	mh "github.com/multiformats/go-multihash"

	"github.com/keel-app/keel/daemon/store"
)

// BlockProtocol is the stream protocol for block requests. Versioned in the
// name so a future incompatible shape can run alongside this one — the
// extension is frozen, but the daemon is not, and peers will run old builds.
const BlockProtocol = protocol.ID("/keel/block/2.0.0")

// maxBlockBytes bounds what a peer can make this node allocate. A block is one
// video's neighbourhood — a few hundred edges — so anything approaching this is
// already pathological.
const maxBlockBytes = 64 << 20 // 64 MiB

// maxBlocksPerReply bounds one bucket reply. A bucket on a large mirror can
// hold many neighbourhoods, and an unbounded reply is both a memory hazard and
// a way for a single request to consume a node's upstream.
const maxBlocksPerReply = 256

// requestTimeout bounds a single block fetch. Prewarm runs ahead of the user,
// so a slow peer must not hold a slot indefinitely.
const requestTimeout = 20 * time.Second

// Store is the slice of the store this package needs. An interface rather than
// the concrete type so the transport can be tested without a database.
type Store interface {
	BlocksInPrefix(prefix, cohort string, mirrorOnly bool, limit int) ([]store.Block, error)
	LocalPrefixes(bits int, mirrorOnly bool) ([]string, error)
	ImportBlock(raw []byte) (*store.Block, int64, error)
	SwarmIdentity() ([]byte, error)
	EphemeralSwarmIdentity() ([]byte, error)
	Cohort() string
}

// Config controls what a node offers and consumes.
type Config struct {
	// Serve advertises this node's blocks and answers requests. False at
	// contribution Level 1, which offers nothing.
	Serve bool
	// Fetch allows on-demand block requests to peers.
	//
	// False at Level 1, and that is the whole of Level 1's promise: a request
	// discloses to the peer answering it which video was asked about, so a node
	// that never asks discloses nothing. Level 1 runs on the seed pack and its
	// own recording, both of which involve no query. Turning this on is part of
	// what a user accepts when moving to Level 2.
	Fetch bool
	// ServeOwnObservations includes this node's own edges in served blocks.
	//
	// False is Level 2 — mirror and re-serve what came from other people,
	// donating storage and bandwidth while disclosing nothing personal. True
	// is Level 3 and above, where the user has opted into publishing what
	// they were recommended. Getting this wrong publishes a funnel, so the
	// choice is made once here and enforced by which builder runs.
	ServeOwnObservations bool
	// ListenAddrs overrides the default listen set. Empty means all
	// interfaces on an OS-assigned port, over both TCP and QUIC.
	ListenAddrs []string
	// Bootstrap overrides the DHT bootstrap peers. Empty means the public
	// IPFS bootstrap nodes.
	Bootstrap []peer.AddrInfo
	// PrefixBits sets the bucket size for requests. Zero means the default.
	//
	// Smaller means larger buckets, so a larger anonymity set and a larger
	// transfer per request. This is the knob the disk budget drives.
	PrefixBits int
	// EphemeralIdentity generates a new network identity each start instead of
	// reusing the stored one.
	//
	// Required for prefix caching to mean anything against a peer that keeps
	// records: each prefix is k-anonymous alone, but a sequence of them under
	// one stable peer id is a trajectory, and trajectories re-identify.
	EphemeralIdentity bool
	// Log receives one-line progress messages. Nil discards them.
	Log func(string, ...any)
}

// Node is a running swarm participant.
type Node struct {
	host host.Host
	dht  *dht.IpfsDHT
	st   Store
	cfg  Config

	mu       sync.Mutex
	inflight map[string]chan struct{} // dedupes concurrent fetches of one key
}

func (n *Node) logf(format string, args ...any) {
	if n.cfg.Log != nil {
		n.cfg.Log(format, args...)
	}
}

// Start brings up the host, joins the DHT and registers the block handler.
func Start(ctx context.Context, st Store, cfg Config) (*Node, error) {
	identity := st.SwarmIdentity
	if cfg.EphemeralIdentity {
		identity = st.EphemeralSwarmIdentity
	}
	raw, err := identity()
	if err != nil {
		return nil, fmt.Errorf("swarm identity: %w", err)
	}
	priv, err := crypto.UnmarshalEd25519PrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("swarm identity is unusable: %w", err)
	}

	listen := cfg.ListenAddrs
	if len(listen) == 0 {
		listen = []string{
			"/ip4/0.0.0.0/tcp/0",
			"/ip4/0.0.0.0/udp/0/quic-v1",
			"/ip6/::/tcp/0",
			"/ip6/::/udp/0/quic-v1",
		}
	}

	opts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(listen...),
		// Most users are behind NAT. These three together are what make a
		// home machine reachable without the user configuring anything:
		// NAT port mapping where the router allows it, relay as a fallback,
		// and hole punching to upgrade a relayed connection to a direct one.
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.EnableNATService(),
	}
	if cfg.Serve {
		// A reachable node helps others punch through; an unreachable one
		// cannot, so only offer this when serving is on anyway.
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(nil))
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("libp2p host: %w", err)
	}

	// A nil slice means "use the public defaults"; an explicitly empty one
	// means "join nothing", which is how tests get an isolated network.
	bootstrap := cfg.Bootstrap
	if bootstrap == nil {
		bootstrap = dht.GetDefaultBootstrapPeerAddrInfos()
	}
	kdht, err := dht.New(h,
		dht.Mode(dht.ModeAuto),
		dht.BootstrapPeers(bootstrap...),
	)
	if err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("dht: %w", err)
	}
	if err := kdht.Bootstrap(ctx); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("dht bootstrap: %w", err)
	}

	n := &Node{host: h, dht: kdht, st: st, cfg: cfg, inflight: map[string]chan struct{}{}}

	if cfg.Serve {
		h.SetStreamHandler(BlockProtocol, n.handleBlockRequest)
		// Relay service is cheap and makes the network work for people whose
		// routers do not cooperate. Only serving nodes run it.
		if _, err := relay.New(h); err != nil {
			n.logf("relay service unavailable: %v", err)
		}
	}

	n.logf("swarm up as %s (serving=%v)", h.ID(), cfg.Serve)
	for _, a := range h.Addrs() {
		n.logf("  listening on %s/p2p/%s", a, h.ID())
	}
	return n, nil
}

// ID is this node's libp2p peer id.
func (n *Node) ID() peer.ID { return n.host.ID() }

// Peers is the count of currently connected peers.
func (n *Node) Peers() int { return len(n.host.Network().Peers()) }

// Close shuts the node down.
func (n *Node) Close() error {
	if n.dht != nil {
		_ = n.dht.Close()
	}
	return n.host.Close()
}

// prefixCID maps a prefix bucket to the DHT key its holders announce under.
//
// Buckets rather than individual videos is the whole point: a provider record
// says "this node holds something in bucket a3f", which thousands of videos
// share, instead of naming a neighbourhood the node's user watched.
func prefixCID(prefix string) (cid.Cid, error) {
	sum, err := mh.Sum([]byte("keel/prefix/1/"+prefix), mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func (n *Node) prefixBits() int {
	if n.cfg.PrefixBits > 0 {
		return n.cfg.PrefixBits
	}
	return store.DefaultPrefixBits
}

// handleBlockRequest answers one stream: a prefix in, every block held in that
// bucket out.
//
// The server never learns which neighbourhood the requester actually wanted,
// because the requester asks for the whole bucket and takes the whole bucket.
// There is no real-versus-decoy structure here for repeated observation to
// separate, which is why this survives the intersection attack that defeats
// decoy traffic.
func (n *Node) handleBlockRequest(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	line, err := bufio.NewReader(io.LimitReader(s, 256)).ReadString('\n')
	if err != nil && err != io.EOF {
		return
	}
	prefix := trimLine(line)
	if prefix == "" {
		return
	}

	blocks, err := n.st.BlocksInPrefix(prefix, n.st.Cohort(), !n.cfg.ServeOwnObservations, maxBlocksPerReply)
	if err != nil {
		n.logf("prefix %s: %v", prefix, err)
		return
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return
	}
	_, _ = s.Write(raw)
}

func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// Announce publishes provider records for everything this node can serve.
//
// Called periodically; DHT provider records expire, so re-announcing is how a
// node stays findable. Silent no-op when not serving.
func (n *Node) Announce(ctx context.Context) error {
	if !n.cfg.Serve {
		return nil
	}
	keys, err := n.st.LocalPrefixes(n.prefixBits(), !n.cfg.ServeOwnObservations)
	if err != nil {
		return err
	}
	var announced int
	for _, k := range keys {
		c, err := prefixCID(k)
		if err != nil {
			continue
		}
		if err := n.dht.Provide(ctx, c, true); err != nil {
			// One failure is normal — the DHT is best-effort and a partially
			// announced node is still useful.
			continue
		}
		announced++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	n.logf("announced %d/%d prefix buckets", announced, len(keys))
	return nil
}

// Fetch retrieves one block from the swarm and merges it locally.
//
// Returns the number of edges gained. A miss is not an error: no provider for a
// video simply means nobody holding it is online, which is the normal case for
// the long tail.
func (n *Node) Fetch(ctx context.Context, key string) (int64, error) {
	if !n.cfg.Fetch {
		// Level 1 asks nobody anything. Not a failure — the seed pack is
		// expected to answer the common case, and a miss simply means this
		// node works from what it already has.
		return 0, nil
	}
	// Collapse duplicate concurrent requests — prewarm and a user-driven walk
	// routinely want the same block at the same moment.
	n.mu.Lock()
	if wait, busy := n.inflight[key]; busy {
		n.mu.Unlock()
		select {
		case <-wait:
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	done := make(chan struct{})
	n.inflight[key] = done
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.inflight, key)
		n.mu.Unlock()
		close(done)
	}()

	prefix := store.BlockPrefix(key, n.prefixBits())
	c, err := prefixCID(prefix)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	for p := range n.dht.FindProvidersAsync(ctx, c, 8) {
		if p.ID == n.host.ID() || len(p.Addrs) == 0 {
			continue
		}
		raw, err := n.request(ctx, p, prefix)
		if err != nil {
			n.logf("prefix %s from %s: %v", prefix, p.ID, err)
			continue
		}
		blocks, edges := n.importReply(raw)
		if blocks == 0 {
			continue
		}
		n.logf("prefix %s: %d blocks, %d edges from %s", prefix, blocks, edges, p.ID)
		return edges, nil
	}
	return 0, nil
}

// importReply merges every block in a bucket reply.
//
// Taking the whole bucket is what makes the request k-anonymous, and it is also
// what makes the node a useful mirror: the cover traffic becomes hosting
// capacity for other people. One bad block is skipped rather than failing the
// batch — a peer must not be able to poison a whole bucket with one bad row.
func (n *Node) importReply(raw []byte) (blocks int, edges int64) {
	var list []store.Block
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, 0
	}
	for i := range list {
		encoded, err := list[i].Encode()
		if err != nil {
			continue
		}
		if _, got, err := n.st.ImportBlock(encoded); err == nil {
			blocks++
			edges += got
		}
	}
	return blocks, edges
}

// AddrInfo is how another node reaches this one.
func (n *Node) AddrInfo() peer.AddrInfo {
	return peer.AddrInfo{ID: n.host.ID(), Addrs: n.host.Addrs()}
}

// FetchFrom retrieves a block from one known peer, skipping discovery.
//
// Discovery is the right default, but a known peer is not always a worse
// answer: a node that has already talked to a peer holding a neighbourhood can
// go straight back to it instead of paying a DHT round trip, and a bootstrap
// peer is reachable before any provider record exists.
func (n *Node) FetchFrom(ctx context.Context, p peer.AddrInfo, key string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	prefix := store.BlockPrefix(key, n.prefixBits())
	raw, err := n.request(ctx, p, prefix)
	if err != nil {
		return 0, err
	}
	blocks, edges := n.importReply(raw)
	n.logf("prefix %s: %d blocks, %d edges from %s", prefix, blocks, edges, p.ID)
	return edges, nil
}

// request opens one stream and reads the response.
func (n *Node) request(ctx context.Context, p peer.AddrInfo, key string) ([]byte, error) {
	if err := n.host.Connect(ctx, p); err != nil {
		return nil, err
	}
	s, err := n.host.NewStream(ctx, p.ID, BlockProtocol)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	if _, err := io.WriteString(s, key+"\n"); err != nil {
		return nil, err
	}
	if err := s.CloseWrite(); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(s, maxBlockBytes))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("peer returned nothing")
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("peer returned %d bytes that are not JSON", len(raw))
	}
	return raw, nil
}
