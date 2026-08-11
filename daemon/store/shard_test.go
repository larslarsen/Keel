// SPDX-License-Identifier: Apache-2.0
package store

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// TestTokenizeWordAnchored checks the concrete scheme WO-059 specifies: every
// k-letter run of a lowercased word, with only the word's first run carrying
// the leading-space anchor — that asymmetry is what a word-start match relies
// on (see the tokenize doc comment). "AI" is shorter than k=3 and emits
// nothing, per the tokenizability floor "adaptive stepping" exists to cover.
func TestTokenizeWordAnchored(t *testing.T) {
	got := tokenize("Recommendation AI", 3)
	want := []string{" rec", "eco", "com", "omm", "mme", "men", "end", "nda", "dat", "ati", "tio", "ion"}
	if len(got) != len(want) {
		t.Fatalf("tokenize() = %v (%d tokens), want %d", got, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTokenizeCrossWordBleed is WO-059 attack #7: a query for the whole word
// "men" must not match the bare run "men" buried mid-word in
// "recommendation" — the query's word-initial token (" men", anchored) must
// differ from the title's interior token at that position ("men", unanchored).
// The doc calls this "minimal, not zero": only the anchored, word-initial
// case is guaranteed distinct; interior tokens are still a shared, unanchored
// space (why a title re-check remains a backstop).
func TestTokenizeCrossWordBleed(t *testing.T) {
	title := tokenize("recommendation", 3)
	query := TokenizeQuery("men")
	if len(query) != 1 || query[0] != " men" {
		t.Fatalf("TokenizeQuery(\"men\") = %v, want exactly [\" men\"]", query)
	}
	for _, tt := range title {
		if tt == query[0] {
			t.Errorf("title token %q collided with anchored query token %q", tt, query[0])
		}
	}
	// The interior run does still appear, unanchored — documenting the
	// accepted residual bleed rather than asserting it away.
	foundInterior := false
	for _, tt := range title {
		if tt == "men" {
			foundInterior = true
		}
	}
	if !foundInterior {
		t.Fatal("expected the unanchored interior run \"men\" in recommendation's tokens (test assumption changed)")
	}
}

// TestTokenizeQueryStepsDownForShortSingleWord covers the one degenerate case
// WO-059's "adaptive stepping" names: a query that is a single word shorter
// than ShardK emits nothing at ShardK, so the tokenizer must fall back to
// k=2 rather than returning an empty, unsearchable query.
func TestTokenizeQueryStepsDownForShortSingleWord(t *testing.T) {
	if got := TokenizeQuery("ai"); len(got) == 0 {
		t.Fatal("TokenizeQuery(\"ai\") produced no tokens even after stepping down")
	}
	// A normal multi-word query is never stepped, even if one word is short:
	// the other words already produce tokens at ShardK. "ai" itself must not
	// contribute (too short at k=3), so its 2-letter fallback tokens must be
	// absent — evidence stepping did not fire.
	got := TokenizeQuery("recommendation ai")
	for _, tok := range got {
		if tok == " ai" {
			t.Error(`TokenizeQuery("recommendation ai") stepped down to k=2 and emitted " ai" — should not have, "recommendation" already tokenizes at k=3`)
		}
	}
	if len(got) == 0 {
		t.Fatal("TokenizeQuery(\"recommendation ai\") produced no tokens")
	}
}

// TestShardOfIsDeterministic is the property every peer must agree on: the
// same token must land in the same shard every time, on any node.
func TestShardOfIsDeterministic(t *testing.T) {
	for _, tok := range []string{" rec", " ai", " the", " end"} {
		a, b := ShardOf(tok), ShardOf(tok)
		if a != b {
			t.Fatalf("ShardOf(%q) is not deterministic: %d then %d", tok, a, b)
		}
		if a < 0 || a >= ShardM {
			t.Fatalf("ShardOf(%q) = %d, out of range [0,%d)", tok, a, ShardM)
		}
	}
}

// TestShardDomainIndependentOfBlockAndCatalogue mirrors
// TestPropertyCatalogueAndBlockBucketsAreIndependent: a shard hash must not
// correlate with the block or catalogue bucket a video with the same text
// would land in, or an observer could join the two datasets.
func TestShardDomainIndependentOfBlockAndCatalogue(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	same := 0
	const count = 20000
	for i := 0; i < count; i++ {
		tok := randomVideoID(r) // any distinguishing string works as a token stand-in
		sh := ShardOf(tok)
		bp := BlockPrefix(tok, 8) // 256 buckets, same cardinality as ShardM
		if got, ok := PrefixOf(bp); ok && got == 8 {
			// Compare shard number against the block bucket's numeric value to
			// look for correlation, not identity of representation.
			var bucketNum int
			_, _ = fscanHex(bp, &bucketNum)
			if bucketNum == sh {
				same++
			}
		}
	}
	if limit := count/ShardM*8 + 8; same > limit {
		t.Errorf("shard and block bucket agreed %d times in %d (chance ≈ %d) — shardDomain is not independent",
			same, count, count/ShardM)
	}
}

// fscanHex parses the hex payload of a "bits:hex" prefix string into an int,
// for the independence check above only.
func fscanHex(prefix string, out *int) (int, error) {
	idx := strings.Index(prefix, ":")
	if idx < 0 {
		return 0, nil
	}
	hex := prefix[idx+1:]
	n := 0
	for _, c := range hex {
		n = n*16 + int(hexDigit(c))
	}
	*out = n % ShardM
	return 1, nil
}

func hexDigit(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	}
	return 0
}

// TestShardsAreEvenlyPopulated mirrors TestPropertyBucketsAreEvenlyPopulated:
// grouping only fixes the rare-token leak if shards actually end up close to
// uniform, which is the empirical claim the whole design leans on.
func TestShardsAreEvenlyPopulated(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	pop := map[int]int{}
	const count = 25600
	for i := 0; i < count; i++ {
		pop[ShardOf(randomVideoID(r))]++
	}
	minShards := ShardM * 9 / 10
	if len(pop) < minShards {
		t.Errorf("%d of %d shards used — the hash is not spreading tokens", len(pop), ShardM)
	}
	worst := count
	for _, n := range pop {
		if n < worst {
			worst = n
		}
	}
	expected := count / ShardM
	if worst < expected/3 {
		t.Errorf("smallest shard holds %d of an expected ~%d — tokens are clumping", worst, expected)
	}
}

// TestShardSliceOnlyReturnsMatchingShard is the correctness half of ShardSlice:
// every entry it returns must actually belong to the requested shard, however
// the request is called.
func TestShardSliceOnlyReturnsMatchingShard(t *testing.T) {
	st := openStore(t, "shard-slice.sqlite")
	seedTitle(t, st, "vid00000001", "Recommendation systems explained")
	seedTitle(t, st, "vid00000002", "Ambient piano for studying")
	seedTitle(t, st, "vid00000003", "Recipe: sourdough bread at home")

	for shard := 0; shard < ShardM; shard++ {
		entries, err := st.ShardSlice(shard, false)
		if err != nil {
			t.Fatalf("shard %d: %v", shard, err)
		}
		for _, e := range entries {
			for _, tok := range e.Tokens {
				if ShardOf(tok) != shard {
					t.Fatalf("shard %d entry %s carries token %q, which hashes to shard %d",
						shard, e.VideoID, tok, ShardOf(tok))
				}
			}
		}
	}
}

// TestShardSliceRespectsMirrorOnly mirrors catalogue.go's rule 2 test: below
// Level 3, a shard reply must never include tokens derived from this node's
// own impressions, only from peer_catalogue.
func TestShardSliceRespectsMirrorOnly(t *testing.T) {
	st := openStore(t, "shard-mirror.sqlite")
	seedTitle(t, st, "ownvideo001", "Owner watched this exact video")

	found := false
	for shard := 0; shard < ShardM; shard++ {
		entries, err := st.ShardSlice(shard, true) // mirrorOnly=true, Level 2
		if err != nil {
			t.Fatalf("shard %d: %v", shard, err)
		}
		for _, e := range entries {
			if e.VideoID == "ownvideo001" {
				found = true
			}
		}
	}
	if found {
		t.Error("ShardSlice(mirrorOnly=true) served a video from this node's own impressions — Level 2 must never disclose own viewing")
	}

	// The same video must be servable once it exists in peer_catalogue, i.e.
	// as something mirrored on behalf of someone else.
	if _, _, err := st.ImportEdges("peer-a", nil,
		[]bridge.CatalogueEntry{{VideoID: "mirroredvid1", Title: "Owner watched this exact video"}}); err != nil {
		t.Fatal(err)
	}
	found = false
	for shard := 0; shard < ShardM; shard++ {
		entries, err := st.ShardSlice(shard, true)
		if err != nil {
			t.Fatalf("shard %d: %v", shard, err)
		}
		for _, e := range entries {
			if e.VideoID == "mirroredvid1" {
				found = true
			}
		}
	}
	if !found {
		t.Error("ShardSlice(mirrorOnly=true) did not serve a video that arrived via peer_catalogue")
	}
}

// TestTitlesForReturnsEveryIDTitledOrNot checks the helper PEER_SEARCH uses to
// title its hits: a known id comes back with a title, but an id this node has
// never seen still comes back — with an empty title — rather than being
// dropped. A real peer-search find is not silently discarded just because
// this node cannot name it (see TitlesFor's doc comment for why no live
// catalogue fetch fills the gap here).
func TestTitlesForReturnsEveryIDTitledOrNot(t *testing.T) {
	st := openStore(t, "titles-for.sqlite")
	seedTitle(t, st, "knownvideo1", "A video this node has seen")

	hits, err := st.TitlesFor([]string{"knownvideo1", "neverheardof1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("TitlesFor = %+v, want 2 hits (one titled, one not)", hits)
	}
	byID := map[string]bridge.SearchHit{}
	for _, h := range hits {
		byID[h.VideoID] = h
	}
	if byID["knownvideo1"].Title != "A video this node has seen" {
		t.Errorf("knownvideo1 title = %q, want the seeded title", byID["knownvideo1"].Title)
	}
	if got, ok := byID["neverheardof1"]; !ok {
		t.Error("neverheardof1 missing entirely — a real find must not be dropped")
	} else if got.Title != "" {
		t.Errorf("neverheardof1 title = %q, want empty (never seen locally)", got.Title)
	}
}

// seedTitle records one impression carrying a title, for tests that only
// care about the catalogue/search side rather than the graph.
func seedTitle(t *testing.T, st *Store, videoID, title string) {
	t.Helper()
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: nextPageLoad(),
		ObservedAt: time.Now().UnixMilli(), Surface: "HOME",
		SlotIndex: 0, VideoID: videoID, Title: title,
	}}); err != nil {
		t.Fatal(err)
	}
}
