// SPDX-License-Identifier: Apache-2.0
/**
 * Regression test for the live-page thumbnail bug.
 *
 * The live-page rewrite (WO-057) dropped the thumbnail <img> from each stream
 * row in the live table. This test locks the fix: every rendered live row must
 * contain a thumbnail image keyed by the stream's video id, and the thumbnail
 * must be populated from the daemon (not the network).
 *
 * Per WO-062: one test per bug. This is the test for the thumbnail regression.
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

// Isolated module registry so each test starts clean (the page module reads
// globals at import time).
const testModuleUrl = join(here, "..", "extension", "page", "index.js");

const THUMB_DATA_URL = "data:image/png;base64,AAAA";

describe("live page renders a thumbnail per stream row", () => {
  let document;
  let module;

  before(async () => {
    // linkedom document built from the real page markup so every getElementById
    // the module performs at load resolves.
    const { document: doc } = parseHTML(pageHtml);
    document = doc;
    globalThis.document = document;

    // WebExtension shim. lib/browser.js requires globalThis.browser.runtime.id
    // to exist before the page module imports it.
    globalThis.browser = {
      runtime: {
        id: "keel-test",
        sendMessage: async () => ({ ok: true, daemon: { data_url: THUMB_DATA_URL } }),
        onMessage: { addListener() {} },
      },
    };

    module = await import(testModuleUrl);
  });

  after(() => {
    delete globalThis.document;
    delete globalThis.browser;
  });

  it("puts a thumbnail image in every live row", async () => {
    const streams = [
      { v: "dQw4w9WgXcQ", t: "Never Gonna Give You Up", c: "RickAstleyVEVO", p: "yt", s: 1_700_000_000 },
      { v: "abcdefghij", t: "Some TikTok Live", c: "ttuser", p: "tt", s: 1_700_000_100 },
    ];
    module.renderLive({ available: true, streams });

    const rows = document.querySelectorAll("#live-list tr");
    assert.equal(rows.length, streams.length, "one row per stream");

    for (let i = 0; i < streams.length; i++) {
      const img = rows[i].querySelector("img.thumb[data-vid]");
      assert.ok(img, `row ${i} has a thumbnail image`);
      assert.equal(
        decodeURIComponent(img.getAttribute("data-vid")),
        streams[i].v,
        `row ${i} thumbnail is keyed by the stream's video id`
      );
    }

    // fillThumb resolves the THUMBNAIL rpc asynchronously; let it land.
    await new Promise((r) => setTimeout(r, 0));
    for (let i = 0; i < streams.length; i++) {
      const img = rows[i].querySelector("img.thumb[data-vid]");
      assert.equal(
        img.getAttribute("src"),
        THUMB_DATA_URL,
        `row ${i} thumbnail was populated from the daemon`
      );
    }
  });

  it("renders no rows and no thumbnails when there are no streams", () => {
    module.renderLive({ available: true, streams: [] });
    assert.equal(document.querySelectorAll("#live-list tr").length, 0);
  });
});
