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
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/store"
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
//
// No key scheme in this id, unlike the block and catalogue protocols (WO-060).
// The live index is not bucketed — entries are keyed by platform and video id,
// which no constant in the key scheme touches — so widening prefix buckets or
// changing the tokenizer would partition the live mesh for no reason. It is the
// feature that most needs every node it can get.
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

	// maxLiveAge bounds how long a stream can plausibly be "live". Even
	// marathon charity / music streams end; a record whose first sighting is
	// older than this cannot still be running. Without this bound, a stream a
	// peer keeps re-announcing stays in the index forever (its SeenAt is bumped
	// to "now" on every gossip, so the LiveRecency freeze and the 12h sweep
	// never fire) while firstSeen stays anchored in the past — so the UI shows
	// "17+ hours" live with a "5 min ago" last-seen. maxLiveAge lets us retire
	// such dead entries outright, regardless of peer re-claims.
	maxLiveAge = 12 * time.Hour

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
	// Platform the stream is on: "yt", "tt". Absent means YouTube, so records
	// from older nodes still merge correctly.
	Platform string `json:"p,omitempty"`
	// StartedAt is the earliest anyone reporting this stream saw it live.
	//
	// A lower bound on how long it has been running. "Seen live just now" is
	// true of a stream that has been going for eleven hours and tells a user
	// nothing they can act on; how long it has been live tells them what kind
	// of thing it is.
	StartedAt int64 `json:"b,omitempty"`
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
	mu          sync.RWMutex
	entries     map[string]*liveEntry
	localSeenAt map[string]int64
	// retired holds keys we have decided are dead (firstSeen older than
	// maxLiveAge) along with when the tombstone expires. A dead stream must not
	// be re-admitted by a peer re-announcement: without it, the sweep deletes
	// the frozen entry and the next gossip re-inserts it fresh (firstSeen =
	// now), resurrecting the very stream we just retired. The tombstone refuses
	// re-admission for maxLiveAge, after which a genuinely-still-live stream may
	// return.
	retired map[string]time.Time

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
	if n.ps == nil {
		return fmt.Errorf("pubsub not available")
	}
	// Reject junk before it reaches any subscriber, so a malformed flood costs
	// the network one hop rather than propagating.
	if err := n.ps.RegisterTopicValidator(LiveTopic, validateLiveMessage); err != nil {
		return err
	}
	topic, err := n.ps.Join(LiveTopic)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		return err
	}

	n.live = &LiveIndex{
		entries:     map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		topic:       topic,
		sub:         sub,
		self:        n.host.ID(),
		logf:        n.logf,
	}
	// Serve snapshots to other joining nodes. Ungated: the records are what
	// gossip already broadcast to everyone, so passing them on discloses
	// nothing this node did.
	n.host.SetStreamHandler(LiveSnapshotProtocol, n.handleLiveSnapshot)

	n.seedLiveFromLocal()
	go n.live.consume(ctx)
	go n.live.sweep(ctx)
	go n.backfillLive(ctx)
	n.logf("live index subscribed to %s", LiveTopic)
	return nil
}

// seedLiveFromLocal replays this node's own recent sightings into the index.
//
// The index is in-memory by design, so a restart empties it, and with no peers
// it refills only from what this node happens to see next. That made the Live
// tab look emptier and staler after every rebuild. The sightings are already on
// disk in `impressions`, so replaying them costs nothing and persists nothing
// new.
//
// Seeded records keep their true observation time rather than being stamped
// with now — the whole point of this pass is that observation time is what the
// feed ranks and filters on.
func (n *Node) seedLiveFromLocal() {
	if n.live == nil {
		return
	}
	cutoff := time.Now().Add(-liveTTL).UnixMilli()
	seen, err := n.st.RecentLiveSightings(cutoff)
	if err != nil || len(seen) == 0 {
		return
	}
	for _, v := range seen {
		n.live.merge(LiveRecord{
			VideoID: v.VideoID, Title: v.Title, ChannelID: v.ChannelID,
			SeenAt: v.SeenAt, StartedAt: v.StartedAt, Platform: v.Platform,
		})
		n.live.setLocalSeenAt(v.Platform, v.VideoID, v.SeenAt)
	}
	n.logf("live index seeded with %d local sightings", len(seen))
}

