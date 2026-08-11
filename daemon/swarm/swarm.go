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
	"github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"

	"github.com/keel-app/keel/daemon/store"
)

// BlockProtocol is the stream protocol for block requests. Versioned in the
// name so a future incompatible shape can run alongside this one — the
// extension is frozen, but the daemon is not, and peers will run old builds.
var BlockProtocol = keelProtocol("block", "2.0.0", store.KeySchemeVersion)

// CatalogueProtocol carries titles, which travel separately from the graph.
var CatalogueProtocol = keelProtocol("catalogue", "1.0.0", store.KeySchemeVersion)

// ShardProtocol carries token shards for distributed search (WO-059). See
// shard.go in this package for the request/fetch side.
var ShardProtocol = keelProtocol("shard", "1.0.0", store.KeySchemeVersion)

// maxBlockBytes bounds what a peer can make this node allocate. A block is one
// video's neighbourhood — a few hundred edges — so anything approaching this is
// already pathological.
const maxBlockBytes = 64 << 20 // 64 MiB

// maxBlocksPerReply bounds one bucket reply. A bucket on a large mirror can
// hold many neighbourhoods, and an unbounded reply is both a memory hazard and
// a way for a single request to consume a node's upstream.
const maxBlocksPerReply = 256

// maxCatalogueRows bounds one catalogue bucket reply.
const maxCatalogueRows = 4096

// requestTimeout bounds a single block fetch. Prewarm runs ahead of the user,
// so a slow peer must not hold a slot indefinitely.
const requestTimeout = 20 * time.Second

// Store is the slice of the store this package needs. An interface rather than
// the concrete type so the transport can be tested without a database.
type Store interface {
	BlocksInPrefix(prefix, cohort string, mirrorOnly bool, limit int) ([]store.Block, error)
	LocalPrefixes(bits int, mirrorOnly bool) ([]string, error)
	BuildCataloguePack(prefix string, mirrorOnly bool, limit int) (*store.CataloguePack, error)
	ImportCataloguePack(raw []byte) (int, error)
	RememberPeer(id string, addrs []string) error
	KnownPeers(limit int) ([]store.KnownPeer, error)
	LocalCataloguePrefixes(bits int, mirrorOnly bool) ([]string, error)
	MissingCataloguePrefixes(videoIDs []string, bits int) ([]string, error)
	ImportBlock(raw []byte) (*store.Block, int64, error)
	RecentLiveSightings(cutoff int64) ([]store.LiveSighting, error)
	SwarmIdentity() ([]byte, error)
	EphemeralSwarmIdentity() ([]byte, error)
	Cohort() string
	// ShardSlice, LocalShards and BuildShardPack back distributed
	// token-shard search (WO-059) and its signing layer (WO-067) — see
	// shard.go in this package for the request/fetch side.
	ShardSlice(shard int, mirrorOnly bool) ([]store.ShardEntry, error)
	LocalShards(mirrorOnly bool) ([]int, error)
	BuildShardPack(shard int, mirrorOnly bool, limit int) (*store.ShardPack, error)
}

