// SPDX-License-Identifier: Apache-2.0
/**
 * Regression tests for the native-messaging bridge (WO-004 §6.1, §6.2 and
 * WO-008's alarm-based reconnect).
 *
 * Source bugs:
 *   §6.1 — post() enforced MAX_HOST_MSG (1 MiB) on BROWSER→HOST messages, so a
 *     large-but-legit payload (an IMPRESSIONS batch with many cards) was
 *     silently dropped. The browser→host limit is 64 MiB.
 *   §6.2 — rejectPending() left every pending request's timeout timer running
 *     after a disconnect, leaking timers for the port's lifetime.
 *   WO-008 — reconnect must survive service-worker eviction: backoff is
 *     scheduled via chrome.alarms (name "keel-native-reconnect"), not a bare
 *     setTimeout that eviction would cancel.
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";
import {
  HOST_NAME,
  MAX_BROWSER_TO_HOST,
  envelope,
} from "../extension/lib/protocol.js";

let createNativeBridge;

// Per-test mutable state. installStub() closes over `state`, so rebinding it
// in each test gives the next test a clean slate without re-importing.
let state;

function makePort() {
  const port = {
    _onMessage: null,
    _onDisconnect: null,
    _sent: [],
    onMessage: { addListener: (fn) => (port._onMessage = fn) },
    onDisconnect: { addListener: (fn) => (port._onDisconnect = fn) },
    postMessage: (msg) => port._sent.push(msg),
    disconnect() {},
  };
  return port;
}

function freshState() {
  return {
    connections: [],
    ports: [],
    alarmsCreated: [],
    alarmsCleared: [],
    alarmListener: null,
  };
}

function installStub() {
  globalThis.browser = {
    runtime: {
      id: "test-extension-id",
      lastError: undefined,
      onMessage: { addListener() {} },
      onInstalled: { addListener() {} },
      // connectNative is called synchronously by native.js (no await), so the
      // stub must return the port object directly, not a Promise.
      connectNative(name) {
        state.connections.push(name);
        const p = makePort();
        state.ports.push(p);
        return p;
      },
    },
    alarms: {
      create: async (name, opts) => state.alarmsCreated.push({ name, opts }),
      clear: async (name) => state.alarmsCleared.push(name),
      onAlarm: { addListener: (fn) => (state.alarmListener = fn) },
    },
  };
}

before(async () => {
  state = freshState();
  installStub();
  const native = await import("../extension/lib/native.js");
  createNativeBridge = native.createNativeBridge;
});

after(() => {
  delete globalThis.browser;
});

describe("native bridge (WO-004 §6.1, §6.2, WO-008)", () => {
  it("accepts browser→host messages far beyond the old 1 MiB cap", () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    assert.equal(state.ports.length, 1, "connectNative was not called");
    assert.equal(state.ports[0]._sent[0].type, "HELLO");

    // A ~2 MiB payload. The old code enforced the 1 MiB HOST→browser cap
    // (MAX_HOST_MSG) on outbound browser→host messages and dropped this.
    const p2 = "x".repeat(2 * 1024 * 1024);
    assert.equal(
      bridge.post(envelope("IMPRESSIONS", { blob: p2 })),
      true,
      "post() rejected a ~2 MiB browser→host message (WO-004 §6.1 regression)"
    );
    assert.equal(state.ports[0]._sent.length, 2, "the 2 MiB message was not sent");
  });

  it("still drops browser→host messages over the real 64 MiB limit", () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    // Envelope overhead (~100 bytes) pushes this just over the cap.
    const huge = "x".repeat(MAX_BROWSER_TO_HOST - 32);
    assert.equal(bridge.post(envelope("IMPRESSIONS", { blob: huge })), false);
  });

  it("clears the pending request's timeout timer when the port disconnects", async () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    state.ports[0]._onMessage(
      envelope("HELLO_ACK", {
        ok: true,
        compatible: true,
        api: 1,
        capabilities: { core: 1, queue: 1 },
      })
    ); // hello gate opens
    assert.equal(bridge.helloOk, true);
    assert.equal(bridge.hasCapability("queue"), true);

    const originalSetTimeout = globalThis.setTimeout;
    const originalClearTimeout = globalThis.clearTimeout;
    const scheduled = [];
    const cleared = [];
    globalThis.setTimeout = (fn, ms, ...rest) => {
      const h = originalSetTimeout(fn, ms, ...rest);
      scheduled.push(h);
      return h;
    };
    globalThis.clearTimeout = (h) => {
      cleared.push(h);
      return originalClearTimeout(h);
    };

    try {
      const p = bridge.request("STATS", {}, 60000);
      assert.equal(
        scheduled.length,
        1,
        "the request must register exactly one timeout timer"
      );
      state.ports[0]._onDisconnect();
      await assert.rejects(p, /disconnected/);
      assert.ok(
        cleared.includes(scheduled[0]),
        "pending request timer survived the disconnect (WO-004 §6.2 regression)"
      );
    } finally {
      globalThis.setTimeout = originalSetTimeout;
      globalThis.clearTimeout = originalClearTimeout;
    }
  });

  it("schedules reconnect via chrome.alarms, and the alarm reconnects", () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    assert.deepEqual(state.connections, [HOST_NAME]);

    state.ports[0]._onDisconnect(); // the host died
    assert.equal(
      state.alarmsCreated.length,
      1,
      "a disconnect must schedule a reconnect alarm, not a bare setTimeout (WO-008)"
    );
    assert.equal(state.alarmsCreated[0].name, "keel-native-reconnect");
    assert.equal(state.alarmsCreated[0].opts.delayInMinutes, 0.5);

    // The alarm fires later — possibly after the SW was evicted. It must open
    // a fresh port.
    assert.ok(state.alarmListener, "bridge must subscribe to onAlarm");
    state.alarmListener({ name: "keel-native-reconnect" });
    assert.deepEqual(state.connections, [HOST_NAME, HOST_NAME]);
    assert.equal(state.ports.length, 2, "alarm fire must open a fresh port");
    assert.equal(state.ports[1]._sent[0].type, "HELLO");

    // A successful handshake on the new port clears the outstanding alarm.
    const clearsBeforeAck = state.alarmsCleared.length;
    state.ports[1]._onMessage(
      envelope("HELLO_ACK", {
        ok: true,
        compatible: true,
        api: 1,
        capabilities: { core: 1 },
      })
    );
    assert.ok(
      state.alarmsCleared.length > clearsBeforeAck,
      "HELLO_ACK must clear the reconnect alarm"
    );
    assert.equal(bridge.connected, true);

    // An alarm for anything else must not reconnect.
    state.alarmListener({ name: "some-other-alarm" });
    assert.equal(state.connections.length, 2, "unrelated alarms must be ignored");
  });

  it("WO-081: HELLO carries api/required/optional capability maps", () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    const hello = state.ports[0]._sent[0];
    assert.equal(hello.type, "HELLO");
    assert.equal(hello.payload.client_version, "0.1.0");
    assert.deepEqual(hello.payload.api, { min: 1, max: 1 });
    assert.equal(hello.payload.required.core, 1);
    assert.equal(
      hello.payload.required.network_consent,
      1,
      "an old daemon without the consent gate must fail HELLO closed (WO-089)"
    );
    assert.equal(hello.payload.optional.contribution_runtime, 1);
  });

  it("WO-081: incompatible HELLO_ACK does not set connected/ready", () => {
    state = freshState();
    const statuses = [];
    const bridge = createNativeBridge({
      onStatus(ok, detail, meta) {
        statuses.push({ ok, detail, meta });
      },
      onMessage() {},
    });
    bridge.connect();
    state.ports[0]._onMessage(
      envelope("HELLO_ACK", {
        ok: false,
        compatible: false,
        code: "api_non_overlap",
        reason: "desktop app update required: no overlapping API revision",
      })
    );
    assert.equal(bridge.helloOk, false);
    assert.equal(bridge.connected, false);
    assert.equal(statuses.at(-1).ok, false);
    assert.match(statuses.at(-1).detail, /desktop app update required/i);
    assert.equal(statuses.at(-1).meta?.incompatible, true);
    // Must keep trying after an incompatible host (user may upgrade).
    assert.ok(state.alarmsCreated.length >= 1);
  });

  it("WO-081: compatible partial map exposes only negotiated caps", () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    state.ports[0]._onMessage(
      envelope("HELLO_ACK", {
        ok: true,
        compatible: true,
        api: 1,
        capabilities: { core: 1, queue: 1 },
      })
    );
    assert.equal(bridge.hasCapability("core"), true);
    assert.equal(bridge.hasCapability("queue"), true);
    assert.equal(bridge.hasCapability("peer_search"), false);
    assert.equal(bridge.hasCapability("contribution_runtime"), false);
  });

  /**
   * WO-083 acceptance: several requests may be in flight at once, and each
   * must resolve with its OWN reply.
   *
   * The pending map is keyed by correlation id precisely so replies can arrive
   * out of order — the daemon answers PEER_SEARCH and SUGGEST on their own
   * goroutines, so it does. A bridge that resolved by arrival order would
   * hand the panel another RPC's payload, which is silent and wrong rather
   * than a visible failure.
   */
  it("correlates concurrent requests by id, including out-of-order replies", async () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    const port = state.ports[0];
    port._onMessage(envelope("HELLO_ACK", { ok: true, compatible: true, api: 1 }));

    const first = bridge.request("SUGGEST", { limit: 1 });
    const second = bridge.request("SEARCH", { query: "a" });
    const third = bridge.request("STATS", {});

    const sent = port._sent.filter((m) => m.type !== "HELLO");
    assert.equal(sent.length, 3, "each request must go out on its own envelope");
    const ids = new Set(sent.map((m) => m.id));
    assert.equal(ids.size, 3, "correlation ids must be unique per request");

    // Answer last-to-first: arrival order carries no meaning.
    port._onMessage({ ...envelope("STATS_RESULT", { n: 3 }, sent[2].id) });
    port._onMessage({ ...envelope("SEARCH_RESULT", { n: 2 }, sent[1].id) });
    port._onMessage({ ...envelope("SUGGEST_RESULT", { n: 1 }, sent[0].id) });

    assert.equal((await first).payload.n, 1);
    assert.equal((await second).payload.n, 2);
    assert.equal((await third).payload.n, 3);
  });

  /**
   * A disconnect must reject every request still waiting, not leave the caller
   * hanging until its own timeout. The panel's surfaces await these directly.
   */
  it("rejects every in-flight request when the port disconnects", async () => {
    state = freshState();
    const bridge = createNativeBridge({ onStatus() {}, onMessage() {} });
    bridge.connect();
    const port = state.ports[0];
    port._onMessage(envelope("HELLO_ACK", { ok: true, compatible: true, api: 1 }));

    const pending = [
      bridge.request("SUGGEST", {}),
      bridge.request("SEARCH", { query: "a" }),
    ];
    port._onDisconnect();

    for (const p of pending) {
      await assert.rejects(() => p, /disconnected/);
    }
    // And a request made while down fails fast rather than queueing forever.
    await assert.rejects(() => bridge.request("STATS", {}), /not connected/);
  });
});

// WO-091 live QA: the daemon sent a clear refusal and all that reached the
// screen was "[object Object]". The extension error list on chrome://extensions
// stringifies whatever it is handed, and INSTALL.md sends people there to
// report problems, so the payload has to be text before it is logged.
describe("describeError", () => {
  let describeError;
  before(async () => {
    ({ describeError } = await import("../extension/lib/native.js"));
  });

  it("renders code and message as one readable line", () => {
    assert.equal(
      describeError({ code: "invalid_capability", message: "desktop app update required" }),
      "invalid_capability: desktop app update required",
    );
  });

  it("falls back through message, code and JSON", () => {
    assert.equal(describeError({ message: "no code here" }), "no code here");
    assert.equal(describeError({ code: "bare_code" }), "bare_code");
    assert.equal(describeError({ odd: 1 }), '{"odd":1}');
    assert.equal(describeError(null), "(no payload)");
    assert.equal(describeError("already text"), "already text");
  });

  it("never emits [object Object]", () => {
    for (const v of [{ code: "x" }, { message: "y" }, { odd: 1 }, null, undefined, 7]) {
      assert.ok(!describeError(v).includes("[object Object]"), `leaked for ${JSON.stringify(v)}`);
    }
  });
});
