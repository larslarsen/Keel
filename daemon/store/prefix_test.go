// SPDX-License-Identifier: Apache-2.0
package store

import (
	"fmt"
	"testing"
)

// TestPrefixBucketsAreEven is why the prefix is taken over a hash rather than
// the video id: uneven buckets mean some have one member, and k-anonymity with
// k=1 is no anonymity.
func TestPrefixBucketsAreEven(t *testing.T) {
	const bits, n = 8, 20000
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		counts[BlockPrefix(fmt.Sprintf("video%08d", i), bits)]++
	}
	if len(counts) != 1<<bits {
		t.Fatalf("%d buckets used, want %d", len(counts), 1<<bits)
	}
	want := n / (1 << bits)
	for p, c := range counts {
		// Poisson spread at ~78 per bucket; a 3x band catches real skew
		// without being flaky.
		if c < want/3 || c > want*3 {
			t.Errorf("bucket %s holds %d, want near %d", p, c, want)
		}
	}
}

// TestPrefixIsStableAndWidthTagged — a bucket must mean the same thing to every
// node, and two different widths must never collide.
func TestPrefixIsStableAndWidthTagged(t *testing.T) {
	a := BlockPrefix("dQw4w9WgXcQ", 12)
	if a != BlockPrefix("dQw4w9WgXcQ", 12) {
		t.Error("prefix is not deterministic")
	}
	if a == BlockPrefix("dQw4w9WgXcQ", 16) {
		t.Error("different widths produced the same prefix")
	}
	if bits, ok := PrefixOf(a); !ok || bits != 12 {
		t.Errorf("PrefixOf(%q) = %d, %v", a, bits, ok)
	}
}

// TestPrefixHidesTheTarget is the anonymity property stated as a test: many
// distinct videos must share a bucket, so a request names a crowd.
func TestPrefixHidesTheTarget(t *testing.T) {
	const bits = 8
	target := BlockPrefix("dQw4w9WgXcQ", bits)
	share := 0
	for i := 0; i < 20000; i++ {
		if BlockPrefix(fmt.Sprintf("video%08d", i), bits) == target {
			share++
		}
	}
	if share < 20 {
		t.Errorf("only %d of 20000 videos share the bucket; anonymity set too small", share)
	}
}

// TestLocalPrefixesDoNotNameVideos is the leak this replaced. Advertising block
// keys published the user's watch history; advertising buckets must not.
func TestLocalPrefixesDoNotNameVideos(t *testing.T) {
	st := openStore(t, "a.sqlite")
	seedEdge(t, st, "watchedvid1", "targetaaaa1", 0)
	seedEdge(t, st, "watchedvid2", "targetbbbb1", 0)

	prefixes, err := st.LocalPrefixes(DefaultPrefixBits, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) == 0 {
		t.Fatal("node advertises nothing at all")
	}
	for _, p := range prefixes {
		if p == "watchedvid1" || p == "watchedvid2" {
			t.Fatalf("advertisement names a watched video: %v", prefixes)
		}
		if _, ok := PrefixOf(p); !ok {
			t.Errorf("advertised %q, which is not a prefix", p)
		}
	}
}

// TestBlocksInPrefixReturnsTheWholeBucket — the requester must receive every
// neighbourhood the server holds in the bucket, not just the one wanted. That
// is what leaves the server unable to tell which was the target.
func TestBlocksInPrefixReturnsTheWholeBucket(t *testing.T) {
	st := openStore(t, "a.sqlite")
	// 4 bits keeps buckets coarse enough that seeds collide reliably.
	const bits = 4
	seeds := []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc", "ddddddddddd",
		"eeeeeeeeeee", "fffffffffff", "ggggggggggg", "hhhhhhhhhhh"}
	for i, sd := range seeds {
		seedEdge(t, st, sd, fmt.Sprintf("target%05d", i), i)
	}

	target := BlockPrefix(seeds[0], bits)
	expect := 0
	for _, sd := range seeds {
		if BlockPrefix(sd, bits) == target {
			expect++
		}
	}
	got, err := st.BlocksInPrefix(target, "GB-en", false, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != expect {
		t.Errorf("bucket returned %d blocks, want %d", len(got), expect)
	}
	if expect < 2 {
		t.Skip("fixture did not produce a shared bucket; nothing to assert about cover")
	}
}
