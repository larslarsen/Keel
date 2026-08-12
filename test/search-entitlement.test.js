// SPDX-License-Identifier: Apache-2.0
/**
 * WO-085: the network-search control follows the daemon's contribution level.
 *
 * Two properties are being locked here, and they fail in opposite directions:
 *
 *   - At Level 1 the control must be disabled *and explained*, with a route to
 *     the setting that changes it. A silently dead checkbox and a vanished one
 *     are both dead ends.
 *   - A level change must reach an already-open search view through the
 *     CONTRIBUTION_STATUS broadcast alone (WO-079), with no reload and no
 *     second RPC — that broadcast is the only message a browser profile which
 *     did not make the change receives.
 *
 * Driven through the page's real runtime.onMessage listener rather than by
 * calling an exported helper, because the listener wiring is half of what the
 * ticket asks for.
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

/** Contribution state the fake daemon currently reports. */
let daemonState = {
  level: 1,
  effective_level: 1,
  stored_level: 1,
  startup_level: 1,
  transition: "idle",
  detail: "",
  max_implemented: 2,
  levels_disagree: false,
  distributed_search: false,
  distributed_search_min_level: 2,
};

/** Capability map the fake daemon negotiated. */
let capabilities = { core: 1, peer_search: 2, contribution_runtime: 1 };

const listeners = [];

/** Deliver a broadcast the way the SW does, then let the handlers settle. */
async function broadcast(msg) {
  for (const fn of listeners) fn(msg);
  // The listener kicks off async refreshes; a few turns of the microtask queue
  // is enough for stubs that resolve immediately.
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

describe("network-search control follows the contribution level (WO-085)", () => {
  let document;

  before(async () => {
    const { document: doc } = parseHTML(pageHtml);
    document = doc;
    globalThis.document = document;
    globalThis.requestAnimationFrame = (fn) => fn();

    globalThis.browser = {
      runtime: {
        id: "keel-test",
        onMessage: { addListener: (fn) => listeners.push(fn) },
        sendMessage: async (msg) => {
          switch (msg?.type) {
            case "GET_STATUS":
              return { ok: true, connected: true, capabilities: { ...capabilities } };
            case "GET_CONTRIBUTION":
              return { ok: true, daemon: { ...daemonState } };
            case "GET_STATS":
              return { ok: true, connected: true, stats: null };
            default:
              return { ok: true };
          }
        },
      },
    };

    await import(pageModule);
    // The page seeds the control from GET_STATUS + GET_CONTRIBUTION on load.
    for (let i = 0; i < 8; i++) await Promise.resolve();
  });

  after(() => {
    delete globalThis.document;
    delete globalThis.browser;
    delete globalThis.requestAnimationFrame;
  });

  it("disables the control at Level 1 and says why, with a route to the setting", async () => {
    daemonState = { ...daemonState, effective_level: 1, distributed_search: false };
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    const box = document.getElementById("search-network");
    const note = document.getElementById("search-network-note");
    const reason = document.getElementById("search-network-reason");
    const route = document.getElementById("search-network-route");

    assert.equal(box.disabled, true, "the control must be disabled at Level 1");
    assert.equal(box.checked, false, "a disabled control must not stay checked");
    assert.equal(note.hidden, false, "the reason must be shown, not only in a tooltip");
    assert.match(
      reason.textContent,
      /broad sharing/i,
      "the copy must name the setting that changes this"
    );
    assert.match(
      reason.textContent,
      /local search/i,
      "the copy must say what still works — Level 1 is personal, not offline"
    );
    assert.equal(route.hidden, false, "a disabled control with no route is a dead end");
  });

  it("enables the control the moment the daemon reports Level 2, with no reload", async () => {
    daemonState = { ...daemonState, effective_level: 2, distributed_search: true };
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    const box = document.getElementById("search-network");
    const note = document.getElementById("search-network-note");
    const route = document.getElementById("search-network-route");

    assert.equal(box.disabled, false, "Level 2 must enable the control immediately");
    assert.equal(note.hidden, true, "no explanation is needed once the control works");
    assert.equal(route.hidden, true);
  });

  it("re-disables it on a downgrade broadcast from another browser profile", async () => {
    daemonState = { ...daemonState, effective_level: 1, distributed_search: false };
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    assert.equal(document.getElementById("search-network").disabled, true);
    assert.equal(document.getElementById("search-network-note").hidden, false);
  });

  it("leaves the control alone against a daemon that predates the boundary", async () => {
    // peer_search:1 means the daemon has no level rule and would answer at
    // Level 1. Presenting it as gated would be a UI-only restriction — the
    // disagreement the capability revision exists to prevent.
    capabilities = { core: 1, peer_search: 1, contribution_runtime: 1 };
    daemonState = { ...daemonState, effective_level: 1, distributed_search: false };
    await broadcast({
      type: "DAEMON_STATUS",
      payload: { connected: true, capabilities: { ...capabilities } },
    });
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    const box = document.getElementById("search-network");
    assert.equal(box.disabled, false, "an old daemon has no level rule to enforce");
    assert.equal(
      document.getElementById("search-network-note").hidden,
      true,
      "there is nothing to explain when the control works"
    );
    capabilities = { core: 1, peer_search: 2, contribution_runtime: 1 };
  });

  it("disables the control with the update message when peer_search is absent", async () => {
    capabilities = { core: 1, contribution_runtime: 1 };
    await broadcast({
      type: "DAEMON_STATUS",
      payload: { connected: true, capabilities: { ...capabilities } },
    });

    const box = document.getElementById("search-network");
    const reason = document.getElementById("search-network-reason");
    assert.equal(box.disabled, true);
    assert.match(
      reason.textContent,
      /desktop app/i,
      "an un-negotiated capability is an update problem, not a settings one"
    );
    assert.equal(
      document.getElementById("search-network-route").hidden,
      true,
      "changing the contribution level would not fix a missing capability"
    );
    capabilities = { core: 1, peer_search: 2, contribution_runtime: 1 };
  });
});
