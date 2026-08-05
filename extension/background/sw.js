// SPDX-License-Identifier: Apache-2.0
/**
 * Thin client: bridge + PAGE_CONTEXT SidePanel enable.
 * No observation data persisted in the browser.
 */
import { browser } from "../lib/browser.js";
import { validateImpressionList } from "../lib/protocol.js";
import { createNativeBridge } from "../lib/native.js";
import {
  DEFAULT_HIDE_MODE,
  HIDE_MODE_KEY,
  CONSENT_KEY,
  coerceHideMode,
  isChannelId,
  isHideMode,
} from "../lib/prefs.js";

const LOG = "[Keel SW]";
const BUFFER_MAX = 200;
/** Standing self-heal alarm (WO-008): wakes an evicted SW, reconnects the
 * daemon link, and re-injects the observer into already-open YouTube tabs.
 * Match is site-wide (WO-010); the observer idles off HOME/WATCH_NEXT. */
const WATCHDOG_ALARM = "keel-watchdog";
const YT_URL = "*://www.youtube.com/*";
/** Port name used by the SidePanel to signal open/close (WO-009). */
const SIDEPANEL_PORT = "keel-sidepanel";

/** @type {object[]} */
let buffer = [];
/** Live page proof (memory only). generation = rail replacement counter. */
let lastPage = { pageLoadId: null, impressions: [], failures: 0, generation: null };
let connected = false;
/** Open SidePanel documents (one port each). In-memory only. */
let sidePanelPorts = 0;

/** Extension pages only (SidePanel). Does not reach content scripts. */
function broadcast(msg) {
  browser.runtime.sendMessage(msg).catch(() => {});
}

/**
 * Content scripts are not on the runtime message bus — they need
 * tabs.sendMessage per tab (WO-009 live QA). Host permission covers YT tabs;
 * no "tabs" permission. Tabs without a live content script reject; swallow.
 */
async function broadcastToYoutubeTabs(msg) {
  if (!browser.tabs?.query || !browser.tabs?.sendMessage) return;
  let tabs;
  try {
    tabs = await browser.tabs.query({ url: YT_URL });
  } catch (err) {
    console.warn(LOG, "broadcast tabs.query", err?.message || err);
    return;
  }
  for (const t of tabs) {
    if (t.id == null) continue;
    browser.tabs.sendMessage(t.id, msg).catch(() => {});
  }
}

function panelOpen() {
  return sidePanelPorts > 0;
}

async function readHideMode() {
  if (!browser.storage?.local?.get) return DEFAULT_HIDE_MODE;
  try {
    const bag = await browser.storage.local.get(HIDE_MODE_KEY);
    const mode = coerceHideMode(bag?.[HIDE_MODE_KEY]);
    // Persist migration from legacy never/with-panel/always (WO-017).
    const raw = bag?.[HIDE_MODE_KEY];
    if (raw != null && raw !== mode && browser.storage?.local?.set) {
      await browser.storage.local.set({ [HIDE_MODE_KEY]: mode });
    }
    return mode;
  } catch {
    return DEFAULT_HIDE_MODE;
  }
}

async function writeHideMode(mode) {
  const m = coerceHideMode(mode);
  if (!isHideMode(m)) throw new Error("bad hide mode");
  if (!browser.storage?.local?.set) throw new Error("storage unavailable");
  await browser.storage.local.set({ [HIDE_MODE_KEY]: m });
}

/** Panel via runtime; content scripts via tabs.sendMessage (WO-009 fix 1). */
async function broadcastHideState(mode) {
  const m = mode ?? (await readHideMode());
  const msg = {
    type: "HIDE_STATE",
    payload: { mode: m, panelOpen: panelOpen() },
  };
  broadcast(msg);
  await broadcastToYoutubeTabs(msg);
}

function setStatus(ok, detail = "") {
  connected = ok;
  broadcast({ type: "DAEMON_STATUS", payload: { connected: ok, detail } });
  // Keep bounded in-memory buffer across disconnect; flush on reconnect (WO-004 §8).
  if (ok) {
    flushBuffer();
    reportCohort().catch(() => {});
  }
}

const bridge = createNativeBridge({
  onStatus: setStatus,
  // Do not broadcast STATS_RESULT / IMPRESSIONS_ACK here. The IMPRESSIONS
  // handler (and flushBuffer) already emit STORE_UPDATED with lastPage.
  // Re-broadcasting STATS_RESULT made the panel re-enter GET_STATS on every
  // reply (STORE_UPDATED → refresh → STATS → STORE_UPDATED → …).
  onMessage: () => {},
});

