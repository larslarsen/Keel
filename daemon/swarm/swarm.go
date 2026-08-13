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
// *buckets* it advertises — never a block key, because a provider record names
// a 12-bit hashed prefix that thousands of videos share (prefix.go). Since
// WO-084 those buckets do include neighbourhoods the node derived from its own
// observations, so the honest statement is that an observer learns the node
// holds something in a bucket, not that everything advertised is somebody
// else's. Advertising is gated on contribution level by the caller, because at
// Level 1 nothing is offered at all.
package swarm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
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
//
// 3.0.0 (WO-084) is a deliberate break with 2.0.0 rather than a tolerated
// mismatch. A 2.0.0 peer re-aggregates and re-signs whatever it imports, so
// handing it a preserved claim destroys the claim identity that makes broad
// buckets unlinkable and reintroduces the count growth cycles used to cause.
// The claim-preservation invariant is not something the other side can be
// asked to honour after the fact, so the stream is simply never opened.
var BlockProtocol = keelProtocol("block", "3.0.0", store.KeySchemeVersion)

// CatalogueProtocol carries titles, which travel separately from the graph.
var CatalogueProtocol = keelProtocol("catalogue", "1.0.0", store.KeySchemeVersion)

// ShardProtocol carries token shards for distributed search (WO-059). See
// shard.go in this package for the request/fetch side.
var ShardProtocol = keelProtocol("shard", "1.0.0", store.KeySchemeVersion)

// WordTelemetryProtocol is declared in words.go (WO-068): on-demand pack
// fetch for display-only corpus word stats — not a gossip topic.

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
	BlocksInPrefix(prefix, cohort string, sources store.SourceSet, limit int) (*store.BlockBucket, error)
	LocalPrefixes(bits int, sources store.SourceSet) ([]string, error)
	BuildCataloguePack(prefix string, sources store.SourceSet, limit int) (*store.CataloguePack, error)
	ImportCataloguePack(raw []byte) (int, error)
	RememberPeer(id string, addrs []string) error
	KnownPeers(limit int) ([]store.KnownPeer, error)
	LocalCataloguePrefixes(bits int, sources store.SourceSet) ([]string, error)
	MissingCataloguePrefixes(videoIDs []string, bits int) ([]string, error)
	ImportBlock(raw []byte) (*store.Block, int64, error)
	RecentLiveSightings(cutoff int64) ([]store.LiveSighting, error)
	SwarmIdentity() ([]byte, error)
	EphemeralSwarmIdentity() ([]byte, error)
	Cohort() string
	// ShardSlice, LocalShards and BuildShardPack back distributed
	// token-shard search (WO-059) and its signing layer (WO-067) — see
	// shard.go in this package for the request/fetch side.
	ShardSlice(shard int, sources store.SourceSet) ([]store.ShardEntry, error)
	LocalShards(sources store.SourceSet) ([]int, error)
	BuildShardPack(shard int, sources store.SourceSet, limit int) (*store.ShardPack, error)
	// LocalYieldVector backs yield-vector gossip (WO-067) — see
	// daemon/swarm/yield.go.
	LocalYieldVector(sources store.SourceSet) ([]byte, error)
	// MergeTokenSketch, DueTokenSketches and MarkTokenGossiped back
	// token-sketch gossip (WO-067) — see daemon/swarm/sketch.go.
	MergeTokenSketch(idx int, incoming *store.Sketch) error
	DueTokenSketches(limit int) ([]store.TokenSketchRow, error)
	MarkTokenGossiped(idx int) error
	// TokenEstimate and RecordTokenSearch back FetchShard's target-aware
	// stop condition (WO-067) — see daemon/swarm/shard.go.
	TokenEstimate(token string) (uint64, bool)
	RecordTokenSearch(token string, foundVideoIDs []string) error
	// LocalWordTelemetry backs on-demand word corpus stats (WO-068).
	LocalWordTelemetry(sources store.SourceSet) (*store.WordTelemetry, error)
	// RecordContributionServe and ContributionImpactSnapshot back the WO-086
	// contribution-impact panel — see contribution_impact.go in this package
	// and daemon/store/contribution_impact.go.
	RecordContributionServe(bytesWritten int) error
	ContributionImpactSnapshot(prefixBits int, graphSources, catalogueSources store.SourceSet) (store.ImpactSnapshot, error)
}

