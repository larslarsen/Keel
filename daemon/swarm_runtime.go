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

// swarmNode is set once before the message loop starts and read by handlers.
// A single daemon process has exactly one node, so a package-level value is
// honest about the shape of the thing rather than threading it through every
// signature.
var (
	swarmNode *swarm.Node
	prewarmMu sync.Mutex
	prewarmed = map[string]time.Time{}
)

// swarmConfigFor maps the stored contribution level onto what the node offers.
//
// Level 1 neither serves nor asks. That is a stronger promise than "we do not
// upload anything": a block request tells the peer answering it which video was
// asked about, so the only way nothing leaves is to never ask. Level 1 runs on
// the seed pack plus its own recording, and neither involves a query.
//
// Level 2 opts into the query-based system. It mirrors — re-serving blocks other
// people published, using the disk space the user allots — and fetches on demand
// for anything the seed does not cover. The seed is what keeps that exposure to
// the long tail rather than to everything watched.
//
// Level 3 and above additionally serve the user's own edges, which is the step
// that publishes a funnel and is why it is a separate, explicit choice.
func swarmConfigFor(level int) swarm.Config {
	return swarm.Config{
		Serve:                level >= store.LevelCatalogue,
		Fetch:                level >= store.LevelCatalogue,
		ServeOwnObservations: level >= store.LevelCohort,
		// A stable network identity turns k-anonymous prefix requests back
		// into a trajectory, so every level that is not deliberately
		// attributable gets a fresh one each start. Level 4 is the exception
		// by definition: being identifiable is what that level is for.
		EphemeralIdentity: level < store.LevelTransparency,
		Log:               func(f string, a ...any) { log.Printf("swarm: "+f, a...) },
	}
}

// startSwarm brings the node up. A failure is logged and swallowed: the local
// product works without the network, and refusing to start the daemon because
// a router is unhelpful would be a poor trade.
func startSwarm(ctx context.Context, st *store.Store) {
	cfg := swarmConfigFor(st.ContributionLevel())
	n, err := swarm.Start(ctx, st, cfg)
	if err != nil {
		log.Printf("swarm unavailable, continuing locally: %v", err)
		return
	}
	swarmNode = n

	if cfg.Serve {
		go func() {
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
		}()
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
	if swarmNode == nil || videoID == "" || videoID == store.HomeFrom {
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
		n, err := swarmNode.Fetch(ctx, videoID)
		if err != nil {
			return // a miss is the normal case for the long tail
		}
		if n > 0 {
			log.Printf("swarm: prewarmed %s with %d edges", videoID, n)
		}
	}()
}
