// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// TestParseShard covers the untrusted-input parser handleShardRequest reads
// straight off the wire from a stranger — same reasoning as PrefixOf's own
// rejection tests: a peer can send anything on that line.
func TestParseShard(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  int
		valid bool
	}{
		{"0", 0, true},
		{"255", 255, true},
		{"", 0, false},
		{"-1", 0, false},
		{"1x", 0, false},
		{"x1", 0, false},
		{" 1", 0, false},
		{"99999999999999999999", 0, false}, // overflow-shaped, must not panic
	} {
		got, ok := parseShard(tc.in)
		if ok != tc.valid {
			t.Errorf("parseShard(%q) valid=%v, want %v", tc.in, ok, tc.valid)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parseShard(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// store.ShardM itself and anything above must be rejected — parseShard is
	// the one place that keeps an out-of-range shard from ever reaching
	// ShardSlice.
	if _, ok := parseShard(strconv.Itoa(store.ShardM)); ok {
		t.Errorf("parseShard(%d) accepted store.ShardM, which is out of range", store.ShardM)
	}
}

// TestShouldStopOnSaturation covers WO-067's stop-condition rule directly,
// since real DHT provider ordering can't be controlled from a test — a
// scenario needing "three empty peers before a fourth that has the answer"
// isn't constructible reliably against the real network stack.
func TestShouldStopOnSaturation(t *testing.T) {
	const streak = 3
	cases := []struct {
		name       string
		misses     int
		haveTarget bool
		found      int
		target     uint64
		want       bool
	}{
		{"no target, streak reached: stops (pre-WO-067 behavior)", 3, false, 0, 0, true},
		{"no target, streak not reached: keeps going", 2, false, 0, 0, false},
		{"target known, streak reached, target not met: keeps going", 3, true, 2, 10, false},
		{"target known, streak reached, target exactly met: stops", 3, true, 10, 10, true},
		{"target known, streak reached, target exceeded: stops", 3, true, 15, 10, true},
		{"target known, streak not reached: keeps going regardless", 1, true, 0, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldStopOnSaturation(tc.misses, streak, tc.haveTarget, tc.found, tc.target)
			if got != tc.want {
				t.Errorf("shouldStopOnSaturation(misses=%d, streak=%d, haveTarget=%v, found=%d, target=%d) = %v, want %v",
					tc.misses, streak, tc.haveTarget, tc.found, tc.target, got, tc.want)
			}
		})
	}
}

// TestFetchShardRecordsSearchForTargetFeedback is the end-to-end wiring
// check: a real fetch must feed its result back into RecordTokenSearch, so
// a later search for the same token has a target to consult. Ordering-
// sensitive stop-condition behavior itself is covered by
// TestShouldStopOnSaturation above; this only checks the feedback loop
// connects.
func TestFetchShardRecordsSearchForTargetFeedback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore := newStore(t, "shard-target-server.sqlite")
	putTitle(t, serverStore, "recvideo001", "Recommendation systems explained")
	server, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "shard-target-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !waitUntil(30*time.Second, func() bool {
		return len(server.host.Network().Peers()) > 0 && len(client.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}
	if !waitUntil(45*time.Second, func() bool {
		return server.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce its shards")
	}

	token := store.TokenizeQuery("recommendation")[0]
	if _, known := clientStore.TokenEstimate(token); known {
		t.Fatal("test assumption broken: client already had an estimate")
	}

	ok := waitUntil(45*time.Second, func() bool {
		got, err := client.FetchShard(ctx, token)
		return err == nil && len(got) > 0
	})
	if !ok {
		t.Fatal("FetchShard found nothing")
	}

	if _, known := clientStore.TokenEstimate(token); !known {
		t.Error("FetchShard did not record its result — a later search for this token still has no target")
	}
}

// TestFetchShardTagSelfFilter is WO-059's tag-self-filter requirement: a
// shard reply can legitimately hold a video that is in the shard only because
// some OTHER token of its title hashes there too. FetchShard must keep only
// entries actually tagged with the requested token, never everything the
// peer happened to send.
//
// Uses the same private-DHT discovery harness as TestPeerSearchViaDiscovery:
// FetchShard is DHT-discovery-based (like Fetch), so a direct host.Connect
// alone gives it no provider record to find.
func TestFetchShardTagSelfFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore := newStore(t, "shard-server.sqlite")
	// Two distinct titles. We don't control which shard each token lands in,
	// so the test doesn't assume they collide — it only asserts that
	// whichever shard is fetched, only genuinely-tagged videos come back.
	putTitle(t, serverStore, "recvideo001", "Recommendation systems explained")
	putTitle(t, serverStore, "pianovideo1", "Ambient piano for studying")
	server, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "shard-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !waitUntil(30*time.Second, func() bool {
		return len(server.host.Network().Peers()) > 0 && len(client.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}
	if !waitUntil(45*time.Second, func() bool {
		return server.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce its shards")
	}

	token := store.TokenizeQuery("recommendation")[0] // " rec", anchored
	var got map[string][]string
	ok := waitUntil(45*time.Second, func() bool {
		got, err = client.FetchShard(ctx, token)
		return err == nil && len(got) > 0
	})
	if !ok {
		t.Fatal("FetchShard found nothing for a token its only peer holds")
	}
	if _, ok := got["recvideo001"]; !ok {
		t.Errorf("FetchShard(%q) = %v, missing the video that actually carries it", token, got)
	}
	if _, ok := got["pianovideo1"]; ok {
		t.Errorf("FetchShard(%q) returned pianovideo1, which does not carry this token — "+
			"tag-self-filter did not run", token)
	}
	for _, tokens := range got {
		found := false
		for _, tk := range tokens {
			if tk == token {
				found = true
			}
		}
		if !found {
			t.Errorf("returned entry's own token list %v does not contain the fetched token %q", tokens, token)
		}
	}
}

// TestResolveShardEntriesDropsGenuineDisagreement is WO-067's poison rule:
// two peers of equal trust level who both claim to hold the same video, but
// disagree on whether it carries the token, must have that video dropped —
// and a third peer later agreeing with either side must not resurrect it.
func TestResolveShardEntriesDropsGenuineDisagreement(t *testing.T) {
	known := map[string]shardClaim{}
	poisoned := map[string]bool{}
	out := map[string][]string{}

	// Peer A (unsigned): video V carries the token.
	gained := resolveShardEntries(
		[]store.ShardEntry{{VideoID: "v1", Tokens: []string{" rec"}}},
		" rec", false, known, poisoned, out)
	if gained != 1 || out["v1"] == nil {
		t.Fatalf("after peer A: gained=%d out=%v, want v1 present", gained, out)
	}

	// Peer B (unsigned): same video, but its tag list does NOT include the
	// token — a direct contradiction, not mere absence (V is present in B's
	// reply, just tagged differently).
	gained = resolveShardEntries(
		[]store.ShardEntry{{VideoID: "v1", Tokens: []string{" the"}}},
		" rec", false, known, poisoned, out)
	if gained != 0 {
		t.Errorf("peer B's disagreement counted as a gain: %d", gained)
	}
	if _, ok := out["v1"]; ok {
		t.Error("v1 still in result after two equal-trust peers disagreed")
	}
	if !poisoned["v1"] {
		t.Error("v1 not marked poisoned after genuine disagreement")
	}

	// Peer C (unsigned) agrees with peer A's original claim — must not
	// resurrect v1. Once poisoned, stays poisoned.
	gained = resolveShardEntries(
		[]store.ShardEntry{{VideoID: "v1", Tokens: []string{" rec"}}},
		" rec", false, known, poisoned, out)
	if gained != 0 {
		t.Errorf("a corroborating peer after poisoning counted as a gain: %d", gained)
	}
	if _, ok := out["v1"]; ok {
		t.Error("v1 was resurrected by a peer agreeing with the original claim — poison must be sticky")
	}
}

// TestResolveShardEntriesSignedOverridesUnsigned is the other half of the
// trust rule: a signed claim beats an unsigned one outright — that is an
// override, not a poison signal, so the video survives with the signed
// claim's answer.
func TestResolveShardEntriesSignedOverridesUnsigned(t *testing.T) {
	known := map[string]shardClaim{}
	poisoned := map[string]bool{}
	out := map[string][]string{}

	// Unsigned peer claims v1 has the token.
	resolveShardEntries(
		[]store.ShardEntry{{VideoID: "v1", Tokens: []string{" rec"}}},
		" rec", false, known, poisoned, out)
	if _, ok := out["v1"]; !ok {
		t.Fatal("unsigned claim did not add v1")
	}

	// Signed peer disagrees: v1 does NOT have the token. Signed wins —
	// v1 must be removed from the result, and must NOT be poisoned (an
	// override is not a disagreement between equals).
	resolveShardEntries(
		[]store.ShardEntry{{VideoID: "v1", Tokens: []string{" the"}}},
		" rec", true, known, poisoned, out)
	if _, ok := out["v1"]; ok {
		t.Error("signed claim did not override the unsigned one")
	}
	if poisoned["v1"] {
		t.Error("a signed override was treated as poison — it should not be")
	}

	// A later unsigned peer disputing the now-established signed claim must
	// not change anything.
	gained := resolveShardEntries(
		[]store.ShardEntry{{VideoID: "v1", Tokens: []string{" rec"}}},
		" rec", false, known, poisoned, out)
	if gained != 0 || out["v1"] != nil {
		t.Errorf("an unsigned dispute overturned an established signed claim: gained=%d out=%v", gained, out)
	}
}

// TestPeerSearchViaDiscovery mirrors TestFetchViaDiscoveryNotManualDial: the
// client never learns the server's address directly, only through the DHT
// provider record the server's Announce publishes for its shards. This is
// the falsifiable form — a search that only worked because the test wired
// the nodes together would prove nothing about whether a stranger can be
// found.
func TestPeerSearchViaDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore := newStore(t, "psearch-server.sqlite")
	putTitle(t, serverStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	server, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "psearch-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !waitUntil(30*time.Second, func() bool {
		return len(server.host.Network().Peers()) > 0 && len(client.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}
	if !waitUntil(45*time.Second, func() bool {
		return server.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce its shards")
	}
	if client.host.Network().Connectedness(server.host.ID()).String() == "Connected" {
		t.Fatal("nodes were already connected; this test would prove nothing")
	}

	var ids []string
	ok := waitUntil(45*time.Second, func() bool {
		ids, _, err = client.PeerSearch(ctx, "sourdough")
		if err != nil {
			t.Logf("search attempt: %v", err)
			return false
		}
		return len(ids) > 0
	})
	if !ok {
		t.Fatalf("PeerSearch found nothing via discovery alone (got %v)", ids)
	}
	found := false
	for _, id := range ids {
		if id == "findmevideo1" {
			found = true
		}
	}
	if !found {
		t.Errorf("PeerSearch(\"sourdough\") = %v, missing findmevideo1", ids)
	}
}

// TestPeerSearchProgressReportsPerToken is Build 4's data contract: PeerSearch
// must return one TokenProgress per distinct query token, and the reported
// target must be the PRE-fetch estimate, not one already inflated by folding
// in what this exact search just found.
func TestPeerSearchProgressReportsPerToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	boot := privateDHT(t, ctx)
	bootInfo := peer.AddrInfo{ID: boot.ID(), Addrs: boot.Addrs()}

	serverStore := newStore(t, "progress-server.sqlite")
	putTitle(t, serverStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	server, err := Start(ctx, serverStore, bootstrappedTo(bootInfo, true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "progress-client.sqlite")
	client, err := Start(ctx, clientStore, bootstrappedTo(bootInfo, false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if !waitUntil(30*time.Second, func() bool {
		return len(server.host.Network().Peers()) > 0 && len(client.host.Network().Peers()) > 0
	}) {
		t.Fatal("nodes never connected to the private DHT")
	}
	if !waitUntil(45*time.Second, func() bool {
		return server.Announce(ctx) == nil
	}) {
		t.Fatal("serving node could not announce its shards")
	}

	wantTokens := len(store.TokenizeQuery("sourdough"))
	var progress []TokenProgress
	ok := waitUntil(45*time.Second, func() bool {
		_, progress, err = client.PeerSearch(ctx, "sourdough")
		return err == nil && len(progress) > 0
	})
	if !ok {
		t.Fatal("PeerSearch returned no progress entries")
	}
	if len(progress) != wantTokens {
		t.Errorf("progress has %d entries, want one per distinct token (%d)", len(progress), wantTokens)
	}
	// This client had never searched or heard anything about "sourdough"'s
	// tokens before this call, so every entry's pre-fetch target must read
	// as unknown — not inflated by this search's own results.
	for _, p := range progress {
		if p.Known {
			t.Errorf("token index %d reports Known=true on a client's first-ever search for it", p.TokenIndex)
		}
	}
}

// putTitle writes an impression whose title is exactly what's given, unlike
// seed() (block-transfer tests) which hardcodes "Title <id>". Shard tests care
// about the title text itself, not the graph edge.
func putTitle(t *testing.T, st *store.Store, videoID, title string) {
	t.Helper()
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: videoID + "-load-4444-8444-000000000000",
		ObservedAt: time.Now().UnixMilli(), Surface: "HOME",
		SlotIndex: 0, VideoID: videoID, Title: title,
	}}); err != nil {
		t.Fatal(err)
	}
}
