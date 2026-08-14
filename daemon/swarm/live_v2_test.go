// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"
)

func TestLiveV2AcceptsCanonicalTikTokLocator(t *testing.T) {
	now := time.Now().UnixMilli()
	valid := LiveRecord{Platform: "tt", LiveLocator: "@creator.name/live", ChannelID: "@Creator.Name", Title: "A live stream", SeenAt: now}
	if !ValidLiveRecord(valid) {
		t.Fatal("canonical TikTok locator rejected")
	}
	if liveKey(valid) != "tt:@creator.name/live" {
		t.Fatalf("locator key = %q", liveKey(valid))
	}
	for _, bad := range []LiveRecord{
		{Platform: "tt", LiveLocator: "@creator/live/extra", Title: "x", SeenAt: now},
		{Platform: "tt", LiveLocator: "@creator/live", ChannelID: "@other", Title: "x", SeenAt: now},
		{Platform: "tt", LiveLocator: "@creator/live", VideoID: "not-a-video", Title: "x", SeenAt: now},
	} {
		if ValidLiveRecord(bad) {
			t.Fatalf("accepted malformed record %#v", bad)
		}
	}
}

func TestLiveV2PublishesLocatorBetweenNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	publisher, err := Start(ctx, newStore(t, "locator-pub.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	subscriber, err := Start(ctx, newStore(t, "locator-sub.sqlite"), liveCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()
	connect(t, publisher, subscriber)
	waitFor(t, "Live-v2 topic mesh", func() bool { return publisher.Peers() > 0 && subscriber.Peers() > 0 })
	time.Sleep(1500 * time.Millisecond)
	if !publisher.PublishLive(ctx, LiveRecord{
		Platform: "tt", LiveLocator: "@creator/live", ChannelID: "@creator", Title: "Locator publication", SeenAt: time.Now().UnixMilli(),
	}) {
		t.Fatal("locator publication refused")
	}
	waitFor(t, "locator at subscriber", func() bool { return len(subscriber.Live().Search("locator publication", 10)) == 1 })
	hit := subscriber.Live().Search("locator publication", 10)[0]
	if hit.LiveLocator != "@creator/live" {
		t.Fatalf("peer locator = %q", hit.LiveLocator)
	}
}

func TestLiveV2TombstoneAndMetadataAdmission(t *testing.T) {
	now := time.Now().UnixMilli()
	li := &LiveIndex{entries: map[string]*liveEntry{}, localSeenAt: map[string]int64{}}
	r := LiveRecord{Platform: "tt", LiveLocator: "@creator/live", ChannelID: "@creator", Title: "Fresh local title", SeenAt: now}
	if !li.merge(r, false) {
		t.Fatal("local locator was not admitted")
	}
	if !li.merge(LiveRecord{Platform: "tt", LiveLocator: "@creator/live", ChannelID: "@creator", Title: "Stale peer title", SeenAt: now - 60_000}, true) {
		t.Fatal("valid peer metadata was not merged")
	}
	if got := li.entries[liveKey(r)].rec.Title; got != "Fresh local title" {
		t.Fatalf("stale peer replaced fresher local title: %q", got)
	}

	li.mu.Lock()
	delete(li.entries, liveKey(r))
	li.retire(liveKey(r), time.Now())
	li.mu.Unlock()
	if li.merge(r, true) {
		t.Fatal("retired locator was resurrected by a peer")
	}
	if !li.merge(r, false) {
		t.Fatal("fresh local locator observation did not clear its tombstone")
	}
}

func TestLiveV2SnapshotDoesNotCountRetiredPeerRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	warm, err := Start(ctx, newStore(t, "snapshot-warm.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	cold, err := Start(ctx, newStore(t, "snapshot-cold.sqlite"), liveCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	r := LiveRecord{Platform: "tt", LiveLocator: "@creator/live", ChannelID: "@creator", Title: "Retired snapshot", SeenAt: time.Now().UnixMilli()}
	if !warm.Live().merge(r, false) {
		t.Fatal("warm record was not admitted")
	}
	cold.Live().mu.Lock()
	cold.Live().retire(liveKey(r), time.Now())
	cold.Live().mu.Unlock()
	connect(t, cold, warm)
	if cold.fetchLiveSnapshot(ctx, warm.ID()) {
		t.Fatal("a refused retired record counted as snapshot admission")
	}
	if cold.Live().Size() != 0 {
		t.Fatal("retired snapshot record was inserted")
	}
}