// Config controls what a node offers and consumes.
type Config struct {
	// AppVersion is the daemon version announced to peers (WO-061). Empty
	// means unknown, which peers treat as "not comparable" rather than as
	// version zero — an unset field must never make everyone else look newer.
	AppVersion string

	// Policy is the capability set this node runs with (WO-077).
	//
	// It replaced three booleans (Serve/Fetch/ServeOwnObservations) whose
	// coarseness was itself the bug: "serve" gated topic subscription and
	// topic origination together, and "fetch" was off at Level 1, withholding
	// peer search and pre-walk from every non-contributor. See policy.go.
	//
	// The zero value is the strictest possible policy — no live, no fetch, no
	// service — so a Config built without one cannot accidentally serve.
	Policy Policy
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

	// ps is the one gossipsub instance this node runs. libp2p-pubsub is one
	// instance per host with many topics joined on it, not one instance per
	// topic — a second independent pubsub.NewGossipSub call on the same host
	// would silently break the first by re-registering its stream handler.
	// live/yield/sketch all join their own topic on this shared instance
	// (see newPubSub below and each subsystem's start* function). Nil if
	// construction failed at Start — each subsystem treats that as
	// "unavailable", the same non-fatal handling Start already gives a
	// failed startLive.
	ps *pubsub.PubSub

	live   *LiveIndex
	yield  *YieldIndex
	sketch *SketchIndex

	// outbound gates everything this node offers to peers, independently of
	// cfg.Policy, and can be shut in one atomic store (WO-077).
	//
	// A downgrade cannot wait for the replacement node: stopping a host takes
	// long enough for in-flight and newly-arriving requests to be answered
	// under the *old* policy, and a block served after the user chose Level 1
	// is exactly the failure this ticket exists to prevent. So the supervisor
	// shuts this first, synchronously, and only then tears the node down —
	// every serve path, provider loop and three-gram publisher reads it.
	//
	// Open at construction. There is no reopen: a node whose gate has been
	// shut is being replaced, never revived.
	outbound atomic.Bool

	// serve bounds how much this node answers, independently of what its
	// policy permits it to answer at all (WO-085). Never nil — see newNode.
	serve *serveLimiter
}

// closeOutbound shuts this node's outbound permission gate.
//
// Idempotent and safe from any goroutine. After it returns, no handler,
// provider loop or gossip publisher on this node will offer anything further,
// though the host itself is still up until Close.
func (n *Node) closeOutbound() { n.outbound.Store(false) }

// CloseOutbound is the exported form used by the daemon's runtime supervisor
// before it detaches and stops a node.
func (n *Node) CloseOutbound() { n.closeOutbound() }

// mayServeBlocks reports whether a block/catalogue/shard request may be
// answered right now: the policy allows it and the gate is still open.
func (n *Node) mayServeBlocks() bool {
	return n.cfg.Policy.ServeBroadBuckets && n.outbound.Load()
}

// MayDistributedSearch reports whether a user-triggered distributed search may
// run right now: the policy entitles this node to one and the gate is open
// (WO-085).
//
// Gate-aware like the serving paths, and for the same reason in reverse: a
// downgrade to Level 1 must stop distributed search from the instant the user
// chooses it, not once the replacement node finishes coming up. Exported
// because the daemon answers PEER_SEARCH with a typed refusal before reaching
// the transport at all — see handlePeerSearchContext.
func (n *Node) MayDistributedSearch() bool {
	return n.cfg.Policy.DistributedSearch && n.outbound.Load()
}

// mayAnnounce reports whether provider records may be published right now.
func (n *Node) mayAnnounce() bool {
	return n.cfg.Policy.AnnounceProviders && n.outbound.Load()
}

// mayGossipSearchTelemetry reports whether the three-gram yield/sketch topics
// may be originated on right now. Subscription is decided once at Start;
// this gates publication, which continues on a timer.
func (n *Node) mayGossipSearchTelemetry() bool {
	return n.cfg.Policy.JoinSearchTelemetry && n.outbound.Load()
}

