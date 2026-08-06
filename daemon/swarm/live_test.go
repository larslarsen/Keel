// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"

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
	hits := sub.Live().Search("breaking", 1, 10)
	if len(hits) != 1 {
		t.Fatalf("search returned %d hits, want 1", len(hits))
	}
	if hits[0].VideoID != "dQw4w9WgXcQ" {
		t.Errorf("got %q", hits[0].VideoID)
	}
	if hits[0].Publishers != 1 {
		t.Errorf("publishers = %d, want 1", hits[0].Publishers)
	}
	// A term that matches nothing must return nothing — the filter is real.
	if got := sub.Live().Search("cooking", 1, 10); len(got) != 0 {
		t.Errorf("unrelated query returned %d hits", len(got))
	}
}

// TestLiveCorroborationCounts checks the only defence against a fabricated
// record: distinct publishers reporting the same stream.
func TestLiveCorroborationCounts(t *testing.T) {
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
		hits := watcher.Live().Search("", 2, 10)
		return len(hits) == 1 && hits[0].Publishers >= 2
	})

	// A stream only one node claims must be filterable out.
	a.PublishLive(ctx, LiveRecord{VideoID: "lonelyvid01", Title: "Unconfirmed stream"})
	waitFor(t, "single report", func() bool { return len(watcher.Live().Search("unconfirmed", 1, 10)) == 1 })
	if got := watcher.Live().Search("unconfirmed", 2, 10); len(got) != 0 {
		t.Error("a stream with one publisher passed a two-publisher filter")
	}
}

// TestLevelOneHoldsNoLiveIndex pins the level boundary: Level 1 asks the network
// for nothing, and a topic subscription is a form of asking.
func TestLevelOneHoldsNoLiveIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := isolated(false, t) // Fetch false — Level 1
	n, err := Start(ctx, newStore(t, "l1.sqlite"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	if n.Live() != nil {
		t.Error("a Level 1 node subscribed to the live topic")
	}
	// Publishing must be a no-op rather than a panic.
	n.PublishLive(ctx, LiveRecord{VideoID: "dQw4w9WgXcQ"})
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
	ok := []byte(`{"v":"dQw4w9WgXcQ","t":"Fine","s":1}`)
	if !validateLiveMessage(context.Background(), "", newMsg(ok)) {
		t.Error("a valid record was rejected")
	}
}
