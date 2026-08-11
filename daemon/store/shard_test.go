// SPDX-License-Identifier: Apache-2.0
package store

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// TestTokenizeFixedSizeSpaceAware checks the concrete scheme: normalize pads
// with a leading/trailing space and collapses inter-word gaps to one space,
// then every consecutive ShardK-length window of that string is a token —
// fixed size always, space included as an ordinary character rather than
// anchored to word starts specially.
func TestTokenizeFixedSizeSpaceAware(t *testing.T) {
	// normalize("Recommendation AI") = " recommendation ai " (19 chars);
	// k=3 gives 19-3+1 = 17 windows.
	got := tokenize("Recommendation AI", 3)
	want := []string{
		" re", "rec", "eco", "com", "omm", "mme", "men", "end", "nda", "dat",
		"ati", "tio", "ion", "on ", "n a", " ai", "ai ",
	}
	if len(got) != len(want) {
		t.Fatalf("tokenize() = %v (%d tokens), want %d", got, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, tok := range got {
		if len(tok) != 3 {
			t.Errorf("token %q has length %d, want fixed size 3", tok, len(tok))
		}
	}
}

// TestTokenizeCrossWordBleed is WO-059 attack #7, restated for the fixed-
// window scheme: a query for the whole word "men" shares its bare interior
// window ("men", no space either side) with the same run buried mid-word in
// "recommendation" — that residual overlap is the accepted "minimal, not
// zero" bleed. But the query's word-boundary windows (" me", "en ", which
// only occur where "men" is a whole word on its own) must NOT appear in
// recommendation's tokens, because "men" sits mid-word there with letters
// (not spaces) on both sides.
func TestTokenizeCrossWordBleed(t *testing.T) {
	title := tokenize("recommendation", 3)
	query := TokenizeQuery("men") // normalize("men") = " men ": " me","men","en "
	want := []string{" me", "en ", "men"}
	if len(query) != len(want) {
		t.Fatalf("TokenizeQuery(\"men\") = %v, want %v", query, want)
	}

	inTitle := map[string]bool{}
	for _, tt := range title {
		inTitle[tt] = true
	}
	if !inTitle["men"] {
		t.Fatal(`expected the bare interior window "men" in recommendation's tokens (residual bleed) — test assumption changed`)
	}
	for _, edge := range []string{" me", "en "} {
		if inTitle[edge] {
			t.Errorf("title token set contains %q, a word-boundary window that should only occur where \"men\" stands alone", edge)
		}
	}
}

// TestTokenizeQueryNeverFallsBackToADifferentK is the correction to an
// earlier, wrong design: ShardK is a versioned protocol constant
// (keyscheme.go, WO-060) that every node's ShardSlice tokenizes titles at.
// A client falling back to some other k for a short query would compute
// shards no server populates at that width and silently find nothing — so
// there must be no fallback, and padding (normalize) must make even a
// one- or two-letter query produce a real ShardK token on its own.
func TestTokenizeQueryNeverFallsBackToADifferentK(t *testing.T) {
	for _, q := range []string{"a", "ai", "go"} {
		got := TokenizeQuery(q)
		if len(got) == 0 {
			t.Errorf("TokenizeQuery(%q) produced no tokens — padding should guarantee at least one at ShardK=%d", q, ShardK)
			continue
		}
		for _, tok := range got {
			if len(tok) != ShardK {
				t.Errorf("TokenizeQuery(%q) = %v contains a token of length %d, want fixed ShardK=%d",
					q, got, len(tok), ShardK)
			}
		}
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

// TestShardPackSignRoundTrip is WO-067's hardening layer: a pack built and
// signed by one store must verify cleanly, including on a second store that
// never built it — the receiver side of the actual network path.
func TestShardPackSignRoundTrip(t *testing.T) {
	st := openStore(t, "shard-pack.sqlite")
	seedTitle(t, st, "vid00000001", "Recommendation systems explained")

	shard := ShardOf(TokenizeQuery("recommendation")[0])
	pack, err := st.BuildShardPack(shard, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pack.Signature == "" || pack.PublicKey == "" {
		t.Fatal("BuildShardPack produced an unsigned pack")
	}
	if len(pack.Entries) == 0 {
		t.Fatal("pack has no entries; the property below would be vacuous")
	}
	if err := VerifyShardPack(pack); err != nil {
		t.Fatalf("VerifyShardPack rejected a pack this store just built and signed: %v", err)
	}
}

// TestShardPackRejectsForgedContent mirrors TestImportBlockRejectsForged: a
// pack whose entries were tampered with after signing, or whose signature is
// simply bogus, must fail verification rather than being accepted.
func TestShardPackRejectsForgedContent(t *testing.T) {
	st := openStore(t, "shard-pack-forge.sqlite")
	seedTitle(t, st, "vid00000001", "Recommendation systems explained")
	shard := ShardOf(TokenizeQuery("recommendation")[0])
	pack, err := st.BuildShardPack(shard, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Entries) == 0 {
		t.Fatal("pack has no entries; the property below would be vacuous")
	}

	// Tamper with the content after signing: digest and signature both stay
	// as they were computed, but the entries no longer match.
	tampered := *pack
	tampered.Entries = append([]ShardEntry{}, pack.Entries...)
	tampered.Entries[0].VideoID = "forgedvideo1"
	if err := VerifyShardPack(&tampered); err == nil {
		t.Error("VerifyShardPack accepted a pack whose entries were altered after signing")
	}

	// Bogus signature entirely, correct digest.
	bogus := *pack
	bogus.Signature = "not-a-real-signature"
	if err := VerifyShardPack(&bogus); err == nil {
		t.Error("VerifyShardPack accepted a bogus signature")
	}

	// Unsigned is accepted (matches ImportCataloguePack's policy) — the
	// negative case here is that a BROKEN signature must still be rejected,
	// not that every signature is mandatory.
	unsigned := *pack
	unsigned.Signature = ""
	unsigned.PublicKey = ""
	if err := VerifyShardPack(&unsigned); err != nil {
		t.Errorf("VerifyShardPack rejected an honestly-unsigned pack: %v", err)
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
