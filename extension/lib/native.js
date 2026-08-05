// SPDX-License-Identifier: Apache-2.0
/** Native-messaging port with alarm-based reconnect (survives SW eviction). */
import { browser } from "./browser.js";
import {
  HOST_NAME,
  MAX_BROWSER_TO_HOST,
  envelope,
  envelopeBytes,
  validateEnvelope,
} from "./protocol.js";

const LOG = "[Keel native]";
const ALARM_NAME = "keel-native-reconnect";
/** Chrome alarms min practical delay ~30s; first retries use 0.5 min. */
const RECONNECT_DELAY_MIN = 0.5;

/**
 * @param {{ onStatus: (ok: boolean, detail?: string) => void, onMessage: (env: object) => void }} hooks
 */
export function createNativeBridge(hooks) {
  /** @type {ReturnType<typeof browser.runtime.connectNative> | null} */
  let port = null;
  let helloOk = false;
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

  function onMessage(msg) {
    const v = validateEnvelope(msg);
    if (!v.ok) {
      console.error(LOG, "drop bad envelope", v.error);
      return;
    }
    const env = v.value;
    if (env.type === "HELLO_ACK") {
      helloOk = true;
      clearReconnectAlarm();
      hooks.onStatus(true, "ok");
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
      hooks.onStatus(false, "not running");
      scheduleReconnect();
      return;
    }
    port = p;
    p.onMessage.addListener(onMessage);
    p.onDisconnect.addListener(() => {
      const err = browser.runtime.lastError;
      console.warn(LOG, "disconnect", err?.message || "closed");
      port = null;
      helloOk = false;
      hooks.onStatus(false, err?.message || "not running");
      rejectPending("disconnected");
      scheduleReconnect();
    });
    post(envelope("HELLO", { client: "keel-extension", version: "0.1.0" }));
  }

  function request(type, payload, timeoutMs = 8000) {
    return new Promise((resolve, reject) => {
      if (!port || !helloOk) {
        reject(new Error("daemon not connected"));
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
    get helloOk() {
      return helloOk;
    },
    get connected() {
      return Boolean(port) && helloOk;
    },
  };
}
