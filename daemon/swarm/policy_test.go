// SPDX-License-Identifier: Apache-2.0
// WO-077 over the wire: what a node at each level actually answers.
//
// These are the acceptance proofs that cannot be made by reading the policy
// struct. A Level-1 node holding a fully populated cache must still refuse
// every block stream, while answering the word-telemetry stream from the same
// cache — that pair only means something if a real peer tries both.
package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/store"
	"github.com/libp2p/go-libp2p/core/peer"
)

// levelCfg is an isolated node at one contribution level, with the real
// capability mapping rather than hand-set booleans.
func levelCfg(t *testing.T, level int) Config {
	return Config{
		Policy:      PolicyForLevel(level),
		Bootstrap:   []peer.AddrInfo{},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Log:         func(f string, a ...any) { t.Logf(f, a...) },
	}
}

// TestLevelOneServesNoBlocksEvenWithAFullCache is the ticket's central
// acceptance criterion.
//
// The cache is deliberately populated first: "serves nothing" must hold
// because of policy, not because there happened to be nothing to serve. A node
// that had fetched and cached rows and then dropped to Level 1 is exactly the
// case a downgrade produces.
func TestLevelOneServesNoBlocksEvenWithAFullCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	quiet := newStore(t, "quiet.sqlite")
	seed(t, quiet, "seedaaaaaaa", "cachedvid01", 0)
	seed(t, quiet, "seedaaaaaaa", "cachedvid02", 1)
	qNode, err := Start(ctx, quiet, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer qNode.Close()

	asker := newStore(t, "asker.sqlite")
	aNode, err := Start(ctx, asker, levelCfg(t, store.LevelCohort))
	if err != nil {
		t.Fatal(err)
	}
	defer aNode.Close()

	if _, err := aNode.FetchFrom(ctx, qNode.AddrInfo(), "seedaaaaaaa"); err == nil {
		t.Error("a Level 1 node answered a graph block request from its cache")
	}
	// The catalogue and shard streams are separate protocols and separate
	// handler registrations; each needs its own proof.
	if err := aNode.host.Connect(ctx, qNode.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := aNode.requestOn(ctx, qNode.AddrInfo(), "0", CatalogueProtocol); err == nil {
		t.Error("a Level 1 node answered a catalogue request from its cache")
	}
	if _, err := aNode.requestOn(ctx, qNode.AddrInfo(), "0", ShardProtocol); err == nil {
		t.Error("a Level 1 node answered a shard request from its cache")
	}
	if qNode.Serving() {
		t.Error("a Level 1 node reports itself as serving")
	}
	// And it must not advertise a cache it will not serve.
	if err := qNode.Announce(ctx); err != nil {
		t.Errorf("Announce on a Level 1 node returned %v, want a silent no-op", err)
	}
	if qNode.MayAnnounceForTest() {
		t.Error("a Level 1 node would publish provider records")
	}
}

// TestLevelOneStillAnswersWordTelemetry is the other half of the same
// boundary, and the one most easily got wrong: the word pack rides its own
// capability, so a node that serves no blocks still answers it.
func TestLevelOneStillAnswersWordTelemetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	quiet := newStore(t, "quiet.sqlite")
	seed(t, quiet, "seedaaaaaaa", "cachedvid01", 0)
	qNode, err := Start(ctx, quiet, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer qNode.Close()

	asker := newStore(t, "asker.sqlite")
	aNode, err := Start(ctx, asker, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer aNode.Close()

	if err := aNode.host.Connect(ctx, qNode.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	pack, err := aNode.fetchWordTelemetry(ctx, qNode.host.ID())
	if err != nil {
		t.Fatalf("a Level 1 node refused the word telemetry stream: %v", err)
	}
	if pack == nil {
		t.Fatal("word telemetry pack was nil")
	}
	// The pack must cover the serving node's own corpus — the point of
	// including local titles is that the global statistic is not systematically
	// missing every node that reads it.
	if pack.DistinctWords() == 0 {
		t.Error("a Level 1 node's word pack excluded its own corpus")
	}
}

// TestLevelOneJoinsNoSearchTopics proves the subscription boundary, not just
// the publication one: the three-gram topics exist to locate blocks a node
// serves, so a node serving none must not be on them at all.
func TestLevelOneJoinsNoSearchTopics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	one := newStore(t, "one.sqlite")
	oneNode, err := Start(ctx, one, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer oneNode.Close()

	yieldJoined, sketchJoined := oneNode.JoinedSearchTopicsForTest()
	if yieldJoined || sketchJoined {
		t.Errorf("a Level 1 node joined search topics (yield=%v sketch=%v)", yieldJoined, sketchJoined)
	}
	if oneNode.MayGossipSearchTelemetryForTest() {
		t.Error("a Level 1 node would originate three-gram telemetry")
	}
	// Live is a different category and must remain on: the shared index is
	// whole-feed gossip at every level.
	if !oneNode.LiveStartedForTest() {
		t.Error("a Level 1 node did not join the live index")
	}

	two := newStore(t, "two.sqlite")
	twoNode, err := Start(ctx, two, levelCfg(t, store.LevelMirror))
	if err != nil {
		t.Fatal(err)
	}
	defer twoNode.Close()

	yieldJoined, sketchJoined = twoNode.JoinedSearchTopicsForTest()
	if !yieldJoined || !sketchJoined {
		t.Errorf("a Level 2 node did not join search topics (yield=%v sketch=%v)", yieldJoined, sketchJoined)
	}
}

// TestLevelOneStillFetchesAndSearches is the product-boundary half of the
// ticket: privacy is not a toll booth. A non-contributor gets the full
// consumer product, including peer search against a Level-2 mirror.
func TestLevelOneStillFetchesAndSearches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mirror := newStore(t, "mirror.sqlite")
	seed(t, mirror, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, mirror, "seedaaaaaaa", "targetaaaa2", 1)
	mNode, err := Start(ctx, mirror, levelCfg(t, store.LevelCohort))
	if err != nil {
		t.Fatal(err)
	}
	defer mNode.Close()

	// The consumer contributes nothing at all.
	consumer := newStore(t, "consumer.sqlite")
	cNode, err := Start(ctx, consumer, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	edges, err := cNode.FetchFrom(ctx, mNode.AddrInfo(), "seedaaaaaaa")
	if err != nil {
		t.Fatalf("a Level 1 node could not fetch: %v", err)
	}
	if edges != 2 {
		t.Fatalf("Level 1 fetch gained %d edges, want 2", edges)
	}
	// And the fetched neighbourhood powers its local walk, which is the
	// product the old Fetch=false mapping withheld from non-contributors.
	sug, err := consumer.Suggest("seedaaaaaaa", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sug.Suggestions) == 0 {
		t.Error("a Level 1 node fetched edges but its walk returns nothing")
	}
}

// TestShuttingTheGateStopsServiceOnALiveNode is the downgrade mechanism in
// isolation: no teardown, no replacement, just the gate.
//
// This is what makes a downgrade immediate. Teardown of a libp2p host is not
// instant and requests keep arriving while it winds down, so if the gate did
// not hold on its own there would be a window in which the user has been told
// Level 1 is in force while blocks are still going out.
func TestShuttingTheGateStopsServiceOnALiveNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "server.sqlite")
	seed(t, server, "seedaaaaaaa", "targetaaaa1", 0)
	sNode, err := Start(ctx, server, levelCfg(t, store.LevelCohort))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	// Serving before.
	if edges, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), "seedaaaaaaa"); err != nil || edges == 0 {
		t.Fatalf("baseline fetch failed: %d edges, %v", edges, err)
	}

	sNode.CloseOutbound()

	if sNode.Serving() {
		t.Error("node still reports itself serving after the gate was shut")
	}
	if sNode.MayAnnounceForTest() {
		t.Error("node would still announce provider records after the gate was shut")
	}
	// Same host, same handlers registered, same connection — refused anyway.
	client2 := newStore(t, "client2.sqlite")
	c2Node, err := Start(ctx, client2, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer c2Node.Close()
	if edges, err := c2Node.FetchFrom(ctx, sNode.AddrInfo(), "seedaaaaaaa"); err == nil && edges > 0 {
		t.Errorf("a gated node served %d edges", edges)
	}
	if err := sNode.Announce(ctx); err != nil {
		t.Errorf("Announce after gating returned %v, want a silent no-op", err)
	}
}
