// SPDX-License-Identifier: Apache-2.0
// The live index (WO-052, DESIGN_v2 §7.5).
//
// Livestreams are the one dataset small enough to hand every node in full, and
// that is exactly why they get the strongest privacy property in the system.
//
// Blocks and the catalogue are fetched by prefix bucket, which leaks a little:
// *which* bucket a node asks for narrows its interests. The live index is a few
// hundred KB across the whole network, so a node subscribes to all of it. There
// is no bucket to choose and therefore nothing to narrow — a peer learns only
// that this node looks at livestreams, which is what running the feature means.
//
// The dividing line for the whole system: **gossip what is small and ephemeral,
// fetch what is large and durable.**
//
// Search happens entirely in local memory. The daemon never sends a keyword
// anywhere; it holds the whole index and filters it on this machine.
//
// **Messages carry no author, so publishing is ungated.** An earlier version
// signed them and counted distinct publishers as corroboration, which forced
// publishing behind Level 2. That was the wrong trade twice over:
//
//   - It broke the product. Most livestreams have one observer, and a feed
//     showing only corroborated streams is YouTube's live search with extra
//     steps. The long tail — streams almost nobody sees — is the entire reason
//     to build this.
//   - It cost privacy for nothing. Signed messages name their origin to every
//     subscriber, not just the first hop, so reporting a stream disclosed that
//     the reporter saw it.
//
// Unsigned and authorless, a report says only "this stream exists". In a gossip
// mesh originating and forwarding are indistinguishable to neighbours, so even a
// direct peer cannot tell whether this node saw the stream or is passing on
// somebody else's report. Every node can therefore report at every level,
// including the default, which is also what fills the long tail.
//
// What is given up: records remain unverifiable claims — nothing signs YouTube
// state (DESIGN_v2 §6.4) — and without authorship they cannot be corroborated by
// counting publishers. Flooding is handled at the transport instead, by
// gossipsub's peer scoring of whoever *delivers* a message, by the topic
// validator, and by a cap on the index. That is weaker, and the right call here:
// a bogus livestream entry is a nuisance, where a bogus edge would corrupt the
// research corpus.
package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// LiveTopic is the gossipsub topic carrying the whole live index.
//
// One topic rather than per-prefix topics: sharding would reintroduce the
// selection disclosure that subscribing to everything avoids. Shard only if
// volume ever forces it, and take that cost knowingly.
const LiveTopic = "keel/live/1"

// LiveSnapshotProtocol hands a joining node the whole current index.
//
// Gossip carries only what is published after a node subscribes, so a daemon
// that has just started holds nothing and would take hours to fill — worse now
// that publish suppression keeps redundant announcements off the wire. A
// snapshot makes the feed useful the moment the process starts.
//
// Requesting it leaks nothing: there is no query, the whole index is asked for
// every time, and it is the same index every node holds. This is §7.3a tier 1,
// the same shape as the seed pack.
const LiveSnapshotProtocol = protocol.ID("/keel/live-snapshot/1.0.0")

// maxSnapshotBytes bounds a snapshot reply.
const maxSnapshotBytes = 16 << 20

const (
	// liveTTL is how long a record survives without being seen again.
	//
	// Long on purpose. The index does not try to mirror what is live this
	// instant; letting records linger removes the need for heartbeats, which
	// would otherwise tie one identity to one stream over time — the trajectory
	// problem ephemeral identity exists to prevent. A stale record costs almost
	// nothing, because a finished livestream keeps its URL and resolves to the
	// recording.
	liveTTL = 12 * time.Hour

	// maxLiveRecords bounds memory against a flood. Gossipsub's peer scoring is
	// the first defence; this is the backstop.
	maxLiveRecords = 200000

	// maxLiveRecordBytes rejects oversized messages before they are parsed.
	maxLiveRecordBytes = 4096

	liveSweepInterval = 10 * time.Minute

	// liveTimeGranularity rounds the sighting time in a record.
	//
	// This is what makes duplicate reports nearly free. Message ids are the hash
	// of the payload, so two nodes reporting the same stream in the same hour
	// produce byte-identical messages, which gossipsub recognises as duplicates
	// and stops forwarding. Without rounding, a per-millisecond timestamp would
	// make every report unique and the network would carry one message per
	// sighting rather than one per stream per hour.
	//
	// A coarse timestamp is also less of a fingerprint than an exact one.
	liveTimeGranularity = time.Hour

	// liveRefreshAfter lets a stream be re-announced once its record is ageing,
	// so suppression cannot let a still-running stream expire out of the index.
	liveRefreshAfter = liveTTL / 4
)

