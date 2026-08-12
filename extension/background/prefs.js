// SPDX-License-Identifier: Apache-2.0
/**
 * The service worker's only door to browser storage (WO-083).
 *
 * Two keys live here and nothing else: the hide-recommendations mode (WO-009 /
 * WO-017) and first-run recording consent (WO-049). Both are user *preferences*
 * — settings the browser must remember across a service-worker eviction. No
 * observation ever passes through this module, and that is the point of giving
 * storage exactly one owner in the control plane: a reviewer can decide whether
 * DESIGN_v2 §2.1's "no observation data in browser storage" holds by reading one
 * file, instead of trusting that no handler anywhere reached for
 * `browser.storage` on the way past.
 *
 * The validation itself stays in `lib/prefs.js`, which is pure and shared with
 * the content script. This module is the *adapter*: it knows about async
 * storage, missing APIs and the legacy-value migration. That split is why
 * `lib/prefs.js` can be imported by anyone — it holds no state and cannot
 * reach the browser.
 *
 * Storage arrives as a constructor argument rather than an import so a test can
 * exercise the migration and the failure paths with a plain object, and so the
 * dependency is visible in the wiring rather than implied by a module-level
 * import (`test/background-structure.test.js` asserts nothing else in the
 * control plane receives one).
 */
import {
  DEFAULT_HIDE_MODE,
  HIDE_MODE_KEY,
  CONSENT_KEY,
  coerceHideMode,
  isHideMode,
} from "../lib/prefs.js";

/**
 * @param {{ storage?: { local?: { get?: Function, set?: Function } } }} deps
 */
export function createPrefs({ storage } = {}) {
  /**
   * Read the hide mode, migrating a legacy three-state value in place.
   *
   * Every failure answers DEFAULT_HIDE_MODE rather than throwing: this is read
   * on the paint path via HIDE_STATE, and an unreadable preference must degrade
   * to the default rather than break the surface asking for it.
   */
  async function readHideMode() {
    if (!storage?.local?.get) return DEFAULT_HIDE_MODE;
    try {
      const bag = await storage.local.get(HIDE_MODE_KEY);
      const mode = coerceHideMode(bag?.[HIDE_MODE_KEY]);
      // Persist migration from legacy never/with-panel/always (WO-017).
      const raw = bag?.[HIDE_MODE_KEY];
      if (raw != null && raw !== mode && storage?.local?.set) {
        await storage.local.set({ [HIDE_MODE_KEY]: mode });
      }
      return mode;
    } catch {
      return DEFAULT_HIDE_MODE;
    }
  }

  /**
   * Write the hide mode.
   *
   * Throws where readHideMode defaults, and deliberately so: a failed *write*
   * is a setting the user chose and did not get, which the caller has to be
   * able to report. A failed read is only a missing preference.
   */
  async function writeHideMode(mode) {
    const m = coerceHideMode(mode);
    if (!isHideMode(m)) throw new Error("bad hide mode");
    if (!storage?.local?.set) throw new Error("storage unavailable");
    await storage.local.set({ [HIDE_MODE_KEY]: m });
  }

  /** Recording consent, or null when the user has not been asked yet. */
  async function readConsent() {
    if (!storage?.local?.get) return null;
    const bag = await storage.local.get(CONSENT_KEY);
    return bag?.[CONSENT_KEY] ?? null;
  }

  /**
   * Record the consent decision.
   *
   * Only the two explicit values are accepted. `lib/prefs.js`'s consentGranted
   * treats anything else as "no", so a junk value could not grant consent — but
   * it could silently *revoke* an existing grant, so it is refused here rather
   * than stored and reinterpreted later.
   */
  async function writeConsent(v) {
    if (v !== "granted" && v !== "declined") throw new Error("bad consent value");
    if (!storage?.local?.set) throw new Error("storage unavailable");
    await storage.local.set({ [CONSENT_KEY]: v });
    return v;
  }

  return { readHideMode, writeHideMode, readConsent, writeConsent };
}
