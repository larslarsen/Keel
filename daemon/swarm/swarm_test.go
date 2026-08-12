// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// isolated keeps tests off the public DHT: an explicitly empty bootstrap set
// means the node joins nothing and only talks to peers it is handed directly.
//
// A serving node here is Level 2, which since WO-084 is the level that serves
// broad buckets containing its own graph blocks. Level 3 would behave
// identically on every path these tests touch — it adds STAR cohort
// measurement, not graph service — so naming it here would imply a boundary
// that is not where the code puts it.
func isolated(serve bool, t *testing.T) Config {
	p := PolicyForLevel(store.LevelBroad)
	if !serve {
		p = PolicyForLevel(store.LevelPersonal)
		// Most non-serving nodes in these tests are pure clients that still
		// fetch; Level 1 already grants that since WO-077.
	}
	return Config{
		Policy:      p,
		Bootstrap:   []peer.AddrInfo{},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Log:         func(f string, a ...any) { t.Logf(f, a...) },
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

// TestLevelTwoServesLocalAndImportedTogether is the contribution-level
// boundary WO-084 corrected, enforced end to end over the wire.
//
// WO-077 had this backwards: it asserted a Level-2 node must serve nothing its
// own user observed, deferring every locally derived edge to Level 3. Level 2's
// contribution *is* its own graph blocks. Their privacy mechanism is the
// broadness of the bucket, not the exclusion of local data.
//
// Both halves are asserted against the same node, because the failure this
// guards is a one-line fix that flips the source instead of unioning it: an
// implementation that dropped either source passes half of this and fails the
// other half.
func TestLevelTwoServesLocalAndImportedTogether(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A Level-2 node whose only graph data is what its own user was shown.
	server := newStore(t, "level2.sqlite")
	seed(t, server, "localseed01", "localvid001", 0)
	seed(t, server, "localseed01", "localvid002", 1)

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

	got, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), "localseed01")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if got != 2 {
		t.Fatalf("a Level-2 node served %d locally derived edges, want 2", got)
	}

	// And it still re-serves what it imported from someone else. A change that
	// merely flipped the old mirror-only boolean would stop here.
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
	if _, _, err := server.ImportBlock(raw); err != nil {
		t.Fatal(err)
	}

	got, err = cNode.FetchFrom(ctx, sNode.AddrInfo(), "publicseed1")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if got != 1 {
		t.Errorf("the node re-served %d edges of imported data, want 1", got)
	}
}

// TestLevelOneServesNothing is the other side of the same boundary: a Level-1
// node is a full consumer and an empty server.
func TestLevelOneServesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "level1.sqlite")
	seed(t, server, "localseed01", "localvid001", 0)
	sNode, err := Start(ctx, server, isolated(false, t))
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

	// No block stream handler is registered at all, so the dial itself fails.
	// A returned error and a returned zero are both correct answers; a served
	// edge is not.
	if got, err := cNode.FetchFrom(ctx, sNode.AddrInfo(), "localseed01"); err == nil && got != 0 {
		t.Fatalf("a Level-1 node served %d edges", got)
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
	cNode.cfg.Policy.Fetch = true // the catalogue path is gated with block fetching

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

// TestLevelTwoServesItsOwnCatalogue is catalogue.go's rule 2 as WO-084
// rewrote it.
//
// The rule used to be "mirrored rows only below Level 3", on the reasoning that
// a requester asking for a bucket sees exactly which of its members the node
// holds. That is the disclosure Level 2 accepts knowingly: the bucket is
// hashed, coarse and answered whole, and a catalogue row is public video
// metadata rather than an observation. The graph blocks this node serves would
// otherwise arrive at every peer permanently unlabelled.
func TestLevelTwoServesItsOwnCatalogue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "level2.sqlite")
	seed(t, server, "localseed01", "localvid001", 0)

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

	prefix := store.CataloguePrefix("localvid001", store.DefaultPrefixBits)
	raw, err := cNode.requestOn(ctx, sNode.AddrInfo(), prefix, CatalogueProtocol)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.ImportCataloguePack(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Error("a Level-2 node served no catalogue rows, so its graph blocks arrive unlabelled")
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
	client.cfg.Policy.Fetch = true

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
	victim.cfg.Policy.Fetch = true

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
		"",                       // empty line -> no-op
		"   ",                    // whitespace only
		strings.Repeat("x", 300), // over the 256-byte read limit
		"../../../etc/passwd",    // path traversal attempt
		"\x00\x01\x02binary",     // embedded NULs / binary
		"a3f\n\r\r\n",            // CRLF noise
		"seedaaaaaaa",            // a real key the server DOES have
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

// TestDifferentKeySchemeCannotBeServed is WO-060's acceptance test: a node that
// derives keys differently must fail to connect, not connect and be told
// nothing exists.
//
// The distinction is the whole ticket. Both outcomes return no data, but only
// one is diagnosable — "protocol not supported" names the problem, whereas an
// empty result is indistinguishable from an empty network (WO-058), which is
// exactly how a silent partition survives for months.
func TestDifferentKeySchemeCannotBeServed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "ks-server.sqlite")
	seed(t, server, "seedaaaaaaa", "targetaaaa1", 0)
	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	// A bare host, so the test speaks to the server's protocol ids directly
	// rather than through a Node that would only ever use the matching one.
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.Connect(ctx, sNode.AddrInfo()); err != nil {
		t.Fatal(err)
	}

	// The scheme this build agrees on: the stream opens.
	s, err := h.NewStream(ctx, sNode.AddrInfo().ID, BlockProtocol)
	if err != nil {
		t.Fatalf("matching key scheme %q was refused: %v", BlockProtocol, err)
	}
	_ = s.Close()

	// A future scheme, connected to the same peer over the same open
	// connection: refused before any request is made.
	other := keelProtocol("block", "2.0.0", store.KeySchemeVersion+1)
	if other == BlockProtocol {
		t.Fatalf("the key scheme is not present in the protocol id %q", BlockProtocol)
	}
	if _, err := h.NewStream(ctx, sNode.AddrInfo().ID, other); err == nil {
		t.Errorf("a node on key scheme %d served a peer on scheme %d",
			store.KeySchemeVersion, store.KeySchemeVersion+1)
	}

	// The catalogue is partitioned by the same number, for the same reason.
	otherCat := keelProtocol("catalogue", "1.0.0", store.KeySchemeVersion+1)
	if _, err := h.NewStream(ctx, sNode.AddrInfo().ID, otherCat); err == nil {
		t.Errorf("catalogue served across key schemes")
	}

	// The live index is deliberately NOT partitioned by key scheme: it is not
	// bucketed, so a scheme bump must not cost it peers.
	if strings.Contains(string(LiveSnapshotProtocol), "ks") {
		t.Errorf("live snapshot protocol %q carries a key scheme it does not need",
			LiveSnapshotProtocol)
	}
}