// LiveRecord is one announcement that a stream was seen live.
type LiveRecord struct {
	VideoID   string `json:"v"`
	Title     string `json:"t,omitempty"`
	ChannelID string `json:"c,omitempty"`
	// SeenAt is when the publisher observed it, in unix milliseconds.
	SeenAt int64 `json:"s"`
}

// LiveEntry is the merged view of one stream across every node that reported it.
type LiveEntry struct {
	LiveRecord
	// FirstSeen and LastSeen bound local knowledge, not the stream itself.
	FirstSeen int64 `json:"first_seen"`
	LastSeen  int64 `json:"last_seen"`
}

type liveEntry struct {
	rec       LiveRecord
	firstSeen time.Time
	lastSeen  time.Time
}

// LiveIndex is the in-memory index. Nothing here is written to disk: these
// records are worthless within a day, and persisting them would accumulate dead
// rows in a database whose other tables are durable by design.
type LiveIndex struct {
	mu      sync.RWMutex
	entries map[string]*liveEntry

	topic *pubsub.Topic
	sub   *pubsub.Subscription
	self  peer.ID
	logf  func(string, ...any)
}

// startLive joins the topic and begins accumulating.
//
// **Every node subscribes, including Level 1.** An earlier version gated this on
// Fetch, which contradicted §7.3a: gossip is a tier-1 mechanism, meaning there
// is no per-item query at all. Level 1's promise is that nothing about what the
// user watches leaves the machine, and subscribing discloses only that this node
// looks at livestreams — the same class of fact as downloading the seed everyone
// downloads.
//
// So the default, maximum-privacy setting gets a working global live feed while
// disclosing nothing about its user. That asymmetry is the point: receiving is
// free, publishing is the part that costs something.
//
// The one real cost is that gossipsub subscribers relay for their topic — a node
// cannot receive without carrying traffic for others. At a few hundred KB a day
// that is a fair bargain, and it is intrinsic to gossip rather than a policy
// choice.
func (n *Node) startLive(ctx context.Context) error {
	ps, err := pubsub.NewGossipSub(ctx, n.host,
		// No signature and no author: a report must not name whoever saw the
		// stream. Neighbours cannot distinguish originating from forwarding, so
		// publishing discloses nothing and needs no contribution gate.
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
		pubsub.WithNoAuthor(),
		// Required once messages have no author, since the default id is
		// sender plus sequence number. Hashing the payload also makes identical
		// reports collapse into one message network-wide.
		pubsub.WithMessageIdFn(func(m *pb.Message) string {
			sum := sha256.Sum256(m.Data)
			return string(sum[:])
		}),
	)
	if err != nil {
		return err
	}
	// Reject junk before it reaches any subscriber, so a malformed flood costs
	// the network one hop rather than propagating.
	if err := ps.RegisterTopicValidator(LiveTopic, validateLiveMessage); err != nil {
		return err
	}
	topic, err := ps.Join(LiveTopic)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		return err
	}

	n.live = &LiveIndex{
		entries: map[string]*liveEntry{},
		topic:   topic,
		sub:     sub,
		self:    n.host.ID(),
		logf:    n.logf,
	}
	// Serve snapshots to other joining nodes. Ungated: the records are what
	// gossip already broadcast to everyone, so passing them on discloses
	// nothing this node did.
	n.host.SetStreamHandler(LiveSnapshotProtocol, n.handleLiveSnapshot)

	go n.live.consume(ctx)
	go n.live.sweep(ctx)
	go n.backfillLive(ctx)
	n.logf("live index subscribed to %s", LiveTopic)
	return nil
}

