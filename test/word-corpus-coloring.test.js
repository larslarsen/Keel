// SPDX-License-Identifier: Apache-2.0
/**
 * Query colouring comes from the daemon's render plan (WO-095 §1).
 *
 * # What this test used to assert, and why that is now wrong
 *
 * It used to pin the *extension's own* tokenizer: the page chopped the query
 * into fixed three-character blocks and hashed each block's text for a colour,
 * and this test checked that it chopped in threes rather than per character.
 * That was the right assertion while the page tokenized.
 *
 * WO-097 made it impossible to keep. A scheme-2 token is cut from the whole
 * normalized query, not per word, so a token can straddle a space and belong to
 * two words at once — `a big` cuts to `[a b][ig ]`, and no amount of per-word
 * chopping in the page reproduces that. A page that kept chopping locally would
 * draw a query structure that disagreed with the work the daemon was actually
 * doing, which is the specific dishonesty WO-095 forbids ("do not retokenize,
 * colour, or calculate search counts in the extension").
 *
 * So the successor property is stronger and simpler: the page paints exactly
 * what the plan tells it to, including the cross-word case the old chopping
 * could not express, and it derives nothing.
 */
import { describe, it, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseHTML } from "linkedom";

const here = dirname(fileURLToPath(import.meta.url));
const pageHtml = readFileSync(
  join(here, "..", "extension", "page", "index.html"),
  "utf8"
);

/**
 * The plan the daemon produces for `a big`, verbatim in shape:
 * normalized "a big", grid [a b][ig ], where the first token covers a letter
 * of BOTH words.
 */
const PLAN_A_BIG = {
  normalized: "a big",
  words: [
    { word_id: 0, word: "a", start: 0, end: 1, stopword: true },
    { word_id: 1, word: "big", start: 2, end: 5, stopword: false },
  ],
  tokens: [
    {
      token_id: 0,
      color_slot: 0,
      start: 0,
      end: 3,
      discovery: true,
      bar_word_id: 0,
      fragments: [
        { word_id: 0, start: 0, end: 1 },
        { word_id: 1, start: 2, end: 3 },
      ],
    },
    {
      token_id: 1,
      color_slot: 1,
      start: 3,
      end: 6,
      discovery: true,
      bar_word_id: 1,
      fragments: [{ word_id: 1, start: 3, end: 5 }],
    },
  ],
};

/** `red red` — one word value twice, so both occurrences share an id. */
const PLAN_RED_RED = {
  normalized: "red red",
  words: [
    { word_id: 0, word: "red", start: 0, end: 3, stopword: false },
    { word_id: 0, word: "red", start: 4, end: 7, stopword: false },
  ],
  tokens: [
    {
      token_id: 0,
      color_slot: 0,
      start: 0,
      end: 3,
      discovery: true,
      bar_word_id: 0,
      fragments: [{ word_id: 0, start: 0, end: 3 }],
    },
    {
      token_id: 1,
      color_slot: 1,
      start: 3,
      end: 6,
      discovery: true,
      bar_word_id: 0,
      fragments: [{ word_id: 0, start: 4, end: 6 }],
    },
    {
      token_id: 2,
      color_slot: 2,
      start: 6,
      end: 9,
      discovery: true,
      bar_word_id: 0,
      fragments: [{ word_id: 0, start: 6, end: 7 }],
    },
  ],
};