async function sendImpressions(impressions) {
  if (!impressions.length) return { inserted: 0 };
  if (!bridge.helloOk) {
    return { inserted: 0, queued: impressions.length };
  }
  const env = await bridge.request("IMPRESSIONS", { impressions });
  return env.payload || {};
}

function flushBuffer() {
  if (!buffer.length || !bridge.helloOk) return;
  const batch = buffer;
  buffer = [];
  sendImpressions(batch)
    .then((result) => {
      broadcast({ type: "STORE_UPDATED", payload: { ...result, lastPage } });
    })
    .catch((e) => console.warn(LOG, e.message));
}

function rememberPage(values, failures, generation) {
  if (!values.length) {
    lastPage.failures += failures;
    return;
  }
  const pid = values[0].page_load_id;
  // Reset on navigation OR on rail replacement (same page, new suggestion
  // set). YouTube swaps the watch-next rail ~2s after load; without this the
  // panel accumulates two sets whose slots collide.
  if (
    lastPage.pageLoadId !== pid ||
    (generation != null && generation !== lastPage.generation)
  ) {
    lastPage = {
      pageLoadId: pid,
      impressions: [],
      failures: 0,
      generation: generation ?? lastPage.generation,
    };
  }
  const seen = new Set(
    lastPage.impressions.map((i) => `${i.video_id}|${i.slot_index}`)
  );
  for (const imp of values) {
    const k = `${imp.video_id}|${imp.slot_index}`;
    if (!seen.has(k)) {
      seen.add(k);
      lastPage.impressions.push(imp);
    }
  }
  lastPage.impressions.sort((a, b) => a.slot_index - b.slot_index);
  lastPage.failures += failures;
}

/**
 * Per-tab side panel availability (WO-021).
 *
 * manifest `side_panel.default_path` makes the panel available on every tab,
 * so it stayed open on non-YouTube tabs showing stale data. The default is now
 * disabled and each tab is enabled only when its content script reports an
 * observed surface — the `sender.tab.id` route DESIGN_v2 §2 specifies, which
 * needs no `tabs` permission and no extra host access.
 */
async function setSidePanelForTab(tabId, enabled) {
  if (!browser.sidePanel?.setOptions || tabId == null) return;
  try {
    await browser.sidePanel.setOptions({
      tabId,
      path: "sidepanel/index.html",
      enabled,
    });
  } catch (err) {
    console.warn(LOG, "setOptions", err?.message || err);
  }
}

/** Default the panel off everywhere; observed tabs opt themselves in. */
async function disableSidePanelByDefault() {
  if (!browser.sidePanel?.setOptions) return;
  try {
    await browser.sidePanel.setOptions({ enabled: false });
  } catch (err) {
    console.warn(LOG, "setOptions default", err?.message || err);
  }
}

browser.runtime.onMessage.addListener((message, sender, sendResponse) => {
  handle(message, sender)
    .then((r) => sendResponse({ ok: true, ...r }))
    .catch((err) =>
      sendResponse({ ok: false, error: String(err?.message || err) })
    );
  return true;
});

