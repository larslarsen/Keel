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
    state.ports[0]._onMessage(envelope("HELLO_ACK", {})); // hello gate opens
    assert.equal(bridge.helloOk, true);

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
    state.ports[1]._onMessage(envelope("HELLO_ACK", {}));
    assert.ok(
      state.alarmsCleared.length > clearsBeforeAck,
      "HELLO_ACK must clear the reconnect alarm"
    );
    assert.equal(bridge.connected, true);

    // An alarm for anything else must not reconnect.
    state.alarmListener({ name: "some-other-alarm" });
    assert.equal(state.connections.length, 2, "unrelated alarms must be ignored");
  });
});