// handleLiveSnapshot returns every record this node holds.
func (n *Node) handleLiveSnapshot(s network.Stream) {
	defer s.Close()
	// Level 2+ (WO-089), and gate-aware. startLive is the only thing that
	// registers this handler, so a Level-1 node never reaches it — but a
	// downgrade shuts the gate before the node is torn down, and the snapshot
	// is built from an index this user's own sightings seeded.
	if !n.mayServeLive() {
		return
	}
	// Limited as well as gated (WO-085). The snapshot is the largest single
	// reply this node produces on demand, so it needs the byte budget most.
	release, ok := n.serve.admit(s.Conn().RemotePeer())
	if !ok {
		return
	}
	defer release()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))
	if n.live == nil {
		return
	}
	raw, err := json.Marshal(n.live.Snapshot())
	if err != nil {
		return
	}
	if !n.serve.chargeBytes(len(raw)) {
		n.logf("live snapshot: over the serving byte budget, dropping the reply")
		return
	}
	_, _ = s.Write(raw)
	if err := n.st.RecordContributionServe(len(raw)); err != nil {
		n.logf("live snapshot: recording contribution activity: %v", err)
	}
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
	if !validVideoID(r.Platform, r.VideoID) || len(r.Title) > 300 {
		return false
	}
	// A record with no observation time cannot be ranked or filtered honestly —
	// the feed sorts on when a stream was seen, and promotion depends on it
	// being recent. Forwarding one would put an unplaceable entry into every
	// node's index.
	if r.SeenAt <= 0 {
		return false
	}
	// A record claiming to have been seen far in the future is either a broken
	// clock or an attempt to outlive the TTL. Neither is worth forwarding.
	if r.SeenAt > time.Now().Add(time.Hour).UnixMilli() {
		return false
	}
	return true
}

