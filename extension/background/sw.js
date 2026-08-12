// SPDX-License-Identifier: Apache-2.0
/**
 * Service worker: composition root (WO-083).
 *
 * This file used to be the whole control plane — transport hooks, a
 * ~500-line command switch, panel policy, tab sweeps, storage reads and six
 * module-level `let`s that any of them could reach. That shape produced real
 * defects rather than merely untidy code: a `case` label fell through to the
 * next handler and returned the wrong payload type, and page-proof state was
 * global enough that two tabs could overwrite each other's.
 *
 * What is left here is wiring, and only wiring:
 *
 *   - construct each owner and hand it the narrow browser adapter it needs;
 *   - register the browser's listeners and forward to the owner that answers;
 *   - start the bridge and the standing watchdog.
 *
 * No feature state lives in this file, and no `case` label. Each module below
 * receives a *slice* of the browser API rather than the whole adapter, which is
 * what makes DESIGN_v2 §2.1 checkable: only `prefs.js` is handed storage, so no
 * other part of the control plane can put observation data in it even by
 * mistake. `test/background-structure.test.js` enforces that.
 *
 * Thin client throughout: no observation data is persisted in the browser.
 */
import { browser } from "../lib/browser.js";
import { createNativeBridge } from "../lib/native.js";
import { createProofStore } from "./page_proofs.js";
import { createPrefs } from "./prefs.js";
import { createPanelContext } from "./panel_context.js";
import { createRpcRouter } from "./rpc.js";

const LOG = "[Keel SW]";
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
/** Port name used by the SidePanel to signal open/close (WO-009). */
const SIDEPANEL_PORT = "keel-sidepanel";

const log = (...args) => console.warn(LOG, ...args);

/* ---------- messaging adapters ---------- */

/** Extension pages only (SidePanel). Does not reach content scripts. */
function broadcast(msg) {
  browser.runtime.sendMessage(msg).catch(() => {});
}

/**
 * Content scripts are not on the runtime message bus — they need
 * tabs.sendMessage per tab (WO-009 live QA). Host permission covers YT/TT tabs;
 * no "tabs" permission. Tabs without a live content script reject; swallow.
 */
async function broadcastToSiteTabs(msg) {
  if (!browser.tabs?.query || !browser.tabs?.sendMessage) return;
  let tabs;
  try {
    tabs = await browser.tabs.query({ url: SITE_URLS });
  } catch (err) {
    log("broadcast tabs.query", err?.message || err);
    return;
  }
  for (const t of tabs) {
    if (t.id == null) continue;
    browser.tabs.sendMessage(t.id, msg).catch(() => {});
  }
}

/* ---------- owners ---------- */

/**
 * Tab-scoped page proof store (WO-080): one proof per observed tab, keyed by
 * sender-derived tab id, so two same-platform tabs can never overwrite one
 * another, and a panel only ever sees its window's ACTIVE tab's proof. The
 * module is pure (no browser APIs); this instance is in-memory only and dies
 * with the SW — never persisted.
 */
const pageProofs = createProofStore();

/** The only holder of browser storage in the control plane (WO-083). */
const prefs = createPrefs({ storage: browser.storage });

const panel = createPanelContext({
  tabs: browser.tabs,
  sidePanel: browser.sidePanel,
  windows: browser.windows,
  runtime: browser.runtime,
  proofs: pageProofs,
  broadcast,
  log,
});

/**
 * Panel via runtime; content scripts via tabs.sendMessage (WO-009 fix 1).
 *
 * Spans prefs and panel, so it is composed here rather than owned by either:
 * the hide state is a *pair* — the stored mode and whether a panel is open —
 * and neither module can answer both halves.
 */
async function broadcastHideState(mode) {
  const m = mode ?? (await prefs.readHideMode());
  const msg = {
    type: "HIDE_STATE",
    payload: { mode: m, panelOpen: panel.panelOpen() },
  };
  broadcast(msg);
  await broadcastToSiteTabs(msg);
}

/**
 * The bridge is read through a getter, not captured.
 *
 * It is assigned once below and swapped by the test seam; a router holding the
 * value would keep talking to a replaced port.
 */
let bridge = null;
const router = createRpcRouter({
  getBridge: () => bridge,
  proofs: pageProofs,
  prefs,
  panel,
  tabs: browser.tabs,
  broadcast,
  broadcastToSiteTabs,
  onHideModeChanged: broadcastHideState,
  openConsentPage: () => {
    browser.tabs
      ?.create?.({ url: browser.runtime.getURL("consent/index.html") })
      .catch((err) => log("openConsentPage", err?.message || err));
  },
  log,
});

bridge = createNativeBridge({
  onStatus: router.onBridgeStatus,
  onMessage: router.onBridgeMessage,
});

const handle = router.handle;

/* ---------- listeners ---------- */

browser.runtime.onMessage.addListener((message, sender, sendResponse) => {
  handle(message, sender)
    .then((r) => sendResponse({ ok: true, ...r }))
    .catch((err) =>
      sendResponse({ ok: false, error: String(err?.message || err) })
    );
  return true;
});

