// SPDX-License-Identifier: Apache-2.0
/**
 * WO-009 / WO-017 — display-only recommendation suppression.
 * Injects one <style> on documentElement. No DOM mutation of YouTube nodes.
 * Mode is "on" | "off" (legacy values coerced via coerceHideMode).
 */
import { browser } from "../lib/browser.js";
import {
  DEFAULT_HIDE_MODE,
  HIDE_MODE_KEY,
  coerceHideMode,
  shouldHide,
} from "../lib/prefs.js";

const STYLE_ID = "keel-hide-recommendations";

/**
 * Watch page only: hide the secondary column itself so the player can reclaim
 * width. Home grid is never hidden.
 */
const CSS_TEXT = `
ytd-watch-flexy #secondary {
  display: none !important;
}
`.trim();

/** @type {import('../lib/prefs.js').HideMode} */
let mode = DEFAULT_HIDE_MODE;
let panelOpen = false;

function effective() {
  return shouldHide(mode, panelOpen);
}

/** Nudge ytd-watch-flexy to re-measure after #secondary appears/disappears. */
function nudgeFlexyLayout() {
  requestAnimationFrame(() => {
    window.dispatchEvent(new Event("resize"));
  });
}

function inject() {
  if (document.getElementById(STYLE_ID)) return false;
  const style = document.createElement("style");
  style.id = STYLE_ID;
  style.textContent = CSS_TEXT;
  (document.documentElement || document.head).appendChild(style);
  return true;
}

function remove() {
  const el = document.getElementById(STYLE_ID);
  if (!el) return false;
  el.remove();
  return true;
}

function apply() {
  const changed = effective() ? inject() : remove();
  if (changed) nudgeFlexyLayout();
}

/**
 * @param {{ mode?: unknown }} state
 */
export function setHideState(state) {
  if (state && "mode" in state) {
    mode = coerceHideMode(state.mode);
  }
  if (state && typeof state.panelOpen === "boolean") {
    panelOpen = state.panelOpen;
  }
  apply();
}

async function loadMode() {
  if (!browser.storage?.local?.get) return DEFAULT_HIDE_MODE;
  try {
    const bag = await browser.storage.local.get(HIDE_MODE_KEY);
    return coerceHideMode(bag?.[HIDE_MODE_KEY]);
  } catch {
    return DEFAULT_HIDE_MODE;
  }
}

/** Arm hide controller for this tab. */
export async function startHide() {
  mode = await loadMode();
  // Persist migration if storage still holds a legacy three-state value.
  if (browser.storage?.local?.set) {
    try {
      const bag = await browser.storage.local.get(HIDE_MODE_KEY);
      const raw = bag?.[HIDE_MODE_KEY];
      if (raw != null && raw !== mode) {
        await browser.storage.local.set({ [HIDE_MODE_KEY]: mode });
      }
    } catch {
      /* ignore */
    }
  }
  try {
    const r = await browser.runtime.sendMessage({ type: "GET_HIDE_STATE" });
    if (r?.ok) mode = coerceHideMode(r.mode);
    if (typeof r?.panelOpen === "boolean") panelOpen = r.panelOpen;
  } catch {
    /* SW may be restarting */
  }
  apply();

  browser.runtime.onMessage.addListener((msg) => {
    if (msg?.type !== "HIDE_STATE") return;
    const p = msg.payload || {};
    setHideState({ mode: p.mode, panelOpen: p.panelOpen });
  });

  const onChanged = browser.storage?.onChanged;
  if (onChanged?.addListener) {
    onChanged.addListener((changes, area) => {
      if (area && area !== "local") return;
      const ch = changes?.[HIDE_MODE_KEY];
      if (!ch) return;
      mode = ch.newValue == null ? DEFAULT_HIDE_MODE : coerceHideMode(ch.newValue);
      apply();
    });
  }
}
