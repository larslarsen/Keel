// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TestProductionLiveStartedAtPropagates covers the real bridge shape. The
// bridge supplies equal exact SeenAt and StartedAt values; wire coarsening must
// keep their ordering valid while retaining the exact local SeenAt. StartedAt
// is a shared earliest lower bound, so a relayed coarse report may lower it.
func TestProductionLiveStartedAtPropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pub, err := Start(ctx, newStore(t, "production-live-pub.sqlite"), liveCfg(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	sub, err := Start(ctx, newStore(t, "production-live-sub.sqlite"), liveCfg(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	connect(t, sub, pub)
	waitFor(t, "Live topic mesh", func() bool { return pub.Peers() > 0 && sub.Peers() > 0 })
	time.Sleep(1500 * time.Millisecond)

	// Keep this a gossip test. A snapshot arriving after publication must not
	// be able to hide a rejected gossipsub message.
	pub.host.RemoveStreamHandler(LiveSnapshotProtocol)
	observed := time.Now().Truncate(time.Millisecond)
	if !pub.PublishLive(ctx, LiveRecord{
		VideoID: "prodLive001", Title: "Production timestamp stream",
		SeenAt: observed.UnixMilli(), StartedAt: observed.UnixMilli(),
	}) {
		t.Fatal("production-shaped Live record was refused")
	}
	waitFor(t, "production-shaped record to cross gossip", func() bool {
		return len(sub.Live().Search("production timestamp", 10)) == 1
	})

	local := pub.Live().Search("production timestamp", 10)
	if len(local) != 1 || local[0].SeenAt != observed.UnixMilli() {
		t.Fatalf("exact local observation time was not preserved: %#v", local)
	}
	remote := sub.Live().Search("production timestamp", 10)[0]
	if remote.StartedAt > remote.SeenAt {
		t.Fatalf("wire timestamps are invalid: started %d > seen %d", remote.StartedAt, remote.SeenAt)
	}
}

// TestLiveReconciliationTracksConnections proves the scheduler without wall
// clock sleeps: a late peer is fetched, a reconciled connection is not fetched
// again, disconnect forgets it, and a failed fetch observes retry backoff.
func TestLiveReconciliationTracksConnections(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	id := peer.ID("late-live-peer")
	info := peer.AddrInfo{ID: id}
	state := make(map[peer.ID]liveBackfillPeerState)
	var calls atomic.Int32
	fetchOK := func(context.Context, peer.ID) bool {
		calls.Add(1)
		return true
	}

	reconcileLivePeers(ctx, nil, state, now, fetchOK)
	reconcileLivePeers(ctx, []peer.AddrInfo{info}, state, now, fetchOK)
	reconcileLivePeers(ctx, []peer.AddrInfo{info}, state, now.Add(time.Minute), fetchOK)
	if got := calls.Load(); got != 1 {
		t.Fatalf("one connected peer was fetched %d times, want 1", got)
	}
	reconcileLivePeers(ctx, nil, state, now.Add(2*time.Minute), fetchOK)
	reconcileLivePeers(ctx, []peer.AddrInfo{info}, state, now.Add(3*time.Minute), fetchOK)
	if got := calls.Load(); got != 2 {
		t.Fatalf("reconnected peer was fetched %d total times, want 2", got)
	}

	failed := make(map[peer.ID]liveBackfillPeerState)
	var failures atomic.Int32
	fetchFail := func(context.Context, peer.ID) bool {
		failures.Add(1)
		return false
	}
	reconcileLivePeers(ctx, []peer.AddrInfo{info}, failed, now, fetchFail)
	reconcileLivePeers(ctx, []peer.AddrInfo{info}, failed, now.Add(time.Second), fetchFail)
	if got := failures.Load(); got != 1 {
		t.Fatalf("failed peer retried inside its backoff: %d calls", got)
	}
	reconcileLivePeers(ctx, []peer.AddrInfo{info}, failed, now.Add(liveBackfillInterval), fetchFail)
	if got := failures.Load(); got != 2 {
		t.Fatalf("failed peer did not retry after backoff: %d calls", got)
	}
}
