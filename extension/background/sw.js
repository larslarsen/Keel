// SPDX-License-Identifier: Apache-2.0
/**
 * Thin client: bridge + PAGE_CONTEXT SidePanel enable.
 * No observation data persisted in the browser.
 */
import { browser } from "../lib/browser.js";
import { validateImpressionList } from "../lib/protocol.js";
import { createNativeBridge } from "../lib/native.js";
import { surfaceFromUrl } from "../content/extract.js";
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
/**
 * Every site Keel runs on (WO-057).
 *
 * A single YouTube pattern was fine while YouTube was the only platform; with
 * two it becomes a silent filter that drops the other one. Listed once here so
 * adding a platform is one edit rather than a hunt through call sites.
 */
const SITE_URLS = ["*://www.youtube.com/*", "*://www.tiktok.com/*"];

/** Which platform a tab URL belongs to, or null if Keel does not run there. */
function platformForUrl(url) {
  const u = String(url || "");
  if (/^https:\/\/www\.youtube\.com\//.test(u)) return "yt";
  if (/^https:\/\/www\.tiktok\.com\//.test(u)) return "tt";
  return null;
}
/** Port name used by the SidePanel to signal open/close (WO-009). */
const SIDEPANEL_PORT = "keel-sidepanel";

/** @type {object[]} */
let buffer = [];
/** Live page proof (memory only). generation = rail replacement counter. */
let lastPage = {
  platform: "yt",
  pageLoadId: null,
  impressions: [],
  failures: 0,
  generation: null,
};
let connected = false;
/** Open SidePanel documents (one port each). In-memory only. */
let sidePanelPorts = 0;
/**
 * Window ids with an open panel document, learned from the panel's
 * PANEL_HANDSHAKE (WO-075). Chrome's side panel is per-window, so the toolbar
 * toggle must measure the CLICKED window — a bare document count lets a panel
 * open in another window silently turn the toggle into a close-no-op.
 */
const sidePanelWindows = new Set();
/** Connected ports whose window is not yet known (handshake still in flight). */
let sidePanelPortsNoWindow = 0;

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
    tabs = await browser.tabs.query({ url: SITE_URLS });
  } catch (err) {
    console.warn(LOG, "broadcast tabs.query", err?.message || err);
    return;
  }
  for (const t of tabs) {
    if (t.id == null) continue;
    browser.tabs.sendMessage(t.id, msg).catch(() => {});
  }
}