async function handle(message, sender) {
  if (!message || typeof message !== "object") throw new Error("bad message");

  switch (message.type) {
    case "PAGE_CONTEXT": {
      // Enable on observed surfaces, disable elsewhere on youtube.com so the
      // panel does not linger on /feed, channel pages or search (WO-021).
      const observed =
        message.payload?.surface === "WATCH_NEXT" ||
        message.payload?.surface === "HOME";
      await setSidePanelForTab(sender?.tab?.id, observed);
      if (message.payload?.pageLoadId) {
        lastPage = {
          pageLoadId: message.payload.pageLoadId,
          impressions: [],
          failures: 0,
          generation: null,
        };
      }
      return {};
    }

    case "IMPRESSIONS": {
      const list = message.payload?.impressions || [];
      const failures =
        typeof message.payload?.failures === "number"
          ? message.payload.failures
          : 0;
      const { values, errors } = validateImpressionList(list);
      if (errors.length) console.warn(LOG, "invalid", errors);
      rememberPage(values, failures, message.payload?.generation);

      if (!bridge.helloOk) {
        buffer.push(...values);
        if (buffer.length > BUFFER_MAX) buffer = buffer.slice(-BUFFER_MAX);
        return { queued: values.length, connected: false };
      }
      const result = await sendImpressions(values);
      broadcast({ type: "STORE_UPDATED", payload: { ...result, lastPage } });
      return { result, connected: true };
    }

    case "GET_STATUS":
      return { connected, lastPage, buffered: buffer.length };

    case "GET_STATS": {
      if (!bridge.helloOk) {
        return { connected: false, stats: null, lastPage };
      }
      const env = await bridge.request("STATS", {});
      return { connected: true, stats: env.payload, lastPage };
    }

    /** WO-012: daemon writes file; bridge returns path only — no corpus in browser. */
    case "EXPORT": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("EXPORT", {});
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "export failed");
      }
      return { export: env.payload };
    }

    case "WIPE": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("WIPE", {});
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "wipe failed");
      }
      // Clear in-memory page proof; counts refresh from panel.
      lastPage = {
        pageLoadId: lastPage.pageLoadId,
        impressions: [],
        failures: 0,
        generation: lastPage.generation,
      };
      buffer = [];
      broadcast({
        type: "STORE_UPDATED",
        payload: {
          inserted: 0,
          wiped: env.payload?.deleted ?? 0,
          lastPage,
          stats: {
            total: 0,
            by_surface: {
              WATCH_NEXT: 0,
              HOME: 0,
              SEARCH: 0,
              CHANNEL: 0,
              SHORTS: 0,
            },
            first_observed_at: null,
            last_observed_at: null,
            extraction_failures: 0,
          },
        },
      });
      return { wipe: env.payload };
    }

    case "PING":
      return { pong: true, connected };

    case "GET_HIDE_STATE": {
      const mode = await readHideMode();
      return { mode, panelOpen: panelOpen() };
    }

    case "SET_HIDE_MODE": {
      const mode = coerceHideMode(message.payload?.mode);
      if (!isHideMode(mode)) throw new Error("bad hide mode");
      await writeHideMode(mode);
      await broadcastHideState(mode);
      return { mode, panelOpen: panelOpen() };
    }

    /** WO-016: blocklist is daemon-owned (SQLite). Extension does not decide. */
    case "GET_BLOCKLIST": {
      if (!bridge.helloOk) {
        return { connected: false, blocklist: [] };
      }
      const env = await bridge.request("GET_BLOCKLIST", {});
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "GET_BLOCKLIST failed");
      }
      return {
        connected: true,
        blocklist: env.payload?.blocklist || [],
      };
    }

    case "BLOCK_CHANNEL": {
      const id = message.payload?.channel_id;
      if (!isChannelId(id)) throw new Error("bad channel_id");
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("BLOCK_CHANNEL", { channel_id: id });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "BLOCK_CHANNEL failed");
      }
      return { blocklist: env.payload?.blocklist || [] };
    }

    case "UNBLOCK_CHANNEL": {
      const id = message.payload?.channel_id;
      if (!isChannelId(id)) throw new Error("bad channel_id");
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("UNBLOCK_CHANNEL", { channel_id: id });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "UNBLOCK_CHANNEL failed");
      }
      return { blocklist: env.payload?.blocklist || [] };
    }

    /** WO-018: observational funnel for a video_id (daemon query only). */
    case "SEARCH": {
      const query = message.payload?.query;
      if (typeof query !== "string" || !query.trim()) throw new Error("bad query");
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("SEARCH", {
        query,
        limit: Number(message.payload?.limit) || 100,
      });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "SEARCH failed");
      }
      return { search: env.payload };
    }

    case "SUGGEST": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("SUGGEST", {
        seed_video_id: String(message.payload?.seed_video_id || ""),
        entropy: Number(message.payload?.entropy) || 0,
        limit: Number(message.payload?.limit) || 25,
      });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "SUGGEST failed");
      }
      return { suggest: env.payload };
    }

	case "AGGREGATE_SUMMARY":
	case "EXPORT_BUNDLE":
	{
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request(message.type, {});
      if (env.type === "ERROR") throw new Error(env.payload?.message || message.type);
      return { bundle: env.payload };
    }

    case "THUMBNAIL":
    case "GET_CONSENT": {
      const bag = await browser.storage.local.get(CONSENT_KEY);
      return { consent: bag?.[CONSENT_KEY] ?? null };
    }

    case "SET_CONSENT": {
      const v = message.payload?.consent;
      if (v !== "granted" && v !== "declined") throw new Error("bad consent value");
      await browser.storage.local.set({ [CONSENT_KEY]: v });
      // Content scripts gate on this; tell them without waiting for a reload.
      await broadcastToYoutubeTabs({ type: "CONSENT_CHANGED", payload: { consent: v } });
      return { consent: v };
    }

    case "GET_CONTRIBUTION":
    case "SET_CONTRIBUTION":
    case "GET_DISK_BUDGET":
    case "SET_DISK_BUDGET": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request(message.type, message.payload || {});
      if (env.type === "ERROR") throw new Error(env.payload?.message || message.type);
      return { daemon: env.payload };
    }

    case "ANALYSIS": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("ANALYSIS", {});
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "ANALYSIS failed");
      }
      return { analysis: env.payload };
    }

    case "EXPLAIN_VIDEO": {
      const videoId = message.payload?.video_id;
      if (typeof videoId !== "string" || !videoId) {
        throw new Error("bad video_id");
      }
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("EXPLAIN_VIDEO", { video_id: videoId });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "EXPLAIN_VIDEO failed");
      }
      return { explain: env.payload };
    }

    default:
      throw new Error(`unknown type ${message.type}`);
  }
}

