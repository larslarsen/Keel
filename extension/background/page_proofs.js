// SPDX-License-Identifier: Apache-2.0
/**
 * Tab-scoped page proof store (WO-080).
 *
 * Pure module: no browser APIs, no storage, no timers — unit-testable against
 * fixtures. The SW is the only caller and is responsible for resolving
 * `sender.tab` identity and the tab URL; identifiers supplied by page
 * payloads are never accepted as ownership here.
 *
 * A proof is the page evidence one tab has produced: which window it lives
 * in, what page (page_load_id), what platform/surface it is on, whether the
 * panel may focus it, and the impressions collected on that page so far.
 * The panel may only ever show the proof of its window's ACTIVE tab — the
 * per-tab keying is what makes two same-platform tabs unable to overwrite
 * one another.
 */

const DEFAULT_BOUND = 32;

export function createProofStore(bound = DEFAULT_BOUND) {
  /** Map<tabId, proof>. Insertion-ordered for bound eviction. */
  const map = new Map();

  function evict() {
    while (map.size > bound) {
      const oldest = map.keys().next().value;
      if (oldest == null) break;
      map.delete(oldest);
    }
  }

  return {
    /**
     * Replace the tab's proof wholesale: a new document/surface invalidates
     * the old pageLoadId before anything else can touch it. A missing
     * page_load_id drops the write entirely (off-surface pages leave no
     * proof). Returns a copy of the new proof, or null when the inputs are
     * unusable.
     */
    observeContext({ tabId, windowId, pageLoadId, platform, surface, focus, railGeneration }) {
      if (typeof tabId !== "number" || typeof windowId !== "number") return null;
      if (typeof pageLoadId !== "string" || !pageLoadId) return null;
      const proof = {
        tabId,
        windowId,
        pageLoadId,
        platform: platform || "yt",
        surface: surface ?? null,
        focus: Boolean(focus),
        impressions: [],
        failures: 0,
        railGeneration: railGeneration ?? null,
      };
      map.set(tabId, proof);
      evict();
      return copy(proof);
    },

    /**
     * Merge impressions into the tab's CURRENT proof. Entries whose
     * page_load_id does not match that proof are dropped: a late message
     * from the previous document must not restore the old page (WO-080).
     * Returns { accepted, proof, stale } — accepted is the daemon-bound
     * list, proof a copy of the updated proof (null when the tab is
     * unknown), stale true when anything was dropped.
     */
    observeImpressions({ tabId, values = [], failures = 0, railGeneration = null }) {
      const current = map.get(tabId);
      if (!current) return { accepted: [], proof: null, stale: false };
      const pid = current.pageLoadId;
      const accepted = [];
      let stale = false;
      for (const v of values) {
        if (!v || v.page_load_id !== pid) {
          stale = true;
          continue;
        }
        accepted.push(v);
      }
      if (accepted.length) {
        mergeImpressions(current, accepted);
        if (railGeneration != null) current.railGeneration = railGeneration;
      }
      // Failures count on the page whether or not any card extracted.
      if (failures > 0) current.failures += failures;
      return { accepted, proof: copy(current), stale };
    },

    /** @param {number} tabId */
    remove(tabId) {
      map.delete(tabId);
    },

    /** Service-worker restart: the store is in-memory only, never persisted. */
    clear() {
      map.clear();
    },

    /** @returns {object|null} a copy of the tab's proof. */
    get(tabId) {
      const p = map.get(tabId);
      return p ? copy(p) : null;
    },

    size() {
      return map.size;
    },

    /** Copies of every held proof, for tests and diagnostics. */
    snapshot() {
      return [...map.values()].map(copy);
    },
  };
}

function copy(proof) {
  return { ...proof, impressions: proof.impressions.slice() };
}

function mergeImpressions(proof, values) {
  const seen = new Set(proof.impressions.map((i) => `${i.video_id}|${i.slot_index}`));
  for (const imp of values) {
    const k = `${imp.video_id}|${imp.slot_index}`;
    if (!seen.has(k)) {
      seen.add(k);
      proof.impressions.push(imp);
    }
  }
  proof.impressions.sort((a, b) => a.slot_index - b.slot_index);
}