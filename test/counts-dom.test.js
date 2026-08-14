// SPDX-License-Identifier: Apache-2.0
/**
 * WO-108: Counts must keep EXPLORE separate from WATCH_NEXT, and an insert
 * acknowledgement without authoritative stats may only update the total.
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseHTML } from "linkedom";

const here = dirname(fileURLToPath(import.meta.url));
const pageHtml = readFileSync(join(here, "..", "extension", "page", "index.html"), "utf8");
const panelHtml = readFileSync(join(here, "..", "extension", "sidepanel", "index.html"), "utf8");

const stats = {
  total: 12,
  by_surface: { WATCH_NEXT: 3, HOME: 4, EXPLORE: 5 },
  first_observed_at: 1700000000000,
  last_observed_at: 1700000005000,
  channel_unknown: 0,
  channel_known: 12,
};

let runtimeListeners = [];

function settle() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

describe("WO-108 full-page Counts", () => {
  let document;
  let window;

  before(async () => {
    ({ document, window } = parseHTML(pageHtml));
    globalThis.document = document;
    globalThis.window = window;
    globalThis.requestAnimationFrame = (fn) => fn();
    globalThis.browser = {
      runtime: {
        id: "keel-counts-test",
        onMessage: { addListener: (fn) => runtimeListeners.push(fn) },
        connect: () => ({
          postMessage() {},
          disconnect() {},
          onMessage: { addListener() {} },
          onDisconnect: { addListener() {} },
        }),
        sendMessage: async (msg) => {
          if (msg?.type === "GET_STATS") return { ok: true, connected: true, stats };
          if (msg?.type === "WIPE") return { ok: true, wipe: { deleted: stats.total } };
          return { ok: true };
        },
      },
      storage: {
        local: { get: async () => ({ consent: "granted" }), set: async () => {} },
        onChanged: { addListener() {} },
      },
      tabs: { query: async () => [], update: async () => {}, sendMessage: async () => {} },
      windows: { getCurrent: async () => ({ id: 1 }) },
    };
    await import("../extension/page/index.js");
    await settle();
  });

  after(() => {
    delete globalThis.document;
    delete globalThis.window;
    delete globalThis.browser;
    delete globalThis.requestAnimationFrame;
  });

  it("renders EXPLORE from by_surface.EXPLORE", () => {
    assert.equal(document.getElementById("stat-total").textContent, "12");
    assert.equal(document.getElementById("stat-watch").textContent, "3");
    assert.equal(document.getElementById("stat-home").textContent, "4");
    assert.equal(document.getElementById("stat-explore").textContent, "5");
  });
});

describe("WO-108 side-panel Counts", () => {
  let document;
  let window;
  let listeners;
  let realSetInterval;

  before(async () => {
    ({ document, window } = parseHTML(panelHtml));
    globalThis.document = document;
    globalThis.window = window;
    globalThis.requestAnimationFrame = (fn) => fn();
    realSetInterval = globalThis.setInterval;
    globalThis.setInterval = () => 0;
    runtimeListeners = [];
    listeners = runtimeListeners;
    await import("../extension/sidepanel/index.js");
    await settle();
  });

  after(() => {
    if (realSetInterval) globalThis.setInterval = realSetInterval;
    delete globalThis.document;
    delete globalThis.window;
    delete globalThis.browser;
    delete globalThis.requestAnimationFrame;
  });

  it("renders EXPLORE from by_surface.EXPLORE", () => {
    assert.equal(document.getElementById("stat-total").textContent, "12");
    assert.equal(document.getElementById("stat-watch").textContent, "3");
    assert.equal(document.getElementById("stat-home").textContent, "4");
    assert.equal(document.getElementById("stat-explore").textContent, "5");
  });

  it("increments only the total for an unscoped STORE_UPDATED insert", async () => {
    const listener = listeners.at(-1);
    assert.equal(typeof listener, "function");
    listener({ type: "STORE_UPDATED", payload: { inserted: 2 } });
    await settle();
    assert.equal(document.getElementById("stat-total").textContent, "14");
    assert.equal(document.getElementById("stat-watch").textContent, "3");
    assert.equal(document.getElementById("stat-home").textContent, "4");
    assert.equal(document.getElementById("stat-explore").textContent, "5");
  });

  it("clears EXPLORE with the other visible counts on wipe", async () => {
    document.getElementById("btn-wipe").dispatchEvent(new window.Event("click"));
    document.getElementById("btn-wipe-confirm").dispatchEvent(new window.Event("click"));
    await settle();
    assert.equal(document.getElementById("stat-total").textContent, "0");
    assert.equal(document.getElementById("stat-watch").textContent, "0");
    assert.equal(document.getElementById("stat-home").textContent, "0");
    assert.equal(document.getElementById("stat-explore").textContent, "0");
  });
});
