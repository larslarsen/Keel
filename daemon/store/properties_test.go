// SPDX-License-Identifier: Apache-2.0
// Property tests (WO-062 §2).
//
// These assert relationships that must hold for *every* input, rather than
// checking particular ones. That is the point: the bugs that slipped on this
// project were not in the cases anyone thought to write down. A property test
// covers the inputs nobody imagined, which is exactly where a solo tester's
// mental model stops.
package store

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/keel-app/keel/daemon/bridge"
)

// randomVideoID builds an id-shaped string from the alphabet YouTube uses.
func randomVideoID(r *rand.Rand) string {
	const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	b := make([]byte, 11)
	for i := range b {
		b[i] = alpha[r.Intn(len(alpha))]
	}
	return string(b)
}

// TestPropertyPrefixIsDeterministicAndNested covers the constant every peer
// must agree on (WO-060).
//
// Two properties, and the second is the load-bearing one: a narrower prefix
// must be a true prefix of a wider one over the same id. If it were a separate
// hash, a node could not widen or narrow its own bucket view without refetching
// everything, and two nodes running different widths would not merely fetch
// different amounts — they would look at unrelated parts of the keyspace.
func TestPropertyPrefixIsDeterministicAndNested(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		id := randomVideoID(r)

		if a, b := BlockPrefix(id, 12), BlockPrefix(id, 12); a != b {
			t.Fatalf("BlockPrefix(%q) is not deterministic: %q then %q", id, a, b)
		}

		// Whole-byte widths only: the string renders whole hex digits, so
		// comparing 12 bits against 8 means comparing "12:35f0" to "8:35".
		wide := BlockPrefix(id, 16)
		narrow := BlockPrefix(id, 8)
		wideHex := wide[strings.Index(wide, ":")+1:]
		narrowHex := narrow[strings.Index(narrow, ":")+1:]
		if !strings.HasPrefix(wideHex, narrowHex) {
			t.Fatalf("%q: 8-bit bucket %q is not a prefix of the 16-bit bucket %q",
				id, narrowHex, wideHex)
		}
	}
}

// TestPropertyCatalogueAndBlockBucketsAreIndependent checks that the two
// datasets bucket a video without correlation, so one cannot be used to probe
// the other.
//
// Stated as a rate, not a per-id rule. Two independent 12-bit hashes of the
// same id land on the same value about once every 4096 ids purely by chance;
// asserting they never match would be a test that fails on a correct
// implementation roughly half the time it is run over 2000 ids. What actually
// distinguishes a working domain separator from a broken one is that matches
// stay near chance instead of being the rule.
func TestPropertyCatalogueAndBlockBucketsAreIndependent(t *testing.T) {
	const (
		bits  = 12
		count = 40000
	)
	r := rand.New(rand.NewSource(4))
	same := 0
	for i := 0; i < count; i++ {
		id := randomVideoID(r)
		bp := BlockPrefix(id, bits)
		cp := CataloguePrefix(id, bits)
		if bp[strings.Index(bp, ":"):] == cp[strings.Index(cp, ":"):] {
			same++
		}
	}
	// Chance is count/4096 ≈ 10. A shared domain separator would make this
	// count, and the bound is loose enough that randomness cannot trip it.
	if limit := count / 4096 * 8; same > limit {
		t.Errorf("catalogue and block buckets agreed %d times in %d (chance ≈ %d) "+
			"— the domain separators are not independent", same, count, count/4096)
	}
}

// TestPropertyPrefixWidthIsHonoured guards the k-anonymity floor.
//
// A bucket narrower than advertised is a smaller anonymity set than the user
// was promised, and it fails silently — the request still works, it just hides
// the video among fewer others. So the width in the key must always be the
// width that was asked for, and out-of-range widths must fall back to the
// default rather than to something arbitrary.
func TestPropertyPrefixWidthIsHonoured(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 500; i++ {
		id := randomVideoID(r)
		for _, bits := range []int{4, 8, 12, 16, 20, 24, 32} {
			p := BlockPrefix(id, bits)
			got, ok := PrefixOf(p)
			if !ok {
				t.Fatalf("BlockPrefix(%q, %d) = %q, which PrefixOf rejects", id, bits, p)
			}
			if got != bits {
				t.Fatalf("asked for %d bits, key %q claims %d", bits, p, got)
			}
		}
		// Nonsense widths must not produce a nonsense bucket.
		for _, bits := range []int{-1, 0, 65, 1000} {
			p := BlockPrefix(id, bits)
			if got, ok := PrefixOf(p); !ok || got != DefaultPrefixBits {
				t.Fatalf("BlockPrefix(%q, %d) = %q, want a default-width key", id, bits, p)
			}
		}
	}
}

