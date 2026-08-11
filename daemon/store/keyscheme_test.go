// SPDX-License-Identifier: Apache-2.0
package store

import "testing"

// TestKeySchemeGoldenVectors pins every key derivation to a literal digest.
//
// This is the only mechanism that can catch the failure WO-060 is about. The
// compiler is no help: changing a domain string or a bucket width is valid Go
// and every test that round-trips through one node keeps passing, because that
// node agrees with itself. The damage only shows up between two nodes on
// different builds, as an empty network.
//
// The expected values were captured from this implementation, not derived
// independently — they record what scheme 1 has always produced, which is the
// property that matters: every node on scheme 1 must agree with them.
//
// So the expected values below are deliberately literal — not recomputed from
// the constants, which would make the test agree with any change. If one fails,
// a key-deriving constant moved, and the fix is to decide whether that was
// intended and then bump KeySchemeVersion. Do not edit the expectation to match
// the new output without doing that.
func TestKeySchemeGoldenVectors(t *testing.T) {
	if KeySchemeVersion != 1 {
		t.Fatalf("KeySchemeVersion is %d — the vectors below describe scheme 1. "+
			"Add vectors for the new scheme rather than changing these.", KeySchemeVersion)
	}

	// Bucket width. Everything downstream of this changes if it moves, so it is
	// checked on its own as well as through the vectors.
	if DefaultPrefixBits != 12 {
		t.Errorf("DefaultPrefixBits = %d, want 12 for key scheme 1", DefaultPrefixBits)
	}

	for _, tc := range []struct {
		name, videoID, want string
		bits                int
	}{
		{"block bucket, default width", "dQw4w9WgXcQ", "12:35f0", DefaultPrefixBits},
		{"block bucket, another id", "seedaaaaaaa", "12:1590", DefaultPrefixBits},
		// A narrower width must be a genuine prefix of the wider one, not a
		// separate hash — otherwise a node cannot widen its own view without
		// refetching everything.
		{"block bucket, 8 bits", "dQw4w9WgXcQ", "8:35", 8},
	} {
		if got := BlockPrefix(tc.videoID, tc.bits); got != tc.want {
			t.Errorf("%s: BlockPrefix(%q, %d) = %q, want %q",
				tc.name, tc.videoID, tc.bits, got, tc.want)
		}
	}

	// The catalogue is bucketed by the same width but a different domain, so
	// the same video lands in unrelated buckets in the two systems. That is
	// what stops one being used to probe the other.
	if got, want := CataloguePrefix("dQw4w9WgXcQ", DefaultPrefixBits), BlockPrefix("dQw4w9WgXcQ", DefaultPrefixBits); got == want {
		t.Errorf("catalogue and block prefixes collide at %q — the domain separator is not doing its job", got)
	}

	if PrefixDomain != "keel/prefix/1/" {
		t.Errorf("PrefixDomain = %q — DHT provider records would move; bump KeySchemeVersion", PrefixDomain)
	}
	if blockDomain != "keel/block/1/" {
		t.Errorf("blockDomain = %q — every block bucket moves; bump KeySchemeVersion", blockDomain)
	}
	if catalogueDomain != "keel/catalogue/1/" {
		t.Errorf("catalogueDomain = %q — every catalogue bucket moves; bump KeySchemeVersion", catalogueDomain)
	}
}
