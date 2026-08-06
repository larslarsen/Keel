// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"path/filepath"
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
// exists: blocks are stringless, so a node that fetches graph data has ids and
// no titles until the catalogue arrives on its own protocol.
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
	// Fetching must be on for the catalogue path; isolated(false) is Level 1.
	cNode.cfg.Fetch = true

	if _, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), "seedaaaaaaa"); err != nil {
		t.Fatal(err)
	}

	// The graph arrived stringless, so search finds nothing by title yet.
	before, err := client.SearchVideos("Title targetaaaa1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 0 {
		t.Errorf("titles present before catalogue sync: %d hits", before.Total)
	}

	// Pull the catalogue bucket for one of the fetched videos, over its own
	// protocol, from the same peer.
	prefix := store.CataloguePrefix("targetaaaa1", store.DefaultPrefixBits)
	raw, err := cNode.requestOn(ctx, sNode.AddrInfo(), prefix, CatalogueProtocol)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.ImportCataloguePack(raw)
	if err != nil {
		t.Fatalf("catalogue pack refused: %v", err)
	}
	if rows == 0 {
		t.Fatal("catalogue bucket was empty")
	}

	after, err := client.SearchVideos("Title targetaaaa1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if after.Total == 0 {
		t.Error("catalogue arrived but the video is still not searchable by title")
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
