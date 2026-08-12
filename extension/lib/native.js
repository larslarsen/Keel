// SPDX-License-Identifier: Apache-2.0
/** Native-messaging port with alarm-based reconnect (survives SW eviction). */
import { browser } from "./browser.js";
import {
  HOST_NAME,
  MAX_BROWSER_TO_HOST,
  envelope,
  envelopeBytes,
  validateEnvelope,
  CLIENT_VERSION,
  CLIENT_API,
  CLIENT_REQUIRED,
  CLIENT_OPTIONAL,
} from "./protocol.js";

const LOG = "[Keel native]";
const ALARM_NAME = "keel-native-reconnect";
/** Chrome alarms min practical delay ~30s; first retries use 0.5 min. */
const RECONNECT_DELAY_MIN = 0.5;

/**
 * @param {{ onStatus: (ok: boolean, detail?: string, meta?: object) => void, onMessage: (env: object) => void }} hooks
 */
export function createNativeBridge(hooks) {
  /** @type {ReturnType<typeof browser.runtime.connectNative> | null} */
  let port = null;
  let helloOk = false;
  /** @type {Record<string, number>} */
  let capabilities = Object.create(null);
  /** @type {{ code?: string, reason?: string } | null} */
  let lastHelloFailure = null;
  /** @type {Map<string, { resolve: Function, reject: Function, t: ReturnType<typeof setTimeout> }>} */
  const pending = new Map();

  function clearReconnectAlarm() {
    if (browser.alarms?.clear) {
      browser.alarms.clear(ALARM_NAME).catch(() => {});
    }
  }

  function scheduleReconnect() {
    // Prefer chrome.alarms so backoff survives service-worker eviction.
    if (browser.alarms?.create) {
      browser.alarms
        .create(ALARM_NAME, { delayInMinutes: RECONNECT_DELAY_MIN })
        .catch((err) => console.warn(LOG, "alarm", err?.message || err));
      return;
    }
    setTimeout(() => connect(), RECONNECT_DELAY_MIN * 60 * 1000);
  }

  function rejectPending(reason) {
    for (const [, w] of pending) {
      clearTimeout(w.t);
      w.reject(new Error(reason));
    }
    pending.clear();
  }

  function post(env) {
    if (!port) return false;
    const n = envelopeBytes(env);
    // Browser → host limit is 64 MiB (not the 1 MiB host→browser cap).
    if (n > MAX_BROWSER_TO_HOST) {
      console.error(LOG, "over browser→host 64 MiB limit", n);
      return false;
    }
    try {
      port.postMessage(env);
      return true;
    } catch (err) {
      console.error(LOG, "post failed", err?.message || err);
      return false;
    }
  }

  function helloPayload() {
    return {
      client: "keel-extension",
      client_version: CLIENT_VERSION,
      version: CLIENT_VERSION,
      api: { min: CLIENT_API.min, max: CLIENT_API.max },
      required: { ...CLIENT_REQUIRED },
      optional: { ...CLIENT_OPTIONAL },
    };
  }

  function applyHelloAck(payload) {
    const p = payload && typeof payload === "object" ? payload : {};
    const compatible = p.compatible === true && p.ok !== false;
    if (compatible) {
      helloOk = true;
      lastHelloFailure = null;
      capabilities = Object.create(null);
      const caps = p.capabilities && typeof p.capabilities === "object" ? p.capabilities : {};
      for (const [k, v] of Object.entries(caps)) {
        const n = Number(v);
        if (k && Number.isFinite(n) && n >= 1) capabilities[k] = n | 0;
      }
      clearReconnectAlarm();
      hooks.onStatus(true, "ok", { capabilities: { ...capabilities } });
      return;
    }
    helloOk = false;
    capabilities = Object.create(null);
    const code = typeof p.code === "string" ? p.code : "incompatible";
    const reason =
      typeof p.reason === "string" && p.reason
        ? p.reason
        : "desktop app update required";
    lastHelloFailure = { code, reason };
    // Do not set connected/ready. Surface actionable copy; keep reconnecting
    // in case the user upgrades the desktop app while the extension stays open.
    hooks.onStatus(false, reason, { code, reason, incompatible: true });
    scheduleReconnect();
  }

  function onMessage(msg) {
    const v = validateEnvelope(msg);
    if (!v.ok) {
      console.error(LOG, "drop bad envelope", v.error);
      return;
    }
    const env = v.value;
    if (env.type === "HELLO_ACK") {
      applyHelloAck(env.payload);
    }
    if (env.type === "ERROR") console.error(LOG, "ERROR", env.payload);
    const w = pending.get(env.id);
    if (w) {
      pending.delete(env.id);
      clearTimeout(w.t);
      w.resolve(env);
    }
    hooks.onMessage(env);
  }

  function connect() {
    clearReconnectAlarm();
    if (port) {
      try {
        port.disconnect();
      } catch {
        /* ignore */
      }
      port = null;
    }
    let p;
    try {
      p = browser.runtime.connectNative(HOST_NAME);
    } catch (err) {
      console.warn(LOG, "connect throw", err?.message || err);
      helloOk = false;
      capabilities = Object.create(null);
      hooks.onStatus(false, "not running");
      scheduleReconnect();
      return;
    }
    port = p;
    helloOk = false;
    capabilities = Object.create(null);
    p.onMessage.addListener(onMessage);
    p.onDisconnect.addListener(() => {
      const err = browser.runtime.lastError;
      console.warn(LOG, "disconnect", err?.message || "closed");
      port = null;
      helloOk = false;
      capabilities = Object.create(null);
      hooks.onStatus(false, err?.message || "not running");
      rejectPending("disconnected");
      scheduleReconnect();
    });
    post(envelope("HELLO", helloPayload()));
  }

  function hasCapability(name, minRev = 1) {
    const n = capabilities[name];
    return Number.isFinite(n) && n >= minRev;
  }

  function request(type, payload, timeoutMs = 8000) {
    return new Promise((resolve, reject) => {
      if (!port || !helloOk) {
        reject(
          new Error(
            lastHelloFailure?.reason || "daemon not connected"
          )
        );
        return;
      }
      const env = envelope(type, payload);
      const t = setTimeout(() => {
        pending.delete(env.id);
        reject(new Error("timeout"));
      }, timeoutMs);
      pending.set(env.id, { resolve, reject, t });
      if (!post(env)) {
        clearTimeout(t);
        pending.delete(env.id);
        reject(new Error("post failed"));
      }
    });
  }

  function onAlarm(alarm) {
    if (alarm?.name === ALARM_NAME) connect();
  }

  if (browser.alarms?.onAlarm) {
    browser.alarms.onAlarm.addListener(onAlarm);
  }

  return {
    connect,
    request,
    post,
    hasCapability,
    get helloOk() {
      return helloOk;
    },
    get connected() {
      return Boolean(port) && helloOk;
    },
    get capabilities() {
      return { ...capabilities };
    },
    get lastHelloFailure() {
      return lastHelloFailure ? { ...lastHelloFailure } : null;
    },
  };
}