function panelOpen(windowId) {
  if (sidePanelPorts === 0) return false;
  if (windowId == null) return true;
  // A panel whose window is not (yet) known counts as open everywhere:
  // turning the toggle into a close-no-op on a panel that only *might* be
  // here is safer than silently double-opening a window.
  if (sidePanelPortsNoWindow > 0) return true;
  return sidePanelWindows.has(windowId);
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

let bridge = createNativeBridge({
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
    // platform must survive this reset (WO-071 defect 2): the rail-generation
    // reset fires on every watch page ~2s after load (YouTube swaps the
    // watch-next rail), and dropping platform here left it undefined until the
    // next PAGE_CONTEXT — sidepanel/index.js's `lastPageCache?.platform || "yt"`
    // fallback then silently read as YouTube on a TikTok tab.
    lastPage = {
      platform: lastPage.platform || "yt",
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
 * The side panel is gated to YouTube/TikTok watch pages only (WO-071).
 *
 * 7d60797 ("Side panel is always available; narrow the orphan check") made
 * the panel open everywhere, always, to fix a dead-button problem: disabled
 * by default and enabled per tab only on an observed surface meant the
 * toolbar icon did nothing on /feed, /results, channel pages, and everywhere
 * before consent — a click with no effect and no error (setOptions rejects
 * silently), which reads as broken.
 *
 * WO-071 restores the close-on-leave behaviour that dead-button fix cost,
 * without reintroducing it: the toolbar button (`action.onClicked` below)
 * stays clickable on every page and always does something — on a watch page
 * it opens the panel, everywhere else it opens the full-page tab. Only the
 * *panel's* availability is gated by `syncPanelForTab`/`openPanelOnActionClick:
 * false`; the button itself is never dead.
 */

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
      if (message.payload?.platform) lastPage.platform = message.payload.platform;
      if (message.payload?.pageLoadId) {
        lastPage = {
          platform: message.payload.platform || lastPage.platform || "yt",
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
      // Snapshot the page state THIS handler owns before yielding. rememberPage
      // mutates the shared module-level lastPage, and a concurrent IMPRESSIONS
      // (another tab / PAGE_CONTEXT) can change it during the await below.
      // Broadcasting the live lastPage on resume would tag these impressions
      // with the wrong page (BUG S2: stale commit across await).
      const pageSnap = { ...lastPage, impressions: lastPage.impressions.slice() };

      if (!bridge.helloOk) {
        buffer.push(...values);
        if (buffer.length > BUFFER_MAX) buffer = buffer.slice(-BUFFER_MAX);
        return { queued: values.length, connected: false };
      }
      const result = await sendImpressions(values);
      broadcast({ type: "STORE_UPDATED", payload: { ...result, lastPage: pageSnap } });
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

    /**
     * WO-059: search the swarm's token shards, not just this device's own
     * catalogue. A separate RPC from SEARCH because it can reach the
     * network — SEARCH never does.
     */
    case "PEER_SEARCH": {
      const query = message.payload?.query;
      if (typeof query !== "string" || !query.trim()) throw new Error("bad query");
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("PEER_SEARCH", {
        query,
        limit: Number(message.payload?.limit) || 100,
      });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "PEER_SEARCH failed");
      }
      return { peer_search: env.payload };
    }

    /**
     * WO-068: corpus-wide word % + nested char-token coverage. Display-only
     * telemetry (direct peer pack fetch in the daemon) — never a search axis.
     */
    case "WORD_STATS": {
      const query = message.payload?.query;
      if (typeof query !== "string" || !query.trim()) throw new Error("bad query");
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("WORD_STATS", { query });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "WORD_STATS failed");
      }
      return { word_stats: env.payload };
    }

    /**
     * DESIGN_v2 §7.5: the live index lives in the daemon's memory, gossiped
     * whole. The query is matched there against records this machine already
     * holds, so nothing about it reaches the network.
     */
    /**
     * Selector config (WO-056). Data only — the extension validates it again
     * before use and refuses the whole thing on any violation.
     */
    case "GET_SELECTORS": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("GET_SELECTORS", {});
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "GET_SELECTORS failed");
      }
      return { selectors: env.payload };
    }

    case "LIVE_SEARCH": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("LIVE_SEARCH", {
        query: String(message.payload?.query || ""),
        min_publishers: Number(message.payload?.min_publishers) || 1,
        limit: Number(message.payload?.limit) || 100,
      });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "LIVE_SEARCH failed");
      }
      return { live: env.payload };
    }

    case "SUGGEST": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("SUGGEST", {
        platform: String(message.payload?.platform || "yt"),
        seed_video_id: String(message.payload?.seed_video_id || ""),
        entropy: Number(message.payload?.entropy) || 0,
        limit: Number(message.payload?.limit) || 25,
      });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "SUGGEST failed");
      }
      return { suggest: env.payload };
    }

    /**
     * The watched video finished (WO-064 autoplay).
     *
     * The daemon answers with the next queued video, or null when the finished
     * one was never queued — which is most of the time, and is why this cannot
     * hijack ordinary watching. Only then is the tab navigated, and only the
     * tab that reported the end.
     */
    case "VIDEO_ENDED": {
      if (!bridge.helloOk) return { advanced: false };
      const tabId = sender?.tab?.id;
      const env = await bridge.request("QUEUE_ADVANCE", {
        video_id: String(message.payload?.video_id || ""),
        platform: String(message.payload?.platform || "yt"),
      });
      if (env.type === "ERROR") return { advanced: false };
      const next = env.payload?.next;
      if (!next?.video_id || tabId == null) return { advanced: false };
      const href =
        next.platform === "tt"
          ? `https://www.tiktok.com/video/${encodeURIComponent(next.video_id)}`
          : `https://www.youtube.com/watch?v=${encodeURIComponent(next.video_id)}`;
      await browser.tabs.update(tabId, { url: href });
      return { advanced: true, next: next.video_id };
    }

    // The watch queue (WO-064). The daemon owns it — the extension stores no
    // state — so all four verbs are a straight relay and the daemon answers
    // every one of them with the resulting queue.
    case "QUEUE_ADD":
    case "QUEUE_LIST":
    case "QUEUE_REMOVE":
    case "QUEUE_REORDER": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request(message.type, message.payload || {});
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || `${message.type} failed`);
      }
      return { queue: env.payload };
    }

    case "AGGREGATE_SUMMARY":
    case "EXPORT_BUNDLE":
    case "PEERS": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request(message.type, {});
      if (env.type === "ERROR") throw new Error(env.payload?.message || message.type);
      return { bundle: env.payload };
    }

    case "IMPORT_BUNDLE":
    case "FORGET_PEER": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request(message.type, message.payload || {});
      if (env.type === "ERROR") throw new Error(env.payload?.message || message.type);
      return { bundle: env.payload };
    }

    case "SCROLL_HISTORY": {
      if (!bridge.helloOk) throw new Error("daemon not connected");
      const env = await bridge.request("SCROLL_HISTORY", {
        platform: String(message.payload?.platform || "tt"),
        limit: Number(message.payload?.limit) || 50,
      });
      if (env.type === "ERROR") {
        throw new Error(env.payload?.message || "SCROLL_HISTORY failed");
      }
      return { history: env.payload };
    }

    /**
     * The full-page view asks for the panel to be hidden on its own tab.
     *
     * The panel is otherwise available everywhere, which is what makes the
     * toolbar icon reliable. But the full page shows the same data with more
     * room, so a panel beside it is redundant.
     *
     * Scoped to the sender's own tab and requested by the page itself, so this
     * cannot strand a YouTube tab the way the old per-surface gating did — a
     * page that never asks keeps its panel.
     */
    /**
     * The side panel asks which context its window's active tab has, on load
     * and on tabs.onActivated (the SW may have been evicted and missed the
     * broadcast). Watch pages return focus:true plus the platform so the panel
     * re-scopes; anything else returns focus:false.
     */
    case "PANEL_CONTEXT_QUERY": {
      const win = message.payload?.windowId ?? null;
      let tabs = [];
      try {
        tabs = win != null
          ? await browser.tabs.query({ active: true, windowId: win })
          : await browser.tabs.query({ active: true, lastFocusedWindow: true });
      } catch (err) {
        console.warn(LOG, "PANEL_CONTEXT_QUERY", err?.message || err);
      }
      const active = tabs[0] || null;
      const ctx = panelContextPayload(active?.windowId ?? win, active?.url || "");
      return {
        windowId: active?.windowId ?? win,
        platform: ctx.platform,
        focus: ctx.focus,
      };
    }

    case "PANEL_NOT_HERE": {
      const tabId = sender?.tab?.id;
      if (tabId != null && browser.sidePanel?.setOptions) {
        try {
          await browser.sidePanel.setOptions({ tabId, enabled: false });
        } catch (err) {
          console.warn(LOG, "setOptions", err?.message || err);
        }
      }
      return {};
    }

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

    // Plain daemon relays. THUMBNAIL belongs here and was stranded above when
    // the contribution cases were added: its label ended up adjacent to
    // GET_CONSENT, so every thumbnail request fell through and returned a
    // consent value instead of an image. A fall-through is valid JavaScript, so
    // nothing warned — the panel simply rendered blank boxes.
    case "THUMBNAIL":
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
    sidePanelPortsNoWindow += 1;
    broadcastHideState().catch(() => {});
    port.onMessage?.addListener?.((msg) => {
      if (msg?.type !== "PANEL_HANDSHAKE") return;
      const windowId = Number.isInteger(msg.payload?.windowId)
        ? msg.payload.windowId
        : null;
      if (windowId == null) return; // stays unknown: counted as open everywhere
      port.__panelWindowId = windowId;
      sidePanelWindows.add(windowId);
      sidePanelPortsNoWindow = Math.max(0, sidePanelPortsNoWindow - 1);
    });
    port.onDisconnect.addListener(() => {
      sidePanelPorts = Math.max(0, sidePanelPorts - 1);
      if (port.__panelWindowId != null) {
        sidePanelWindows.delete(port.__panelWindowId);
      } else {
        sidePanelPortsNoWindow = Math.max(0, sidePanelPortsNoWindow - 1);
      }
      broadcastHideState().catch(() => {});
    });
  });
}

