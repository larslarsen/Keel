// SPDX-License-Identifier: Apache-2.0
/**
 * Port-to-search routing for streaming distributed search (WO-095 §4).
 *
 * # Why this is its own module rather than more of sw.js
 *
 * `sw.js` is a composition root and holds no feature state (WO-083). Search
 * sessions are feature state with a lifetime: a page connects a named Port,
 * claims one or more `search_id`s, receives events for those and no others, and
 * loses them all when the Port closes. Putting that in the service worker would
 * put a second mutable map next to the ones WO-083 removed.
 *
 * # Why events are routed by search id and not broadcast
 *
 * A search is one page's private activity. Broadcasting an event to every
 * extension page — the way `CONTRIBUTION_STATUS` legitimately is, because a
 * setting really did change for everyone — would hand every open surface a live
 * feed of what one page is looking for. So an event reaches exactly the Port
 * that claimed its `search_id`, and an event for an unknown id is dropped
 * rather than fanned out.
 *
 * # Why the route is installed before the request is sent
 *
 * The daemon may emit a progress event before its own `PEER_SEARCH_STARTED`
 * reply has been processed here — they travel on the same stream and the reply
 * is not special. If the route were installed on the acknowledgement, that
 * first event would arrive unclaimed and be dropped, and the bar it belonged to
 * would never start. So the page claims the id first and starts the search
 * second.
 *
 * Nothing here is persisted. The map dies with the service worker, and a page
 * that outlives an eviction re-issues its search rather than recovering one —
 * DESIGN_v2 §2.1 leaves no room for job state in browser storage.
 */

import { errText } from "../lib/errors.js";

/** Port name a search page connects under. */
export const SEARCH_PORT = "keel-search";

/** Every event type a revision-3 job can emit. */
const SEARCH_EVENTS = new Set([
  "PEER_SEARCH_PROGRESS",
  "PEER_SEARCH_WORD_PROGRESS",
  "PEER_SEARCH_RESULT",
  "PEER_SEARCH_COMPLETE",
  "PEER_SEARCH_CANCELLED",
  "PEER_SEARCH_FAILED",
]);

/** Terminal event types, after which a search id is released. */
const TERMINAL_EVENTS = new Set([
  "PEER_SEARCH_COMPLETE",
  "PEER_SEARCH_CANCELLED",
  "PEER_SEARCH_FAILED",
]);

/**
 * @param {{ onOrphan?: (searchId: string) => void, log?: (...a: unknown[]) => void }} [deps]
 */
export function createSearchSessions({ onOrphan = () => {}, log = () => {} } = {}) {
  /**
   * search_id → Port. One entry per live search, not per page: a page may
   * replace its own search, and the old id is released the moment the new one
   * is claimed so a late event from the replaced job cannot be delivered as if
   * it were current.
   *
   * @type {Map<string, object>}
   */
  const routes = new Map();

  /**
   * Port → Set of search ids it owns, so closing a Port releases exactly its
   * own searches and nobody else's.
   *
   * @type {Map<object, Set<string>>}
   */
  const owned = new Map();

  /** Register a Port and wire its lifecycle. */
  function register(port) {
    if (!port || owned.has(port)) return;
    owned.set(port, new Set());
    port.onDisconnect?.addListener?.(() => release(port));
    port.onMessage?.addListener?.((msg) => {
      if (!msg || typeof msg !== "object") return;
      if (msg.type === "CLAIM_SEARCH" && typeof msg.search_id === "string") {
        claim(port, msg.search_id);
      } else if (msg.type === "RELEASE_SEARCH" && typeof msg.search_id === "string") {
        drop(msg.search_id);
      }
    });
  }

  /**
   * Claim a search id for a Port, releasing whatever that Port held before.
   *
   * One active search per Port is the page's own contract (WO-095 §4), and
   * enforcing it here as well means a page that forgets to release cannot leak
   * a route that keeps receiving events for a search it stopped rendering.
   */
  function claim(port, searchId) {
    if (!searchId) return;
    const mine = owned.get(port);
    if (!mine) return;
    for (const prev of mine) {
      if (prev !== searchId) routes.delete(prev);
    }
    mine.clear();
    mine.add(searchId);
    routes.set(searchId, port);
  }

  /** Release one search id, whoever owns it. */
  function drop(searchId) {
    const port = routes.get(searchId);
    routes.delete(searchId);
    const mine = port && owned.get(port);
    if (mine) mine.delete(searchId);
  }

  /** Release every search a Port owns, and the Port itself. */
  function release(port) {
    const mine = owned.get(port);
    if (mine) {
      for (const id of mine) {
        routes.delete(id);
        // A Port that closed with a search still running leaves a job running
        // on the daemon with nobody to receive it. The caller cancels it —
        // this module knows about routing, not about the bridge.
        onOrphan(id);
      }
    }
    owned.delete(port);
  }

  /**
   * Deliver one daemon envelope if it is a search event.
   *
   * @returns {boolean} true when this envelope was a search event and was
   *   consumed here — the caller must not also treat it as an RPC reply.
   */
  function deliver(env) {
    if (!env || !SEARCH_EVENTS.has(env.type)) return false;
    const searchId = env.payload?.search_id;
    if (typeof searchId !== "string" || !searchId) return true;
    const port = routes.get(searchId);
    if (!port) {
      // An event for a search nobody is rendering. Dropped, not broadcast:
      // this is the normal shape of a race with cancellation, and fanning it
      // out would leak one page's activity to every other surface.
      return true;
    }
    try {
      port.postMessage({ type: env.type, payload: env.payload });
    } catch (err) {
      log("search event post failed", errText(err));
      release(port);
      return true;
    }
    if (TERMINAL_EVENTS.has(env.type)) drop(searchId);
    return true;
  }

  return {
    register,
    deliver,
    /** Test/diagnostic view of the live routes. */
    get size() {
      return routes.size;
    },
    has: (searchId) => routes.has(searchId),
  };
}
