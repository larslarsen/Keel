// SPDX-License-Identifier: Apache-2.0
/**
 * Side-panel lifecycle and the tab/window state it is derived from (WO-083).
 *
 * This module answers three questions that used to be answered by five
 * free functions and three module-level counters in `sw.js`:
 *
 *   1. May this tab have a panel at all? (`panelAllowedFor` — the WO-071/074
 *      hard gate, defined once in terms of `surfaceFromUrl`.)
 *   2. Is a panel open in this window right now? (`panelOpen` — Chrome's
 *      sidePanel API has no getState, so this is tracked from the panel's own
 *      long-lived port.)
 *   3. What context should the panel be showing? (`evalActivePanelContext` —
 *      always the ACTIVE tab's, never a background tab's.)
 *
 * The port bookkeeping is the state worth having one owner for. It is three
 * variables that must agree — a document count, the set of windows known to
 * hold one, and the count of documents whose window has not yet handshaked —
 * and WO-075 exists because they disagreed. Keeping them in one closure with
 * `panelOpen` as the only reader is what makes them checkable.
 *
 * Deliberately given no storage and no bridge: panel policy is a function of
 * URLs and window state, and a panel decision that consulted the daemon would
 * make the gate fail differently when the desktop app is down.
 */
import { surfaceFromUrl } from "../content/extract.js";

/**
 * @param {{
 *   tabs?: object,
 *   sidePanel?: object,
 *   windows?: object,
 *   runtime?: object,
 *   proofs: { get: (tabId: number) => object|null },
 *   broadcast: (msg: object) => void,
 *   log?: (...args: unknown[]) => void,
 * }} deps
 */