if (browser.sidePanel?.setPanelBehavior) {
  browser.sidePanel
    .setPanelBehavior({ openPanelOnActionClick: false })
    .catch(() => {});
}

/**
 * Whether a tab's panel should be enabled: the WO-071 hard gate, made
 * platform-aware (WO-074).
 *
 * The gate is "watch page" per platform — what WATCH_NEXT means differs:
 *
 *  - YouTube: a `/watch?v=` page with a recommendation rail beside the video.
 *    HOME (the FYP equivalent) is deliberately excluded — a feed is a feed.
 *  - TikTok: the For-You feed IS the watch page. TikTok desktop never
 *    navigates to `/@author/video/...` — it plays videos inline and the URL
 *    stays on `/` (confirmed by the WO-063 probe: every capture was
 *    `https://www.tiktok.com/`). Gating the panel to TT's WATCH_NEXT surface
 *    would make the TikTok panel unreachable in normal use. So on TikTok the
 *    FYP (HOME) opens the panel too, and it closes only when the active tab
 *    leaves TikTok.
 *
 * Reuses surfaceFromUrl (content/extract.js) so "watch page" is defined in
 * exactly one place, shared with the content script.
 */
function panelAllowedFor(url) {
  const { surface, platform } = surfaceFromUrl(url || "");
  if (platform === "tt") return surface === "WATCH_NEXT" || surface === "HOME";
  return surface === "WATCH_NEXT";
}

