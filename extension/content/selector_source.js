// SPDX-License-Identifier: Apache-2.0
/**
 * Observer selector selection (WO-107).
 *
 * The page platform is chosen from the URL before any daemon reply. A failed
 * or mismatched GET_SELECTORS must keep that platform's bundled data, never
 * another platform's.
 */
import { platformFromUrl } from "./extract.js";
import {
  bundledSelectorsFor,
  selectorConfigError,
  validateSelectorConfig,
} from "../lib/selectors.js";

/** Bundled selectors for this href, before any daemon response. */
export function initialSelectorsForHref(href) {
  const platform = platformFromUrl(href);
  return { platform, selectors: bundledSelectorsFor(platform) };
}

/**
 * Apply a GET_SELECTORS sendMessage reply to the already-chosen page bundle.
 * Never returns a config whose platform differs from pagePlatform.
 *
 * @param {object | null} current
 * @param {string | null} pagePlatform
 * @param {object | null | undefined} reply
 */
export function applyDaemonSelectorReply(current, pagePlatform, reply) {
  if (!pagePlatform || !current) {
    return { selectors: null, source: "none", reason: "no supported platform" };
  }
  if (!reply || reply.ok !== true) {
    return { selectors: current, source: "bundled", reason: "no daemon reply" };
  }
  const raw = reply.selectors?.selectors ?? null;
  const valid = validateSelectorConfig(raw);
  if (!valid) {
    return {
      selectors: current,
      source: "bundled",
      reason: selectorConfigError(raw),
    };
  }
  if (valid.platform !== pagePlatform) {
    return {
      selectors: current,
      source: "bundled",
      reason: `platform ${valid.platform}, page is ${pagePlatform}`,
    };
  }
  return { selectors: valid, source: "daemon", reason: null };
}