export function createPanelContext({
  tabs,
  sidePanel,
  windows,
  runtime,
  proofs,
  broadcast,
  log = () => {},
}) {
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

  function panelOpen(windowId) {
    if (sidePanelPorts === 0) return false;
    if (windowId == null) return true;
    // A panel whose window is not (yet) known counts as open everywhere:
    // turning the toggle into a close-no-op on a panel that only *might* be
    // here is safer than silently double-opening a window.
    if (sidePanelPortsNoWindow > 0) return true;
    return sidePanelWindows.has(windowId);
  }

  /**
   * Adopt one connected panel port: count it, learn its window from the
   * handshake, and clean up on disconnect.
   *
   * `onChange` fires on connect and disconnect because the hide state depends
   * on whether a panel is open (WO-009's with-panel default), and the surfaces
   * that paint on that answer are not the ones holding this counter.
   */
  function registerPanelPort(port, onChange = () => {}) {
    sidePanelPorts += 1;
    sidePanelPortsNoWindow += 1;
    onChange();
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
    port.onDisconnect?.addListener?.(() => {
      sidePanelPorts = Math.max(0, sidePanelPorts - 1);
      if (port.__panelWindowId != null) {
        sidePanelWindows.delete(port.__panelWindowId);
      } else {
        sidePanelPortsNoWindow = Math.max(0, sidePanelPortsNoWindow - 1);
      }
      onChange();
    });
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

  /**
   * The context the side panel should show, derived from the ACTIVE tab.
   *
   * This is the WO-073 fix for the panel following the wrong page: the panel is
   * a per-window artifact, and the last PAGE_CONTEXT any tab sent was
   * window-global — so switching YT→TT kept the YouTube suggestions. Context
   * must come from the active tab. WO-080 carries the tab identity through the
   * broadcast so the panel can also reject stale proofs by id.
   */
  function panelContextPayload(windowId, url) {
    const { platform, surface } = surfaceFromUrl(url || "");
    return { windowId, platform, surface, focus: panelAllowedFor(url) };
  }

  /** Enable/disable one tab's panel to match the WO-071/WO-074 hard gate. */
  async function syncPanelForTab(tabId, url) {
    if (tabId == null || !sidePanel?.setOptions) return;
    const enabled = panelAllowedFor(url);
    try {
      await sidePanel.setOptions({ tabId, enabled, path: "sidepanel/index.html" });
    } catch (err) {
      log("syncPanelForTab", err?.message || err);
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
    if (!sidePanel?.close) return;
    if (windowId != null) {
      sidePanel.close({ windowId }).catch(() => {});
    }
    if (tabId != null) {
      sidePanel.close({ tabId }).catch(() => {});
    }
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
    broadcast({
      type: "PANEL_CONTEXT",
      payload: { ...ctx, tab_id: tab?.id ?? null },
    });
  }

  /** The window's active tab, or null. Shared by the two lookups below. */
  async function activeTab(windowId) {
    if (!tabs?.query) return null;
    try {
      const found =
        windowId != null
          ? await tabs.query({ active: true, windowId })
          : await tabs.query({ active: true, lastFocusedWindow: true });
      return found?.[0] || null;
    } catch (err) {
      log("activeTab", err?.message || err);
      return null;
    }
  }

  /**
   * The page proof of a window's ACTIVE tab — the only proof a panel may show
   * (WO-080). Resolves the active tab, then asks the per-tab store for THAT
   * tab's proof; a background tab's proof is never offered.
   */
  async function activeProofForWindow(windowId) {
    const tab = await activeTab(windowId);
    if (!tab || tab.id == null) return null;
    return proofs.get(tab.id);
  }

  /**
   * Answer the panel's own context query (PANEL_CONTEXT_QUERY).
   *
   * The panel asks on load and on tabs.onActivated, because the SW may have
   * been evicted and missed the broadcast. Watch pages return focus:true plus
   * the platform so the panel re-scopes; anything else returns focus:false. The
   * proof — if any — is the ACTIVE tab's own, never a background tab's (WO-080).
   */
  async function contextForPanel(windowId) {
    const win = windowId ?? null;
    const active = await activeTab(win);
    const ctx = panelContextPayload(active?.windowId ?? win, active?.url || "");
    const proof = active?.id != null ? proofs.get(active.id) : null;
    return {
      window_id: active?.windowId ?? win,
      tab_id: active?.id ?? null,
      platform: ctx.platform,
      surface: ctx.surface,
      focus: ctx.focus,
      proof,
    };
  }

  /**
   * Disable the panel on one tab, at that tab's own request (PANEL_NOT_HERE).
   *
   * The full-page view shows the same data with more room, so a panel beside it
   * is redundant. Scoped to the requesting tab, so this cannot strand a YouTube
   * tab the way the old per-surface gating did — a page that never asks keeps
   * its panel.
   */
  async function disablePanelForTab(tabId) {
    if (tabId == null || !sidePanel?.setOptions) return;
    try {
      await sidePanel.setOptions({ tabId, enabled: false });
    } catch (err) {
      log("setOptions", err?.message || err);
    }
  }

  /**
   * Sweep every open tab. Covers tabs opened before this SW instance started
   * (install/update/eviction) and any navigation `onUpdated` missed — mirrors
   * rearmYoutubeTabs' own SW-restart-recovery rationale. Active tabs go
   * through the full gate (close/context), inactive ones just get their
   * per-tab enable/disable.
   */
  async function syncAllTabs() {
    if (!tabs?.query) return;
    let all;
    try {
      all = await tabs.query({});
    } catch (err) {
      log("syncAllTabs", err?.message || err);
      return;
    }
    for (const t of all) {
      if (t.id == null) continue;
      if (t.active === true && t.windowId != null) {
        await evalActivePanelContext(t, t.windowId).catch((err) =>
          log("evalActivePanelContext", err?.message || err)
        );
      } else {
        syncPanelForTab(t.id, t.url).catch(() => {});
      }
    }
  }

  /**
   * Open the full-page tab, focusing an already-open instance rather than
   * stacking duplicates.
   *
   * Lives with the panel because it is the other half of one decision: the
   * toolbar button opens whichever of the two surfaces the current tab can
   * have, and splitting those across modules would let them disagree about
   * when a click does nothing.
   */
  async function openFullpageTab() {
    const url = runtime?.getURL?.("page/index.html") ?? "page/index.html";
    if (tabs?.query) {
      try {
        // A trailing "*" (not an exact-match string) so an already-open tab
        // still matches if the fullpage app has added a hash/query of its own
        // (e.g. its own in-page tab routing) — an exact match would silently
        // never find it and stack a duplicate tab on every click instead of
        // focusing the existing one.
        const existing = await tabs.query({ url: url + "*" });
        if (existing.length) {
          const t = existing[0];
          await tabs.update(t.id, { active: true });
          if (t.windowId != null) {
            windows?.update?.(t.windowId, { focused: true })?.catch?.(() => {});
          }
          return;
        }
      } catch {
        /* fall through to create */
      }
    }
    await tabs?.create?.({ url });
  }

  return {
    panelOpen,
    registerPanelPort,
    panelAllowedFor,
    panelContextPayload,
    syncPanelForTab,
    closePanelInWindow,
    evalActivePanelContext,
    activeProofForWindow,
    contextForPanel,
    disablePanelForTab,
    syncAllTabs,
    openFullpageTab,
  };
}
