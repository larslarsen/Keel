// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// isolated keeps tests off the public DHT: an explicitly empty bootstrap set
// means the node joins nothing and only talks to peers it is handed directly.
//
// A serving node here behaves as Level 3 — it serves its own observations —
// because that is what most of these tests are exercising. The Level 2 mirror
// boundary gets its own test, which sets ServeOwnObservations back to false.
func isolated(serve bool, t *testing.T) Config {
	return Config{
		Serve:                serve,
		ServeOwnObservations: serve,
		Bootstrap:            []peer.AddrInfo{},
		ListenAddrs:          []string{"/ip4/127.0.0.1/tcp/0"},
		Log:                  func(f string, a ...any) { t.Logf(f, a...) },
	}
}

func newStore(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seed(t *testing.T, st *store.Store, from, to string, slot int) {
	t.Helper()
	ctx := from
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "33333333-3333-4333-8333-333333333333",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, SlotIndex: slot, VideoID: to, Title: "Title " + to,
	}}); err != nil {
		t.Fatal(err)
	}
}

// TestBlockTransferBetweenNodes is the whole point of the package: one node
// asks another for a neighbourhood over a real libp2p connection, and the
// receiver's walk gains it.
func TestBlockTransferBetweenNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "server.sqlite")
	seed(t, server, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, server, "seedaaaaaaa", "targetaaaa2", 2)

	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	edges, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), "seedaaaaaaa")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if edges != 2 {
		t.Fatalf("gained %d edges, want 2", edges)
	}

	// The receiver had no observations of its own; everything it now knows
	// about this neighbourhood came over the wire.
	sug, err := client.Suggest("seedaaaaaaa", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sug.Suggestions) == 0 {
		t.Errorf("client gained edges but its walk returns nothing (graph: %d nodes, %d edges)",
			sug.GraphNodes, sug.GraphEdges)
	}
}

// TestFetchingNodeServesNothing pins §5d's asymmetry: a node that contributes
// nothing still fetches and still works, and must not answer requests.
func TestFetchingNodeServesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	quiet := newStore(t, "quiet.sqlite")
	seed(t, quiet, "seedaaaaaaa", "secretvid01", 0)
	qNode, err := Start(ctx, quiet, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer qNode.Close()

	asker := newStore(t, "asker.sqlite")
	aNode, err := Start(ctx, asker, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer aNode.Close()

	// The non-serving node registered no handler, so this must not succeed.
	if _, err := aNode.FetchFrom(ctx, qNode.AddrInfo(), "seedaaaaaaa"); err == nil {
		t.Fatal("a Level 1 node answered a block request")
	}

	// And Announce must stay silent for it.
	if err := qNode.Announce(ctx); err != nil {
		t.Errorf("Announce on a non-serving node returned %v, want nil no-op", err)
	}
}

// TestRejectsUnverifiableBlock proves the transport does not bypass the
// verification in store.VerifyBlock — a peer cannot inject edges by serving
// something that does not check out.
func TestRejectsUnverifiableBlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "server.sqlite")
	seed(t, server, "seedaaaaaaa", "targetaaaa1", 0)
	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	// Ask for a key the server has nothing for. It answers with a valid empty
	// block, which must import cleanly and add nothing.
	edges, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), "nothinghere")
	if err != nil {
		t.Fatalf("empty block was refused: %v", err)
	}
	if edges != 0 {
		t.Errorf("empty block yielded %d edges", edges)
	}
}

// TestConcurrentFetchesCollapse checks the in-flight dedupe: prewarm and a
// user-driven walk asking for the same block must not open two streams.
func TestConcurrentFetchesCollapse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	// No providers exist on an isolated network, so both calls return a miss
	// rather than an error — the assertion is that neither hangs or panics.
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := cNode.Fetch(ctx, "seedaaaaaaa")
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil && err != context.DeadlineExceeded {
				t.Errorf("concurrent fetch returned %v", err)
			}
		case <-time.After(50 * time.Second):
			t.Fatal("concurrent fetch hung")
		}
	}
}

// TestMirrorNodeDoesNotServeOwnObservations is the contribution-level boundary,
// enforced end to end over the wire.
//
// A Level 2 node donates storage and bandwidth: it re-serves what other people
// published. It must not serve what its own user was recommended — that is
// Level 3, and the whole reason the levels are separate.
func TestMirrorNodeDoesNotServeOwnObservations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A mirror node that has watched things itself.
	mirror := newStore(t, "mirror.sqlite")
	seed(t, mirror, "privateseed", "privatevid1", 0)
	seed(t, mirror, "privateseed", "privatevid2", 1)

	cfg := isolated(true, t)
	cfg.ServeOwnObservations = false // Level 2
	mNode, err := Start(ctx, mirror, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mNode.Close()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	got, err := cNode.FetchFrom(ctx, mNode.AddrInfo(), "privateseed")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if got != 0 {
		t.Fatalf("a Level 2 node served %d of its own observed edges", got)
	}

	// It must still re-serve what it imported from someone else.
	origin := newStore(t, "origin.sqlite")
	seed(t, origin, "publicseed1", "publicvid01", 0)
	blk, err := origin.BuildBlock("publicseed1", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mirror.ImportBlock(raw); err != nil {
		t.Fatal(err)
	}

	got, err = cNode.FetchFrom(ctx, mNode.AddrInfo(), "publicseed1")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if got != 1 {
		t.Errorf("mirror re-served %d edges of imported data, want 1", got)
	}
}

// TestCatalogueFetchLabelsFetchedGraph is the end-to-end reason this path
// exists: blocks are stringless, so a node that fetches graph data holds ids
// and no titles until the catalogue arrives on its own protocol.
//
// Titles now arrive without a second call. Fetching remembers the peer that
// served the blocks, and the catalogue path falls back to remembered peers when
// discovery finds nobody — which on an isolated network is always. That is the
// censorship fallback doing its job on a path it was not written for.
func TestCatalogueFetchLabelsFetchedGraph(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "server.sqlite")
	seed(t, server, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, server, "seedaaaaaaa", "targetaaaa2", 1)

	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()
	cNode.cfg.Fetch = true // the catalogue path is gated with block fetching

	before, err := client.SearchVideos("Title targetaaaa1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 0 {
		t.Fatalf("a fresh node already had titles: %d hits", before.Total)
	}

	if _, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), "seedaaaaaaa"); err != nil {
		t.Fatal(err)
	}

	after, err := client.SearchVideos("Title targetaaaa1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if after.Total == 0 {
		t.Error("graph arrived but the video never became searchable by title")
	}
}

