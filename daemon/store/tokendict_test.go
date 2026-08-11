// SPDX-License-Identifier: Apache-2.0
package store

import (
	"math/rand"
	"testing"
)

// TestTokenDictIndexRoundTrip is the property the whole scheme depends on:
// every valid token maps to a distinct index, and that index maps straight
// back to the same token.
func TestTokenDictIndexRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	seen := map[int]string{}
	for i := 0; i < 5000; i++ {
		tok := randomDictToken(r)
		idx, ok := TokenDictIndex(tok)
		if !ok {
			t.Fatalf("TokenDictIndex(%q) rejected a valid token", tok)
		}
		if idx < 0 || idx >= TokenDictSize {
			t.Fatalf("TokenDictIndex(%q) = %d, out of range [0,%d)", tok, idx, TokenDictSize)
		}
		if prior, dup := seen[idx]; dup && prior != tok {
			t.Fatalf("TokenDictIndex collision: %q and %q both map to %d", prior, tok, idx)
		}
		seen[idx] = tok

		back, ok := TokenFromDictIndex(idx)
		if !ok || back != tok {
			t.Fatalf("TokenFromDictIndex(%d) = %q, %v — want %q, true", idx, back, ok, tok)
		}
	}
}

// TestTokenDictIndexRejectsInvalid covers the shapes a real tokenize() call
// never produces but a gossip peer or a bug could hand this function anyway.
func TestTokenDictIndexRejectsInvalid(t *testing.T) {
	for _, tok := range []string{
		"",     // empty
		"ab",   // too short
		"abcd", // too long
		"a1c",  // digit, outside the alphabet
		"ABC",  // uppercase, outside the alphabet (tokenize always lowercases)
		"a-c",  // punctuation
	} {
		if _, ok := TokenDictIndex(tok); ok {
			t.Errorf("TokenDictIndex(%q) accepted an invalid token", tok)
		}
	}
}

// TestTokenFromDictIndexRejectsOutOfRange mirrors the untrusted-input
// discipline used elsewhere (PrefixOf, parseShard): an index arriving over
// gossip from a peer is not guaranteed to be in range.
func TestTokenFromDictIndexRejectsOutOfRange(t *testing.T) {
	for _, idx := range []int{-1, TokenDictSize, TokenDictSize + 1000, -1000} {
		if _, ok := TokenFromDictIndex(idx); ok {
			t.Errorf("TokenFromDictIndex(%d) accepted an out-of-range index", idx)
		}
	}
}

// randomDictToken builds a token guaranteed to be in TokenDictAlphabet^ShardK.
func randomDictToken(r *rand.Rand) string {
	b := make([]byte, ShardK)
	for i := range b {
		b[i] = TokenDictAlphabet[r.Intn(len(TokenDictAlphabet))]
	}
	return string(b)
}