// mayServeWordTelemetry reports whether the WO-068 word pack may be sent to a
// peer right now. Level 2+ since WO-089: the pack is derived from the titles
// this user was shown. Fetching one is a separate capability.
func (n *Node) mayServeWordTelemetry() bool {
	return n.cfg.Policy.ServeWordTelemetry && n.outbound.Load()
}

// mayServeLive reports whether the whole-index live snapshot may be handed to
// a peer right now.
//
// Gate-aware, like every other serve path: startLive only registers the
// handler at Level 2+, but a downgrade shuts the gate before the node is torn
// down, and requests keep arriving in that window (WO-077). Without this, a
// user who chose Level 1 could still be answering live snapshots for as long
// as teardown took.
func (n *Node) mayServeLive() bool {
	return n.cfg.Policy.Live && n.outbound.Load()
}

// mayPublishLive reports whether a locally observed sighting may be gossiped.
// Same gate, other direction — see PublishLive.
func (n *Node) mayPublishLive() bool {
	return n.cfg.Policy.Live && n.outbound.Load()
}

// Policy exposes the capability set this node was constructed with, so the
// daemon can report the effective policy rather than the stored setting.
func (n *Node) Policy() Policy { return n.cfg.Policy }

// Serving reports whether this node would answer a block, catalogue or shard
// request right now — policy and gate together.
//
// Reported rather than inferred from the level: after a downgrade the gate is
// shut while the node is still being torn down, and during that window "what
// level is stored" and "is this thing still serving" have different answers.
// The second is the one a status display should show.
func (n *Node) Serving() bool { return n.mayServeBlocks() }

// ContributionImpact reports the live half of WO-086's panel: what this node
// currently holds and would announce, computed from the exact selectors
// Announce and the serve handlers already use, so it is provably the corpus
// this node actually serves rather than a value that could drift from it.
func (n *Node) ContributionImpact() (store.ImpactSnapshot, error) {
	return n.st.ContributionImpactSnapshot(n.prefixBits(), n.cfg.Policy.GraphSources(), n.cfg.Policy.CatalogueSources())
}

// newNode builds a Node with its outbound gate open. Every construction goes
// through here so a node can never exist with the gate in its zero value
// (shut), which would silently disable service on a serving node.
func newNode(h host.Host, kdht *dht.IpfsDHT, st Store, cfg Config) *Node {
	n := &Node{
		host: h, dht: kdht, st: st, cfg: cfg,
		inflight: map[string]chan struct{}{},
		serve:    newServeLimiter(),
	}
	n.outbound.Store(true)
	return n
}

