// SPDX-License-Identifier: Apache-2.0
// The token dictionary (WO-067): a fixed, shared way to name any ShardK-
// length token by a small integer, without shipping a wordlist.
//
// Both yield-vector gossip and per-token sketch gossip need every node to
// agree on which bit or slot belongs to which token. A generated or curated
// "common tokens" list would need to be shipped and kept in sync — and no
// real token-frequency corpus is retained in this repo to build one from
// (checked before writing this). Instead, every possible ShardK-length token
// over the tokenizer's own alphabet (TokenDictAlphabet, keyscheme.go) gets an
// index by a plain base-27 positional encoding of its characters. This is a
// pure function: nothing to generate, ship, or keep in sync, and it names
// every token that could ever exist under ShardK, not just ones seen so far.
package store

import "strings"

// TokenDictIndex returns token's position in the dictionary.
//
// token must be exactly ShardK runes, every one of them in
// TokenDictAlphabet — which is exactly the shape tokenize/normalize
// produces, so any real token always has an index. Anything else (wrong
// length, a rune outside the alphabet) returns false rather than a
// best-effort guess, because a wrong index is worse than a refusal: it would
// silently point at some other token's slot.
func TokenDictIndex(token string) (int, bool) {
	if len(token) != ShardK {
		return 0, false
	}
	idx := 0
	for _, r := range token {
		pos := strings.IndexRune(TokenDictAlphabet, r)
		if pos < 0 {
			return 0, false
		}
		idx = idx*len(TokenDictAlphabet) + pos
	}
	return idx, true
}

// TokenFromDictIndex inverts TokenDictIndex — used by the gossip receiving
// side, which only ever sees an index on the wire and needs the token back
// to merge into per-token local state.
func TokenFromDictIndex(idx int) (string, bool) {
	if idx < 0 || idx >= TokenDictSize {
		return "", false
	}
	n := len(TokenDictAlphabet)
	runes := make([]rune, ShardK)
	for i := ShardK - 1; i >= 0; i-- {
		runes[i] = rune(TokenDictAlphabet[idx%n])
		idx /= n
	}
	return string(runes), true
}