// validVideoID checks an id against the platform that claims it.
//
// YouTube ids are exactly 11 characters from a known alphabet. TikTok ids are
// numeric and longer, and vary in length, so the old blanket length check would
// have rejected every TikTok stream. An unknown platform is refused outright
// rather than waved through — a record naming a platform this build does not
// understand cannot be displayed or acted on, and accepting it would put
// unusable entries in everyone's index.
func validVideoID(platform, id string) bool {
	switch platform {
	case "", "yt":
		if len(id) != 11 {
			return false
		}
		for _, c := range id {
			if !(c == '-' || c == '_' ||
				(c >= '0' && c <= '9') ||
				(c >= 'a' && c <= 'z') ||
				(c >= 'A' && c <= 'Z')) {
				return false
			}
		}
		return true
	case "tt":
		if len(id) < 15 || len(id) > 25 {
			return false
		}
		for _, c := range id {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	default:
		return false
	}
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

// retire marks a key as a dead stream for maxLiveAge, so peer re-announcements
// cannot re-admit it (see the retired field note).
func (li *LiveIndex) retire(key string, now time.Time) {
	if li.retired == nil {
		li.retired = map[string]time.Time{}
	}
	li.retired[key] = now.Add(maxLiveAge)
}

// isRetired reports whether key is currently tombstoned.
func (li *LiveIndex) isRetired(key string, now time.Time) bool {
	exp, ok := li.retired[key]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(li.retired, key) // tombstone expired; let it return if live
		return false
	}
	return true
}

// merge folds one report into the index.
//
// There is no publisher to record: messages are authorless by design, and the
// index is a set of streams rather than a tally of who saw what.
func (li *LiveIndex) merge(r LiveRecord) {
	if r.SeenAt <= 0 || !validVideoID(r.Platform, r.VideoID) {
		return // unplaceable: see validateLiveMessage
	}
	if r.Platform == "" {
		r.Platform = "yt"
	}
	now := time.Now()
	li.mu.Lock()
	defer li.mu.Unlock()

	// A livestream cannot have been "seen" longer ago than maxLiveAge and still
	// be running. A peer reporting SeenAt that old is re-announcing a stream
	// that has finished; folding it in would re-seed a dead entry (see the
	// maxLiveAge note on the constant). Drop it.
	if now.UnixMilli()-r.SeenAt > maxLiveAge.Milliseconds() {
		return
	}

	// Keyed by platform and id together. Nothing guarantees TikTok and YouTube
	// will never mint the same string, and one colliding id would merge two
	// unrelated streams into one entry.
	key := r.Platform + ":" + r.VideoID
	e := li.entries[key]
	// A stream whose start time is older than maxLiveAge cannot still be live
	// — even a marathon ends. Use StartedAt (the stream's own start, which
	// survives re-publishing) when present; fall back to firstSeen. Keying off
	// firstSeen alone misses streams a node keeps re-observing: each local
	// sighting refreshes firstSeen, so the entry never ages out and a 17h-old
	// stream shows forever (the "17+ hours / 24 min ago" bug).
	startMs := r.StartedAt
	if startMs <= 0 {
		if e != nil {
			startMs = e.firstSeen.UnixMilli()
		} else {
			startMs = now.UnixMilli()
		}
	}
	if now.UnixMilli()-startMs > maxLiveAge.Milliseconds() {
		if e != nil {
			li.retire(key, now)
			e.lastSeen = e.firstSeen
		}
		return
	}
	if e == nil {
		// Refuse to re-admit a stream we have tombstoned as dead. Without
		// this, a peer re-announcement after the sweep deletes the frozen
		// entry would re-insert it fresh and it would reappear in the feed.
		if li.isRetired(key, now) {
			return
		}
		if len(li.entries) >= maxLiveRecords {
			return // backstop; sweep will make room
		}
		// First insert: if we have a local observation, use it to guard
		// against incredible peer claims. A peer claiming SeenAt more recent
		// than localSeenAt + LiveRecency (i.e., stream live well after we saw
		// it end) is not credible. If we have no local observation, accept the
		// peer's claim — the long-tail tradeoff for unsigned gossip.
		initialSeenAt := r.SeenAt
		if localSeenAt, ok := li.localSeenAt[key]; ok {
			if r.SeenAt > localSeenAt+store.LiveRecency.Milliseconds() {
				// Peer claims stream live long after our local sighting ended.
				// Not credible — keep our true observation time.
				initialSeenAt = localSeenAt
			} else if localSeenAt > initialSeenAt {
				// Our local sighting is more recent than peer's claim.
				initialSeenAt = localSeenAt
			}
		}
		// Copy the record and override only what the guard decided. Listing
		// fields by hand here silently dropped Platform and StartedAt when they
		// were added — a constructor that has to be updated for every new field
		// will eventually not be.
		rec := r
		rec.SeenAt = initialSeenAt
		e = &liveEntry{rec: rec, firstSeen: now}
		li.entries[key] = e
	}
	// Accumulate the earliest start seen. A peer (or this node's earlier
	// sighting) that spotted the stream live before we did lowers StartedAt,
	// which is the lower bound on how long it has been running — and the field
	// the maxLiveAge cap keys off. Without this, a later report with
	// StartedAt=0 (the default when a sender omits it) would wipe a real
	// start time, defeating the staleness filter.
	if r.StartedAt > 0 && (e.rec.StartedAt == 0 || r.StartedAt < e.rec.StartedAt) {
		e.rec.StartedAt = r.StartedAt
	}
	// Keep the richest version seen: a later report may carry a title an
	// earlier one lacked.
	if r.Title != "" {
		e.rec.Title = r.Title
	}
	if r.ChannelID != "" {
		e.rec.ChannelID = r.ChannelID
	}
	// Only accept a newer SeenAt while this stream is still inside the live
	// window. Once its last observation is older than LiveRecency the stream is
	// finished; letting re-gossip keep bumping SeenAt would make a dead stream
	// look fresh forever (a peer re-announcing an old stream pushes SeenAt
	// forward, and liveRefreshAfter re-announces indefinitely). Freeze the
	// stored SeenAt once it has aged out, so the display shows the true age and
	// sweep retires it.
	//
	// Use localSeenAt (our own last local observation) as the source of truth.
	// If we have a local observation and it's > LiveRecency old, the stream is
	// finished — reject any peer claim that would bump SeenAt forward. This
	// prevents re-gossip from resurrecting dead streams, regardless of whether
	// the entry was created via first-insert or already existed.
	acceptSeenAtBump := true
	if localSeenAt, ok := li.localSeenAt[key]; ok {
		// Our last local observation is the ground truth. If it's older than
		// LiveRecency, the stream ended — don't let peer claims revive it.
		if now.UnixMilli()-localSeenAt > store.LiveRecency.Milliseconds() {
			acceptSeenAtBump = false
		}
	} else {
		// No local observation: fall back to the existing heuristic (stored
		// SeenAt within LiveRecency). This is the long-tail tradeoff.
		if e.rec.SeenAt < now.Add(-store.LiveRecency).UnixMilli() {
			acceptSeenAtBump = false
		}
	}
	if acceptSeenAtBump && r.SeenAt > e.rec.SeenAt {
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
func (li *LiveIndex) shouldPublish(platform, videoID string) bool {
	if platform == "" {
		platform = "yt"
	}
	li.mu.RLock()
	defer li.mu.RUnlock()
	e := li.entries[platform+":"+videoID]
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
	// Cut on when the stream was *observed*, not on when this node last heard
	// gossip about it. Records are re-announced while they age, so lastSeen
	// stays warm for a stream that finished hours ago — filtering on it lets
	// dead streams sit at the top of the feed forever.
	cutoff := time.Now().Add(-liveTTL).UnixMilli()
	// A stream first seen longer ago than maxLiveAge cannot still be live.
	maxAgeCutoff := time.Now().Add(-maxLiveAge)

	li.mu.RLock()
	defer li.mu.RUnlock()

	out := make([]LiveEntry, 0, 64)
	for _, e := range li.entries {
		if e.rec.SeenAt < cutoff {
			continue
		}
		// Skip streams whose start time is older than maxLiveAge: they cannot
		// still be live (a finished stream lingers only because peers keep
		// re-announcing it, and a node re-observing it refreshes firstSeen).
		// Key off StartedAt when present, else firstSeen. Hidden from the feed
		// immediately; the sweep retires the entry from memory.
		startMs := e.rec.StartedAt
		if startMs <= 0 {
			startMs = e.firstSeen.UnixMilli()
		}
		if startMs < maxAgeCutoff.UnixMilli() {
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

	// Most recently *seen live* first — again the observation time, not gossip
	// freshness. There is no popularity signal here and that is deliberate: this
	// feed exists for the streams nobody else surfaces, so ranking by any
	// measure of attention would rebuild what it replaces.
	sort.Slice(out, func(a, b int) bool {
		if out[a].SeenAt != out[b].SeenAt {
			return out[a].SeenAt > out[b].SeenAt
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

// setLocalSeenAt records this node's own local observation time for a video.
// Called from seedLiveFromLocal (startup) and PublishLive (live observation).
//
// Keyed by platform and id together, matching `entries` — the resurrection
// guard reads this map, and a collision between platforms would let one
// platform's sighting vouch for another's stream.
func (li *LiveIndex) setLocalSeenAt(platform, videoID string, seenAt int64) {
	if platform == "" {
		platform = "yt"
	}
	key := platform + ":" + videoID
	li.mu.Lock()
	defer li.mu.Unlock()
	if li.localSeenAt == nil {
		li.localSeenAt = map[string]int64{}
	}
	// Keep the most recent local observation.
	if seenAt > li.localSeenAt[key] {
		li.localSeenAt[key] = seenAt
	}
}

// Live returns the index, or nil when not subscribed.
func (n *Node) Live() *LiveIndex { return n.live }

// PublishLive announces a stream this node saw.
//
// Level 2+ (WO-089). This used to run at every level on the argument that an
// authorless report discloses nothing about who saw the stream. That argument
// does not survive a direct neighbour with connection topology and timing, and
// a sighting is derived from what this user was shown either way — so it is
// sharing, and sharing starts at Level 2.
//
// A Level-1 node has no index at all, so the nil check below is the whole
// stop. The gate check after it is for the downgrade window: the supervisor
// shuts the gate synchronously and tears the node down afterwards, and a
// sighting published in between is one the user has already declined to send.
func (n *Node) PublishLive(ctx context.Context, r LiveRecord) {
	if n.live == nil || !n.mayPublishLive() {
		return
	}
	// Track our local observation so merge can corroborate peer claims.
	n.live.setLocalSeenAt(r.Platform, r.VideoID, r.SeenAt)
	// Always refresh our own index from our own observation, so the panel's
	// "seen live" time reflects when we actually saw it — independent of whether
	// we gossip it. Gossip to peers stays gated on shouldPublish below.
	n.live.merge(r)
	if !n.live.shouldPublish(r.Platform, r.VideoID) {
		return
	}
	if err := n.live.Publish(ctx, r); err != nil {
		n.logf("live publish %s: %v", r.VideoID, err)
	}
}