/** Enable/disable one tab's panel to match the WO-071/WO-074 hard gate. */
async function syncPanelForTab(tabId, url) {
  if (tabId == null || !browser.sidePanel?.setOptions) return;
  const enabled = panelAllowedFor(url);
  try {
    await browser.sidePanel.setOptions({ tabId, enabled, path: "sidepanel/index.html" });
  } catch (err) {
    console.warn(LOG, "syncPanelForTab", err?.message || err);
  }
}

/**
 * Close the panel for a window/tab.
 *
 * setOptions({enabled:false}) never closes an ALREADY-open panel — it only
 * stops the next open — which is why the panel kept lingering on the fullpage
 * tab and on tabs navigated away from the watch surface (reported after the
 * WO-071 gate landed). close() (Chrome 141+) does close. Both forms are
 * fired: close({windowId}) handles the global panel this extension opens via
 * open({windowId}), close({tabId}) catches tab-specific leftovers (and on
 * Chrome ≥145 rejects harmlessly when only the global panel is open). No-ops
 * when nothing is open, so calling it on every active-tab change is safe.
 */
function closePanelInWindow(windowId, tabId) {
  if (!browser.sidePanel?.close) return;
  if (windowId != null) {
    browser.sidePanel.close({ windowId }).catch(() => {});
  }
  if (tabId != null) {
    browser.sidePanel.close({ tabId }).catch(() => {});
  }
}