// TestPropertyBucketsAreEvenlyPopulated is the other half of the k-anonymity
// floor, and the reason prefixes are taken over a hash rather than over the id.
//
// YouTube ids are not uniformly distributed. Bucketing raw ids would leave some
// buckets holding a single video — k-anonymity with k=1, which is no anonymity
// at all — and would let anyone choose an id that lands in a sparse one.
func TestPropertyBucketsAreEvenlyPopulated(t *testing.T) {
	const (
		bits  = 8 // 256 buckets
		count = 25600
	)
	r := rand.New(rand.NewSource(3))
	pop := map[string]int{}
	for i := 0; i < count; i++ {
		pop[BlockPrefix(randomVideoID(r), bits)]++
	}

	// With 100 expected per bucket, a hash this size should fill nearly all of
	// them and leave none pathologically small.
	if len(pop) < 250 {
		t.Errorf("%d of 256 buckets used — the hash is not spreading ids", len(pop))
	}
	worst := count
	for _, n := range pop {
		if n < worst {
			worst = n
		}
	}
	// Generous: this is a smoke alarm for a broken hash (the FNV high-bit
	// collapse that cost us the sketch), not a statistical test.
	if worst < 40 {
		t.Errorf("smallest bucket holds %d of an expected ~100 — ids are clumping", worst)
	}
}

// TestPropertyBlocksCarryNoStrings is the stringless invariant.
//
// A block is the thing published to strangers. Titles and search text must
// never ride along in it, whatever they contain — and "whatever they contain"
// is why this is a property test rather than a fixed case: the failure mode is
// a title that happens to survive some encoding step nobody tested.
func TestPropertyBlocksCarryNoStrings(t *testing.T) {
	st := openStore(t, "stringless.sqlite")

	titles := []string{
		"An ordinary video title",
		"Ünïcödé and emoji 🎬🔥",
		`quotes "and" 'apostrophes' and \backslashes\`,
		"<script>alert(1)</script>",
		strings.Repeat("verylongtitle", 40),
		"null\x00byte",
	}
	ctx := "seedaaaaaaa"
	imps := make([]bridge.Impression, 0, len(titles))
	for i, title := range titles {
		imps = append(imps, bridge.Impression{
			PageLoadID: "33333333-3333-4333-8333-333333333333",
			ObservedAt: 1, Surface: "WATCH_NEXT",
			ContextVideoID: &ctx, SlotIndex: i,
			VideoID: "vid" + strconv.Itoa(i) + "aaaaa", Title: title,
		})
	}
	if _, err := st.PutImpressions(imps); err != nil {
		t.Fatal(err)
	}

	blk, err := st.BuildBlock(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if blk == nil || len(blk.Edges) == 0 {
		t.Fatal("no block built; the property below would be vacuous")
	}

	// Every string field of the whole structure, gathered by encoding it.
	encoded, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	hay := string(encoded)
	for _, title := range titles {
		// Short fragments could collide with base64 or an id by chance; a
		// distinctive slice cannot.
		frag := title
		if len(frag) > 12 {
			frag = frag[:12]
		}
		if strings.Contains(hay, frag) {
			t.Errorf("block carries title text %q — blocks are published to strangers", frag)
		}
	}

	// The bucket key is derived from the video id alone, so a title can never
	// influence where a block is filed.
	if BlockPrefix(ctx, DefaultPrefixBits) != BlockPrefix(ctx, DefaultPrefixBits) {
		t.Error("prefix derivation is not stable")
	}
}
