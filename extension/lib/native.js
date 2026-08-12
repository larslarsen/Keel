// SPDX-License-Identifier: Apache-2.0
/** Native-messaging port with alarm-based reconnect (survives SW eviction). */
import { browser } from "./browser.js";
import { errText } from "./errors.js";
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

/**
 * Detect an extension folder holding files from two different builds.
 *
 * Extracting a new release over an existing folder leaves a mixture: this file
 * can be new while protocol.js is old, and then CLIENT_API or CLIENT_REQUIRED
 * is simply `undefined`. What that produces is a TypeError inside
 * helloPayload(), reported against the line `client: "keel-extension"` — an
 * object literal, which tells the reader nothing at all about the real cause
 * and sends them looking at protocol negotiation instead of at their own
 * folder.
 *
 * Checked up front so the report names the cause and the fix.
 *
 * @returns {string} empty when the contract is intact
 */
export function protocolContractError() {
  const missing = [];
  if (!HOST_NAME) missing.push("HOST_NAME");
  if (!CLIENT_VERSION) missing.push("CLIENT_VERSION");
  if (!CLIENT_API || typeof CLIENT_API.min !== "number" || typeof CLIENT_API.max !== "number") {
    missing.push("CLIENT_API");
  }
  if (!CLIENT_REQUIRED || typeof CLIENT_REQUIRED !== "object") missing.push("CLIENT_REQUIRED");
  if (!CLIENT_OPTIONAL || typeof CLIENT_OPTIONAL !== "object") missing.push("CLIENT_OPTIONAL");
  if (!missing.length) return "";
  return (
    `lib/protocol.js is missing ${missing.join(", ")} — this extension folder ` +
    "holds files from two different builds. Delete the folder and extract " +
    "keel-extension.zip into an empty one, then reload the extension."
  );
}

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
  /**
   * The last thing the host said before it went away.
   *
   * The proxy reports why it could not start — owner_paths, owner_secret,
   * owner_unavailable — as an ERROR envelope with id "0", then exits. Nothing is
   * waiting on that id, so it used to be logged and dropped, and the disconnect
   * that followed a millisecond later replaced the status with "not running".
   * The one message that explains the failure was discarded, every time, and
   * the panel showed the least informative sentence available.
   * @type {string}
   */
  let lastHostError = "";
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
        .catch((err) => console.warn(LOG, "alarm", errText(err)));
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
      console.error(LOG, "post failed", errText(err));
      return false;
    }
  }

  function helloPayload() {
    return {
      client: "keel-extension",
      // If you are reading this line in an error report, the throw is on the
      // next few: see protocolContractError.
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
      lastHostError = "";
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
    // Also log it: the panel shows one line of copy, but the extension's own
    // error list on chrome://extensions is where INSTALL.md sends people, and a
    // refused negotiation has to be legible there or it cannot be reported.
    console.error(LOG, "HELLO refused", `${code}: ${reason}`);
    // Do not set connected/ready. Surface actionable copy; keep reconnecting
    // in case the user upgrades the desktop app while the extension stays open.
    hooks.onStatus(false, reason, { code, reason, incompatible: true });
    scheduleReconnect();
  }

  function onMessage(msg) {
    const v = validateEnvelope(msg);
    if (!v.ok) {
      console.error(LOG, "drop bad envelope", errText(v.error));
      return;
    }
    const env = v.value;
    if (env.type === "HELLO_ACK") {
      applyHelloAck(env.payload);
    }
    if (env.type === "ERROR") {
      const detail = errText(env.payload);
      console.error(LOG, "ERROR", detail);
      // An ERROR nobody asked for is the host explaining why it is about to
      // exit. Show it: it is the only account of the failure that exists.
      if (!pending.has(env.id)) {
        lastHostError = detail;
        helloOk = false;
        hooks.onStatus(false, detail, {
          code: (env.payload && env.payload.code) || "host_error",
        });
      }
    }
    const w = pending.get(env.id);
    if (w) {
      pending.delete(env.id);
      clearTimeout(w.t);
      w.resolve(env);
    }
    hooks.onMessage(env);
  }

  /**
   * connect() runs at the service worker's TOP LEVEL (sw.js calls it during
   * module evaluation). An uncaught throw there aborts the worker's
   * installation, and every listener registered above it dies with it —
   * including the toolbar button, which then does nothing at all on any page.
   *
   * So nothing in here may throw into module scope, and no hook may be invoked
   * synchronously: a status callback that fails must not be able to take the
   * extension down with it. The daemon link is allowed to be broken; the
   * browser UI is not allowed to break with it.
   */
  function connect() {
    try {
      connectInner();
    } catch (err) {
      console.error(LOG, "connect failed", errText(err));
      report(false, "not running", { code: "connect_failed" });
    }
  }

  /** Deliver a status update without letting it reach module scope. */
  function report(ok, detail, meta) {
    queueMicrotask(() => {
      try {
        hooks.onStatus(ok, detail, meta);
      } catch (err) {
        console.error(LOG, "onStatus threw", errText(err));
      }
    });
  }

  function connectInner() {
    // A mixed-build folder cannot be fixed by retrying, so say what is wrong
    // and stop rather than reconnecting forever against a broken contract.
    const contract = protocolContractError();
    if (contract) {
      console.error(LOG, contract);
      helloOk = false;
      capabilities = Object.create(null);
      report(false, contract, { code: "mixed_build", incompatible: true });
      return;
    }
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
      console.warn(LOG, "connect throw", errText(err));
      helloOk = false;
      capabilities = Object.create(null);
      // Deferred, like the contract branch above: this path is reachable
      // synchronously from the service worker's top-level connect().
      report(false, errText(err) || "not running", { code: "connect_failed" });
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
      // A startup ERROR the host already sent outranks both of these: it says
      // WHY it exited, where lastError only says that it did.
      hooks.onStatus(false, lastHostError || err?.message || "not running");
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
        // lastHostError first: when the host explained why it exited, that is
        // the only account of the failure anyone has. The consent screen shows
        // this string verbatim, and "daemon not connected" told the user
        // nothing they could act on.
        reject(
          new Error(
            lastHostError || lastHelloFailure?.reason || "daemon not connected"
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
