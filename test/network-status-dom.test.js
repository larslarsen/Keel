// SPDX-License-Identifier: Apache-2.0
/**
 * WO-093: the Network row must answer the question, not print a zero.
 *
 * `keel_peers: 0` is the same number in three unrelated situations — this node
 * never published so nobody can find it, it published and nobody else is
 * online, or it is not permitted to advertise at all. A live QA session spent a
 * day in the first while reading it as the second. These tests hold the row to
 * saying which one it is in every state, including the state where the desktop
 * app is too old to know.
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseHTML } from "linkedom";

const here = dirname(fileURLToPath(import.meta.url));
const pageHtml = readFileSync(join(here, "..", "extension", "page", "index.html"), "utf8");
const pageModule = join(here, "..", "extension", "page", "index.js");

/** The swarm status the fake daemon currently reports. */
let swarm = { up: true, peers: 41, keel_peers: 0, id: "12D3KooWabcdef01234567" };
/** The capability map the fake daemon negotiated. */
let capabilities = { core: 1, network_consent: 1, network_status: 1, contribution_runtime: 1 };

const sentTypes = [];
const listeners = [];
/** setInterval callbacks the page registered, so a tick can be forced. */
const intervals = [];
let visibility = "visible";

async function settle() {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

/**
 * Hand the page a capability map the way a reconnect does.
 *
 * This is the real route by which the page learns it is now talking to a
 * different daemon build — GET_STATS carries no capability map — so the
 * older-daemon case is exercised through it rather than by reloading.
 */
async function reconnectWith(caps) {
  capabilities = caps;
  for (const fn of listeners) {
    fn({ type: "DAEMON_STATUS", payload: { connected: true, capabilities: { ...caps } } });
  }
  await settle();
}

/** Re-render the Network row with `network` as the daemon's health payload. */
async function render(network, extra = {}) {
  swarm = { up: true, peers: 41, id: "12D3KooWabcdef01234567", ...extra, network };
  document.getElementById("tab-config").click();
  await settle();
  return document.getElementById("swarm-row").textContent;
}

describe("Network row states (WO-093)", () => {
  let document;

  before(async () => {
    const { document: doc } = parseHTML(pageHtml);
    document = doc;
    globalThis.document = document;
    globalThis.requestAnimationFrame = (fn) => fn();
    Object.defineProperty(document, "visibilityState", { get: () => visibility });

    // Captured rather than scheduled: a real interval would keep the test
    // process alive, and the point here is to fire the tick deliberately.
    globalThis.setInterval = (fn, ms) => {
      intervals.push({ fn, ms });
      return intervals.length;
    };

    globalThis.browser = {
      runtime: {
        id: "keel-test",
        onMessage: { addListener: (fn) => listeners.push(fn) },
        sendMessage: async (msg) => {
          sentTypes.push(msg?.type);
          switch (msg?.type) {
            case "GET_STATUS":
              return { ok: true, connected: true, capabilities: { ...capabilities } };
            case "GET_STATS":
              return {
                ok: true,
                connected: true,
                capabilities: { ...capabilities },
                stats: { swarm: { ...swarm }, total: 0, by_surface: {} },
              };
            case "GET_CONSENT":
              return { ok: true, consent: "granted" };
            case "GET_NETWORK_CONSENT":
              return { ok: true, daemon: { consent_required: false } };
            case "GET_CONTRIBUTION":
              return {
                ok: true,
                daemon: { level: 2, effective_level: 2, stored_level: 2, transition: "idle" },
              };
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

  it("says a Level-1 zero is the setting, not a fault or a quiet network", async () => {
    const text = await render({
      state: "off",
      reason: "level_policy",
      announce_permitted: false,
      published: false,
      consecutive_failures: 0,
      lookup_completed: false,
      keel_peers: 0,
    });
    assert.match(text, /does not advertise itself/i);
    assert.match(text, /zero connected nodes is what to expect/i);
    // The permitted half of Level 1 is still true and still said.
    assert.match(text, /downloading shared data and pre-walking still work/i);
    assert.doesNotMatch(text, /^0\b/, "a bare zero must never be the headline");
  });

  it("distinguishes joining from a quiet network", async () => {
    const text = await render({
      state: "starting",
      reason: "none",
      announce_permitted: true,
      published: false,
      consecutive_failures: 0,
      lookup_completed: false,
      keel_peers: 0,
    });
    assert.match(text, /joining the peer network/i);
    assert.match(text, /nothing can find it/i);
    assert.doesNotMatch(text, /no other compatible Keel node has been found/i);
  });

  it("states the bounded attempt count and the next retry while retrying", async () => {
    const text = await render({
      state: "retrying",
      reason: "routing_unavailable",
      announce_permitted: true,
      published: false,
      consecutive_failures: 2,
      next_retry_at: Date.now() + 2 * 60_000,
      lookup_completed: false,
      keel_peers: 0,
    });
    assert.match(text, /2 attempts/i);
    assert.match(text, /reach the peer network/i);
    assert.match(text, /next attempt in about 2 minutes/i);
  });

  it("states the fault, its reason and the automatic retry — never a zero", async () => {
    const text = await render({
      state: "fault",
      reason: "publish_failed",
      announce_permitted: true,
      published: false,
      consecutive_failures: 3,
      next_retry_at: Date.now() + 4 * 60_000,
      lookup_completed: false,
      keel_peers: 0,
    });
    assert.match(text, /could not make this node discoverable/i);
    assert.match(text, /publishing this node's address to the network failed/i);
    assert.match(text, /keel keeps trying/i);
    assert.match(text, /next attempt in about 4 minutes/i);
    assert.doesNotMatch(text, /^No other/i, "a fault must not read as an empty network");
  });

  it("separates 'discoverable and looking' from 'looked and found nobody'", async () => {
    const looking = await render({
      state: "ready",
      reason: "none",
      announce_permitted: true,
      published: true,
      consecutive_failures: 0,
      lookup_completed: false,
      keel_peers: 0,
    });
    assert.match(looking, /discoverable, and is looking for other Keel nodes/i);

    const quiet = await render({
      state: "ready",
      reason: "none",
      announce_permitted: true,
      published: true,
      consecutive_failures: 0,
      lookup_completed: true,
      last_lookup_at: Date.now(),
      keel_peers: 0,
    });
    assert.match(quiet, /connected to the Keel network and discoverable/i);
    assert.match(quiet, /no other compatible Keel node has been found yet/i);
  });

  it("counts nodes when there are some", async () => {
    const text = await render({
      state: "ready",
      reason: "none",
      announce_permitted: true,
      published: true,
      consecutive_failures: 0,
      lookup_completed: true,
      keel_peers: 3,
    });
    assert.match(text, /connected to 3 other compatible Keel nodes/i);
  });

  it("renders retained Live records neutrally when ready but no nodes are connected", async () => {
    const text = await render(
      {
        state: "ready",
        reason: "none",
        announce_permitted: true,
        published: true,
        consecutive_failures: 0,
        lookup_completed: true,
        keel_peers: 0,
      },
      { live_indexed: 4 },
    );
    assert.match(text, /4 livestreams indexed\./);
    assert.doesNotMatch(text, /all from your own browsing|known\./i);
  });

  it("keeps Live wording neutral while health is off or faulted", async () => {
    const off = await render(
      {
        state: "off",
        reason: "level_policy",
        announce_permitted: false,
        published: false,
        consecutive_failures: 0,
        lookup_completed: false,
        keel_peers: 0,
      },
      { live_indexed: 2 },
    );
    const fault = await render(
      {
        state: "fault",
        reason: "publish_failed",
        announce_permitted: true,
        published: false,
        consecutive_failures: 3,
        lookup_completed: false,
        keel_peers: 0,
      },
      { live_indexed: 2 },
    );
    for (const text of [off, fault]) {
      assert.match(text, /2 livestreams indexed\./);
      assert.doesNotMatch(text, /all from your own browsing|known\./i);
    }
  });

  it("does not turn missing or malformed network health into Live provenance", async () => {
    const missing = await render(undefined, { live_indexed: 1 });
    const malformed = await render("not-an-object", { live_indexed: 1 });
    for (const text of [missing, malformed]) {
      assert.match(text, /1 livestream indexed\./);
      assert.match(text, /desktop app did not report peer-network health/i);
      assert.doesNotMatch(text, /all from your own browsing|connected to 0 other/i);
    }
  });

  it("asks for a desktop app update rather than falling back to the ambiguous zero", async () => {
    await reconnectWith({ core: 1, network_consent: 1, contribution_runtime: 1 });
    const text = await render(undefined, { keel_peers: 0 });
    await reconnectWith({
      core: 1,
      network_consent: 1,
      network_status: 1,
      contribution_runtime: 1,
    });

    assert.match(text, /peer-network health needs a desktop app update/i);
    assert.doesNotMatch(
      text,
      /connected to \d+ other/i,
      "an un-negotiated daemon's count must not be presented as health"
    );
    assert.doesNotMatch(text, /no other compatible Keel node/i);
    // Diagnosis survives: the plumbing figure is still there, still labelled.
    assert.match(text, /41 DHT connections \(network plumbing\)/);
  });

  it("never calls a node a user, and labels the raw DHT figure as plumbing", async () => {
    const text = await render({
      state: "ready",
      reason: "none",
      announce_permitted: true,
      published: true,
      consecutive_failures: 0,
      lookup_completed: true,
      keel_peers: 2,
    });
    assert.doesNotMatch(text, /\buser(s)?\b/i, "this counts installs, not people");
    assert.match(text, /\(network plumbing\)/);
  });

  it("refreshes on a visible timer and stays silent while hidden", async () => {
    await render({
      state: "ready",
      reason: "none",
      announce_permitted: true,
      published: true,
      consecutive_failures: 0,
      lookup_completed: true,
      keel_peers: 1,
    });
    const tick = intervals.find((i) => i.ms >= 10_000 && i.ms <= 15_000);
    assert.ok(tick, `no 10–15s config timer registered: ${intervals.map((i) => i.ms)}`);

    sentTypes.length = 0;
    visibility = "visible";
    tick.fn();
    await settle();
    assert.ok(sentTypes.includes("GET_STATS"), "a visible Config tab must re-read status");

    sentTypes.length = 0;
    visibility = "hidden";
    tick.fn();
    await settle();
    assert.equal(
      sentTypes.includes("GET_STATS"),
      false,
      "a hidden tab must not poll the daemon"
    );
    visibility = "visible";
  });

  it("keeps the peer count out of Your impact, so one section owns it", async () => {
    await render({
      state: "ready",
      reason: "none",
      announce_permitted: true,
      published: true,
      consecutive_failures: 0,
      lookup_completed: true,
      keel_peers: 5,
    });
    const impact = document.getElementById("contrib-impact");
    assert.doesNotMatch(
      impact.textContent || "",
      /Keel peers connected right now/i,
      "the count has one owner: the Network section above"
    );
  });
});
