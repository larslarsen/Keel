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
// Two things are deliberately not solved here:
//
//   - **Publishing discloses that the publisher saw the stream.** For a popular
//     stream that means nothing; for a rare one the publisher set approximates
//     the viewer set. Publishing is therefore opt-in at Level 2 and above and
//     never happens at Level 1. This is weaker than the receive side, which
//     discloses nothing, and the asymmetry is real.
//   - **Records are unverifiable claims.** Nothing signs YouTube state
//     (DESIGN_v2 §6.4), so a node can announce a stream that is not live or
//     attach a misleading title. Corroboration by distinct publishers raises the
//     cost without pretending to fix it.
package swarm

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
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

	// liveEnoughPublishers is the point at which a node stops announcing a
	// stream others have already announced.
	//
	// This is what makes the feature scale, and the reason is easy to miss: the
	// index is small — a few hundred KB of distinct streams — but *message*
	// volume is not, because it grows with publishers × sightings rather than
	// with distinct streams. A thousand users seeing one popular stream would
	// otherwise send a thousand messages carrying one fact, and at a million
	// users that is gigabytes a day rather than kilobytes.
	//
	// Suppression collapses it back: the first few observers announce, everyone
	// after them stays quiet, and traffic scales with distinct streams again.
	liveEnoughPublishers = 3

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
	// Publishers counts distinct peers that reported this stream. It is the
	// corroboration signal: one report is a claim, several from unrelated peers
	// is harder to fake. It is not sybil-proof and must not be presented as
	// proof of anything.
	Publishers int `json:"publishers"`
	// FirstSeen and LastSeen bound local knowledge, not the stream itself.
	FirstSeen int64 `json:"first_seen"`
	LastSeen  int64 `json:"last_seen"`
}

type liveEntry struct {
	rec        LiveRecord
	publishers map[peer.ID]bool
	firstSeen  time.Time
	lastSeen   time.Time
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
	ps, err := pubsub.NewGossipSub(ctx, n.host)
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
// though they had been gossiped. Corroboration counts are not carried over: a
// snapshot is one peer's word for all of it, and inheriting its publisher counts
// would let a single node manufacture apparent agreement.
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
			n.live.merge(r, p)
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
		li.merge(r, msg.GetFrom())
	}
}

// merge folds one report into the index.
//
// Publisher identity comes from gossipsub, which signs messages with the
// sender's key, so corroboration needs no application-level signature. Under
// ephemeral identity a node's key changes per session, which weakens
// corroboration slightly — the same node across two sessions counts twice — and
// that is the correct trade: unlinkability matters more than an exact count.
func (li *LiveIndex) merge(r LiveRecord, from peer.ID) {
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
		e = &liveEntry{rec: r, publishers: map[peer.ID]bool{}, firstSeen: now}
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
	e.publishers[from] = true
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
// Staying quiet about a well-corroborated, freshly-reported stream costs the
// network nothing and saves it a message. It also happens to reduce disclosure:
// an announcement says its publisher saw the stream, so not making a redundant
// one is strictly better for the publisher too.
func (li *LiveIndex) shouldPublish(videoID string) bool {
	li.mu.RLock()
	defer li.mu.RUnlock()
	e := li.entries[videoID]
	if e == nil {
		return true // nobody has reported it
	}
	if len(e.publishers) < liveEnoughPublishers {
		return true // corroboration is still worth adding
	}
	// Well corroborated. Announce only if the record is ageing, so suppression
	// cannot quietly let a still-running stream expire.
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
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// Fold our own announcement in locally: gossipsub does not deliver a node
	// its own messages, and a user should see their own discovery in the feed.
	li.merge(r, li.self)
	return li.topic.Publish(ctx, raw)
}

// Search filters the index locally.
//
// This is the whole privacy argument for the feature: the query never leaves the
// machine, because the machine already holds the entire index. minPublishers
// applies the corroboration filter.
func (li *LiveIndex) Search(query string, minPublishers, limit int) []LiveEntry {
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
		if len(e.publishers) < minPublishers {
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
			Publishers: len(e.publishers),
			FirstSeen:  e.firstSeen.UnixMilli(),
			LastSeen:   e.lastSeen.UnixMilli(),
		})
	}

	// Most corroborated first, then most recently reported. Corroboration leads
	// because it is the only signal here that resists a single bad actor.
	sort.Slice(out, func(a, b int) bool {
		if out[a].Publishers != out[b].Publishers {
			return out[a].Publishers > out[b].Publishers
		}
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

// PublishLive announces a stream if this node's level permits it.
//
// Serve is the Level 2 gate, and it is the only gate this feature needs. A Level
// 1 node receives the whole feed and announces nothing — publishing is what
// discloses that its publisher saw the stream.
func (n *Node) PublishLive(ctx context.Context, r LiveRecord) {
	if n.live == nil || !n.cfg.Serve {
		return
	}
	if !n.live.shouldPublish(r.VideoID) {
		return
	}
	if err := n.live.Publish(ctx, r); err != nil {
		n.logf("live publish %s: %v", r.VideoID, err)
	}
}
