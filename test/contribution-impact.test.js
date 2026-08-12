// SPDX-License-Identifier: Apache-2.0
/**
 * WO-086: the Level-2 contribution-impact panel.
 *
 * Same shape as WO-085/WO-089/WO-090's entitlement tests, because the panel
 * follows the identical rule: below Broad sharing it explains rather than
 * shows a zero, an absent capability disables it without ever sending the
 * RPC, and a level change reaches an already-open panel through the
 * CONTRIBUTION_STATUS broadcast alone — no reload, no second RPC to
 * disagree with the first.
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
  distributed_search: false,
  distributed_search_min_level: 2,
  live: false,
  live_min_level: 2,
};

/** Capability map the fake daemon negotiated. */
let capabilities = {
  core: 1,
  peer_search: 2,
  contribution_runtime: 1,
  contribution_impact: 1,
};

/** GET/RESET_CONTRIBUTION_IMPACT response the fake daemon currently holds. */
let impactResult = {
  requests_answered: 42,
  bytes_served: 1048576,
  since_day: "2026-08-01",
  graph_claims_local: 10,
  graph_claims_peer_cached: 5,
  catalogue_local: 8,
  catalogue_peer_cached: 3,
  buckets_announced: 4,
  shards_announced: 2,
  connected_peers: 6,
  keel_peers: 2,
  available: true,
};

const sentTypes = [];
const listeners = [];

/** Deliver a broadcast the way the SW does, then let the handlers settle. */
async function broadcast(msg) {
  for (const fn of listeners) fn(msg);
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

describe("Level-2 contribution impact panel (WO-086)", () => {
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
          sentTypes.push(msg?.type);
          switch (msg?.type) {
            case "GET_STATUS":
              return { ok: true, connected: true, capabilities: { ...capabilities } };
            case "GET_CONTRIBUTION":
              return { ok: true, daemon: { ...daemonState } };
            case "GET_CONTRIBUTION_IMPACT":
              return { ok: true, daemon: { ...impactResult } };
            case "RESET_CONTRIBUTION_IMPACT":
              impactResult = { ...impactResult, requests_answered: 0, bytes_served: 0 };
              return { ok: true, daemon: { ...impactResult } };
            case "GET_STATS":
              return { ok: true, connected: true, stats: { swarm: { up: false } } };
            case "GET_CONSENT":
              return { ok: true, consent: "granted" };
            case "GET_NETWORK_CONSENT":
              return { ok: true, daemon: { consent_required: false } };
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

  it("explains that impact starts at Broad sharing at Level 1, without sending the RPC", async () => {
    sentTypes.length = 0;
    daemonState = { ...daemonState, effective_level: 1 };
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    const note = document.getElementById("contrib-impact-note");
    const reason = document.getElementById("contrib-impact-reason");
    const body = document.getElementById("contrib-impact");
    const actions = document.getElementById("contrib-impact-actions");

    assert.equal(note.hidden, false, "the reason must be shown, not left blank");
    assert.match(reason.textContent, /broad sharing/i);
    assert.equal(body.children.length, 0, "no numbers — invented or real — at Level 1");
    assert.equal(actions.hidden, true, "no reset button for a panel with nothing in it");
    assert.equal(
      sentTypes.includes("GET_CONTRIBUTION_IMPACT"),
      false,
      "the RPC must never be sent while gated by level"
    );
  });

  it("renders the daemon's numbers at Level 2", async () => {
    sentTypes.length = 0;
    daemonState = { ...daemonState, effective_level: 2 };
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    assert.equal(
      sentTypes.includes("GET_CONTRIBUTION_IMPACT"),
      true,
      "Level 2 must fetch the panel's numbers"
    );
    const note = document.getElementById("contrib-impact-note");
    const body = document.getElementById("contrib-impact");
    const actions = document.getElementById("contrib-impact-actions");
    assert.equal(note.hidden, true, "no gate copy once the panel is live");
    assert.match(
      body.textContent,
      /42broad requests answered/,
      "requests_answered must reach the render"
    );
    assert.equal(actions.hidden, false, "reset is offered once numbers exist");
  });

  it("re-gates on a downgrade broadcast from another browser profile, with no reload", async () => {
    daemonState = { ...daemonState, effective_level: 1 };
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    assert.equal(document.getElementById("contrib-impact-note").hidden, false);
    assert.equal(document.getElementById("contrib-impact").children.length, 0);
  });

  it("resets the counters and re-renders zero", async () => {
    impactResult = { ...impactResult, requests_answered: 42, bytes_served: 1048576 };
    daemonState = { ...daemonState, effective_level: 2 };
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });
    assert.match(document.getElementById("contrib-impact").textContent, /42broad requests answered/);

    sentTypes.length = 0;
    document.getElementById("contrib-impact-reset").click();
    for (let i = 0; i < 8; i++) await Promise.resolve();

    assert.equal(sentTypes.includes("RESET_CONTRIBUTION_IMPACT"), true);
    assert.equal(
      sentTypes.includes("GET_CONTRIBUTION_IMPACT"),
      true,
      "the panel re-fetches after resetting rather than trusting the reset reply alone"
    );
    assert.match(
      document.getElementById("contrib-impact").textContent,
      /0broad requests answered/,
      "the panel must reflect the reset, not stale numbers"
    );
  });

  it("disables the panel and never sends the RPC when the capability is absent", async () => {
    capabilities = { core: 1, peer_search: 2, contribution_runtime: 1 };
    sentTypes.length = 0;
    daemonState = { ...daemonState, effective_level: 2 };
    await broadcast({
      type: "DAEMON_STATUS",
      payload: { connected: true, capabilities: { ...capabilities } },
    });
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });

    const note = document.getElementById("contrib-impact-note");
    const reason = document.getElementById("contrib-impact-reason");
    assert.equal(note.hidden, false);
    assert.match(reason.textContent, /desktop app/i);
    assert.equal(
      sentTypes.includes("GET_CONTRIBUTION_IMPACT"),
      false,
      "an un-negotiated capability must never be asked"
    );

    capabilities = {
      core: 1,
      peer_search: 2,
      contribution_runtime: 1,
      contribution_impact: 1,
    };
  });
});
