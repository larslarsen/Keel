// SPDX-License-Identifier: Apache-2.0
/**
 * The three-gram tokenizer, mirrored from the daemon.
 *
 * The daemon never sends token text — a `TokenCoverageWire` carries an opaque
 * `token_index`, an estimate and nothing else, deliberately. So the interface
 * cannot be told which three-gram a bar belongs to; it has to work it out.
 *
 * It can, without any protocol change and without the daemon revealing
 * anything: the words being described are the user's own query, already on
 * their screen, and the tokenizer is deterministic. Running the same function
 * over the same word reproduces the same list, and `token_index` indexes into
 * it.
 *
 * That makes this a copy of daemon/store logic, which is a liability — two
 * implementations of one rule drift, and a drifted mapping would colour the
 * wrong three-gram with total confidence. It is pinned by
 * `test/tokens.test.js` against values taken from the Go implementation, and
 * the constant is carried in the key scheme (WO-060) so a change to either side
 * is a protocol change and cannot happen quietly.
 *
 * Mirrors, exactly:
 *   store.splitWords         lowercase, keep runs of a–z, split on anything else
 *   store.normalize          " " + words.join(" ") + " "
 *   store.tokenize(s, k)     every k-length substring, in order
 *   store.CharTokensForWord  uniqueSorted(tokenize(word, ShardK))
 */

/** ShardK in daemon/store/keyscheme.go. Three-grams. */
export const SHARD_K = 3;

/** store.splitWords: lowercase, runs of a–z only. */
export function splitWords(s) {
  const out = [];
  let cur = "";
  for (const ch of String(s ?? "").toLowerCase()) {
    if (ch >= "a" && ch <= "z") {
      cur += ch;
    } else if (cur) {
      out.push(cur);
      cur = "";
    }
  }
  if (cur) out.push(cur);
  return out;
}

/** store.normalize: space-delimited, with leading and trailing spaces. */
export function normalize(s) {
  const words = splitWords(s);
  return words.length ? ` ${words.join(" ")} ` : "";
}

/** store.tokenize: every k-length substring of the normalized text, in order. */
export function tokenize(text, k = SHARD_K) {
  if (k <= 0) return [];
  const norm = normalize(text);
  if (norm.length < k) return [];
  const out = [];
  for (let i = 0; i + k <= norm.length; i++) out.push(norm.slice(i, i + k));
  return out;
}

/**
 * store.CharTokensForWord: unique, sorted. The sort is what makes
 * `token_index` meaningful — it is a position in THIS list, not in the word.
 */
export function charTokensForWord(word) {
  const seen = new Set();
  const out = [];
  for (const t of tokenize(word, SHARD_K)) {
    if (!seen.has(t)) {
      seen.add(t);
      out.push(t);
    }
  }
  return out.sort();
}

/**
 * Which token index owns each character of a word, for colouring it.
 *
 * Three-grams overlap: every character after the first belongs to up to three
 * of them, so a character cannot be tinted by "its" token without choosing one.
 * The choice here is the token that STARTS at that character, which gives each
 * three-gram exactly one character of the word and reads left to right in the
 * same order as the letters.
 *
 * Returns an array parallel to the word's normalized characters, each entry the
 * index into charTokensForWord(word) or -1 where no token starts.
 *
 * @param {string} word
 * @returns {{ chars: string[], tokenIndex: number[] }}
 */
export function tokenColoringForWord(word) {
  const norm = normalize(word);
  const sorted = charTokensForWord(word);
  const position = new Map(sorted.map((t, i) => [t, i]));
  const chars = [...norm];
  const tokenIndex = chars.map((_, i) => {
    if (i + SHARD_K > chars.length) return -1;
    const tok = norm.slice(i, i + SHARD_K);
    const at = position.get(tok);
    return at === undefined ? -1 : at;
  });
  return { chars, tokenIndex };
}
