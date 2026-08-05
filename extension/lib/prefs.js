// SPDX-License-Identifier: Apache-2.0
/**
 * User preferences helpers.
 * WO-009 / WO-017: hide_recommendations — "on" | "off" in chrome.storage.
 * Legacy never/with-panel/always are coerced on read (WO-017).
 * WO-016: channel blocklist lives in the daemon SQLite — not here.
 */

/** @typedef {'on' | 'off'} HideMode */

export const HIDE_MODE_KEY = "hide_recommendations";

/** First-run consent (WO-049): "granted" | "declined" | absent = undecided. */
export const CONSENT_KEY = "consent";

/**
 * Only an explicit "granted" permits observation. Absent, unreadable or
 * anything unexpected means no — a corrupt value must never read as consent.
 */
export function consentGranted(v) {
  return v === "granted";
}

/** @type {readonly HideMode[]} */
export const HIDE_MODES = Object.freeze(["on", "off"]);

/** Default on: hide the rail (migrates from former with-panel default). */
export const DEFAULT_HIDE_MODE = "on";

const UC_RE = /^UC[\w-]{22}$/;

/**
 * @param {unknown} v
 * @returns {v is HideMode}
 */
export function isHideMode(v) {
  return v === "on" || v === "off";
}

/**
 * Coerce stored value including legacy WO-009 three-state modes.
 * with-panel / always → on; never → off.
 * @param {unknown} v
 * @returns {HideMode}
 */
export function coerceHideMode(v) {
  if (v === "on" || v === "off") return v;
  if (v === "never") return "off";
  if (v === "with-panel" || v === "always") return "on";
  return DEFAULT_HIDE_MODE;
}

/**
 * Effective paint suppression from stored mode.
 * @param {unknown} mode
 * @param {boolean} [panelOpen]
 */
export function shouldHide(mode, panelOpen) {
  return coerceHideMode(mode) === "on" && Boolean(panelOpen);
}

/** @param {unknown} id */
export function isChannelId(id) {
  return typeof id === "string" && UC_RE.test(id);
}

/**
 * Normalize a list of channel ids for RPC validation (order preserved, unique).
 * @param {unknown} raw
 * @returns {string[]}
 */
export function normalizeBlocklist(raw) {
  if (!Array.isArray(raw)) return [];
  const out = [];
  const seen = new Set();
  for (const x of raw) {
    if (!isChannelId(x) || seen.has(x)) continue;
    seen.add(x);
    out.push(x);
  }
  return out;
}
