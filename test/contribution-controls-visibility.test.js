// SPDX-License-Identifier: Apache-2.0
/**
 * WO-088: contribution-level controls follow the same capability-gating rule
 * WO-081 already applies to peer search — visible and disabled with an
 * actionable reason when the negotiated capability is missing, never removed
 * from the page.
 *
 * Two failure modes, both locked here:
 *
 *   - `applyCapabilityUi()` used to call `contrib.replaceChildren()` when
 *     `contribution_runtime` was absent, so the heading and note stayed but
 *     the four Level 1-4 rows vanished. A control that disappears reads as
 *     "removed", not "update your desktop app".
 *   - Rendering *some* state for an incompatible daemon risks inventing a
 *     level it never reported. The disabled rows here must show no checked
 *     radio and must never trigger GET_CONTRIBUTION/SET_CONTRIBUTION.
 */
import { describe, it, before, after, beforeEach } from "node:test";
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
  level: 2,
  effective_level: 2,
  stored_level: 2,
  startup_level: 2,
  transition: "idle",
  detail: "",
  max_implemented: 2,
  levels_disagree: false,
  distributed_search: true,
  distributed_search_min_level: 2,
};

/** Capability map the fake daemon negotiated. */
let capabilities = { core: 1, peer_search: 2, contribution_runtime: 1 };

/** Every RPC type sent, so a test can assert one was never made. */
let sentTypes = [];

const listeners = [];

async function broadcast(msg) {
  for (const fn of listeners) fn(msg);
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

async function settle() {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

describe("contribution controls stay visible and disabled without the capability (WO-088)", () => {
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
            case "SET_CONTRIBUTION":
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
    await settle();
  });

  after(() => {
    delete globalThis.document;
    delete globalThis.browser;
    delete globalThis.requestAnimationFrame;
  });

  beforeEach(() => {
    sentTypes = [];
  });

  /** Mirrors what the real listener does: DAEMON_STATUS carries capabilities,
   * CONTRIBUTION_STATUS carries daemon state and is what actually triggers
   * refreshContribution() (extension/page/index.js's onMessage listener). */
  async function pushDaemonStatus() {
    await broadcast({
      type: "DAEMON_STATUS",
      payload: { connected: true, capabilities: { ...capabilities } },
    });
  }
  async function pushContributionStatus() {
    await broadcast({ type: "CONTRIBUTION_STATUS", payload: { ...daemonState } });
  }

  function contribInputs() {
    return [...document.querySelectorAll('#contrib-levels input[name="contrib"]')];
  }

  it("keeps all four rows visible, disabled and unchecked when contribution_runtime is absent", async () => {
    capabilities = { core: 1, peer_search: 2 }; // no contribution_runtime
    await pushDaemonStatus();
    await pushContributionStatus();

    const inputs = contribInputs();
    assert.equal(inputs.length, 4, "all four levels must still be listed, not removed");
    for (const input of inputs) {
      assert.equal(input.disabled, true, `level ${input.value} must be disabled`);
      assert.equal(input.checked, false, `level ${input.value} must not be checked — no level is known`);
    }

    const note = document.getElementById("contrib-note");
    assert.match(
      note.textContent,
      /desktop app/i,
      "the note must say this is a desktop-app update problem, not a settings one"
    );

    assert.equal(
      sentTypes.includes("GET_CONTRIBUTION"),
      false,
      "must not call GET_CONTRIBUTION without the negotiated capability"
    );
    assert.equal(
      sentTypes.includes("SET_CONTRIBUTION"),
      false,
      "must not call SET_CONTRIBUTION without the negotiated capability"
    );
  });

  it("does not invent a selected level while the capability is absent", async () => {
    capabilities = { core: 1 };
    daemonState = { ...daemonState, effective_level: 1 };
    await pushDaemonStatus();
    await pushContributionStatus();

    // Even though the last-known daemon state (from a prior negotiation, or
    // just the module's initial default) says level 1, nothing may render as
    // checked: this daemon never reported that to us.
    for (const input of contribInputs()) {
      assert.equal(input.checked, false);
    }
  });

  it("renders the real effective state, unchanged, once contribution_runtime is negotiated", async () => {
    capabilities = { core: 1, peer_search: 2, contribution_runtime: 1 };
    daemonState = {
      ...daemonState,
      effective_level: 2,
      stored_level: 2,
      transition: "idle",
      max_implemented: 2,
    };
    await pushDaemonStatus();
    await pushContributionStatus();

    const inputs = contribInputs();
    assert.equal(inputs.length, 4);
    const checked = inputs.find((i) => i.checked);
    assert.ok(checked, "the negotiated daemon's effective level must be checked");
    assert.equal(checked.value, "2");
    assert.equal(inputs.find((i) => i.value === "1").disabled, false);
    assert.equal(inputs.find((i) => i.value === "2").disabled, false);
    assert.equal(
      inputs.find((i) => i.value === "3").disabled,
      true,
      "levels above max_implemented stay disabled for their own, different reason"
    );

    assert.equal(sentTypes.includes("GET_CONTRIBUTION"), true);

    const note = document.getElementById("contrib-note");
    assert.doesNotMatch(note.textContent, /desktop app/i);
  });

  it("leaves the peer-search control's own visible/disabled behavior unaffected", async () => {
    // contribution_runtime absent, peer_search present and reciprocal-capable:
    // the two controls are gated by different capabilities and must not leak
    // into each other's state.
    capabilities = { core: 1, peer_search: 2 };
    daemonState = { ...daemonState, effective_level: 2, distributed_search: true };
    await pushDaemonStatus();

    const box = document.getElementById("search-network");
    assert.equal(
      box.disabled,
      false,
      "peer search must still work off its own negotiated capability"
    );

    await pushContributionStatus();
    for (const input of contribInputs()) {
      assert.equal(input.disabled, true, "contribution rows stay disabled on their own missing capability");
    }

    capabilities = { core: 1, peer_search: 2, contribution_runtime: 1 };
  });
});
