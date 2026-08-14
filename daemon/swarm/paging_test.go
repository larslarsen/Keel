// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

// seedTitles inserts n videos sharing one meaningful word, so they all land in
// that word's token shards and the shard needs more than one page.
func seedTitles(t *testing.T, st *store.Store, n int, word string) {
	t.Helper()
	batch := make([]bridge.Impression, 0, n)
	now := time.Now().UnixMilli()
	for i := 0; i < n; i++ {
		batch = append(batch, bridge.Impression{
			PageLoadID: "33333333-3333-4333-8333-333333333333",
			ObservedAt: now, Surface: "HOME", SlotIndex: 0,
			VideoID: fmt.Sprintf("vid%08d", i),
			Title:   fmt.Sprintf("%s episode %d", word, i),
		})
	}
	if _, err := st.PutImpressions(batch); err != nil {
		t.Fatal(err)
	}
}

// TestMultiPageShardResponseArrivesWhole is the network half of WO-097 §6: a
// shard larger than one page crosses a real libp2p stream as one logical
// response and reassembles completely.
//
// The pre-WO-097 path could not have passed this at any size, because there
// was no continuation at all — one pack, capped, and everything after it
// unreachable.
func TestMultiPageShardResponseArrivesWhole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const rows = store.MaxPageEntries*2 + 137 // deliberately not a page multiple
	server := newStore(t, "shard-pages-server.sqlite")
	seedTitles(t, server, rows, "recommendation")

	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "shard-pages-client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	token := store.TokenizeQuery("recommendation")[0]
	shard := store.ShardOf(token)

	entries, signed, complete, err := cNode.fetchShardPages(ctx, sNode.AddrInfo(), shard)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Error("the provider reported an incomplete traversal of a bucket well " +
			"inside its budget")
	}
	if !signed {
		t.Error("a page arrived unsigned")
	}
	if len(entries) != rows {
		t.Errorf("reassembled %d rows, want %d — pages are being dropped or the "+
			"traversal is stopping early", len(entries), rows)
	}

	// Deterministically deduplicated across pages: no video id twice.
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.VideoID] {
			t.Fatalf("%s arrived in more than one page", e.VideoID)
		}
		seen[e.VideoID] = true
	}

	// And the answer is usable: every row carries the token that was wanted.
	for _, e := range entries {
		has := false
		for _, tok := range e.Tokens {
			if tok == token {
				has = true
				break
			}
		}
		if !has {
			t.Fatalf("%s came back in shard %d without the token it was fetched for", e.VideoID, shard)
		}
	}
}

// TestMultiPageCatalogueResponseArrivesWhole is the same property on the
// catalogue/string path, which had the identical defect.
func TestMultiPageCatalogueResponseArrivesWhole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "cat-pages-server.sqlite")
	seedTitles(t, server, 4000, "recommendation")

	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "cat-pages-client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	// Widen the bucket to 1 bit so one prefix holds several pages' worth. The
	// prefix arithmetic is identical at every width; the property under test is
	// that a bucket larger than a page arrives whole.
	counts := map[string]int{}
	for i := 0; i < 4000; i++ {
		counts[store.CataloguePrefix(fmt.Sprintf("vid%08d", i), 1)]++
	}
	var prefix string
	for p, n := range counts {
		if n > store.MaxPageEntries {
			prefix = p
			break
		}
	}
	if prefix == "" {
		t.Fatalf("no 1-bit bucket exceeded one page: %v", counts)
	}

	got, err := cNode.fetchCataloguePagesFrom(ctx, sNode.AddrInfo(), prefix, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != counts[prefix] {
		t.Errorf("imported %d rows from bucket %s, want %d", got, prefix, counts[prefix])
	}
}