// Config controls what a node offers and consumes.
type Config struct {
	// AppVersion is the daemon version announced to peers (WO-061). Empty
	// means unknown, which peers treat as "not comparable" rather than as
	// version zero — an unset field must never make everyone else look newer.
	AppVersion string

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

	live *LiveIndex
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
		// Announce what this build is, so peers can tell whether they are
		// behind us or incompatible with us (WO-061). libp2p exchanges this on
		// connect via identify, which is why there is no Keel handshake.
		libp2p.UserAgent(AgentVersion(cfg.AppVersion)),
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
	// Bound the bootstrap specifically, not the node's lifetime. Reaching the
	// public DHT can be slow, firewalled or impossible, and a node that cannot
	// bootstrap is still useful — it serves and answers on direct connections,
	// and the DHT may become reachable later.
	//
	// The deadline must not come from the caller's long-lived context: gossipsub
	// and the index sweeper run on that one, so a timeout there would quietly
	// kill the live feed a minute after startup.
	bootCtx, cancelBoot := context.WithTimeout(ctx, 30*time.Second)
	defer cancelBoot()
	if err := kdht.Bootstrap(bootCtx); err != nil {
		n := &Node{host: h, dht: kdht, st: st, cfg: cfg, inflight: map[string]chan struct{}{}}
		n.logf("dht bootstrap failed, continuing without discovery: %v", err)
	}

	n := &Node{host: h, dht: kdht, st: st, cfg: cfg, inflight: map[string]chan struct{}{}}

	if cfg.Serve {
		h.SetStreamHandler(BlockProtocol, n.handleBlockRequest)
		h.SetStreamHandler(CatalogueProtocol, n.handleCatalogueRequest)
		h.SetStreamHandler(ShardProtocol, n.handleShardRequest)
		// Relay service is cheap and makes the network work for people whose
		// routers do not cooperate. Only serving nodes run it.
		if _, err := relay.New(h); err != nil {
			n.logf("relay service unavailable: %v", err)
		}
	}

	if err := n.startLive(ctx); err != nil {
		// The live index is additive: without it the rest of the node works.
		n.logf("live index unavailable: %v", err)
	}

	n.logf("swarm up as %s (serving=%v)", h.ID(), cfg.Serve)
	for _, a := range h.Addrs() {
		n.logf("  listening on %s/p2p/%s", a, h.ID())
	}
	return n, nil
}

// ID is this node's libp2p peer id.
func (n *Node) ID() peer.ID { return n.host.ID() }

// Peers is the count of currently connected libp2p peers.
//
// Most of these are not running Keel. Joining the public IPFS DHT means
// connecting to whoever else is on it, so this number goes above zero as soon
// as the node starts and says nothing about whether anyone else uses this
// software. KeelPeers is the number that answers that.
func (n *Node) Peers() int { return len(n.host.Network().Peers()) }

// KeelPeers counts connected peers that speak the Keel block protocol.
//
// This is the one worth showing a person: another node that supports this
// protocol is another install, where a bare peer count is mostly strangers
// routing DHT traffic.
func (n *Node) KeelPeers() int {
	count := 0
	for _, p := range n.host.Network().Peers() {
		got, err := n.host.Peerstore().SupportsProtocols(p, BlockProtocol)
		if err == nil && len(got) > 0 {
			count++
		}
	}
	return count
}

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
// keelProtocol builds a stream protocol id carrying the key scheme (WO-060).
//
// Putting the scheme in the protocol id rather than in a handshake message is
// what makes a mismatch fail structurally: libp2p refuses to open a stream for
// a protocol the remote does not speak, so two nodes that derive keys
// differently never exchange a byte. The alternative — connect, then compare
// versions — has to be remembered at every call site, and the cost of
// forgetting is the silent partition this whole file exists to prevent.
//
// The service version and the key scheme are separate numbers on purpose. A
// change to what a block *contains* bumps the former; a change to how its key
// is *derived* bumps the latter. They break compatibility for different
// reasons and would otherwise be conflated.
func keelProtocol(name, version string, scheme int) protocol.ID {
	return protocol.ID(fmt.Sprintf("/keel/%s/%s/ks%d", name, version, scheme))
}

