// SPDX-License-Identifier: Apache-2.0
// WO-077 over the wire: what a node at each level actually answers.
//
// These are the acceptance proofs that cannot be made by reading the policy
// struct. A Level-1 node holding a fully populated cache must refuse every
// block stream, the live snapshot and the word-telemetry pack built from that
// same cache, while still fetching all three from other people — and that pair
// of claims only means something if a real peer tries both directions.
package swarm

import (
	"context"
	"errors"
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

// TestLevelOneNeverAnswersWordTelemetry is the other half of the same
// boundary, and it reversed with WO-089.
//
// The old test asserted the opposite — that a Level-1 node answers this stream
// even though it serves no blocks — on the reasoning that a fixed-shape HLL/CMS
// aggregate discloses nothing per-item. What changed is not that reasoning but
// the question being asked: the pack is built from the titles this user was
// shown, so sending one is observation-derived sharing whatever its shape, and
// a guessed word can still be tested against a CMS.
//
// The accepted cost is that the global word statistic under-counts every
// non-sharing install. Downloading it is unaffected, which is the half the
// user actually sees.
func TestLevelOneNeverAnswersWordTelemetry(t *testing.T) {
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
	if _, err := aNode.fetchWordTelemetry(ctx, qNode.host.ID()); err == nil {
		t.Error("a Level 1 node answered the word-telemetry stream from its own corpus")
	}
	if qNode.Policy().ServeWordTelemetry {
		t.Error("a Level 1 policy permits serving word telemetry")
	}
	// Fetching is the half that stays: the corpus bars under the search box are
	// a consumer feature, and Level 1 keeps every consumer feature.
	if !qNode.Policy().FetchWordTelemetry {
		t.Error("a Level 1 node cannot download global word statistics")
	}

	// Level 2 answers, and its pack covers its own corpus — the reason for
	// including local titles at all is that a sharing node is not missing from
	// the statistic it contributes to.
	sharing := newStore(t, "sharing.sqlite")
	seed(t, sharing, "seedaaaaaaa", "cachedvid02", 0)
	sNode, err := Start(ctx, sharing, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()
	if err := aNode.host.Connect(ctx, sNode.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	pack, err := aNode.fetchWordTelemetry(ctx, sNode.host.ID())
	if err != nil {
		t.Fatalf("a Level 2 node refused the word telemetry stream: %v", err)
	}
	if pack == nil || pack.DistinctWords() == 0 {
		t.Error("a Level 2 node's word pack excluded its own corpus")
	}

	// And the gate takes it back immediately on a downgrade, before teardown.
	sNode.CloseOutbound()
	if _, err := aNode.fetchWordTelemetry(ctx, sNode.host.ID()); err == nil {
		t.Error("a gated node still answered the word-telemetry stream")
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
	// Live is off here too since WO-089, for a different reason than the
	// three-gram topics: those exist to advertise blocks this node serves,
	// while Live is off because a sighting is derived from what this user was
	// shown. Same answer, different question — asserted here so a future change
	// that re-enables one cannot quietly re-enable the other.
	if oneNode.LiveStartedForTest() {
		t.Error("a Level 1 node joined the live index; Live begins at Level 2 (WO-089)")
	}

	two := newStore(t, "two.sqlite")
	twoNode, err := Start(ctx, two, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer twoNode.Close()

	yieldJoined, sketchJoined = twoNode.JoinedSearchTopicsForTest()
	if !yieldJoined || !sketchJoined {
		t.Errorf("a Level 2 node did not join search topics (yield=%v sketch=%v)", yieldJoined, sketchJoined)
	}
	if !twoNode.LiveStartedForTest() {
		t.Error("a Level 2 node did not join the live index")
	}
}

// TestLevelOneStillFetchesAndPreWalks is the product-boundary half of the
// ticket: privacy is not a toll booth. A non-contributor gets the bounded
// consumer product — seed, fetch, pre-walk and the local walk those feed.
//
// It was TestLevelOneStillFetchesAndSearches, and asserted distributed peer
// search here too. WO-085 split that claim off rather than deleting it: the
// two halves are now separately falsifiable, because they are separately
// decided. Fetch stays on at Level 1 for exactly the reason it always did;
// distributed search does not, and
// TestLevelOneDistributedSearchIsRefusedBeforePeerContact is where that is
// proved. One test asserting both would go green if the boundary moved to the
// wrong one of them.
func TestLevelOneStillFetchesAndPreWalks(t *testing.T) {
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

// TestLevelOneDistributedSearchIsRefusedBeforePeerContact is WO-085's boundary
// over the wire.
//
// The refusal has to happen before contact, not as an empty result after it:
// the whole point is that a Level-1 node does not place search load on the
// serving population. So the serving peer here holds a match and is *already
// connected* — a search that reached it would succeed, which is what makes a
// zero-hit result meaningful rather than vacuous.
func TestLevelOneDistributedSearchIsRefusedBeforePeerContact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	serverStore := newStore(t, "reciprocity-server.sqlite")
	putTitle(t, serverStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	server, err := Start(ctx, serverStore, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "reciprocity-client.sqlite")
	client, err := Start(ctx, clientStore, levelCfg(t, store.LevelPersonal))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.host.Connect(ctx, server.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if client.Peers() == 0 {
		t.Fatal("client has no peers; the refusal below would prove nothing")
	}

	ids, progress, err := client.PeerSearch(ctx, "sourdough")
	if !errors.Is(err, ErrDistributedSearchNotPermitted) {
		t.Errorf("Level 1 PeerSearch error = %v, want ErrDistributedSearchNotPermitted", err)
	}
	if len(ids) != 0 || len(progress) != 0 {
		t.Errorf("a refused search still produced results: ids=%v progress=%v", ids, progress)
	}
	if client.MayDistributedSearch() {
		t.Error("a Level 1 node reports itself entitled to distributed search")
	}

	// The same client at Level 2 finds it, so the refusal above is the
	// entitlement and not a broken transport.
	reciprocal := newStore(t, "reciprocity-level2.sqlite")
	rNode, err := Start(ctx, reciprocal, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer rNode.Close()
	if err := rNode.host.Connect(ctx, server.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !rNode.MayDistributedSearch() {
		t.Fatal("a Level 2 node is not entitled to distributed search")
	}
	if _, _, err := rNode.PeerSearch(ctx, "sourdough"); err != nil {
		t.Errorf("Level 2 PeerSearch returned %v, want the transport to work", err)
	}
}

// TestShuttingTheGateStopsDistributedSearch is the downgrade half: choosing
// Level 1 must stop searches from that instant, not once the replacement node
// has come up. Same reasoning as the serving gate below, in the other
// direction — the supervisor shuts this gate synchronously and tears down
// afterwards, so anything that only checked cfg.Policy would keep searching
// throughout the teardown window.
func TestShuttingTheGateStopsDistributedSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st := newStore(t, "gate-search.sqlite")
	n, err := Start(ctx, st, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	if !n.MayDistributedSearch() {
		t.Fatal("a Level 2 node is not entitled to distributed search")
	}
	n.CloseOutbound()
	if n.MayDistributedSearch() {
		t.Error("a gated node still reports itself entitled to distributed search")
	}
	if _, _, err := n.PeerSearch(ctx, "sourdough"); !errors.Is(err, ErrDistributedSearchNotPermitted) {
		t.Errorf("PeerSearch after gating returned %v, want ErrDistributedSearchNotPermitted", err)
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

// TestLevelTwoAnnouncesEverythingItServes is WO-084 requirement 4, checked
// against the policy rather than against a hand-passed flag.
//
// Announcement and service are four separate namespaces — graph prefixes,
// catalogue prefixes, shards, and the gossiped yield vector — and each is
// computed on its own call. The failure this guards is one of them being wired
// to a different corpus than the rest, which produces provider records and
// yield bits describing material the corresponding stream refuses to return.
//
// The node here holds nothing but its own observations, which is also the first
// acceptance criterion: a Level-2 node with no imported data still advertises
// and still answers.
func TestLevelTwoAnnouncesEverythingItServes(t *testing.T) {
	st := newStore(t, "announce.sqlite")
	seed(t, st, "localseed01", "localvid001", 0)
	seed(t, st, "localseed01", "localvid002", 1)

	p := PolicyForLevel(store.LevelBroad)
	graph, catalogue := p.GraphSources(), p.CatalogueSources()

	prefixes, err := st.LocalPrefixes(store.DefaultPrefixBits, graph)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) == 0 {
		t.Fatal("a Level-2 node holding only its own observations announced no graph buckets")
	}
	for _, prefix := range prefixes {
		bucket, err := st.BlocksInPrefix(prefix, st.Cohort(), graph, 256)
		if err != nil {
			t.Fatalf("announced graph bucket %s cannot be served: %v", prefix, err)
		}
		if len(bucket.Blocks) == 0 {
			t.Errorf("announced graph bucket %s returns nothing", prefix)
		}
	}

	catPrefixes, err := st.LocalCataloguePrefixes(store.DefaultPrefixBits, catalogue)
	if err != nil {
		t.Fatal(err)
	}
	if len(catPrefixes) == 0 {
		t.Fatal("a Level-2 node announced no catalogue buckets, so its blocks arrive unlabelled")
	}
	for _, prefix := range catPrefixes {
		pack, err := st.BuildCataloguePack(prefix, catalogue, 4096)
		if err != nil {
			t.Fatalf("announced catalogue bucket %s cannot be served: %v", prefix, err)
		}
		if len(pack.Entries) == 0 {
			t.Errorf("announced catalogue bucket %s returns nothing", prefix)
		}
	}

	shards, err := st.LocalShards(catalogue)
	if err != nil {
		t.Fatal(err)
	}
	if len(shards) == 0 {
		t.Fatal("a Level-2 node announced no shards, so it is unsearchable")
	}
	for _, sh := range shards {
		entries, err := st.ShardSlice(sh, catalogue)
		if err != nil {
			t.Fatalf("announced shard %d cannot be served: %v", sh, err)
		}
		if len(entries) == 0 {
			t.Errorf("announced shard %d returns nothing", sh)
		}
	}

	// Every yield bit claims a shard fetch from this node is worth making, so
	// no bit may name a shard the node does not serve.
	vec, err := st.LocalYieldVector(catalogue)
	if err != nil {
		t.Fatal(err)
	}
	served := map[int]bool{}
	for _, sh := range shards {
		served[sh] = true
	}
	set := 0
	for idx := 0; idx < store.TokenDictSize; idx++ {
		if !store.YieldBitSet(vec, idx) {
			continue
		}
		set++
		tok, ok := store.TokenFromDictIndex(idx)
		if !ok {
			t.Fatalf("yield bit %d has no token", idx)
		}
		if !served[store.ShardOf(tok)] {
			t.Errorf("yield bit %d points at shard %d, which this node does not serve",
				idx, store.ShardOf(tok))
		}
	}
	if set == 0 {
		t.Error("a Level-2 node holding its own titles gossiped an empty yield vector")
	}
}