/**
 * The context the side panel should show, derived from the ACTIVE tab.
 *
 * This is the WO-073 fix for the panel following the wrong page: the panel is
 * a per-window artifact, and the last PAGE_CONTEXT any tab sent is
 * window-global — so switching YT→TT kept the YouTube suggestions. Context
 * must come from the active tab, not from whichever tab last wrote
 * `lastPage`.
 */
function panelContextPayload(windowId, url) {
  const { platform } = surfaceFromUrl(url || "");
  return { windowId, platform, focus: panelAllowedFor(url) };
}

/**
 * Apply the hard gate to the ACTIVE tab of a window.
 *
 * Only callers that know the tab is active may call this: a background tab's
 * surface must never close or re-scope the window's panel.
 */
async function evalActivePanelContext(tab, windowId) {
  const url = tab?.url ?? "";
  if (tab?.id != null) await syncPanelForTab(tab.id, url);
  const ctx = panelContextPayload(windowId, url);
  if (!ctx.focus && windowId != null) {
    closePanelInWindow(windowId, tab?.id ?? null);
  }
  broadcast({ type: "PANEL_CONTEXT", payload: ctx });
}

/**
 * Sweep every open tab. Covers tabs opened before this SW instance started
 * (install/update/eviction) and any navigation `onUpdated` missed — mirrors
 * rearmYoutubeTabs' own SW-restart-recovery rationale, below. Active tabs go
 * through the full gate (close/context), inactive ones just get their
 * per-tab enable/disable.
 */
async function syncAllTabsPanelState() {
  if (!browser.tabs?.query) return;
  let tabs;
  try {
    tabs = await browser.tabs.query({});
  } catch (err) {
    console.warn(LOG, "syncAllTabsPanelState", err?.message || err);
    return;
  }
  for (const t of tabs) {
    if (t.id == null) continue;
    if (t.active === true && t.windowId != null) {
      await evalActivePanelContext(t, t.windowId).catch((err) =>
        console.warn(LOG, "evalActivePanelContext", err?.message || err)
      );
    } else {
      syncPanelForTab(t.id, t.url).catch(() => {});
    }
  }
}

if (browser.tabs?.onUpdated) {
  browser.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
    if (!(changeInfo.url || changeInfo.status === "complete")) return;
    const url = changeInfo.url || tab?.url;
    const t = { ...(tab || {}), id: tabId, url: url || "" };
    // The ACTIVE tab's navigation re-runs the full gate (close-on-leave for
    // real navigations; onUpdated url events do fire for navigations, unlike
    // SPA history changes, which PAGE_CONTEXT covers below).
    if (tab?.active === true && tab.windowId != null) {
      evalActivePanelContext(t, tab.windowId).catch((err) =>
        console.warn(LOG, "onUpdated gate", err?.message || err)
      );
    } else {
      syncPanelForTab(tabId, url).catch(() => {});
    }
  });
}
if (browser.tabs?.onCreated) {
  browser.tabs.onCreated.addListener((tab) => {
    syncPanelForTab(tab.id, tab.url).catch(() => {});
  });
}
if (browser.tabs?.onActivated) {
  browser.tabs.onActivated.addListener((info) => {
    (async () => {
      if (info?.tabId == null) return;
      let tab = null;
      try {
        tab = await browser.tabs.get(info.tabId);
      } catch {
        return; // tab closed before we could read it; nothing to gate
      }
      await evalActivePanelContext(tab, info.windowId);
    })().catch((err) => console.warn(LOG, "onActivated", err?.message || err));
  });
}

/**
 * Open the full-page tab, focusing an already-open instance rather than
 * stacking duplicates.
 */