// newPubSub constructs the one gossipsub instance every gossip topic in this
// package joins. Pulled out of live.go, which used to build its own —
// options unchanged from what startLive always used.
func newPubSub(ctx context.Context, h host.Host) (*pubsub.PubSub, error) {
	return pubsub.NewGossipSub(ctx, h,
		// No signature and no author: none of live/yield/sketch's messages
		// should name whoever published them — see live.go's package doc for
		// why that matters for the live index specifically, and the same
		// reasoning applies to yield/sketch: a searching node's own gossip
		// activity must not become a second identity.
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
		pubsub.WithNoAuthor(),
		// Required once messages have no author, since the default id is
		// sender plus sequence number. Hashing the payload also makes
		// identical messages collapse into one, network-wide.
		pubsub.WithMessageIdFn(func(m *pb.Message) string {
			sum := sha256.Sum256(m.Data)
			return string(sum[:])
		}),
	)
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
	if cfg.Policy.ServeBroadBuckets {
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
		n := newNode(h, kdht, st, cfg)
		n.logf("dht bootstrap failed, continuing without discovery: %v", err)
	}

	n := newNode(h, kdht, st, cfg)

	if cfg.Policy.ServeBroadBuckets {
		h.SetStreamHandler(BlockProtocol, n.handleBlockRequest)
		h.SetStreamHandler(CatalogueProtocol, n.handleCatalogueRequest)
		h.SetStreamHandler(ShardProtocol, n.handleShardRequest)
		// Relay service is cheap and makes the network work for people whose
		// routers do not cooperate. Only serving nodes run it.
		if _, err := relay.New(h); err != nil {
			n.logf("relay service unavailable: %v", err)
		}
	}
	// Registered independently of block service, and only for a node that may
	// answer it (WO-089). The pack is built from the titles this node has
	// seen, so serving it is observation-derived sharing even though the wire
	// form is a fixed-shape sketch. Level 1 still *fetches* packs — that is
	// Policy.FetchWordTelemetry, and it needs no handler.
	if cfg.Policy.ServeWordTelemetry {
		h.SetStreamHandler(WordTelemetryProtocol, n.handleWordTelemetry)
	}

	if ps, err := newPubSub(ctx, h); err != nil {
		// All of live/yield/sketch ride this one instance; losing it means
		// losing all three gossip subsystems, not the node — blocks,
		// catalogue and shard fetch/serve never touch pubsub.
		n.logf("pubsub unavailable, gossip subsystems disabled: %v", err)
	} else {
		n.ps = ps
	}

	// Level 2+ only since WO-089. Everything Live does — joining the topic,
	// relaying, seeding from local sightings, serving the snapshot — hangs off
	// this one call, so a Level-1 node has no live object, no subscription and
	// no snapshot handler at all. There is deliberately no receive-only mode:
	// see Policy.Live.
	if cfg.Policy.Live {
		if err := n.startLive(ctx); err != nil {
			// The live index is additive: without it the rest works.
			n.logf("live index unavailable: %v", err)
		}
	}
	// Three-gram topics exist to locate and size blocks *this* node serves, so
	// a node that serves none must not join them at all — not merely decline
	// to publish (WO-077). Both used to start unconditionally, which put every
	// Level-1 node on two topics it had nothing to say on and made its
	// participation visible to the mesh.
	if cfg.Policy.JoinSearchTelemetry {
		if err := n.startYield(ctx); err != nil {
			n.logf("yield gossip unavailable: %v", err)
		}
		if err := n.startSketch(ctx); err != nil {
			n.logf("sketch gossip unavailable: %v", err)
		}
	}

	n.logf("swarm up as %s (serving=%v)", h.ID(), cfg.Policy.ServeBroadBuckets)
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
	// Re-checked per request, not just at handler registration: a downgrade
	// shuts the gate before the host stops, and requests keep arriving in
	// that window (WO-077).
	if !n.mayServeBlocks() {
		return
	}
	// Independent of the level check above (WO-085): what this node is
	// permitted to answer and how much of it are separate questions, and a
	// modified peer can ask as often as it likes.
	release, ok := n.serve.admit(s.Conn().RemotePeer())
	if !ok {
		return
	}
	defer release()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	line, err := bufio.NewReader(io.LimitReader(s, 256)).ReadString('\n')
	if err != nil && err != io.EOF {
		return
	}
	prefix := trimLine(line)
	if prefix == "" {
		return
	}

	bucket, err := n.st.BlocksInPrefix(prefix, n.st.Cohort(), n.cfg.Policy.GraphSources(), maxBlocksPerReply)
	if err != nil {
		// Includes the anonymity-floor refusal (store.BucketAnonymityFloor):
		// serving nothing is the correct answer to "I cannot answer this
		// broadly", and it is indistinguishable from holding nothing.
		n.logf("prefix %s: %v", prefix, err)
		return
	}
	if bucket.Truncated {
		n.logf("prefix %s: capped at %d of %d held claims", prefix, len(bucket.Blocks), bucket.Held)
	}
	raw, err := json.Marshal(bucket)
	if err != nil {
		return
	}
	if !n.serve.chargeBytes(len(raw)) {
		n.logf("prefix %s: over the serving byte budget, dropping the reply", prefix)
		return
	}
	_, _ = s.Write(raw)
	if err := n.st.RecordContributionServe(len(raw)); err != nil {
		n.logf("prefix %s: recording contribution activity: %v", prefix, err)
	}
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
// The rows come from Policy.CatalogueSources — at Level 2, local and imported
// together (WO-084). Catalogue metadata is a public fact about a public video
// and travels in its own complete-prefix namespace, so what a requester learns
// is which bucket this node holds something in, at the same 12-bit granularity
// the graph path uses.
func (n *Node) handleCatalogueRequest(s network.Stream) {
	defer s.Close()
	if !n.mayServeBlocks() {
		return
	}
	release, ok := n.serve.admit(s.Conn().RemotePeer())
	if !ok {
		return
	}
	defer release()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	line, err := bufio.NewReader(io.LimitReader(s, 256)).ReadString('\n')
	if err != nil && err != io.EOF {
		return
	}
	prefix := trimLine(line)
	if prefix == "" {
		return
	}
	pack, err := n.st.BuildCataloguePack(prefix, n.cfg.Policy.CatalogueSources(), maxCatalogueRows)
	if err != nil {
		n.logf("catalogue %s: %v", prefix, err)
		return
	}
	raw, err := pack.Encode()
	if err != nil {
		return
	}
	if !n.serve.chargeBytes(len(raw)) {
		n.logf("catalogue %s: over the serving byte budget, dropping the reply", prefix)
		return
	}
	_, _ = s.Write(raw)
	if err := n.st.RecordContributionServe(len(raw)); err != nil {
		n.logf("catalogue %s: recording contribution activity: %v", prefix, err)
	}
}

// syncCatalogue fetches titles for every target in a graph bucket reply.
//
// The argument must be the whole bucket, never the blocks the caller cared
// about. Deriving the catalogue request set from a subset would identify which
// block was wanted and undo the graph fetch's anonymity — the reason this takes
// the full reply rather than a video id.
func (n *Node) syncCatalogue(ctx context.Context, blocks []store.Block) {
	if !n.cfg.Policy.Fetch || len(blocks) == 0 {
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

// provideAll publishes a set of CIDs with bounded concurrency.
//
// Each Provide is a full DHT walk taking seconds, and a node holding a real
// corpus has thousands of keys — announcing them one at a time takes hours,
// during which the node is not discoverable at all. Nothing about the DHT
// requires them to be sequential; the worker limit exists so a node does not
// open thousands of lookups at once.
//
// Returns how many succeeded and the first failure, so a round that publishes
// nothing can say why.
func provideAll[K any](ctx context.Context, n *Node, keys []K,
	cidFor func(K) (cid.Cid, error)) (int, error) {
	const workers = 12
	jobs := make(chan K)
	type result struct {
		ok  bool
		err error
	}
	results := make(chan result, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range jobs {
				c, err := cidFor(k)
				if err != nil {
					results <- result{err: err}
					continue
				}
				if err := n.dht.Provide(ctx, c, true); err != nil {
					results <- result{err: err}
					continue
				}
				results <- result{ok: true}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, k := range keys {
			select {
			case <-ctx.Done():
				return
			case jobs <- k:
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var ok int
	var firstErr error
	for r := range results {
		if r.ok {
			ok++
		} else if firstErr == nil {
			firstErr = r.err
		}
	}
	return ok, firstErr
}

// ErrAnnouncedNothing means the announce round published no provider records at
// all. A node in that state cannot be found by any other install, so a caller
// that retries on the usual multi-hour cadence would leave it invisible for
// hours — see announceLoop.
var ErrAnnouncedNothing = errors.New("published no provider records")

// RoutingTableSize is how many peers the DHT can actually route to.
//
// Provide fails outright with "failed to find any peer in table" while this is
// zero, which is the state right after start: joining the DHT and populating a
// routing table take time that announcing does not wait for.
func (n *Node) RoutingTableSize() int {
	if n.dht == nil {
		return 0
	}
	return n.dht.RoutingTable().Size()
}

// Announce publishes provider records for everything this node can serve.
//
// Called periodically; DHT provider records expire, so re-announcing is how a
// node stays findable. Silent no-op when not serving.
func (n *Node) Announce(ctx context.Context) error {
	// Gate-aware, not just policy-aware: this runs on a 6-hour timer, so a
	// downgrade must be able to stop the next tick from re-advertising a
	// cache the user just withdrew (WO-077).
	if !n.mayAnnounce() {
		return nil
	}
	keys, err := n.st.LocalPrefixes(n.prefixBits(), n.cfg.Policy.GraphSources())
	if err != nil {
		return err
	}
	announced, graphErr := provideAll(ctx, n, keys, prefixCID)

	// Catalogue buckets are announced separately, because the two datasets are
	// held independently — a node can hold titles for videos whose graph it has
	// evicted, and vice versa.
	catKeys, err := n.st.LocalCataloguePrefixes(n.prefixBits(), n.cfg.Policy.CatalogueSources())
	if err != nil {
		return err
	}
	catAnnounced, catErr := provideAll(ctx, n, catKeys, prefixCID)

	// Shards are announced separately again, same reasoning: a node's title
	// index and its shard index are recomputed from the same source but are
	// their own namespace (shardCID, shard.go), so a shard fetch and a
	// catalogue fetch for the same node never correlate.
	shardKeys, err := n.st.LocalShards(n.cfg.Policy.CatalogueSources())
	if err != nil {
		return err
	}
	shardAnnounced, shardErr := provideAll(ctx, n, shardKeys, shardCID)

	firstErr := graphErr
	if firstErr == nil {
		firstErr = catErr
	}
	if firstErr == nil {
		firstErr = shardErr
	}
	n.logf("announced %d/%d graph buckets, %d/%d catalogue buckets, %d/%d shards",
		announced, len(keys), catAnnounced, len(catKeys), shardAnnounced, len(shardKeys))
	// Announcing nothing means this node cannot be found by any other install,
	// which is indistinguishable from "nobody else is running Keel" — the two
	// have to be told apart, and only this line can do it.
	if announced == 0 && catAnnounced == 0 && shardAnnounced == 0 && firstErr != nil {
		n.logf("announce published NOTHING; this node is not discoverable: %v", firstErr)
		return fmt.Errorf("%w: %v", ErrAnnouncedNothing, firstErr)
	}
	return nil
}

// Fetch retrieves one block from the swarm and merges it locally.
//
// Returns the number of edges gained. A miss is not an error: no provider for a
// video simply means nobody holding it is online, which is the normal case for
// the long tail.
func (n *Node) Fetch(ctx context.Context, key string) (int64, error) {
	if !n.cfg.Policy.Fetch {
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

// importReply merges every claim in a bucket reply.
//
// Taking the whole bucket is what makes the request k-anonymous, and it is also
// what makes the node useful to everyone else: the cover traffic becomes
// hosting capacity, and every claim kept here is one this node can hand on
// unchanged. One bad block is skipped rather than failing the batch — a peer
// must not be able to poison a whole bucket with one bad row.
//
// A truncated reply is logged, not rejected. It is a smaller anonymity set than
// the bucket promised, which the requester deserves to know about (WO-084);
// discarding it would trade a real answer for nothing.
func (n *Node) importReply(raw []byte) (imported []store.Block, blocks int, edges int64) {
	var bucket store.BlockBucket
	if err := json.Unmarshal(raw, &bucket); err != nil {
		return nil, 0, 0
	}
	if bucket.Truncated {
		n.logf("bucket %s arrived truncated: %d of %d claims", bucket.Prefix, len(bucket.Blocks), bucket.Held)
	}
	for i := range bucket.Blocks {
		encoded, err := bucket.Blocks[i].Encode()
		if err != nil {
			continue
		}
		_, got, err := n.st.ImportBlock(encoded)
		switch {
		case errors.Is(err, store.ErrOwnClaim):
			// This node's own claim came back around a relay cycle. It is
			// still part of the bucket the catalogue sync must cover — that
			// request set has to be a function of the bucket's whole public
			// contents, never of what this node chose to keep — but it
			// contributes no edges and is not stored.
			imported = append(imported, bucket.Blocks[i])
		case err == nil:
			imported = append(imported, bucket.Blocks[i])
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
	// Gated like the discovery path. A direct dial is still a request, and a
	// request still tells the peer answering it which bucket was asked about
	// — skipping discovery does not skip the disclosure, so it must not skip
	// the capability check either (WO-077).
	if !n.cfg.Policy.Fetch {
		return 0, fmt.Errorf("fetching is not enabled at this contribution level")
	}
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