/**
 * SidePanel open/close via long-lived port (WO-009 with-panel default).
 * Closing the panel disconnects the port → rail returns when mode is with-panel.
 */
if (browser.runtime.onConnect) {
  browser.runtime.onConnect.addListener((port) => {
    if (port?.name !== SIDEPANEL_PORT) return;
    panel.registerPanelPort(port, () => {
      broadcastHideState().catch(() => {});
    });
  });
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
 * *panel's* availability is gated by `panel.syncPanelForTab` /
 * `openPanelOnActionClick: false`; the button itself is never dead.
 */
if (browser.sidePanel?.setPanelBehavior) {
  browser.sidePanel
    .setPanelBehavior({ openPanelOnActionClick: false })
    .catch(() => {});
}

if (browser.tabs?.onUpdated) {
  browser.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
    if (!(changeInfo.url || changeInfo.status === "complete")) return;
    const url = changeInfo.url || tab?.url;
    const t = { ...(tab || {}), id: tabId, url: url || "" };
    // The ACTIVE tab's navigation re-runs the full gate (close-on-leave for
    // real navigations; onUpdated url events do fire for navigations, unlike
    // SPA history changes, which PAGE_CONTEXT covers).
    if (tab?.active === true && tab.windowId != null) {
      panel
        .evalActivePanelContext(t, tab.windowId)
        .catch((err) => log("onUpdated gate", err?.message || err));
    } else {
      panel.syncPanelForTab(tabId, url).catch(() => {});
    }
  });
}
if (browser.tabs?.onCreated) {
  browser.tabs.onCreated.addListener((tab) => {
    panel.syncPanelForTab(tab.id, tab.url).catch(() => {});
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
      await panel.evalActivePanelContext(tab, info.windowId);
    })().catch((err) => log("onActivated", err?.message || err));
  });
}
if (browser.tabs?.onRemoved) {
  // A closed tab can no longer own a proof (WO-080). The map stays bounded
  // to the open observed tabs; onRemoved is what keeps it honest.
  browser.tabs.onRemoved.addListener((tabId) => {
    pageProofs.remove(tabId);
  });
}

/**
 * Toolbar button: a toggle on a watch page — if the panel is already open it
 * closes (sidePanel.close needs no gesture, so this branch is safe anywhere
 * in the handler); otherwise it opens. Everywhere else (the full-page tab, a
 * blank tab, any other site) it opens the full-page tab instead. Never a
 * dead click — see the block comment above.
 *
 * "Is the panel open" is tracked per-window by the sidepanel's long-lived
 * port (SIDEPANEL_PORT, the same counter `panel.panelOpen()` drives for the
 * with-panel hide mode) — Chrome's sidePanel API offers no getState, and the
 * port is connected exactly while a panel document lives. Each panel doc
 * handshakes its window id (PANEL_HANDSHAKE), so the toggle measures the
 * CLICKED window only — a panel open in another window must not turn this
 * click into a close-no-op.
 */
if (browser.action?.onClicked) {
  browser.action.onClicked.addListener((tab) => {
    (async () => {
      const watch =
        tab?.id != null &&
        panel.panelAllowedFor(tab.url) &&
        Boolean(browser.sidePanel?.open);
      if (watch) {
        if (panel.panelOpen(tab.windowId)) {
          panel.closePanelInWindow(tab.windowId, tab.id);
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
          log("sidePanel.open", err?.message || err);
        }
      }
      await panel.openFullpageTab();
    })().catch((err) => log("action click", err?.message || err));
  });
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
    log("rearm scan", err?.message || err);
    return;
  }
  for (const t of tabs) {
    if (t.id == null) continue;
    browser.scripting
      .executeScript({
        target: { tabId: t.id },
        files: ["content/bootstrap.js"],
      })
      .catch((err) => log("rearm tab", t.id, err?.message || err));
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
  await panel.syncAllTabs();
}

if (browser.alarms?.create && browser.alarms?.onAlarm) {
  browser.alarms.onAlarm.addListener((alarm) => {
    if (alarm?.name === WATCHDOG_ALARM) {
      onWatchdog().catch((err) => log(err?.message || err));
    }
  });
  browser.alarms.create(WATCHDOG_ALARM, { periodInMinutes: 0.5 }).catch((err) => {
    log("watchdog alarm", err?.message || err);
  });
}

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

bridge.connect();
panel.syncAllTabs().catch(() => {});
console.info(LOG, "ready");

// Test-only seam: exposes the message handler and flush machinery so tests
// can drive interleaved IMPRESSIONS messages without a live native-messaging
// connection, plus the per-tab proof store for WO-080 assertions. Production
// never calls these; the injector only swaps the module-level `bridge`
// binding, which the router reads through getBridge().
export { handle, pageProofs };
export const flushBuffer = router.flushBuffer;
export function __test_setBridge(b) {
  bridge = b;
}