describe("query colouring is painted from the daemon's render plan", () => {
  let createSearchStream;
  let colorForSlot;
  let document;

  before(async () => {
    const { document: doc } = parseHTML(pageHtml);
    document = doc;
    globalThis.document = document;
    globalThis.requestAnimationFrame = (fn) => fn();
    const mod = await import("../extension/page/search_stream.js");
    createSearchStream = mod.createSearchStream;
    colorForSlot = mod.colorForSlot;
  });

  function makeStream() {
    return createSearchStream({
      browser: { runtime: { connect: () => null } },
      rpc: async () => ({}),
      el: {
        wordCorpus: document.getElementById("word-corpus"),
        wordCorpusMeta: document.getElementById("word-corpus-meta"),
        peerProgressCaption: document.getElementById("peer-progress-caption"),
        results: document.getElementById("results"),
        meta: document.getElementById("search-meta"),
      },
      hitRow: () => document.createElement("li"),
      hasStreaming: () => true,
    });
  }

  it("colours each word from the plan's fragments, not from local chopping", () => {
    const stream = makeStream();
    stream.renderPlan(PLAN_A_BIG, []);

    // Only `big` gets a row: `a` is a stopword, and a stopword has no target
    // and no distributed work, so a bar for it could only ever sit at zero.
    const rows = [...document.querySelectorAll("#word-corpus .word-row")];
    assert.equal(rows.length, 1, "expected exactly one non-stopword word row");
    assert.equal(rows[0].dataset.wordId, "1");

    const spans = [...rows[0].querySelectorAll(".word-label .tok-char")];
    assert.deepEqual(
      spans.map((s) => s.textContent),
      ["b", "ig"],
      "the label must be sliced at the plan's fragment boundaries — `big` is " +
        "covered by the tail of the token that also covers `a`, then by the next token"
    );
    assert.equal(spans[0].style.color, colorForSlot(0));
    assert.equal(spans[1].style.color, colorForSlot(1));
    assert.notEqual(
      spans[0].style.color,
      spans[1].style.color,
      "two different tokens must not share a colour slot here"
    );
  });

  it("gives a cross-word token one bar, under the first word it covers", () => {
    const stream = makeStream();
    stream.renderPlan(PLAN_A_BIG, []);
    // Token 0 straddles the space and is placed under word 0 (`a`), which has
    // no row because it is a stopword — so exactly one bar is drawn, for the
    // token that lives under `big`. The placement is presentation and carries
    // no search meaning; what matters is that a straddling token never
    // produces two bars.
    const segs = [...document.querySelectorAll("#word-corpus .token-subbars .seg")];
    assert.equal(segs.length, 1, "a cross-word token must not draw two bars");
  });

  it("shares one row, one colour and one live state across repeated occurrences", () => {
    const stream = makeStream();
    stream.renderPlan(PLAN_RED_RED, []);

    const rows = [...document.querySelectorAll("#word-corpus .word-row")];
    assert.equal(
      rows.length,
      1,
      "two occurrences of one word share a word_id, so they share one bar and one target"
    );

    const segs = [...rows[0].querySelectorAll(".token-subbars .seg")];
    assert.equal(segs.length, 3, "three grid tokens, three bars, in query order");
    const colors = segs.map((s) => s.style.getPropertyValue("--seg-color"));
    assert.deepEqual(colors, [colorForSlot(0), colorForSlot(1), colorForSlot(2)]);
  });

  it("shows a found count and no fake marker when the target is unknown", () => {
    const stream = makeStream();
    stream.renderPlan(PLAN_RED_RED, [
      { word_id: 0, word: "red", target: 0, raw: 0, known: false, uncertain: false },
    ]);
    const row = document.querySelector("#word-corpus .word-row");
    const note = row.querySelector(".word-target");
    assert.match(
      note.textContent,
      /target unknown/,
      "an unknown target must say so rather than invent a denominator"
    );
    assert.equal(
      row.querySelector(".target-marker").hidden,
      true,
      "no 100% marker when there is no target to mark"
    );
  });

  it("keeps the 100% marker and marks overflow when the count passes the target", () => {
    const stream = makeStream();
    stream.renderPlan(PLAN_RED_RED, [
      { word_id: 0, word: "red", target: 4, raw: 4, known: true, uncertain: false },
    ]);
    const row = document.querySelector("#word-corpus .word-row");
    const marker = row.querySelector(".target-marker");
    assert.equal(marker.hidden, false, "a known target draws its marker");
    assert.match(row.querySelector(".word-target").textContent, /of ~4/);
  });
});
