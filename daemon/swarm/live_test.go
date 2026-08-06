// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
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
	c.Fetch = true // subscribing is gated on Fetch; Level 1 holds no index
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

// TestLevelOneParticipatesFully pins what this feature gates: nothing.
//
// Reports carry no author, so publishing one discloses nothing about who saw the
// stream, and in a gossip mesh originating is indistinguishable from forwarding.
// Every node therefore both receives and reports at every level, which is also
// what fills the long tail the feed exists for.
func TestLevelOneParticipatesFully(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	publisher, err := Start(ctx, newStore(t, "pub.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	// Level 1: Fetch and Serve both off.
	lvl1, err := Start(ctx, newStore(t, "l1.sqlite"), isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer lvl1.Close()

	if lvl1.Live() == nil {
		t.Fatal("a Level 1 node has no live index; the feed should work at every level")
	}
	connect(t, lvl1, publisher)
	waitFor(t, "mesh", func() bool { return lvl1.Peers() > 0 })
	time.Sleep(1500 * time.Millisecond)

	publisher.PublishLive(ctx, LiveRecord{VideoID: "dQw4w9WgXcQ", Title: "Open feed"})
	waitFor(t, "record at level 1", func() bool { return lvl1.Live().Size() > 0 })

	// And it reports its own sightings, at the default setting.
	lvl1.PublishLive(ctx, LiveRecord{VideoID: "shouldnotgo", Title: "Level one leak"})
	time.Sleep(1500 * time.Millisecond)
	if got := publisher.Live().Search("level one leak", 10); len(got) == 0 {
		t.Error("a Level 1 node's report did not reach the network")
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
	if !li.shouldPublish("dQw4w9WgXcQ") {
		t.Error("refused to announce a stream nobody has reported")
	}

	// Already known and fresh: stay quiet.
	li.merge(LiveRecord{VideoID: "dQw4w9WgXcQ", SeenAt: time.Now().UnixMilli()})
	if li.shouldPublish("dQw4w9WgXcQ") {
		t.Error("re-announced a stream already in the index")
	}

	// Ageing: announce again, so suppression cannot let a live stream expire.
	li.mu.Lock()
	li.entries["dQw4w9WgXcQ"].lastSeen = time.Now().Add(-liveRefreshAfter - time.Minute)
	li.mu.Unlock()
	if !li.shouldPublish("dQw4w9WgXcQ") {
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
		warm.Live().merge(r)
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
	})
	// Seen live ten minutes ago.
	li.merge(LiveRecord{
		VideoID: "freshbbbbbb", Title: "Actually running",
		SeenAt: now.Add(-10 * time.Minute).UnixMilli(),
	})

	for _, id := range []string{"staleaaaaaa", "freshbbbbbb"} {
		if li.entries[id].lastSeen.Before(now.Add(-time.Minute)) {
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
