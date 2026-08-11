// SPDX-License-Identifier: Apache-2.0
/** Promise-based WebExtension shim. Never use chrome.* outside this file. */

const raw =
  typeof globalThis.browser !== "undefined" && globalThis.browser?.runtime?.id
    ? globalThis.browser
    : globalThis.chrome;

if (!raw) throw new Error("WebExtension API unavailable");

function p(fn, self) {
  return (...args) => {
    let r;
    try {
      r = fn.apply(self, args);
    } catch (e) {
      return Promise.reject(e);
    }
    if (r != null && typeof r.then === "function") return r;
    return new Promise((resolve, reject) => {
      fn.call(self, ...args, (result) => {
        const err = raw.runtime?.lastError;
        if (err) reject(new Error(err.message || String(err)));
        else resolve(result);
      });
    });
  };
}

export const browser = {
  runtime: {
    id: raw.runtime?.id,
    getURL: (...a) => raw.runtime.getURL(...a),
    sendMessage: p(raw.runtime.sendMessage, raw.runtime),
    connect: (...a) => raw.runtime.connect(...a),
    onInstalled: raw.runtime.onInstalled,
    connectNative: (...a) => raw.runtime.connectNative(...a),
    onMessage: raw.runtime.onMessage,
    onConnect: raw.runtime.onConnect,
    lastError: raw.runtime?.lastError,
  },
  storage: raw.storage
    ? {
        local: {
          get: p(raw.storage.local.get, raw.storage.local),
          set: p(raw.storage.local.set, raw.storage.local),
        },
        onChanged: raw.storage.onChanged,
      }
    : undefined,
  sidePanel: raw.sidePanel
    ? {
        setOptions: p(raw.sidePanel.setOptions, raw.sidePanel),
        setPanelBehavior: p(raw.sidePanel.setPanelBehavior, raw.sidePanel),
        open: p(raw.sidePanel.open, raw.sidePanel),
      }
    : undefined,
  action: raw.action ? { onClicked: raw.action.onClicked } : undefined,
  tabs: raw.tabs
    ? {
        create: p(raw.tabs.create, raw.tabs),
        update: p(raw.tabs.update, raw.tabs),
        query: p(raw.tabs.query, raw.tabs),
        get: p(raw.tabs.get, raw.tabs),
        // Host permission on youtube.com is enough — no "tabs" permission.
        sendMessage: p(raw.tabs.sendMessage, raw.tabs),
        onUpdated: raw.tabs.onUpdated,
        onCreated: raw.tabs.onCreated,
      }
    : undefined,
  windows: raw.windows ? { update: p(raw.windows.update, raw.windows) } : undefined,
  scripting: raw.scripting
    ? { executeScript: p(raw.scripting.executeScript, raw.scripting) }
    : undefined,
  alarms: raw.alarms
    ? {
        create: p(raw.alarms.create, raw.alarms),
        clear: p(raw.alarms.clear, raw.alarms),
        onAlarm: raw.alarms.onAlarm,
      }
    : undefined,
};

export default browser;