// TestPagedResponseRejectsTamperedFrames is the safety property that makes
// framing acceptable at all: a response is only as trustworthy as its terminal.
func TestPagedResponseRejectsTamperedFrames(t *testing.T) {
	st := newStore(t, "frames.sqlite")
	seedTitles(t, st, 3, "recommendation")
	shard := store.ShardOf(store.TokenizeQuery("recommendation")[0])
	rows, offset, err := st.ShardRows(shard, store.AllSources, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := st.SignShardPage(shard, 0, offset, rows)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := st.SignTerminal(fmt.Sprintf("%d", shard), len(rows), 1, true,
		store.ReasonComplete, []string{page.ContentSHA256})
	if err != nil {
		t.Fatal(err)
	}

	encode := func(frames ...any) string {
		out := ""
		for _, f := range frames {
			raw, err := jsonMarshal(f)
			if err != nil {
				t.Fatal(err)
			}
			out += string(raw) + "\n"
		}
		return out
	}
	header := store.PageHeader{Kind: "header", SchemaVersion: 1,
		Bucket: fmt.Sprintf("%d", shard), Total: len(rows), Offset: offset}

	// The honest response parses.
	if _, err := readPagedResponse(stringReader(encode(header, page, terminal))); err != nil {
		t.Fatalf("a well-formed response was rejected: %v", err)
	}

	// Truncated: the stream ends before the terminal. This is the case that
	// used to be indistinguishable from a small bucket.
	if _, err := readPagedResponse(stringReader(encode(header, page))); err == nil {
		t.Error("a response that ended without a terminal was accepted as complete")
	}

	// A duplicated page frame no longer matches the terminal's page count.
	if _, err := readPagedResponse(stringReader(encode(header, page, page, terminal))); err == nil {
		t.Error("a response with a duplicated page frame was accepted")
	}

	// No header at all.
	if _, err := readPagedResponse(stringReader(encode(page, terminal))); err == nil {
		t.Error("a response with no header frame was accepted")
	}
}

// TestLevelOneRetainsAWordSnapshotButNeverServesOne is WO-097 §7's Level
// policy, and WO-089's line held under a new feature.
//
// A Level-1 node fetches and retains the public aggregate — that is
// consumption, and withholding it would make a privacy setting withhold the
// product. What it must never do is answer or relay the protocol, because a
// pack is derived from titles this user was shown.
func TestLevelOneRetainsAWordSnapshotButNeverServesOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A Level-2 node with a corpus, to answer.
	server := newStore(t, "word-server.sqlite")
	seedTitles(t, server, 20, "recommendation")
	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	// A Level-1 node.
	client := newStore(t, "word-client.sqlite")
	seedTitles(t, client, 5, "documentary")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()

	if cNode.Policy().ServeWordTelemetry {
		t.Fatal("a Level-1 policy claims ServeWordTelemetry")
	}
	if !cNode.Policy().FetchWordTelemetry {
		t.Fatal("a Level-1 policy cannot fetch word telemetry, so it cannot hold a target")
	}

	if err := cNode.host.Connect(ctx, sNode.AddrInfo()); err != nil {
		t.Fatal(err)
	}

	// It can complete a refresh round and retain the result.
	snap, err := cNode.RefreshWordSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Sources == 0 {
		t.Error("the refresh round aggregated nothing")
	}
	targets, err := client.WordTargets([]string{"documentary"})
	if err != nil {
		t.Fatal(err)
	}
	if !targets[0].Known {
		t.Error("a Level-1 node that completed a round has no usable target")
	}

	// But it answers nobody. The handler is not registered at Level 1, so the
	// stream is refused at the protocol layer rather than answered with
	// nothing — the same structural fence the key scheme uses.
	if _, err := sNode.fetchWordTelemetry(ctx, cNode.host.ID()); err == nil {
		t.Error("a Level-1 node answered a word-telemetry request — WO-089 says " +
			"anything derived from what this user was shown starts at Level 2")
	}
}

