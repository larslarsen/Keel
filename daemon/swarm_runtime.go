// SPDX-License-Identifier: Apache-2.0
// Running the swarm inside the daemon (WO-052).
//
// Three things happen here, and DESIGN_BOOTSTRAP §5d specifies all of them:
//
//  1. The node joins the network at startup — fetching for everyone, serving
//     only if the user has opted in.
//  2. It re-announces what it can serve, because DHT provider records expire.
//  3. It prewarms: the observer already tells the daemon which video is being
//     watched, so the seed's block is fetched when the watch page loads, before
//     the panel's SUGGEST arrives. "First request on a brand-new topic: seconds,
//     once."
//
// The asymmetry in (1) is deliberate and is the reason the privacy promise is
// not a toll booth: consumption is ungated, contribution is opt-in.
package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/bridge"

	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

// announceInterval re-publishes provider records. DHT records expire in about
// 24 hours; refreshing well inside that keeps a node findable without adding
// meaningful traffic.
const announceInterval = 6 * time.Hour

// prewarmTimeout bounds a background fetch. It runs ahead of the user, so it
// must never delay anything the user is waiting for.
const prewarmTimeout = 30 * time.Second

// A single owner process has exactly one node at a time, owned by the
// supervisor in contribution_runtime.go. Callers ask for it per operation and
// must not hold it across a contribution change — the node they have may be a
// stopped one moments later.
var (
	prewarmMu sync.Mutex
	prewarmed = map[string]time.Time{}
)

func currentSwarmNode() *swarm.Node { return supervisor.currentNode() }

// swarmConfigFor maps a contribution level onto what the node offers.
//
// The capability table lives in swarm.PolicyForLevel (see swarm/policy.go);
// what remains here is the transport-shaped part of the config. The mapping
// used to say Level 1 neither serves nor asks — "the only way nothing leaves
// is to never ask" — and WO-077 corrected it: withholding fetch also withheld
// peer search, pre-walk and the shared product from every non-contributor,
// which is a toll booth, not a privacy guarantee. Level 1 is a full consumer
// that serves nothing.
func swarmConfigFor(level int) swarm.Config {
	return swarm.Config{
		// Announced to peers so they can tell whether they are behind us or
		// incompatible with us (WO-061).
		AppVersion: version,
		Policy:     swarm.PolicyForLevel(level),
		// A stable network identity turns k-anonymous prefix requests back
		// into a trajectory, so every level that is not deliberately
		// attributable gets a fresh one each start. Level 4 is the exception
		// by definition: being identifiable is what that level is for.
		EphemeralIdentity: level < store.LevelTransparency,
		Log:               func(f string, a ...any) { log.Printf("swarm: "+f, a...) },
	}
}

// startSwarm brings the first node up, at the level a fresh process may
// construct. Failure is logged and swallowed: the local product works without
// the network.
func startSwarm(ctx context.Context, st *store.Store) {
	supervisor.start(ctx, st)
}

// announceLoop re-publishes provider records for as long as this node lives.
//
// Announce itself re-checks the outbound gate, so a downgrade stops the next
// tick from re-advertising a cache the user just withdrew even before this
// loop's context is cancelled.
func announceLoop(ctx context.Context, n *swarm.Node) {
	for {
		if err := n.Announce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("swarm: announce: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(announceInterval):
		}
	}
}

// announceLive publishes any livestream in a batch of observations.
//
// The extractor already tags live cards with a LIVE badge, so detection costs
// nothing. Level 2+ only since WO-089: a sighting is derived from what this
// user was shown, and n.Live() is nil at Level 1, so this is a no-op there
// before any record is built. The notice still carries no watched-video
// context, slot, query or stable author — that was never the question WO-089
// turned on.
func announceLive(imps []bridge.Impression) {
	n := currentSwarmNode()
	if n == nil || n.Live() == nil {
		return
	}
	for _, imp := range imps {
		live := false
		for _, b := range imp.Badges {
			if b == "LIVE" {
				live = true
				break
			}
		}
		if !live || imp.VideoID == "" {
			continue
		}
		rec := swarm.LiveRecord{
			VideoID:   imp.VideoID,
			Title:     imp.Title,
			SeenAt:    imp.ObservedAt,
			StartedAt: imp.ObservedAt, // node's earliest-known live sighting = best lower bound on start
		}
		if imp.ChannelID != nil {
			rec.ChannelID = *imp.ChannelID
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		n.PublishLive(ctx, rec)
		cancel()
	}
}

// prewarm fetches a video's neighbourhood in the background.
//
// Called when an observation arrives naming the video being watched. Deduped
// over a short window because a single watch page produces many observations
// as the user scrolls, and every one of them names the same seed.
//
// A neighbourhood already held — from the seed pack, from an earlier fetch, or
// from the user's own watching — produces no request at all. That is the point
// of the seed: the head of the watch distribution generates no queries, so
// there is nothing there for a peer to observe.
func prewarm(st *store.Store, videoID string) {
	n := currentSwarmNode()
	if n == nil || videoID == "" || videoID == store.HomeFrom {
		return
	}
	if have, err := st.HaveBlock(videoID); err == nil && have {
		return
	}
	prewarmMu.Lock()
	last, seen := prewarmed[videoID]
	if seen && time.Since(last) < time.Hour {
		prewarmMu.Unlock()
		return
	}
	prewarmed[videoID] = time.Now()
	// Bounded so a long session cannot grow this without limit.
	if len(prewarmed) > 512 {
		cutoff := time.Now().Add(-time.Hour)
		for k, v := range prewarmed {
			if v.Before(cutoff) {
				delete(prewarmed, k)
			}
		}
	}
	prewarmMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), prewarmTimeout)
		defer cancel()
		fetched, err := n.Fetch(ctx, videoID)
		if err != nil {
			return // a miss is the normal case for the long tail
		}
		if fetched > 0 {
			log.Printf("swarm: prewarmed %s with %d edges", videoID, fetched)
		}
	}()
}

// swarmStatus reports what the network layer is doing, for the interface.
//
// Reported rather than logged: a peer-to-peer feature that silently does
// nothing is indistinguishable from one that is working, and the first thing
// anyone will ask is whether they connected to anybody.
func swarmStatus() map[string]any {
	n := currentSwarmNode()
	if n == nil {
		return map[string]any{"up": false}
	}
	out := map[string]any{
		"up": true,
		// Everyone on the DHT, most of whom are not running Keel. Diagnostic
		// only — it is guaranteed non-zero once the node joins and it churns as
		// the DHT pads and prunes connections, so presenting it as a count of
		// people is actively misleading (WO-055).
		"peers": n.Peers(),
		// Peers that speak our protocol: actual other installs. This is the
		// number a person should be shown.
		"keel_peers": n.KeelPeers(),
		"id":         n.ID().String(),
		// What the versions around us look like (WO-061). Reported even when
		// everything agrees, because "no update needed" is itself the answer
		// to the question a person is asking when they look at this.
		"versions": n.Versions(version),
	}
	if li := n.Live(); li != nil {
		out["live_indexed"] = li.Size()
	}
	return out
}
