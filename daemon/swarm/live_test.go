// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/store"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
)

func newMsg(data []byte) *pubsub.Message {
	return &pubsub.Message{Message: &pb.Message{Data: data}}
}

// connect wires two isolated nodes together so gossipsub has a mesh.
func connect(t *testing.T, a, b *Node) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.host.Connect(ctx, b.AddrInfo()); err != nil {
		t.Fatal(err)
	}
}

// waitFor polls until cond holds, so tests do not depend on gossip timing.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func liveCfg(t *testing.T, serve bool) Config {
	c := isolated(serve, t)
	// Live tests that want a subscriber must force the capability on: isolated
	// clients are Level 1, and Level 1 has no Live object since WO-089.
	c.Policy.Live = true
	return c
}

// TestLiveRecordPropagates is the feature: one node sees a stream, every
// subscriber learns about it, and search runs locally.
func TestLiveRecordPropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pub, err := Start(ctx, newStore(t, "pub.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	sub, err := Start(ctx, newStore(t, "sub.sqlite"), liveCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	connect(t, sub, pub)

	// Gossipsub needs a moment to build a mesh for the topic.
	waitFor(t, "topic mesh", func() bool { return pub.Peers() > 0 && sub.Peers() > 0 })
	time.Sleep(1500 * time.Millisecond)

	pub.PublishLive(ctx, LiveRecord{
		VideoID: "dQw4w9WgXcQ", Title: "Breaking news livestream", ChannelID: "UCnewsroom0000000000000",
	})

	waitFor(t, "record to arrive", func() bool { return sub.Live().Size() > 0 })

	// The subscriber searches its own memory; no query left the machine.
	hits := sub.Live().Search("breaking", 10)
	if len(hits) != 1 {
		t.Fatalf("search returned %d hits, want 1", len(hits))
	}
	if hits[0].VideoID != "dQw4w9WgXcQ" {
		t.Errorf("got %q", hits[0].VideoID)
	}
	// A term that matches nothing must return nothing — the filter is real.
	if got := sub.Live().Search("cooking", 10); len(got) != 0 {
		t.Errorf("unrelated query returned %d hits", len(got))
	}
}

// TestLiveLongTailSurvives is the product requirement. A stream seen by one node
// must reach everyone, because most livestreams have exactly one observer.
func TestLiveLongTailSurvives(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	a, err := Start(ctx, newStore(t, "a.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Start(ctx, newStore(t, "b.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	watcher, err := Start(ctx, newStore(t, "w.sqlite"), liveCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	connect(t, watcher, a)
	connect(t, watcher, b)
	connect(t, a, b)
	waitFor(t, "mesh", func() bool { return watcher.Peers() >= 2 })
	time.Sleep(1500 * time.Millisecond)

	rec := LiveRecord{VideoID: "corrobovid1", Title: "Corroborated stream"}
	a.PublishLive(ctx, rec)
	b.PublishLive(ctx, rec)

	waitFor(t, "both reports", func() bool {
		return len(watcher.Live().Search("corroborated", 10)) == 1
	})

	// A stream only one node has seen must appear too. This is the whole point:
	// most livestreams have exactly one observer, and filtering them out would
	// leave the popular subset YouTube already shows.
	a.PublishLive(ctx, LiveRecord{VideoID: "lonelyvid01", Title: "Unconfirmed stream"})
	waitFor(t, "single-observer stream", func() bool {
		return len(watcher.Live().Search("unconfirmed", 10)) == 1
	})
}

// TestLevelOneHasNoLiveAtAll is WO-089's central Live assertion.
//
// The previous test here was TestLevelOneParticipatesFully, and it asserted the
// opposite: that a default-level node both received the feed and reported its
// own sightings. That was WO-078's decision, made on the argument that a live
// notice carries no application-level author and so discloses nothing about who
// saw the stream. WO-089 overturns it, and not because the argument was wrong
// in detail — a notice really does carry no author — but because "no author" is
// not an anonymity proof against a direct neighbour who can watch which
// connection a message first arrived on, and because a sighting is derived from
// what this user was shown either way.
//
// So the assertion is now total. Not "does not originate": no index, no
// subscription, no snapshot handler, nothing on the wire.
func TestLevelOneHasNoLiveAtAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	publisher, err := Start(ctx, newStore(t, "pub.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	lvl1, err := Start(ctx, newStore(t, "l1.sqlite"), isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer lvl1.Close()

	if lvl1.Live() != nil {
		t.Fatal("a Level 1 node built a live index; Live begins at Level 2 (WO-089)")
	}
	if lvl1.LiveStartedForTest() {
		t.Error("a Level 1 node started the live subsystem")
	}
	if lvl1.Policy().Live {
		t.Error("a Level 1 policy permits Live")
	}

	connect(t, lvl1, publisher)
	waitFor(t, "mesh", func() bool { return lvl1.Peers() > 0 })
	time.Sleep(1500 * time.Millisecond)

	// Publishing from Level 1 is a no-op rather than a panic: announceLive
	// calls this on every observed batch, so it has to be safe to call.
	lvl1.PublishLive(ctx, LiveRecord{VideoID: "shouldnotgo", Title: "Level one leak"})
	time.Sleep(1500 * time.Millisecond)
	if got := publisher.Live().Search("level one leak", 10); len(got) != 0 {
		t.Errorf("a Level 1 node's sighting reached the network: %v", got)
	}

	// The snapshot stream is not served either — it is registered inside
	// startLive, which never ran.
	if _, err := publisher.requestOn(
		ctx, lvl1.AddrInfo(), "", LiveSnapshotProtocol,
	); err == nil {
		t.Error("a Level 1 node answered a live-snapshot request")
	}
}

// TestLevelTwoRunsTheWholeLiveSystem is the other half: everything WO-089 moved
// must still work where it moved to, or the change is a removal rather than a
// relocation.
func TestLevelTwoRunsTheWholeLiveSystem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	publisher, err := Start(ctx, newStore(t, "pub2.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	lvl2, err := Start(ctx, newStore(t, "l2.sqlite"), isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer lvl2.Close()

	if lvl2.Live() == nil {
		t.Fatal("a Level 2 node has no live index")
	}
	connect(t, lvl2, publisher)
	waitFor(t, "mesh", func() bool { return lvl2.Peers() > 0 })
	time.Sleep(1500 * time.Millisecond)

	publisher.PublishLive(ctx, LiveRecord{VideoID: "dQw4w9WgXcQ", Title: "Open feed"})
	waitFor(t, "record at level 2", func() bool { return lvl2.Live().Size() > 0 })

	lvl2.PublishLive(ctx, LiveRecord{VideoID: "sharedsight", Title: "Level two sighting"})
	waitFor(t, "sighting reaches the network", func() bool {
		return len(publisher.Live().Search("level two sighting", 10)) > 0
	})
}

// TestDowngradeStopsLivePublishingImmediately is the gate half. A user who
// chooses Level 1 must stop publishing from that instant, not once the
// replacement node has finished coming up — teardown is not instant, and
// announceLive keeps arriving from the browser meanwhile.
func TestDowngradeStopsLivePublishingImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	watcher, err := Start(ctx, newStore(t, "watch.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	sharer, err := Start(ctx, newStore(t, "sharer.sqlite"), isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sharer.Close()

	connect(t, sharer, watcher)
	waitFor(t, "mesh", func() bool { return sharer.Peers() > 0 })
	time.Sleep(1500 * time.Millisecond)

	// Same node, same subscription, same connection — gated anyway.
	sharer.CloseOutbound()
	sharer.PublishLive(ctx, LiveRecord{VideoID: "aftergateshut", Title: "After the gate shut"})
	time.Sleep(1500 * time.Millisecond)
	if got := watcher.Live().Search("after the gate shut", 10); len(got) != 0 {
		t.Errorf("a gated node published a sighting: %v", got)
	}
	if _, err := watcher.requestOn(
		ctx, sharer.AddrInfo(), "", LiveSnapshotProtocol,
	); err == nil {
		t.Error("a gated node answered a live-snapshot request")
	}
}

// TestLiveRecordWireShapeCarriesNoFunnelState pins WO-078's outbound contract
// at the type that actually goes on the wire: Publish does json.Marshal(r)
// on a LiveRecord (live.go), so this is what every peer, including a passive
// observer, receives. Reports are authorless (TestLevelOneParticipatesFully)
// but "no author" is not by itself proof of nothing else disclosed — this
// asserts the payload also carries none of the four fields the decision
// singles out: the context video/query someone was on, their slot in a rail,
// or a stable per-install author. A future field added to LiveRecord for some
// unrelated reason must fail this test and force an explicit privacy-copy
// update rather than silently widening the disclosure.
func TestLiveRecordWireShapeCarriesNoFunnelState(t *testing.T) {
	raw, err := json.Marshal(LiveRecord{
		VideoID:   "dQw4w9WgXcQ",
		Title:     "Open feed",
		ChannelID: "UCxxxxxx",
		SeenAt:    1700000000000,
		Platform:  "yt",
		StartedAt: 1699999000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var onWire map[string]any
	if err := json.Unmarshal(raw, &onWire); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"context_video_id", "context_video", "context_query_hash", "query",
		"slot_index", "slot", "author", "sender", "node_id", "peer_id",
	} {
		if _, present := onWire[forbidden]; present {
			t.Errorf("live wire payload carries %q; WO-078 forbids context video, slot, query and stable author on the live payload", forbidden)
		}
	}
	// And pin the shape is exactly the disclosed fields, not merely "missing
	// the forbidden ones" — an allow-list is what stops the next field added
	// to LiveRecord from shipping without a matching privacy-copy update.
	allowed := map[string]bool{"v": true, "t": true, "c": true, "s": true, "p": true, "b": true}
	for k := range onWire {
		if !allowed[k] {
			t.Errorf("live wire payload carries undocumented field %q; update PRIVACY.md/ARCHITECTURE_CURRENT.md §3 alongside LiveRecord", k)
		}
	}
}

// TestLiveValidatorRejectsJunk — the validator runs before forwarding, so junk
// costs one hop instead of propagating.
func TestLiveValidatorRejectsJunk(t *testing.T) {
	cases := map[string][]byte{
		"not json":       []byte("{{{"),
		"short video id": []byte(`{"v":"abc","s":1}`),
		"oversized":      make([]byte, maxLiveRecordBytes+1),
		"future clock":   []byte(`{"v":"dQw4w9WgXcQ","s":99999999999999}`),
	}
	for name, data := range cases {
		if validateLiveMessage(context.Background(), "", newMsg(data)) {
			t.Errorf("%s was accepted", name)
		}
	}
	ok := []byte(`{"v":"dQw4w9WgXcQ","t":"Fine","s":1}`) // s: any positive time
	if !validateLiveMessage(context.Background(), "", newMsg(ok)) {
		t.Error("a valid record was rejected")
	}
}

// TestPublishSuppressionScales covers what actually bounds this feature.
//
// The index is small, but message volume grows with publishers × sightings, not
// with distinct streams: a thousand users seeing one popular stream would send a
// thousand messages carrying one fact. A node stops announcing once a stream is
// well corroborated and recently reported.
func TestPublishSuppressionScales(t *testing.T) {
	li := &LiveIndex{entries: map[string]*liveEntry{}, logf: func(string, ...any) {}}

	// Unknown stream: announce it.
	if !li.shouldPublish("yt", "dQw4w9WgXcQ") {
		t.Error("refused to announce a stream nobody has reported")
	}

	// Already known and fresh: stay quiet.
	li.merge(LiveRecord{VideoID: "dQw4w9WgXcQ", SeenAt: time.Now().UnixMilli()}, false)
	if li.shouldPublish("yt", "dQw4w9WgXcQ") {
		t.Error("re-announced a stream already in the index")
	}

	// Ageing: announce again, so suppression cannot let a live stream expire.
	li.mu.Lock()
	li.entries["yt:dQw4w9WgXcQ"].lastSeen = time.Now().Add(-liveRefreshAfter - time.Minute)
	li.mu.Unlock()
	if !li.shouldPublish("yt", "dQw4w9WgXcQ") {
		t.Error("suppression would let an ageing record expire out of the index")
	}
}

// TestLiveSnapshotBackfill is what makes the feed useful on a cold start.
//
// Gossip carries only what is published after a node subscribes, so a daemon
// that just started holds nothing — and publish suppression makes that worse by
// keeping redundant announcements off the wire. A joining node asks a peer for
// the whole index.
func TestLiveSnapshotBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	warm, err := Start(ctx, newStore(t, "warm.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()

	// Give the warm node an index without any gossip involved.
	now := time.Now().UnixMilli()
	for _, r := range []LiveRecord{
		{VideoID: "dQw4w9WgXcQ", Title: "Existing stream one", SeenAt: now},
		{VideoID: "oHg5SJYRHA0", Title: "Existing stream two", SeenAt: now},
	} {
		warm.Live().merge(r, false)
	}

	cold, err := Start(ctx, newStore(t, "cold.sqlite"), liveCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	if cold.Live().Size() != 0 {
		t.Fatal("a fresh node should start with an empty index")
	}

	connect(t, cold, warm)
	if !cold.fetchLiveSnapshot(ctx, warm.ID()) {
		t.Fatal("snapshot request failed")
	}
	if cold.Live().Size() != 2 {
		t.Fatalf("index holds %d records after backfill, want 2", cold.Live().Size())
	}
	hits := cold.Live().Search("existing", 10)
	if len(hits) != 2 {
		t.Errorf("backfilled records are not searchable: %d hits", len(hits))
	}
}

// TestBackfillLiveRunsForANodeWithOnlyItsOwnSightings is the regression this
// bug always needed: TestLiveSnapshotBackfill above proves fetchLiveSnapshot
// itself works, but calls it directly on a node it deliberately starts empty
// — it never exercises backfillLive's own decision to call it at all.
//
// startLive runs seedLiveFromLocal synchronously before backfillLive's
// goroutine even starts (live.go's startLive), so any node with its own
// recent live sightings — the ordinary case for anyone who watches live
// content — has a nonzero index from the moment backfillLive takes its
// first look. Gating backfill on Size()>0 meant such a node would never
// once ask a connected peer for its snapshot: two real nodes could report
// "1 peer connected" to each other forever and never converge on a shared
// live count, because neither node's own sightings ever satisfied the
// other's backfill guard, and neither ever legitimately received the
// other's data.
func TestBackfillLiveRunsForANodeWithOnlyItsOwnSightings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	warm, err := Start(ctx, newStore(t, "warm.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	warm.Live().merge(LiveRecord{VideoID: "dQw4w9WgXcQ", Title: "Warm's own stream", SeenAt: time.Now().UnixMilli()}, false)

	// mine is not empty — it has exactly one record of its own, the same
	// shape seedLiveFromLocal leaves behind at real startup. The bug's
	// guard (Size()>0) would treat this identically to a node gossip had
	// already filled, and backfillLive would return without ever trying.
	mine, err := Start(ctx, newStore(t, "mine.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer mine.Close()
	mine.Live().merge(LiveRecord{VideoID: "oHg5SJYRHA0", Title: "Mine's own stream", SeenAt: time.Now().UnixMilli()}, false)
	if mine.Live().Size() == 0 {
		t.Fatal("test setup: mine must start non-empty to exercise the bug")
	}
	if mine.Live().ReceivedFromPeer() {
		t.Fatal("test setup: mine must not already believe it heard from a peer")
	}

	connect(t, mine, warm)
	go mine.backfillLive(ctx)

	waitFor(t, "mine to backfill warm's stream despite already having its own", func() bool {
		return mine.Live().ReceivedFromPeer() && mine.Live().Size() == 2
	})
}

// TestStaleStreamIsNotPromoted is WO-054's regression.
//
// A record's lastSeen is refreshed on every gossip receive, and nodes
// re-announce records as they age so suppression cannot let a running stream
// expire. So a stream that finished six hours ago keeps a warm lastSeen for as
// long as anyone is still passing it around. Ranking or promoting on that put
// six-hour-old streams at the top of the panel while the stated rule was one
// hour.
//
// Observation time is the only field that means what the rule says.
func TestStaleStreamIsNotPromoted(t *testing.T) {
	li := &LiveIndex{entries: map[string]*liveEntry{}, logf: func(string, ...any) {}}
	now := time.Now()

	// Ended six hours ago, still being gossiped: lastSeen is now.
	li.merge(LiveRecord{
		VideoID: "staleaaaaaa", Title: "Finished six hours ago",
		SeenAt: now.Add(-6 * time.Hour).UnixMilli(),
	}, false)
	// Seen live ten minutes ago.
	li.merge(LiveRecord{
		VideoID: "freshbbbbbb", Title: "Actually running",
		SeenAt: now.Add(-10 * time.Minute).UnixMilli(),
	}, false)

	for _, id := range []string{"staleaaaaaa", "freshbbbbbb"} {
		if li.entries["yt:"+id].lastSeen.Before(now.Add(-time.Minute)) {
			t.Fatalf("%s: fixture does not reproduce a warm lastSeen", id)
		}
	}

	// The feed shows both — it is a record of what has been live recently — but
	// the recent one must rank first.
	hits := li.Search("", 10)
	if len(hits) != 2 {
		t.Fatalf("feed returned %d entries, want 2", len(hits))
	}
	if hits[0].VideoID != "freshbbbbbb" {
		t.Errorf("feed ranked %q first; a six-hour-old stream outranked a live one",
			hits[0].VideoID)
	}

	// Promotion applies the one-hour rule to observation time, which is what
	// handleSuggest does with these entries.
	cutoff := now.Add(-store.LiveRecency).UnixMilli()
	promoted := map[string]bool{}
	for _, e := range hits {
		if e.SeenAt >= cutoff {
			promoted[e.VideoID] = true
		}
	}
	if promoted["staleaaaaaa"] {
		t.Error("a stream last seen six hours ago was promoted under a one-hour rule")
	}
	if !promoted["freshbbbbbb"] {
		t.Error("a stream seen ten minutes ago was not promoted")
	}
}

// TestFirstInsertResurrectionGuard is WO-054 Part 4's regression test.
//
// After a daemon restart, the in-memory index is wiped. A peer's gossip can
// create an entry via the first-insert branch, which previously stored the
// peer's claimed SeenAt directly with no guard. This test verifies that if we
// have a local observation (from seeding impressions at startup) showing the
// stream ended > LiveRecency ago, a peer's recent SeenAt claim is rejected.
func TestFirstInsertResurrectionGuard(t *testing.T) {
	li := &LiveIndex{
		entries:     map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		logf:        func(string, ...any) {},
	}
	now := time.Now()

	// Simulate local observation from yesterday (seeded from impressions at startup).
	yesterday := now.Add(-24 * time.Hour).UnixMilli()
	li.setLocalSeenAt("yt", "deadstream1", yesterday)

	// Peer gossips a claim that the stream was live 1 minute ago.
	peerClaim := now.Add(-1 * time.Minute).UnixMilli()
	li.merge(LiveRecord{
		VideoID: "deadstream1",
		Title:   "Dead stream",
		SeenAt:  peerClaim,
	}, false)

	// The stored SeenAt should stay at yesterday (our local truth),
	// not the peer's incredible claim.
	e := li.entries["yt:deadstream1"]
	if e == nil {
		t.Fatal("entry not created")
	}
	if e.rec.SeenAt != yesterday {
		t.Errorf("stored SeenAt=%d (peer's claim), want %d (local truth)",
			e.rec.SeenAt, yesterday)
	}
}

// TestFirstInsertNoLocalObservationAcceptsPeer ensures the long-tail feed
// still works: if we have NO local observation, we accept a peer's recent
// SeenAt claim (the accepted tradeoff for unsigned gossip).
func TestFirstInsertNoLocalObservationAcceptsPeer(t *testing.T) {
	li := &LiveIndex{
		entries:     map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		logf:        func(string, ...any) {},
	}
	now := time.Now()

	// No local observation for this video.
	peerClaim := now.Add(-10 * time.Minute).UnixMilli()
	li.merge(LiveRecord{
		VideoID: "longtailstr",
		Title:   "Long tail stream",
		SeenAt:  peerClaim,
	}, false)

	// Should accept peer's claim since we have no local knowledge to contradict it.
	e := li.entries["yt:longtailstr"]
	if e == nil {
		t.Fatal("entry not created")
	}
	if e.rec.SeenAt != peerClaim {
		t.Errorf("stored SeenAt=%d, want peer's %d", e.rec.SeenAt, peerClaim)
	}
}

// TestFirstInsertLocalMoreRecentKeepsLocal ensures that if our local
// observation is more recent than the peer's claim, we keep our local time.
func TestFirstInsertLocalMoreRecentKeepsLocal(t *testing.T) {
	li := &LiveIndex{
		entries:     map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		logf:        func(string, ...any) {},
	}
	now := time.Now()

	// Local observation 5 minutes ago.
	localSeen := now.Add(-5 * time.Minute).UnixMilli()
	li.setLocalSeenAt("yt", "recentstrm1", localSeen)

	// Peer claims 10 minutes ago (older than our local).
	peerClaim := now.Add(-10 * time.Minute).UnixMilli()
	li.merge(LiveRecord{
		VideoID: "recentstrm1",
		Title:   "Recent stream",
		SeenAt:  peerClaim,
	}, false)

	// Should keep our more recent local observation.
	e := li.entries["yt:recentstrm1"]
	if e == nil {
		t.Fatal("entry not created")
	}
	if e.rec.SeenAt != localSeen {
		t.Errorf("stored SeenAt=%d, want local %d", e.rec.SeenAt, localSeen)
	}
}

// TestFirstInsertPeerWithinWindowAccepted ensures that if peer claims a
// SeenAt within LiveRecency of our local observation, we accept it (stream
// might still be running).
func TestFirstInsertPeerWithinWindowAccepted(t *testing.T) {
	li := &LiveIndex{
		entries:     map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		logf:        func(string, ...any) {},
	}
	now := time.Now()

	// Local observation 30 minutes ago (within LiveRecency = 1 hour).
	localSeen := now.Add(-30 * time.Minute).UnixMilli()
	li.setLocalSeenAt("yt", "runningstrm", localSeen)

	// Peer claims 10 minutes ago (more recent, but within LiveRecency of local).
	peerClaim := now.Add(-10 * time.Minute).UnixMilli()
	li.merge(LiveRecord{
		VideoID: "runningstrm",
		Title:   "Running stream",
		SeenAt:  peerClaim,
	}, false)

	// Should accept peer's more recent claim since stream might still be live.
	e := li.entries["yt:runningstrm"]
	if e == nil {
		t.Fatal("entry not created")
	}
	if e.rec.SeenAt != peerClaim {
		t.Errorf("stored SeenAt=%d, want peer's %d", e.rec.SeenAt, peerClaim)
	}
}

// TestPlatformAwareIDs — WO-057. The live index used to require exactly eleven
// characters, which is a YouTube id and nothing else. TikTok ids are numeric and
// longer, so every TikTok stream would have been discarded at the door.
func TestPlatformAwareIDs(t *testing.T) {
	cases := []struct {
		platform, id string
		ok           bool
		why          string
	}{
		{"yt", "dQw4w9WgXcQ", true, "a real YouTube id"},
		{"", "dQw4w9WgXcQ", true, "absent platform means YouTube, for older nodes"},
		{"yt", "7300000000000000000", false, "a TikTok id is not a YouTube id"},
		{"tt", "7300000000000000000", true, "a real TikTok id"},
		{"tt", "dQw4w9WgXcQ", false, "TikTok ids are numeric"},
		{"tt", "73000", false, "too short to be a TikTok id"},
		{"xx", "dQw4w9WgXcQ", false, "an unknown platform cannot be displayed, so is refused"},
	}
	for _, c := range cases {
		if got := validVideoID(c.platform, c.id); got != c.ok {
			t.Errorf("validVideoID(%q, %q) = %v, want %v — %s", c.platform, c.id, got, c.ok, c.why)
		}
	}
}

// TestPlatformsDoNotCollide — entries are keyed by platform and id together.
// Nothing guarantees two platforms never mint the same string, and one
// collision would merge unrelated streams into a single entry.
func TestPlatformsDoNotCollide(t *testing.T) {
	li := &LiveIndex{entries: map[string]*liveEntry{}, logf: func(string, ...any) {}}
	now := time.Now().UnixMilli()
	li.merge(LiveRecord{Platform: "yt", VideoID: "dQw4w9WgXcQ", Title: "A YouTube stream", SeenAt: now}, false)
	li.merge(LiveRecord{Platform: "tt", VideoID: "7300000000000000000", Title: "A TikTok stream", SeenAt: now}, false)

	if got := li.Size(); got != 2 {
		t.Fatalf("index holds %d entries, want 2", got)
	}
	hits := li.Search("stream", 10)
	seen := map[string]string{}
	for _, h := range hits {
		seen[h.Platform] = h.Title
	}
	if seen["yt"] != "A YouTube stream" || seen["tt"] != "A TikTok stream" {
		t.Errorf("platforms did not stay distinct: %+v", seen)
	}
}

// TestDeadStreamRetiredDespiteReannounce is the regression for the "17+ hours
// live, 5 min ago" stale-list bug.
//
// A stream this node never watched locally has no local observation, so the
// LiveRecency freeze (which keys off a local sighting) never engages. A peer
// re-announcing it bumps SeenAt to "now" on every gossip, so the 12h sweep
// never fires either. The stream's firstSeen stays anchored in the past, so the
// UI shows an impossible "17+ hours" duration. The fix: a hard maxLiveAge cap —
// an entry older than that is retired outright, regardless of re-announcements.
func TestDeadStreamRetiredDespiteReannounce(t *testing.T) {
	li := &LiveIndex{entries: map[string]*liveEntry{}, logf: func(string, ...any) {}}
	now := time.Now()
	key := "yt:deadstreamX"

	// Seed the entry as though it was first seen 17 hours ago.
	li.entries[key] = &liveEntry{
		rec:       LiveRecord{VideoID: "deadstreamX", Title: "Quick Friday Stream", SeenAt: now.Add(-17 * time.Hour).UnixMilli()},
		firstSeen: now.Add(-17 * time.Hour),
	}

	// Peers keep re-announcing it with a fresh SeenAt (the "5 min ago" the UI
	// showed). This used to keep the dead stream in the feed forever.
	for i := 0; i < 3; i++ {
		li.merge(LiveRecord{
			VideoID: "deadstreamX",
			Title:   "Quick Friday Stream",
			SeenAt:  now.Add(-5 * time.Minute).UnixMilli(),
		}, false)
	}

	// The stream must not appear in the feed — ever, even with re-announcements.
	hits := li.Search("", 100)
	for _, h := range hits {
		if h.VideoID == "deadstreamX" {
			t.Fatalf("dead stream (firstSeen 17h ago, peers re-announcing) still appears in Search results")
		}
	}
	// The entry stays in the map (frozen, so it cannot be resurrected) — that is
	// the mechanism preventing re-seed, not a leak.
	if _, ok := li.entries[key]; !ok {
		t.Errorf("freeze should keep the frozen entry in the map to block reseed")
	}

	// And a genuinely fresh stream must survive the same merge path.
	li.merge(LiveRecord{VideoID: "freshliveYY", Title: "Actually live", SeenAt: now.Add(-2 * time.Minute).UnixMilli()}, false)
	if _, ok := li.entries["yt:freshliveYY"]; !ok {
		t.Errorf("a fresh stream was wrongly retired")
	}
	found := false
	for _, h := range li.Search("", 100) {
		if h.VideoID == "freshliveYY" {
			found = true
		}
	}
	if !found {
		t.Errorf("a fresh stream was wrongly hidden from Search")
	}
}

// TestDeadStreamByStartedAt is the regression for the actually-reported bug:
// the UI shows "17+ hours" from StartedAt, but the node re-observes the stream
// every few minutes so firstSeen stays fresh. The earlier firstSeen-based fix
// missed it. The cap must key off StartedAt (the stream's own start time),
// which survives re-publishing.
func TestDeadStreamByStartedAt(t *testing.T) {
	li := &LiveIndex{entries: map[string]*liveEntry{}, logf: func(string, ...any) {}}
	now := time.Now()
	key := "yt:deadstreamX"

	// A stream that started 17h ago (StartedAt), but this node keeps
	// re-observing it so firstSeen keeps resetting to "now".
	li.entries[key] = &liveEntry{
		rec: LiveRecord{
			VideoID:   "deadstreamX",
			Title:     "Quick Friday Stream",
			SeenAt:    now.Add(-24 * time.Minute).UnixMilli(),
			StartedAt: now.Add(-17 * time.Hour).UnixMilli(),
		},
		firstSeen: now.Add(-24 * time.Minute),
	}

	// The node re-observes it repeatedly with a fresh SeenAt (firstSeen would
	// reset on re-insert, but here it already exists so firstSeen holds).
	for i := 0; i < 4; i++ {
		li.merge(LiveRecord{
			VideoID:   "deadstreamX",
			Title:     "Quick Friday Stream",
			SeenAt:    now.Add(-20 * time.Minute).UnixMilli(),
			StartedAt: now.Add(-17 * time.Hour).UnixMilli(),
		}, false)
	}

	if hasVideoID(li.Search("", 100), "deadstreamX") {
		t.Fatalf("17h-old stream (StartedAt) still appears despite repeated re-observation")
	}
}

// TestMergeMinAccumulatesStartedAt locks the rule that merge keeps the EARLIEST
// StartedAt seen across reports. Regression for the announceLive bug: that path
// used to send StartedAt=0, and an incoming 0 must not clobber a real start
// time a peer or earlier sighting already supplied — otherwise the maxLiveAge
// cap (which keys off StartedAt) silently degrades to firstSeen and stale
// streams linger (the "17+ hours / 5 min ago" class).
func TestMergeMinAccumulatesStartedAt(t *testing.T) {
	li := &LiveIndex{entries: map[string]*liveEntry{}, logf: func(string, ...any) {}}
	now := time.Now()
	// A peer already supplied a real start 3h ago (within maxLiveAge, so it is
	// not frozen by the age guard).
	t3 := now.Add(-3 * time.Hour).UnixMilli()

	li.merge(LiveRecord{VideoID: "accXaaaaaaa", Title: "Stream", SeenAt: now.Add(-2 * time.Minute).UnixMilli(), StartedAt: t3}, false)
	if got := li.entries["yt:accXaaaaaaa"].rec.StartedAt; got != t3 {
		t.Fatalf("first merge did not record StartedAt: want %d, got %d", t3, got)
	}

	// A later report OMITS StartedAt (the pre-fix announceLive behaviour).
	// It must NOT clobber the real 3h start with 0.
	li.merge(LiveRecord{VideoID: "accXaaaaaaa", Title: "Stream", SeenAt: now.Add(-2 * time.Minute).UnixMilli()}, false)
	if got := li.entries["yt:accXaaaaaaa"].rec.StartedAt; got != t3 {
		t.Fatalf("merge with StartedAt=0 clobbered real start: want %d, got %d", t3, got)
	}

	// A later report claims a LATER start (1h ago). Min must hold at 3h.
	li.merge(LiveRecord{VideoID: "accXaaaaaaa", Title: "Stream", SeenAt: now.Add(-1 * time.Minute).UnixMilli(), StartedAt: now.Add(-1 * time.Hour).UnixMilli()}, false)
	if got := li.entries["yt:accXaaaaaaa"].rec.StartedAt; got != t3 {
		t.Fatalf("merge raised StartedAt on a later report: want %d, got %d", t3, got)
	}

	// And a genuinely-earlier start (5h) from another reporter must lower it.
	li.merge(LiveRecord{VideoID: "accXaaaaaaa", Title: "Stream", SeenAt: now.Add(-1 * time.Minute).UnixMilli(), StartedAt: now.Add(-5 * time.Hour).UnixMilli()}, false)
	if got := li.entries["yt:accXaaaaaaa"].rec.StartedAt; got != now.Add(-5*time.Hour).UnixMilli() {
		t.Fatalf("merge did not adopt an earlier start: want %d, got %d", now.Add(-5*time.Hour).UnixMilli(), got)
	}
}

// TestDeadStreamTombstoneBlocksResurrection reproduces the failure of the first
// fix attempt: freezing set lastSeen = firstSeen so the sweep deleted the entry,
// but a subsequent peer re-announcement re-inserted it fresh (firstSeen = now),
// and it reappeared in the feed. The tombstone must refuse re-admission even
// after the entry has been swept away.
func TestDeadStreamTombstoneBlocksResurrection(t *testing.T) {
	li := &LiveIndex{entries: map[string]*liveEntry{}, logf: func(string, ...any) {}}
	now := time.Now()
	key := "yt:deadstreamX"

	li.entries[key] = &liveEntry{
		rec:       LiveRecord{VideoID: "deadstreamX", Title: "Quick Friday Stream", SeenAt: now.Add(-17 * time.Hour).UnixMilli()},
		firstSeen: now.Add(-17 * time.Hour),
	}

	// Freeze it (as a gossip re-announcement would).
	li.merge(LiveRecord{VideoID: "deadstreamX", SeenAt: now.Add(-5 * time.Minute).UnixMilli()}, false)
	if hasVideoID(li.Search("", 100), "deadstreamX") {
		t.Fatalf("dead stream visible immediately after freeze")
	}

	// Simulate the sweep deleting the frozen entry (lastSeen aged to firstSeen).
	delete(li.entries, key)

	// Peers re-announce it repeatedly — this used to resurrect it.
	for i := 0; i < 5; i++ {
		li.merge(LiveRecord{VideoID: "deadstreamX", SeenAt: now.Add(-5 * time.Minute).UnixMilli()}, false)
	}

	if _, ok := li.entries[key]; ok {
		t.Errorf("tombstoned stream was re-inserted after sweep")
	}
	if hasVideoID(li.Search("", 100), "deadstreamX") {
		t.Fatalf("tombstoned stream reappeared in the feed after re-announcements")
	}
}

// hasVideoID reports whether a Search result set contains the given video id.
func hasVideoID(hits []LiveEntry, id string) bool {
	for _, h := range hits {
		if h.VideoID == id {
			return true
		}
	}
	return false
}