async function openFullpageTab() {
  const url = browser.runtime.getURL("page/index.html");
  if (browser.tabs?.query) {
    try {
      // A trailing "*" (not an exact-match string) so an already-open tab
      // still matches if the fullpage app has added a hash/query of its own
      // (e.g. its own in-page tab routing) — an exact match would silently
      // never find it and stack a duplicate tab on every click instead of
      // focusing the existing one.
      const existing = await browser.tabs.query({ url: url + "*" });
      if (existing.length) {
        const t = existing[0];
        await browser.tabs.update(t.id, { active: true });
        if (t.windowId != null) {
          browser.windows?.update?.(t.windowId, { focused: true }).catch(() => {});
        }
        return;
      }
    } catch {
      /* fall through to create */
    }
  }
  await browser.tabs?.create?.({ url });
}

/**
 * Toolbar button: a toggle on a watch page — if the panel is already open it
 * closes (sidePanel.close needs no gesture, so this branch is safe anywhere
 * in the handler); otherwise it opens. Everywhere else (the full-page tab, a
 * blank tab, any other site) it opens the full-page tab instead. Never a
 * dead click — see the block comment above.
 *
 * "Is the panel open" is tracked per-window by the sidepanel's long-lived
 * port (SIDEPANEL_PORT, the same counter `panelOpen()` drives for the
 * with-panel hide mode) — Chrome's sidePanel API offers no getState, and the
 * port is connected exactly while a panel document lives. Each panel doc
 * handshakes its window id (PANEL_HANDSHAKE), so the toggle measures the
 * CLICKED window only — a panel open in another window must not turn this
 * click into a close-no-op.
 */
if (browser.action?.onClicked) {
  browser.action.onClicked.addListener((tab) => {
    (async () => {
      const watch = tab?.id != null && panelAllowedFor(tab.url) && Boolean(browser.sidePanel?.open);
      if (watch) {
        if (panelOpen(tab.windowId)) {
          closePanelInWindow(tab.windowId, tab.id);
          return;
        }
        // sidePanel.open() must be the very first thing awaited in this
        // handler — confirmed by direct log evidence (WO-071 regression):
        // adding a setOptions() await ahead of it consumed the click's user
        // gesture and open() failed every time with "may only be called in
        // response to a user gesture." enabled:true is asserted afterward,
        // not before — it's a correctness backstop for the *next* click,
        // not a precondition open() needs on this one (syncPanelForTab's
        // onUpdated/onCreated/watchdog sweep already keeps it current).
        try {
          // windowId, not tabId: .open({tabId}) hard-requires that exact tab
          // to already read as enabled at the moment of the call and throws
          // "No active side panel for tabId" otherwise (confirmed live) —
          // brittle against any timing gap in the onUpdated/onCreated
          // enabling sweep. .open({windowId}) opens against the window's
          // current effective panel state instead, which is what every
          // other Chrome sidePanel example actually uses for this exact
          // toolbar-click pattern.
          await browser.sidePanel.open({ windowId: tab.windowId });
          browser.sidePanel
            .setOptions({ tabId: tab.id, enabled: true, path: "sidepanel/index.html" })
            .catch(() => {});
          return;
        } catch (err) {
          console.warn(LOG, "sidePanel.open", err?.message || err);
        }
      }
      await openFullpageTab();
    })().catch((err) => console.warn(LOG, "action click", err?.message || err));
  });
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
    tabs = await browser.tabs.query({ url: SITE_URLS });
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
  await syncAllTabsPanelState();
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
syncAllTabsPanelState().catch(() => {});
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

// Test-only seam: the SW event handlers (handle/rememberPage/flushBuffer) are
// module-private, and the race in BUG S2 lives in their shared mutation of
// `lastPage` across `await` yield points. Exposing them — and a bridge
// injector — lets the regression test drive two interleaved IMPRESSIONS
// messages without a live native-messaging connection. Production never calls
// these; the injector only swaps the module-level `bridge` binding.
export { handle, rememberPage, flushBuffer };
export function __test_setBridge(b) {
  bridge = b;
}
