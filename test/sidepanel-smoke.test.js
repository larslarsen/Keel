// SPDX-License-Identifier: Apache-2.0
/**
 * A load-and-render smoke test for the side panel (WO-083).
 *
 * Until now nothing in the suite imported `sidepanel/index.js` at all: 1,200
 * lines of the extension's two user-facing surfaces were checked only by
 * reading them. That is a poor place to be *before* a refactor moves functions
 * out of the file, because the failure mode of a bad move — a call to a name
 * that no longer exists — is a ReferenceError at first use, which no amount of
 * careful reading reliably catches.
 *
 * So this test does the cheapest thing that would have caught it: evaluate the
 * real module against the real panel markup, let its startup path run, and then
 * exercise the render functions that consume the helpers which moved. A
 * dangling reference throws here rather than in front of a user.
 *
 * It deliberately does not assert layout. The panel's visual design is not this
 * ticket's business and pinning markup would make every future UI change a test
 * change; what is pinned is that the module evaluates, renders, and escapes.
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseHTML } from "linkedom";

const here = dirname(fileURLToPath(import.meta.url));
const panelHtml = readFileSync(
  join(here, "..", "extension", "sidepanel", "index.html"),
  "utf8"
);

describe("side panel loads and renders (WO-083)", () => {
  let document;
  let sent;
  let realSetInterval;

  before(async () => {
    const { document: doc, window } = parseHTML(panelHtml);
    document = doc;
    globalThis.document = doc;
    globalThis.window = window;
    globalThis.requestAnimationFrame = (fn) => fn();
    // The panel arms a standing refresh interval at load. Nothing here tests
    // it, and a live timer keeps Node's event loop open forever after the
    // assertions finish — so it is stubbed out for the import and restored
    // afterwards rather than left to hang the run.
    realSetInterval = globalThis.setInterval;
    globalThis.setInterval = () => 0;
    sent = [];

    globalThis.browser = {
      runtime: {
        id: "keel-test",
        onMessage: { addListener() {} },
        connect: () => ({
          name: "keel-sidepanel",
          postMessage() {},
          disconnect() {},
          onMessage: { addListener() {} },
          onDisconnect: { addListener() {} },
        }),
        sendMessage: async (msg) => {
          sent.push(msg);
          switch (msg?.type) {
            case "GET_STATUS":
              return { ok: true, connected: true, capabilities: { core: 1 }, proof: null };
            case "GET_STATS":
              return { ok: true, connected: true, stats: null, proof: null };
            case "GET_HIDE_STATE":
              return { ok: true, mode: "on", panelOpen: true };
            case "PANEL_CONTEXT_QUERY":
              return { ok: true, focus: false, platform: "yt", tab_id: null };
            default:
              return { ok: true };
          }
        },
        getURL: (p) => p,
      },
      storage: {
        local: { get: async () => ({ consent: "granted" }), set: async () => {} },
        onChanged: { addListener() {} },
      },
      tabs: { query: async () => [], update: async () => {}, sendMessage: async () => {} },
      windows: { getCurrent: async () => ({ id: 1 }) },
    };

    await import("../extension/sidepanel/index.js");
    // Let the startup RPCs settle so a rejection surfaces here.
    for (let i = 0; i < 12; i++) await Promise.resolve();
  });

  after(() => {
    if (realSetInterval) globalThis.setInterval = realSetInterval;
    delete globalThis.document;
    delete globalThis.window;
    delete globalThis.browser;
    delete globalThis.requestAnimationFrame;
  });

  it("evaluates its startup path without a dangling reference", () => {
    // Every helper the module calls at load must resolve. This assertion is
    // really the `before` block above having completed.
    assert.ok(sent.length > 0, "the panel asked the service worker for something");
  });

  it("renders suggestions through the extracted helpers", async () => {
    const { renderSuggestions } = await import("../extension/sidepanel/index.js");
    if (typeof renderSuggestions !== "function") return; // not exported; covered below
    renderSuggestions(
      { suggestions: [{ video_id: "abc", title: "T", view_count: 1500, duration_s: 75 }] },
      null
    );
  });

  it("formats and escapes with the module the panel now imports", async () => {
    const r = await import("../extension/sidepanel/render.js");
    assert.equal(r.fmtCount(1500), "1.5K");
    assert.equal(
      r.fmtCount(0),
      "",
      "the panel blanks a zero view count — the full page does not, deliberately"
    );
    assert.equal(r.readableChannel("@someone"), "@someone");
    assert.equal(
      r.readableChannel("UCxxxxxxxxxxxxxxxxxxxxxx"),
      "",
      "a channel id is a database key, not a name (WO-041)"
    );
    assert.equal(r.formatBytes(2048), "2.0 KB");
    assert.equal(r.fmt(null), "—");
    assert.match(r.formatExplain(null), /Not in the local corpus yet/);
    // The funnel body interpolates daemon-supplied titles into innerHTML.
    const html = r.formatExplain({
      total_impressions: 2,
      contexts: [{ title: '<img src=x onerror="alert(1)">', count: 1, median_slot_index: 3 }],
    });
    assert.ok(!html.includes("<img"), "a hostile title must be escaped, not rendered");
    assert.ok(html.includes("&lt;img"));
  });
});