func prefixCID(prefix string) (cid.Cid, error) {
	sum, err := mh.Sum([]byte(fmt.Sprintf("%sks%d/%s", store.PrefixDomain, store.KeySchemeVersion, prefix)), mh.SHA2_256, -1)
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

// handleCatalogueRequest answers one stream: a catalogue prefix in, every row
// held in that bucket out.
//
// Below Level 3 the rows come from `peer_catalogue` only. Serving rows derived
// from this node's own impressions would disclose viewing at video granularity —
// a requester would see exactly which members of a bucket this node holds, which
// is what its user watched.
func (n *Node) handleCatalogueRequest(s network.Stream) {
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
	pack, err := n.st.BuildCataloguePack(prefix, !n.cfg.ServeOwnObservations, maxCatalogueRows)
	if err != nil {
		n.logf("catalogue %s: %v", prefix, err)
		return
	}
	raw, err := pack.Encode()
	if err != nil {
		return
	}
	_, _ = s.Write(raw)
}

// syncCatalogue fetches titles for every target in a graph bucket reply.
//
// The argument must be the whole bucket, never the blocks the caller cared
// about. Deriving the catalogue request set from a subset would identify which
// block was wanted and undo the graph fetch's anonymity — the reason this takes
// the full reply rather than a video id.
func (n *Node) syncCatalogue(ctx context.Context, blocks []store.Block) {
	if !n.cfg.Fetch || len(blocks) == 0 {
		return
	}
	ids := []string{}
	for i := range blocks {
		ids = append(ids, blocks[i].Key)
		for _, e := range blocks[i].Edges {
			ids = append(ids, e.To)
		}
	}
	prefixes, err := n.st.MissingCataloguePrefixes(ids, n.prefixBits())
	if err != nil || len(prefixes) == 0 {
		return
	}
	n.logf("catalogue: %d buckets needed for %d videos", len(prefixes), len(ids))
	for _, p := range prefixes {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if rows, err := n.fetchCataloguePrefix(ctx, p); err == nil && rows > 0 {
			n.logf("catalogue %s: %d rows", p, rows)
		}
	}
}

// fetchCataloguePrefix retrieves one catalogue bucket from any provider.
func (n *Node) fetchCataloguePrefix(ctx context.Context, prefix string) (int, error) {
	c, err := prefixCID("catalogue/" + prefix)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	for p := range n.dht.FindProvidersAsync(ctx, c, 8) {
		if p.ID == n.host.ID() || len(p.Addrs) == 0 {
			continue
		}
		raw, err := n.requestOn(ctx, p, prefix, CatalogueProtocol)
		if err != nil {
			continue
		}
		rows, err := n.st.ImportCataloguePack(raw)
		if err != nil {
			n.logf("catalogue %s from %s rejected: %v", prefix, p.ID, err)
			continue
		}
		n.remember(p)
		return rows, nil
	}

	// Same fallback as blocks: titles are no use if only discovery is blocked.
	known, err := n.st.KnownPeers(0)
	if err != nil {
		return 0, nil
	}
	for _, k := range known {
		info, err := addrInfo(k)
		if err != nil {
			continue
		}
		raw, err := n.requestOn(ctx, info, prefix, CatalogueProtocol)
		if err != nil {
			continue
		}
		if rows, err := n.st.ImportCataloguePack(raw); err == nil && rows > 0 {
			return rows, nil
		}
	}
	return 0, nil
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
	// Catalogue buckets are announced separately, because the two datasets are
	// held independently — a node can hold titles for videos whose graph it has
	// evicted, and vice versa.
	catKeys, err := n.st.LocalCataloguePrefixes(n.prefixBits(), !n.cfg.ServeOwnObservations)
	if err != nil {
		return err
	}
	var catAnnounced int
	for _, k := range catKeys {
		c, err := prefixCID("catalogue/" + k)
		if err != nil {
			continue
		}
		if err := n.dht.Provide(ctx, c, true); err != nil {
			continue
		}
		catAnnounced++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	// Shards are announced separately again, same reasoning: a node's title
	// index and its shard index are recomputed from the same source but are
	// their own namespace (shardCID, shard.go), so a shard fetch and a
	// catalogue fetch for the same node never correlate.
	shardKeys, err := n.st.LocalShards(!n.cfg.ServeOwnObservations)
	if err != nil {
		return err
	}
	var shardAnnounced int
	for _, sh := range shardKeys {
		c, err := shardCID(sh)
		if err != nil {
			continue
		}
		if err := n.dht.Provide(ctx, c, true); err != nil {
			continue
		}
		shardAnnounced++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	n.logf("announced %d/%d graph buckets, %d/%d catalogue buckets, %d/%d shards",
		announced, len(keys), catAnnounced, len(catKeys), shardAnnounced, len(shardKeys))
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
		imported, blocks, edges := n.importReply(raw)
		if blocks == 0 {
			continue
		}
		n.logf("prefix %s: %d blocks, %d edges from %s", prefix, blocks, edges, p.ID)
		n.remember(p)
		n.syncCatalogue(ctx, imported)
		return edges, nil
	}

	// Discovery found nobody. That is normal for the long tail, and it is also
	// what a censored DHT looks like (§7.4a, GO-2024-3218 — no fix available).
	// Either way a peer that has served us before can be asked directly, since
	// the block protocol needs no DHT.
	return n.fetchFromKnown(ctx, prefix)
}

// remember records a peer that served a reply which verified.
func (n *Node) remember(p peer.AddrInfo) {
	addrs := make([]string, 0, len(p.Addrs))
	for _, a := range p.Addrs {
		addrs = append(addrs, a.String())
	}
	if err := n.st.RememberPeer(p.ID.String(), addrs); err != nil {
		n.logf("remember %s: %v", p.ID, err)
	}
}

// fetchFromKnown asks peers that have served us before, in order of usefulness.
//
// This is the escape hatch from DHT censorship. It cannot replace discovery —
// a node with no history has nobody to ask, and these peers only hold what they
// happened to cache — but it means an established node degrades to slower rather
// than to nothing.
func (n *Node) fetchFromKnown(ctx context.Context, prefix string) (int64, error) {
	known, err := n.st.KnownPeers(0)
	if err != nil || len(known) == 0 {
		return 0, nil
	}
	for _, k := range known {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
		info, err := addrInfo(k)
		if err != nil {
			continue
		}
		raw, err := n.requestOn(ctx, info, prefix, BlockProtocol)
		if err != nil {
			continue
		}
		imported, blocks, edges := n.importReply(raw)
		if blocks == 0 {
			continue
		}
		n.logf("prefix %s: %d blocks, %d edges from remembered peer %s", prefix, blocks, edges, info.ID)
		n.syncCatalogue(ctx, imported)
		return edges, nil
	}
	return 0, nil
}

// addrInfo rebuilds a libp2p peer address from stored strings.
func addrInfo(k store.KnownPeer) (peer.AddrInfo, error) {
	id, err := peer.Decode(k.ID)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	info := peer.AddrInfo{ID: id}
	for _, a := range k.Addrs {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		info.Addrs = append(info.Addrs, ma)
	}
	if len(info.Addrs) == 0 {
		return peer.AddrInfo{}, fmt.Errorf("no usable addresses")
	}
	return info, nil
}

// importReply merges every block in a bucket reply.
//
// Taking the whole bucket is what makes the request k-anonymous, and it is also
// what makes the node a useful mirror: the cover traffic becomes hosting
// capacity for other people. One bad block is skipped rather than failing the
// batch — a peer must not be able to poison a whole bucket with one bad row.
func (n *Node) importReply(raw []byte) (imported []store.Block, blocks int, edges int64) {
	var list []store.Block
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, 0, 0
	}
	for i := range list {
		encoded, err := list[i].Encode()
		if err != nil {
			continue
		}
		if _, got, err := n.st.ImportBlock(encoded); err == nil {
			imported = append(imported, list[i])
			blocks++
			edges += got
		}
	}
	return imported, blocks, edges
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
	imported, blocks, edges := n.importReply(raw)
	n.logf("prefix %s: %d blocks, %d edges from %s", prefix, blocks, edges, p.ID)
	if blocks > 0 {
		n.remember(p)
	}
	n.syncCatalogue(ctx, imported)
	return edges, nil
}

// request opens one stream on the block protocol and reads the response.
func (n *Node) request(ctx context.Context, p peer.AddrInfo, key string) ([]byte, error) {
	return n.requestOn(ctx, p, key, BlockProtocol)
}

func (n *Node) requestOn(ctx context.Context, p peer.AddrInfo, key string, proto protocol.ID) ([]byte, error) {
	if err := n.host.Connect(ctx, p); err != nil {
		return nil, err
	}
	s, err := n.host.NewStream(ctx, p.ID, proto)
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