// handleLiveSnapshot returns every record this node holds.
func (n *Node) handleLiveSnapshot(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))
	if n.live == nil {
		return
	}
	raw, err := json.Marshal(n.live.Snapshot())
	if err != nil {
		return
	}
	_, _ = s.Write(raw)
}

// Snapshot returns the live records this node holds.
func (li *LiveIndex) Snapshot() []LiveRecord {
	li.mu.RLock()
	defer li.mu.RUnlock()
	out := make([]LiveRecord, 0, len(li.entries))
	for _, e := range li.entries {
		out = append(out, e.rec)
	}
	return out
}

// backfillLive asks connected peers for their index once a mesh exists.
//
// Peers are asked in turn until one answers, and their records are merged as
// though they had been gossiped.
func (n *Node) backfillLive(ctx context.Context) {
	for attempt := 0; attempt < 12; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		if n.live == nil || n.live.Size() > 0 {
			return // gossip already filled it, or we are shutting down
		}
		for _, p := range n.host.Network().Peers() {
			if n.fetchLiveSnapshot(ctx, p) {
				return
			}
		}
	}
}

func (n *Node) fetchLiveSnapshot(ctx context.Context, p peer.ID) bool {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	s, err := n.host.NewStream(ctx, p, LiveSnapshotProtocol)
	if err != nil {
		return false
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))
	if err := s.CloseWrite(); err != nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(s, maxSnapshotBytes))
	if err != nil || len(raw) == 0 {
		return false
	}
	var recs []LiveRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		return false
	}
	for _, r := range recs {
		if len(r.VideoID) == 11 {
			n.live.merge(r)
		}
	}
	if len(recs) > 0 {
		n.logf("live index backfilled with %d records from %s", len(recs), p)
	}
	return len(recs) > 0
}

// validateLiveMessage is the gossipsub validator. It runs before a message is
// forwarded, so rejecting here stops propagation rather than merely ignoring.
func validateLiveMessage(_ context.Context, _ peer.ID, msg *pubsub.Message) bool {
	if len(msg.Data) > maxLiveRecordBytes {
		return false
	}
	var r LiveRecord
	if err := json.Unmarshal(msg.Data, &r); err != nil {
		return false
	}
	if len(r.VideoID) != 11 || len(r.Title) > 300 {
		return false
	}
	// A record claiming to have been seen far in the future is either a broken
	// clock or an attempt to outlive the TTL. Neither is worth forwarding.
	if r.SeenAt > time.Now().Add(time.Hour).UnixMilli() {
		return false
	}
	return true
}

func (li *LiveIndex) consume(ctx context.Context) {
	for {
		msg, err := li.sub.Next(ctx)
		if err != nil {
			return // context cancelled or subscription closed
		}
		var r LiveRecord
		if err := json.Unmarshal(msg.Data, &r); err != nil {
			continue
		}
		li.merge(r)
	}
}

// merge folds one report into the index.
//
// There is no publisher to record: messages are authorless by design, and the
// index is a set of streams rather than a tally of who saw what.
func (li *LiveIndex) merge(r LiveRecord) {
	if r.VideoID == "" {
		return
	}
	now := time.Now()
	li.mu.Lock()
	defer li.mu.Unlock()

	e := li.entries[r.VideoID]
	if e == nil {
		if len(li.entries) >= maxLiveRecords {
			return // backstop; sweep will make room
		}
		e = &liveEntry{rec: r, firstSeen: now}
		li.entries[r.VideoID] = e
	}
	// Keep the richest version seen: a later report may carry a title an
	// earlier one lacked.
	if r.Title != "" {
		e.rec.Title = r.Title
	}
	if r.ChannelID != "" {
		e.rec.ChannelID = r.ChannelID
	}
	if r.SeenAt > e.rec.SeenAt {
		e.rec.SeenAt = r.SeenAt
	}
	e.lastSeen = now
}