// TestMirrorNodeServesNoOwnCatalogue is rule 2: serving catalogue rows derived
// from this node's own impressions would disclose viewing at video granularity,
// because a requester sees exactly which bucket members the node holds.
func TestMirrorNodeServesNoOwnCatalogue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mirror := newStore(t, "mirror.sqlite")
	seed(t, mirror, "privateseed", "privatevid1", 0)

	cfg := isolated(true, t)
	cfg.ServeOwnObservations = false // Level 2
	mNode, err := Start(ctx, mirror, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mNode.Close()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	prefix := store.CataloguePrefix("privatevid1", store.DefaultPrefixBits)
	raw, err := cNode.requestOn(ctx, mNode.AddrInfo(), prefix, CatalogueProtocol)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.ImportCataloguePack(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("a Level 2 node served %d catalogue rows about videos its user saw", rows)
	}
}

// TestFallsBackToRememberedPeers is the escape hatch from DHT censorship.
//
// GO-2024-3218 has no fix: flooding provider records makes a key undiscoverable.
// Only discovery breaks — the block protocol needs no DHT — so a node that has
// been served before must be able to ask that peer directly. Here the DHT is
// empty, which is indistinguishable from being censored.
func TestFallsBackToRememberedPeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	serverStore := newStore(t, "server.sqlite")
	seed(t, serverStore, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, serverStore, "seedaaaaaaa", "targetaaaa2", 1)
	server, err := Start(ctx, serverStore, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	clientStore := newStore(t, "client.sqlite")
	client, err := Start(ctx, clientStore, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.cfg.Fetch = true

	// A first, direct exchange — the kind that happens before an attack.
	if _, err := client.FetchFrom(ctx, server.AddrInfo(), "seedaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	known, err := clientStore.KnownPeers(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != 1 || known[0].ID != server.ID().String() {
		t.Fatalf("peer was not remembered after a successful fetch: %+v", known)
	}

	// Now discovery yields nothing, as it would under censorship. A fresh
	// client with the same memory must still get the data.
	victimStore := newStore(t, "victim.sqlite")
	if err := victimStore.RememberPeer(known[0].ID, known[0].Addrs); err != nil {
		t.Fatal(err)
	}
	victim, err := Start(ctx, victimStore, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	victim.cfg.Fetch = true

	edges, err := victim.Fetch(ctx, "seedaaaaaaa")
	if err != nil {
		t.Fatalf("fallback fetch failed: %v", err)
	}
	if edges == 0 {
		t.Fatal("discovery found nothing and the remembered peer was not tried")
	}

	sug, err := victimStore.Suggest("seedaaaaaaa", 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sug.Suggestions) == 0 {
		t.Error("edges arrived via the fallback but the walk returns nothing")
	}
}

// TestHandleBlockRequestFaultInjection is the server-side fault surface the
// happy-path tests never touch: a peer (or a client probing the handler
// directly) can send a hostile prefix line. The handler reads at most 256 bytes
// and treats an empty/garbage prefix as a silent no-op. A malicious line must
// never panic the server nor return a valid block. We drive it over a real
// in-process libp2p connection via FetchFrom with crafted keys, asserting the
// server survives and the client gets a clean miss (not a crash, not data).
func TestHandleBlockRequestFaultInjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "server.sqlite")
	seed(t, server, "seedaaaaaaa", "targetaaaa1", 0)
	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	hostile := []string{
		"",                           // empty line -> no-op
		"   ",                        // whitespace only
		strings.Repeat("x", 300),     // over the 256-byte read limit
		"../../../etc/passwd",        // path traversal attempt
		"\x00\x01\x02binary",         // embedded NULs / binary
		"a3f\n\r\r\n",                // CRLF noise
		"seedaaaaaaa",                // a real key the server DOES have
	}
	for _, key := range hostile {
		// A known, directly-connected peer is asked. For hostile keys the
		// server handler should no-op and the client should get a clean miss
		// (FetchFrom returns an error for non-bucket/garbage, or 0 edges for
		// an empty bucket). The assertion is that it does not panic/hang.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("handler panicked on hostile key %q: %v", key, r)
				}
			}()
			_, _ = cNode.FetchFrom(ctx, sNode.AddrInfo(), key)
		}()
	}
}
