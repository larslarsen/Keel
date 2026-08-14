// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/swarm"
)

func TestAnnounceLiveCoalescesTikTokFYPAndWall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := swarm.Start(ctx, testStore(t, "live-coalesce.sqlite"), swarmConfigFor(2))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	restore := adoptNodeForTest(n)
	defer restore()

	channel := "@creator.name"
	now := time.Now().UnixMilli()
	announceLive([]bridge.Impression{
		{Platform: "tt", VideoID: "7300000000000000001", ChannelID: &channel, Title: "FYP sighting", Badges: []string{"LIVE"}, ObservedAt: now},
		{Platform: "tt", VideoID: "7300000000000000002", ChannelID: &channel, Title: "Live wall sighting", Badges: []string{"LIVE"}, ObservedAt: now + 1},
	})

	hits := n.Live().Search("", 10)
	if len(hits) != 1 {
		t.Fatalf("FYP and wall produced %d Live entries, want one", len(hits))
	}
	if hits[0].LiveLocator != "@creator.name/live" {
		t.Fatalf("coalesced locator = %q", hits[0].LiveLocator)
	}
}