// TestSchemeVersionFencesEveryTokenDerivedNamespace is WO-097 §5's acceptance:
// scheme 1 and scheme 2 must be unable to exchange token-derived data on any
// path — streams, provider records, or gossip topics.
func TestSchemeVersionFencesEveryTokenDerivedNamespace(t *testing.T) {
	if store.KeySchemeVersion != 2 {
		t.Fatalf("KeySchemeVersion is %d; this test describes the scheme-2 fence", store.KeySchemeVersion)
	}
	scheme := fmt.Sprintf("ks%d", store.KeySchemeVersion)

	for name, id := range map[string]string{
		"shard protocol":          string(ShardProtocol),
		"catalogue protocol":      string(CatalogueProtocol),
		"block protocol":          string(BlockProtocol),
		"word telemetry protocol": string(WordTelemetryProtocol),
		"yield topic":             YieldTopic,
		"sketch topic":            SketchTopic,
	} {
		if !containsString(id, scheme) {
			t.Errorf("%s is %q, which carries no key scheme — a scheme-1 peer and a "+
				"scheme-2 peer would meet here and misread each other's token data",
				name, id)
		}
	}

	// The two topics must also be distinct from each other, or a yield vector
	// and a sketch would share a mesh.
	if YieldTopic == SketchTopic {
		t.Error("yield and sketch gossip share a topic name")
	}
}

func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// jsonMarshal / stringReader keep the frame-tampering test above readable
// without pulling encoding/json and strings into its assertions.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func stringReader(s string) io.Reader { return strings.NewReader(s) }

// TestCandidateResolutionDownloadsTheWholeBroadPrefix is WO-097 §6's last
// acceptance line, and the one that is a privacy property rather than a
// completeness one.
//
// Resolving a candidate title must download the whole logical broad string
// prefix. The tempting optimization — ask for the row you want, or stop the
// traversal once it arrives — would let the provider infer which member of the
// bucket was of interest from what was requested or from where the stream
// stopped, which is exactly the correlation whole-bucket fetching pays for
// (catalogue.go rule 1). Cover rows arriving alongside the wanted one are the
// mechanism, not waste.
func TestCandidateResolutionDownloadsTheWholeBroadPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := newStore(t, "resolve-server.sqlite")
	seedTitles(t, server, 400, "recommendation")
	sNode, err := Start(ctx, server, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()

	client := newStore(t, "resolve-client.sqlite")
	cNode, err := Start(ctx, client, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Close()
	// The DHT is empty in these tests, so discovery falls back to remembered
	// peers — the same path a censored lookup would take.
	cNode.remember(sNode.AddrInfo())

	// One candidate. Everything sharing its bucket must arrive with it.
	wanted := "vid00000042"
	prefix := store.CataloguePrefix(wanted, cNode.prefixBits())
	expected := 0
	for i := 0; i < 400; i++ {
		if store.CataloguePrefix(fmt.Sprintf("vid%08d", i), cNode.prefixBits()) == prefix {
			expected++
		}
	}
	if expected < 2 {
		t.Skipf("bucket %s holds only %d rows; nothing to prove about cover traffic", prefix, expected)
	}

	rows, err := cNode.ResolveCandidateTitles(ctx, []string{wanted})
	if err != nil {
		t.Fatal(err)
	}
	if rows != expected {
		t.Errorf("resolving one candidate imported %d rows, want the whole bucket of %d — "+
			"a narrower request would tell the provider which row was wanted",
			rows, expected)
	}

	// The wanted title really did arrive, so the breadth is not costing
	// correctness.
	hits, err := client.TitlesFor([]string{wanted})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title == "" {
		t.Errorf("the candidate's title did not arrive: %+v", hits)
	}

	// Candidates sharing a prefix coalesce into one traversal: asking for three
	// members of a bucket already held imports nothing further.
	again, err := cNode.ResolveCandidateTitles(ctx, []string{wanted, "vid00000043", "vid00000044"})
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("re-resolving candidates already held fetched %d rows; catalogue "+
			"traffic must converge to nothing", again)
	}
}
