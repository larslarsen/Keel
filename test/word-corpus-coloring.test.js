// SPDX-License-Identifier: Apache-2.0
/**
 * Tokenizer restoration (46a180e): the query-word display in the word-corpus
 * panel (WO-068) has to chop words the same way the daemon's tokenizer now
 * does — fixed, non-overlapping 3-character blocks, not one span per
 * character. And since coloring moved off the daemon-sent token_index onto a
 * local hash of the block's own text (no dictionary needed in the
 * extension), a repeated block has to land on the same color for free.
 *
 * Driven through the real search form submit, like search-entitlement.test.js
 * drives the page's real runtime.onMessage listener — not by calling an
 * internal render function directly.
 */
import { describe, it, before, after } from "node:test";
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
const pageModule = join(here, "..", "extension", "page", "index.js");

describe("word-corpus query coloring chops in 3s, not per character", () => {
  let document;
  let SubmitEvent;

  before(async () => {
    const { document: doc, window } = parseHTML(pageHtml);
    document = doc;
    // linkedom's own Event class, not Node's global one — dispatchEvent only
    // works with an event built by the same DOM implementation it belongs to.
    SubmitEvent = window.Event;
    globalThis.document = document;
    globalThis.requestAnimationFrame = (fn) => fn();

    globalThis.browser = {
      runtime: {
        id: "keel-test",
        onMessage: { addListener: () => {} },
        sendMessage: async (msg) => {
          switch (msg?.type) {
            case "GET_STATUS":
              return { ok: true, connected: true, capabilities: { core: 1 } };
            case "GET_STATS":
              return { ok: true, connected: true, stats: { swarm: { up: false } } };
            case "GET_CONSENT":
              return { ok: true, consent: "granted" };
            case "GET_NETWORK_CONSENT":
              return { ok: true, daemon: { consent_required: false } };
            case "SEARCH":
              return { ok: true, search: { hits: [], total: 0, truncated: false } };
            case "WORD_STATS":
              return {
                ok: true,
                word_stats: {
                  available: true,
                  words: [
                    // "trading" is 7 letters: fixed, non-overlapping 3-char
                    // blocks cut from the front give exactly 3 pieces —
                    // "tra", "din", "g" (the third padded to width 3 on the
                    // daemon side, but the block shown here is the real
                    // word's own remaining character(s)).
                    {
                      word: "trading",
                      pct: 12.5,
                      tokens: [
                        { token_index: 111, estimate: 40, known: true },
                        { token_index: 222, estimate: 10, known: true },
                        { token_index: 333, estimate: 5, known: false },
                      ],
                    },
                    // "banana" repeats its own 3-char block ("ban" only
                    // once here, but pick a word whose blocks repeat): use
                    // a query word engineered so two blocks are identical
                    // — "banban" chops to "ban","ban".
                    {
                      word: "banban",
                      pct: 1,
                      tokens: [
                        { token_index: 999, estimate: 1, known: true },
                        { token_index: 999, estimate: 1, known: true },
                      ],
                    },
                  ],
                },
              };
            default:
              return { ok: true };
          }
        },
      },
    };

    await import(pageModule);
    for (let i = 0; i < 8; i++) await Promise.resolve();
  });

  after(() => {
    delete globalThis.document;
    delete globalThis.browser;
    delete globalThis.requestAnimationFrame;
  });

  it("chops the label into 3-character blocks and colors bars to match", async () => {
    document.getElementById("q").value = "trading";
    document.getElementById("search-form").dispatchEvent(new SubmitEvent("submit", { cancelable: true }));
    for (let i = 0; i < 12; i++) await Promise.resolve();

    // Scope to the "trading" row specifically — the mocked WORD_STATS
    // response renders a "banban" row too, and querying the whole panel
    // would count both rows' blocks together.
    const rows = [...document.querySelectorAll("#word-corpus .word-row")];
    const row = rows.find((r) => r.querySelector(".word-label").textContent.startsWith("tra"));
    assert.ok(row, "expected a rendered row for the \"trading\" word");

    const tradingRow = [...row.querySelectorAll(".word-label .tok-char")];
    assert.equal(
      tradingRow.length,
      3,
      `expected exactly 3 blocks ("tra","din","g") for a 7-letter word, got ${tradingRow
        .map((e) => e.textContent)
        .join(",")}`
    );
    assert.deepEqual(
      tradingRow.map((e) => e.textContent),
      ["tra", "din", "g"]
    );

    // Every block must be the real 3-character (or shorter, for the tail)
    // chunk of the word, never a single character — the regression this
    // guards is colorizedWord falling back to one span per character when
    // the token list is shorter than the word.
    for (const span of tradingRow) {
      assert.ok(
        span.textContent.length <= 3 && span.textContent.length >= 1,
        `block %${span.textContent}% has an unexpected length`
      );
    }

    // The bar segment under each label block must carry the identical
    // colour as the label block above it — both are computed from the same
    // substring text now, not from the (unused) token_index.
    const barSegs = [...row.querySelectorAll(".token-subbars .seg")];
    assert.equal(barSegs.length, 3, "one sub-bar per block, not per token_index value");
    for (let i = 0; i < tradingRow.length; i++) {
      const labelColor = tradingRow[i].style.color;
      const barColor = barSegs[i].style.getPropertyValue("--seg-color");
      assert.ok(labelColor, "label block must have a colour set");
      assert.equal(
        barColor,
        labelColor,
        `bar ${i} colour (${barColor}) must match label block ${i} colour (${labelColor})`
      );
    }
  });

  it("gives a repeated 3-character block the same colour both times", async () => {
    document.getElementById("q").value = "banban";
    document.getElementById("search-form").dispatchEvent(new SubmitEvent("submit", { cancelable: true }));
    for (let i = 0; i < 12; i++) await Promise.resolve();

    const labels = [...document.querySelectorAll("#word-corpus .word-label .tok-char")].filter(
      (el) => el.textContent === "ban"
    );
    assert.equal(labels.length, 2, "banban must chop into two identical \"ban\" blocks");
    assert.equal(
      labels[0].style.color,
      labels[1].style.color,
      "the same three characters must always land on the same colour"
    );
  });
});