/**
 * SidePanel open/close via long-lived port (WO-009 with-panel default).
 * Closing the panel disconnects the port → rail returns when mode is with-panel.
 */
if (browser.runtime.onConnect) {
  browser.runtime.onConnect.addListener((port) => {
    if (port?.name !== SIDEPANEL_PORT) return;
    sidePanelPorts += 1;
    broadcastHideState().catch(() => {});
    port.onDisconnect.addListener(() => {
      sidePanelPorts = Math.max(0, sidePanelPorts - 1);
      broadcastHideState().catch(() => {});
    });
  });
}

if (browser.sidePanel?.setPanelBehavior) {
  browser.sidePanel
    .setPanelBehavior({ openPanelOnActionClick: true })
    .catch(() => {});
}
/**
 * Report the browser's own locale once connected (WO-029).
 *
 * DESIGN_v2 §6.3 defines the cohort as country plus interface language and
 * nothing else — the browser already knows both, so the daemon never has to
 * infer or geolocate. Sent once per connect; the daemon normalises and stores.
 */
async function reportCohort() {
  try {
    const locale =
      (typeof navigator !== "undefined" && navigator.language) || "";
    if (!locale) return;
    await bridge.request("SET_COHORT", { locale });
  } catch {
    /* cohort is best-effort; "unknown" is a valid value */
  }
}

// Panel is off by default; a tab enables it by reporting an observed surface.
disableSidePanelByDefault().catch(() => {});

/**
 * Re-inject bootstrap.js into every open YouTube tab. Idempotent: the observer
 * is an ES module loaded by dynamic import, so a re-injection is a cached
 * no-op when the module is already evaluated. Covers the MV3 hole where an
 * extension reload/update tears down content scripts without re-injecting
 * them into already-open tabs (WO-008). Site-wide match (WO-010).
 */
async function rearmYoutubeTabs() {
  if (!browser.scripting?.executeScript || !browser.tabs?.query) return;
  let tabs;
  try {
    tabs = await browser.tabs.query({ url: YT_URL });
  } catch (err) {
    console.warn(LOG, "rearm scan", err?.message || err);
    return;
  }
  for (const t of tabs) {
    if (t.id == null) continue;
    browser.scripting
      .executeScript({
        target: { tabId: t.id },
        files: ["content/bootstrap.js"],
      })
      .catch((err) => console.warn(LOG, "rearm tab", t.id, err?.message || err));
  }
}

/**
 * Self-heal watchdog. Fires on a standing 0.5 min alarm, which also keeps
 * the SW from being evicted while any observed surface could produce data.
 * Both links are covered:
 *  - SW → daemon: reconnect when the native port died silently (eviction).
 *  - tab → SW: re-arm the observer in YouTube tabs that lost it (reload/update).
 */
async function onWatchdog() {
  if (!bridge.connected) bridge.connect();
  await rearmYoutubeTabs();
}

if (browser.alarms?.create && browser.alarms?.onAlarm) {
  browser.alarms.onAlarm.addListener((alarm) => {
    if (alarm?.name === WATCHDOG_ALARM) {
      onWatchdog().catch((err) => console.warn(LOG, err?.message || err));
    }
  });
  browser.alarms.create(WATCHDOG_ALARM, { periodInMinutes: 0.5 }).catch((err) => {
    console.warn(LOG, "watchdog alarm", err?.message || err);
  });
}

bridge.connect();
console.info(LOG, "ready");

/**
 * Show the consent screen once, on install (WO-049).
 *
 * Not on update: DESIGN_v2 requires re-notification when data handling changes,
 * not on every version bump.
 */
if (browser.runtime.onInstalled?.addListener) {
  browser.runtime.onInstalled.addListener((details) => {
    if (details?.reason !== "install") return;
    browser.tabs
      ?.create?.({ url: browser.runtime.getURL("consent/index.html") })
      .catch(() => {});
  });
}
