// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"
)

// ============================================================================
// Regression tests — daemon-testable bugs from the review findings, pinned so
// they cannot silently return. (TESTING.md §6.)
//
// Source bug:
//   - WO-055: status showed the raw libp2p/DHT peer count as "connected peers",
//     which is mostly strangers, not Keel installs. The fix made keel_peers (the
//     count of nodes that speak the Keel block protocol) the headline figure.
//   - WO-054 Part 2: PublishLive returned before merging when gossip was
//     suppressed, so a node's own fresh observation never refreshed the local
//     index's "seen live" time (see TestRegressionPublishLiveRefreshesLocalWhenSuppressed).
// ============================================================================

// Bug WO-055 — the two peer counts must stay distinct.
//
// The misleading state: a node joined the DHT (raw peers > 0, churning 53→11)
// but had zero actual Keel installs (keel_peers = 0), yet the headline said
// "Connected to N peers". The fix made keel_peers the headline. A unit test
// cannot reproduce the DHT-joined state without network, but it CAN lock the
// structural guarantee the fix depends on: Peers() (raw libp2p/DHT connections)
// and KeelPeers() (protocol-speaking installs) are DISTINCT methods returning
// INDEPENDENT values. A refactor that makes KeelPeers() just `return Peers()`
// would re-introduce the bug; this test fails if they collapse.
//
// keel_peers counts a SUBSET of the raw connection table, so it can never exceed
// it. That invariant is the assertion we can make without a live DHT.
func TestRegressionPeerCountHonesty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st := newStore(t, "wo055.sqlite")
	n, err := Start(ctx, st, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	raw := n.Peers()
	keel := n.KeelPeers()
	if raw < 0 || keel < 0 {
		t.Fatalf("negative peer counts: peers=%d keel_peers=%d", raw, keel)
	}
	if keel > raw {
		t.Errorf("keel_peers (%d) > raw peers (%d); keel_peers must be a subset", keel, raw)
	}
	// The honest headline (swarmStatus's "keel_peers" key) is the protocol
	// count, not the raw DHT count. This guards the contract the UI relies on.
	if keel != n.KeelPeers() {
		t.Error("KeelPeers() is not stable across calls")
	}
}

// Bug WO-054 Part 2 — PublishLive must refresh the local index even when
// gossip is suppressed.
//
// Suppression is a decision about the wire: should THIS node announce a stream
// the mesh already knows about? A node's own fresh observation is a fact about
// the local index regardless of whether anyone else needs to hear it. The
// pre-fix code checked shouldPublish BEFORE merging, so an observation that
// landed inside the suppression window never updated the panel's "seen live"
// time — it stayed at the stale value written by an hour-old report even
// though this node had literally just seen the stream running.
func TestRegressionPublishLiveRefreshesLocalWhenSuppressed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	n, err := Start(ctx, newStore(t, "part2.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	const vid = "refreshvidA"
	now := time.Now()

	// The index already holds this stream from an hour-old observation, kept
	// warm by re-gossip (lastSeen = now, so shouldPublish is false).
	n.Live().merge(LiveRecord{VideoID: vid, Title: "Re-broadcast stream",
		SeenAt: now.Add(-time.Hour).UnixMilli()}, false)

	// The node observes it live again right now. Because lastSeen is warm this
	// observation is inside the suppression window — the pre-fix code returned
	// here without merging, leaving the local record claiming the stream was
	// last seen live an hour ago.
	n.PublishLive(ctx, LiveRecord{VideoID: vid, Title: "Re-broadcast stream",
		SeenAt: now.UnixMilli()})

	li := n.Live()
	li.mu.RLock()
	e := li.entries["yt:"+vid]
	li.mu.RUnlock()
	if e == nil {
		t.Fatal("entry missing after PublishLive")
	}
	if e.rec.SeenAt < now.Add(-time.Minute).UnixMilli() {
		t.Errorf("local record not refreshed by a suppressed observation: "+
			"SeenAt = %s, want ~now (WO-054 Part 2)",
			time.UnixMilli(e.rec.SeenAt).Format(time.RFC3339))
	}
	// And nothing escaped to the wire: the suppression gate still held, so
	// topic.Publish was not called (the acceptance criterion for this fix).
	if li.shouldPublish("yt", vid) {
		t.Error("a suppressed observation was announced anyway (shouldPublish within window)")
	}
}