// sweep expires records. Nothing else removes them, so this is what keeps the
// index bounded.
func (li *LiveIndex) sweep(ctx context.Context) {
	t := time.NewTicker(liveSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-liveTTL)
			li.mu.Lock()
			for id, e := range li.entries {
				if e.lastSeen.Before(cutoff) {
					delete(li.entries, id)
				}
			}
			li.mu.Unlock()
		}
	}
}

// shouldPublish reports whether this node's announcement would add anything.
//
// Duplicate reports are already nearly free — identical payloads share a message
// id and stop propagating — but originating one still costs a round of mesh
// traffic. A node announces a stream it has not heard of, or one whose record is
// ageing, so suppression cannot quietly let a still-running stream expire.
func (li *LiveIndex) shouldPublish(videoID string) bool {
	li.mu.RLock()
	defer li.mu.RUnlock()
	e := li.entries[videoID]
	if e == nil {
		return true
	}
	return time.Since(e.lastSeen) > liveRefreshAfter
}

// Publish announces one stream.
//
// Callers must gate this on contribution level — see PublishLive on Node, which
// does. Publishing is the disclosing half of this feature.
func (li *LiveIndex) Publish(ctx context.Context, r LiveRecord) error {
	if r.SeenAt == 0 {
		r.SeenAt = time.Now().UnixMilli()
	}
	// Round so two nodes reporting the same stream in the same hour emit
	// byte-identical messages, which the network then carries only once.
	r.SeenAt = time.UnixMilli(r.SeenAt).Truncate(liveTimeGranularity).UnixMilli()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// Fold our own announcement in locally: gossipsub does not deliver a node
	// its own messages, and a user should see their own discovery in the feed.
	li.merge(r)
	return li.topic.Publish(ctx, raw)
}

// Search filters the index locally.
//
// This is the whole privacy argument for the feature: the query never leaves the
// machine, because the machine already holds the entire index. minPublishers
// applies the corroboration filter.
func (li *LiveIndex) Search(query string, limit int) []LiveEntry {
	if limit <= 0 {
		limit = 100
	}
	terms := strings.Fields(strings.ToLower(query))
	cutoff := time.Now().Add(-liveTTL)

	li.mu.RLock()
	defer li.mu.RUnlock()

	out := make([]LiveEntry, 0, 64)
	for _, e := range li.entries {
		if e.lastSeen.Before(cutoff) {
			continue
		}
		if len(terms) > 0 {
			hay := strings.ToLower(e.rec.Title + " " + e.rec.ChannelID)
			matched := true
			for _, t := range terms {
				if !strings.Contains(hay, t) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, LiveEntry{
			LiveRecord: e.rec,
			FirstSeen:  e.firstSeen.UnixMilli(),
			LastSeen:   e.lastSeen.UnixMilli(),
		})
	}

	// Most recently reported first. There is no popularity signal here and that
	// is deliberate: this feed exists for the streams nobody else surfaces, so
	// ranking by any measure of attention would rebuild what it replaces.
	sort.Slice(out, func(a, b int) bool {
		if out[a].LastSeen != out[b].LastSeen {
			return out[a].LastSeen > out[b].LastSeen
		}
		return out[a].VideoID < out[b].VideoID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Size reports how many records are held.
func (li *LiveIndex) Size() int {
	li.mu.RLock()
	defer li.mu.RUnlock()
	return len(li.entries)
}

// Live returns the index, or nil when not subscribed.
func (n *Node) Live() *LiveIndex { return n.live }

// PublishLive announces a stream.
//
// Ungated at every level, including the default. A report carries no author, so
// it discloses nothing about who saw the stream, and every node reporting is
// what fills the long tail this feed exists for.
func (n *Node) PublishLive(ctx context.Context, r LiveRecord) {
	if n.live == nil {
		return
	}
	if !n.live.shouldPublish(r.VideoID) {
		return
	}
	if err := n.live.Publish(ctx, r); err != nil {
		n.logf("live publish %s: %v", r.VideoID, err)
	}
}
