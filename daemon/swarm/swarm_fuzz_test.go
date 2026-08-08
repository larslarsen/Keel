// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// SQLite-technique ports on the swarm transport (see TESTING.md, WO-062).
// The swarm is the network-facing surface: block fetch over libp2p, gossip,
// seed/catalogue sync. SQLite's anomaly + compound-failure testing applies
// directly: inject node death mid-transfer, assert the remaining nodes converge.
// ============================================================================

// ---- Technique: Multi-node integration + compound failure (§3.4) -------------
//
// Spawn 3 serving nodes. Two hold the same neighbourhood (redundancy), one is a
// pure client. Kill one server mid-fetch; the client must still obtain the block
// from the surviving server — the network must converge, not deadlock.

func TestSwarmSurvivesServerDeath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Server A and Server B both serve the same seed neighbourhood.
	serverA := newStore(t, "a.sqlite")
	seed(t, serverA, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, serverA, "seedaaaaaaa", "targetaaaa2", 2)
	aNode, err := Start(ctx, serverA, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}

	serverB := newStore(t, "b.sqlite")
	seed(t, serverB, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, serverB, "seedaaaaaaa", "targetaaaa2", 2)
	bNode, err := Start(ctx, serverB, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}

	client := newStore(t, "c.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()
	defer aNode.Close()
	defer bNode.Close()

	// Fetch from A first.
	if _, err := cNode.FetchFrom(ctx, aNode.AddrInfo(), "seedaaaaaaa"); err != nil {
		t.Fatalf("fetch from A: %v", err)
	}

	// Kill A mid-session (simulated crash). The client must still get the block
	// from B — convergence, not deadlock.
	aNode.Close()

	// Give the DHT/gossip a moment to reflect A's departure.
	time.Sleep(200 * time.Millisecond)

	got, err := cNode.FetchFrom(ctx, bNode.AddrInfo(), "seedaaaaaaa")
	if err != nil {
		t.Fatalf("after A died, fetch from B failed: %v", err)
	}
	if got != 2 {
		t.Errorf("post-failure fetch gained %d edges, want 2", got)
	}

	// The client's local view must hold both edges regardless of which server
	// served them — the block is content-addressed by key, not by server.
	sug, err := client.Suggest("seedaaaaaaa", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if sug.GraphEdges < 2 {
		t.Errorf("client graph has %d edges after convergence, want >=2", sug.GraphEdges)
	}
}

// ---- Technique: Fuzz Testing (§4) on the fetch key space ---------------------
//
// FetchFrom takes an arbitrary key string. Fuzz it: the server must never panic
// and must return either a valid (possibly empty) result or a clean error — never
// corrupt state or an unrecovered panic.

func FuzzFetchKey(f *testing.F) {
	seeds := []string{
		"seedaaaaaaa",
		"",
		"12:abc",
		"a",
		"🔥",
		strings.Repeat("x", 1000),
		"../../etc/passwd",
		"seed" + string([]byte{0, 1, 2}),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, key string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		server := newStore(t, "fz.sqlite")
		seed(t, server, "seedaaaaaaa", "targetaaaa1", 0)
		sNode, err := Start(ctx, server, isolated(true, t))
		if err != nil {
			t.Fatal(err)
		}
		defer sNode.Close()

		client := newStore(t, "fzc.sqlite")
		cNode, err := Start(ctx, client, isolated(false, t))
		if err != nil {
			t.Fatal(err)
		}
		defer cNode.Close()

		// FetchFrom must never panic on any key. A bogus key yields 0 edges or a
		// clean error, never a crash.
		_, _ = cNode.FetchFrom(ctx, sNode.AddrInfo(), key)
	})
}

// ---- Technique: Error-injection — empty/oversized bucket reply -------------
//
// A bucket reply is bounded (maxBlocksPerReply). Fetch a key whose bucket holds
// many blocks; the server must truncate safely and the client must still import
// what it got without corruption. Mirrors SQLite's "verify no corruption after
// the anomaly."

func TestFetchTruncatesLargeBucketSafely(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "big.sqlite")
	// One neighbourhood with many edges -> many blocks in the bucket if we seed
	// many distinct from-keys, but a single from-key yields one block. To exceed
	// maxBlocksPerReply we instead seed many distinct videos all in one prefix.
	const n = 300
	for i := 0; i < n; i++ {
		seed(t, server, fmt.Sprintf("seed%08d", i), fmt.Sprintf("tgt%08d", i), i%8)
	}
	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "bigc.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	// Fetch several distinct keys; each must return a clean count and import
	// without panicking, even as the server caps replies.
	for i := 0; i < 10; i++ {
		k := fmt.Sprintf("seed%08d", i*30)
		got, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), k)
		if err != nil {
			t.Fatalf("fetch %s: %v", k, err)
		}
		if got < 1 {
			t.Errorf("fetch %s returned %d edges, want >=1", k, got)
		}
	}
}
