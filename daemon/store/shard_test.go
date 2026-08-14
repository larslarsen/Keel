// SPDX-License-Identifier: Apache-2.0
package store

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// TestQueryGridIsOneContinuousPass checks the concrete scheme-2 query rule:
// one fixed, non-overlapping pass over the whole normalized query, padded only
// at the tail. Spaces are consumed characters, so a chunk may straddle two
// words — the property scheme 1's per-word cut did not have and the reason a
// query substring can be found at any alignment in a title.
func TestQueryGridIsOneContinuousPass(t *testing.T) {
	spans := queryGrid(NormalizeSearchText("Recommendation AI"))
	want := []string{"rec", "omm", "end", "ati", "on ", "ai "}
	if len(spans) != len(want) {
		t.Fatalf("queryGrid() = %v (%d chunks), want %d", spans, len(spans), len(want))
	}
	for i := range want {
		if spans[i].Token != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, spans[i].Token, want[i])
		}
		if len(spans[i].Token) != ShardK {
			t.Errorf("chunk %q has length %d, want fixed ShardK=%d", spans[i].Token, len(spans[i].Token), ShardK)
		}
	}

	// A chunk that straddles the space belongs to both words, and its range
	// says so. `a big` cuts to [a b][ig ], and the first chunk covers a letter
	// of each word.
	plan := BuildQueryPlan("a big")
	if len(plan.Tokens) != 2 || plan.Tokens[0].Token != "a b" {
		t.Fatalf("plan for %q = %v, want the two chunks [a b][ig ]", "a big", plan.Tokens)
	}
	if len(plan.Tokens[0].Fragments) != 2 {
		t.Errorf("chunk %q covers %d word fragments, want 2 — a cross-space token "+
			"must color both words it touches", plan.Tokens[0].Token, len(plan.Tokens[0].Fragments))
	}
}

// TestIndexCoverageIsBroaderThanTheMatcher is the scheme-2 replacement for
// what used to be TestTokenizeCrossWordBleed, and it records a deliberate
// reversal.
//
// Scheme 1 cut per word, so "recommendation" never produced the token "men"
// and a "men" query could not collide with it. Scheme 2 generates every
// alignment, so "men" IS in that title's index — the letters really are there
// at offsets 5..7. That over-broad discovery is the price of finding a query
// substring wherever it sits, and it is paid back immediately: shard
// membership only nominates candidates, and the final matcher — which respects
// normalized word boundaries — is what rejects this one (WO-097 §4).
//
// If this ever regresses to the matcher trusting shard membership, a search
// for "men" starts returning "recommendation".
func TestIndexCoverageIsBroaderThanTheMatcher(t *testing.T) {
	index := map[string]bool{}
	for _, tok := range TitleTokens("recommendation") {
		index[tok] = true
	}
	query := TokenizeQuery("men")
	if len(query) != 1 || query[0] != "men" {
		t.Fatalf("TokenizeQuery(%q) = %v, want [men]", "men", query)
	}
	if !index["men"] {
		t.Errorf("TitleTokens(%q) does not contain %q — every-alignment coverage "+
			"is what lets a query find a substring off the grid", "recommendation", "men")
	}

	// Discovery nominates it; the matcher throws it out.
	plan := BuildQueryPlan("men")
	if plan.MatchTitle("recommendation") {
		t.Error("the matcher accepted \"recommendation\" for the query \"men\" — " +
			"token membership has become the semantic test, which it must never be")
	}
	if !plan.MatchTitle("men at work") {
		t.Error("the matcher rejected \"men at work\" for the query \"men\"")
	}
}

// TestQueryTokensNeverFallBackToADifferentK is the correction to an earlier,
// wrong design: ShardK is a versioned protocol constant (keyscheme.go,
// WO-060) that every node generates its title windows at. A client falling
// back to some other k for a short query would compute shards no server
// populates at that width and silently find nothing — so there must be no
// fallback, and tail-padding must make even a one- or two-letter query produce
// a real ShardK token on its own.
func TestQueryTokensNeverFallBackToADifferentK(t *testing.T) {
	for _, q := range []string{"ai", "go", "x"} {
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

	// A stopword-only query is the one case that legitimately produces no
	// discovery tokens: there is no distributed work to do, and that has to be
	// visible rather than look like a failed tokenization (WO-097 §3).
	for _, q := range []string{"a", "the", "is is", "the a of"} {
		if got := TokenizeQuery(q); len(got) != 0 {
			t.Errorf("TokenizeQuery(%q) = %v, want no discovery tokens — a "+
				"stopword-only query does local search only", q, got)
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
		entries, err := st.ShardSlice(shard, AllSources)
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

// TestShardSliceFollowsItsSourceSet mirrors catalogue.go's rule 2 test, as
// WO-084 rewrote it: a shard reply is drawn from exactly the corpus its
// SourceSet names, and each half is reachable on its own.
//
// The Level-2 policy is store.AllSources, not PeerSources — a Level-2 node
// serves its own titles too. What this holds is that the selector is honest, so
// LocalShards can announce over the same set the stream will answer from.
func TestShardSliceFollowsItsSourceSet(t *testing.T) {
	st := openStore(t, "shard-sources.sqlite")
	seedTitle(t, st, "ownvideo001", "Owner watched this exact video")

	found := false
	for shard := 0; shard < ShardM; shard++ {
		entries, err := st.ShardSlice(shard, PeerSources)
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
		t.Error("ShardSlice(PeerSources) served a video from this node's own impressions")
	}

	// The same title must be servable once it exists in peer_catalogue, i.e.
	// as something held on behalf of someone else.
	if _, _, err := st.ImportEdges("peer-a", nil,
		[]bridge.CatalogueEntry{{VideoID: "mirroredvid1", Title: "Owner watched this exact video"}}); err != nil {
		t.Fatal(err)
	}
	found = false
	for shard := 0; shard < ShardM; shard++ {
		entries, err := st.ShardSlice(shard, PeerSources)
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
		t.Error("ShardSlice(PeerSources) did not serve a video that arrived via peer_catalogue")
	}

	// And the union serves both — the property that makes Level 2's announced
	// shard set match what it answers with.
	local, imported := false, false
	for shard := 0; shard < ShardM; shard++ {
		entries, err := st.ShardSlice(shard, AllSources)
		if err != nil {
			t.Fatalf("shard %d: %v", shard, err)
		}
		for _, e := range entries {
			switch e.VideoID {
			case "ownvideo001":
				local = true
			case "mirroredvid1":
				imported = true
			}
		}
	}
	if !local || !imported {
		t.Errorf("ShardSlice(AllSources) returned local=%v imported=%v, want both", local, imported)
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
	pack, err := signWholeShard(t, st, shard)
	if err != nil {
		t.Fatal(err)
	}
	if pack.Signature == "" || pack.PublicKey == "" {
		t.Fatal("SignShardPage produced an unsigned page")
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
	pack, err := signWholeShard(t, st, shard)
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

// signWholeShard signs one shard's whole row set as a single page — the
// pre-WO-097 shape, kept for tests about signing rather than about paging.
func signWholeShard(t *testing.T, st *Store, shard int) (*ShardPack, error) {
	t.Helper()
	rows, offset, err := st.ShardRows(shard, AllSources, 0)
	if err != nil {
		return nil, err
	}
	return st.SignShardPage(shard, 0, offset, rows)
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
