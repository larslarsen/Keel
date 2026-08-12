// SPDX-License-Identifier: Apache-2.0
/**
 * The extension's RPC dispatcher (WO-083).
 *
 * Everything a surface or content script can ask the service worker to do
 * arrives here, is validated here, and is gated here. Three concerns that were
 * interleaved in `sw.js` are separated by construction now:
 *
 *   - **Validation.** A message is not trusted because it arrived. Ids are
 *     checked (`isChannelId`), impressions are re-validated against the
 *     protocol schema, and tab identity is taken from the browser's `sender`
 *     rather than from the payload — a page cannot claim another tab's proof
 *     slot or label itself as another platform.
 *   - **Capability gates.** Optional RPCs are refused when the daemon did not
 *     negotiate them (WO-081), with copy that says the desktop app needs
 *     updating rather than failing on an unknown type.
 *   - **Transport.** Owned entirely by `lib/native.js`. This module never
 *     touches a port, an alarm or a request id.
 *
 * # Why the switch is worth having its own module
 *
 * Two shipped defects came out of this code living in the composition root.
 * `THUMBNAIL` was written adjacent to `GET_CONSENT` and fell through to it, so
 * every thumbnail request returned a consent value and the panel rendered blank
 * boxes — a fall-through is valid JavaScript, so nothing warned. And the
 * disconnected buffer, the connection flag and the negotiated capability map
 * were module-level `let`s that any handler could reach. Both are ownership
 * problems, not typo problems: the fix is a module whose state is reachable
 * only through its own returned functions.
 *
 * # The bridge is fetched, not held
 *
 * `getBridge()` rather than a captured `bridge`, because the binding is
 * replaced — by reconnection in production and by the test seam in the suite —
 * and a captured reference would keep answering from a dead port.
 */
import { validateImpressionList, CONSENT_REVISION } from "../lib/protocol.js";
import { isChannelId, coerceHideMode, isHideMode } from "../lib/prefs.js";
import { surfaceFromUrl } from "../content/extract.js";
import { errText } from "../lib/errors.js";

/** Disconnected-impression buffer cap (DESIGN_v2 §2.1: bounded, in-memory). */
const BUFFER_MAX = 200;

/**
 * @param {{
 *   getBridge: () => object,
 *   proofs: object,
 *   prefs: object,
 *   panel: object,
 *   tabs?: object,
 *   broadcast: (msg: object) => void,
 *   broadcastToSiteTabs: (msg: object) => Promise<void>,
 *   onHideModeChanged: (mode: string) => Promise<void>,
 *   openConsentPage?: () => void,
 *   log?: (...args: unknown[]) => void,
 * }} deps
 */
export function createRpcRouter({
  getBridge,
  proofs,
  prefs,
  panel,
  tabs,
  broadcast,
  broadcastToSiteTabs,
  onHideModeChanged,
  openConsentPage,
  log = () => {},
}) {
  /**
   * Impressions observed while the daemon was unreachable.
   *
   * In memory, bounded, and dropped oldest-first — never storage. This is the
   * one place observation data rests in the browser at all, which is why it is
   * a private `let` in this closure rather than a module export: DESIGN_v2 §2.1
   * is checkable by reading who can touch it.
   *
   * @type {object[]}
   */
  let buffer = [];
  let connected = false;
  /**
   * Mirror of the negotiated capability map.
   *
   * `lib/native.js` is the owner; this is a fallback for a bridge that predates
   * `hasCapability` (the test suite injects such stubs), and the source for the
   * DAEMON_STATUS broadcast, which has to report the map even at the moment the
   * bridge is reporting itself down.
   *
   * @type {Record<string, number>}
   */
  let bridgeCaps = Object.create(null);

  function hasCap(name, minRev = 1) {
    const bridge = getBridge();
    if (typeof bridge?.hasCapability === "function") {
      return bridge.hasCapability(name, minRev);
    }
    const n = bridgeCaps[name];
    return Number.isFinite(n) && n >= minRev;
  }

  /** Throw the standard "your desktop app is behind" refusal for an RPC. */
  function requireCap(name, label) {
    if (hasCap(name)) return;
    throw new Error(`${label} unavailable — desktop app update required`);
  }

  /** Require a live, negotiated daemon before relaying anything to it. */
  function requireDaemon() {
    if (!getBridge()?.helloOk) throw new Error("daemon not connected");
  }

  /**
   * Relay one request and unwrap it, turning a daemon ERROR into a rejection.
   *
   * Every daemon-backed case had this same four lines written out; the two that
   * legitimately differ (PEER_SEARCH's typed refusal, VIDEO_ENDED's silent
   * "no") call `request` directly and handle the envelope themselves.
   *
   * `fallback` is spelled out per call rather than always derived from `type`
   * because the existing wording is not uniform ("export failed", bare
   * "THUMBNAIL"), and a refactor is the wrong place to change a string a user
   * might see.
   */
  async function relay(type, payload = {}, fallback = `${type} failed`) {
    const env = await getBridge().request(type, payload);
    if (env.type === "ERROR") {
      throw new Error(env.payload?.message || fallback);
    }
    return env.payload;
  }

  async function sendImpressions(impressions) {
    if (!impressions.length) return { inserted: 0 };
    if (!getBridge().helloOk) {
      return { inserted: 0, queued: impressions.length };
    }
    const env = await getBridge().request("IMPRESSIONS", { impressions });
    return env.payload || {};
  }

  function flushBuffer() {
    if (!buffer.length || !getBridge().helloOk) return;
    const batch = buffer;
    buffer = [];
    sendImpressions(batch)
      .then((result) => {
        // The disconnected buffer is NOT page-proof state (WO-080): a flush is
        // a multi-tab batch that no longer carries tab ownership, so it
        // broadcasts counts only — never a proof. The panel re-pulls the
        // active tab's proof via GET_STATS/GET_STATUS.
        broadcast({
          type: "STORE_UPDATED",
          payload: { ...result, window_id: null, tab_id: null, proof: null },
        });
      })
      .catch((e) => log(e.message));
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
      await getBridge().request("SET_COHORT", { locale });
    } catch {
      /* cohort is best-effort; "unknown" is a valid value */
    }
  }

  /**
   * The bridge's status hook: record it, tell every surface, and catch up.
   *
   * Buffer flush and cohort report ride the *transition* to connected rather
   * than a timer, because that is the only moment at which the queued work
   * becomes possible (WO-004 §8).
   */
  function onBridgeStatus(ok, detail = "", meta = undefined) {
    connected = ok;
    if (ok && meta?.capabilities && typeof meta.capabilities === "object") {
      bridgeCaps = { ...meta.capabilities };
    } else if (!ok) {
      bridgeCaps = Object.create(null);
    }
    broadcast({
      type: "DAEMON_STATUS",
      payload: {
        connected: ok,
        detail,
        code: meta?.code || "",
        incompatible: Boolean(meta?.incompatible),
        capabilities: { ...bridgeCaps },
      },
    });
    // Keep bounded in-memory buffer across disconnect; flush on reconnect.
    if (ok) {
      flushBuffer();
      reportCohort().catch(() => {});
      maybePromptNetworkConsent().catch(() => {});
    }
  }

  /**
   * Existing installs accepted a screen that is no longer the current
   * disclosure (WO-089). Their chrome.storage "granted" is not that acceptance.
   *
   * First-run users already get the tab from onInstalled, so this only opens
   * when this profile already recorded a grant — the migration case. The
   * daemon stays network-off until they answer, whether the tab is opened or
   * not.
   */
  let promptedNetworkConsent = false;
  async function maybePromptNetworkConsent() {
    if (!hasCap("network_consent") || promptedNetworkConsent) return;
    let payload;
    try {
      payload = await relay("GET_NETWORK_CONSENT");
    } catch {
      return;
    }
    broadcast({ type: "CONTRIBUTION_STATUS", payload });
    if (payload?.consent_required !== true) return;
    const local = await prefs.readConsent().catch(() => null);
    if (local !== "granted") return;
    promptedNetworkConsent = true;
    try {
      openConsentPage?.();
    } catch (err) {
      log("openConsentPage", errText(err));
    }
  }

  /**
   * Unsolicited daemon frames.
   *
   * Deliberately NOT a general relay of every reply. Re-broadcasting
   * STATS_RESULT / IMPRESSIONS_ACK made the panel re-enter GET_STATS on every
   * reply (STORE_UPDATED → refresh → STATS → STORE_UPDATED → …); the
   * IMPRESSIONS handler and flushBuffer already emit STORE_UPDATED with counts.
   */
  function onBridgeMessage(env) {
    // Owner-wide policy changes are unsolicited events, not RPC replies. Every
    // browser/profile connected to the shared owner receives one (WO-079).
    if (env?.type === "CONTRIBUTION_STATUS") {
      const payload = env.payload || {};
      broadcast({ type: "CONTRIBUTION_STATUS", payload });
      if (payload.consent_required === true) {
        maybePromptNetworkConsent().catch(() => {});
      }
    }
  }

  async function handle(message, sender) {
    if (!message || typeof message !== "object") throw new Error("bad message");

    switch (message.type) {
      /** WO-080: proof writes are keyed by the SENDER's tab, never by payload ids. */
      case "PAGE_CONTEXT": {
        const tabId = sender?.tab?.id;
        const windowId = sender?.tab?.windowId;
        // A message with no browser-attributed tab cannot claim a proof slot.
        if (typeof tabId !== "number" || typeof windowId !== "number") return {};
        const url = sender?.tab?.url || "";
        proofs.observeContext({
          tabId,
          windowId,
          pageLoadId: message.payload?.pageLoadId,
          // Platform/surface/focus are derived from the sender's URL, not
          // believed from the payload — a page cannot label itself as another
          // platform or claim focus it does not have.
          platform: message.payload?.platform || "yt",
          surface: surfaceFromUrl(url).surface,
          focus: panel.panelAllowedFor(url),
          railGeneration:
            typeof message.payload?.generation === "number"
              ? message.payload.generation
              : null,
        });
        return {};
      }

      case "IMPRESSIONS": {
        const tabId = sender?.tab?.id;
        const windowId = sender?.tab?.windowId;
        const list = message.payload?.impressions || [];
        const failures =
          typeof message.payload?.failures === "number"
            ? message.payload.failures
            : 0;
        const { values, errors } = validateImpressionList(list);
        if (errors.length) log("invalid", errors);
        // The store keeps ONE proof per tab: stale batches (a previous document)
        // are dropped there, and the return is already a snapshot — no shared
        // mutation can cross the await below (BUG S2 is gone by construction).
        const { accepted, proof } = proofs.observeImpressions({
          tabId,
          values,
          failures,
          railGeneration: message.payload?.generation ?? null,
        });
        const pageSnap = proof;

        if (!getBridge().helloOk) {
          buffer.push(...accepted);
          if (buffer.length > BUFFER_MAX) buffer = buffer.slice(-BUFFER_MAX);
          return { queued: accepted.length, connected: false };
        }
        const result = await sendImpressions(accepted);
        broadcast({
          type: "STORE_UPDATED",
          payload: {
            ...result,
            window_id: windowId ?? null,
            tab_id: tabId ?? null,
            proof: pageSnap,
          },
        });
        return { result, connected: true };
      }

      case "GET_STATUS": {
        const proof = await panel.activeProofForWindow(message.payload?.windowId);
        return {
          connected,
          proof,
          buffered: buffer.length,
          capabilities: { ...bridgeCaps },
          incompatible: Boolean(getBridge().lastHelloFailure),
          detail: getBridge().lastHelloFailure?.reason || "",
        };
      }

      case "GET_STATS": {
        const proof = await panel.activeProofForWindow(message.payload?.windowId);
        if (!getBridge().helloOk) {
          return { connected: false, stats: null, proof };
        }
        const env = await getBridge().request("STATS", {});
        return { connected: true, stats: env.payload, proof };
      }

      /** WO-012: daemon writes file; bridge returns path only — no corpus in browser. */
      case "EXPORT": {
        requireDaemon();
        return { export: await relay("EXPORT", {}, "export failed") };
      }

      case "WIPE": {
        requireDaemon();
        const payload = await relay("WIPE", {}, "wipe failed");
        // Clear all in-memory page proofs (WO-080); counts refresh from panel.
        proofs.clear();
        buffer = [];
        broadcast({
          type: "STORE_UPDATED",
          payload: {
            inserted: 0,
            wiped: payload?.deleted ?? 0,
            window_id: null,
            tab_id: null,
            proof: null,
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
        return { wipe: payload };
      }

      case "PING":
        return { pong: true, connected };

      case "GET_HIDE_STATE": {
        const mode = await prefs.readHideMode();
        return { mode, panelOpen: panel.panelOpen() };
      }

      case "SET_HIDE_MODE": {
        const mode = coerceHideMode(message.payload?.mode);
        if (!isHideMode(mode)) throw new Error("bad hide mode");
        await prefs.writeHideMode(mode);
        await onHideModeChanged(mode);
        return { mode, panelOpen: panel.panelOpen() };
      }

      /** WO-016: blocklist is daemon-owned (SQLite). Extension does not decide. */
      case "GET_BLOCKLIST": {
        if (!getBridge().helloOk) {
          return { connected: false, blocklist: [] };
        }
        const payload = await relay("GET_BLOCKLIST");
        return { connected: true, blocklist: payload?.blocklist || [] };
      }

      case "BLOCK_CHANNEL":
      case "UNBLOCK_CHANNEL": {
        const id = message.payload?.channel_id;
        if (!isChannelId(id)) throw new Error("bad channel_id");
        requireDaemon();
        const payload = await relay(message.type, { channel_id: id });
        return { blocklist: payload?.blocklist || [] };
      }

      /** WO-022: local catalogue only. SEARCH never reaches the network. */
      case "SEARCH": {
        const query = requireQuery(message);
        requireDaemon();
        return {
          search: await relay("SEARCH", {
            query,
            limit: Number(message.payload?.limit) || 100,
          }),
        };
      }

      /**
       * WO-059: search the swarm's token shards, not just this device's own
       * catalogue. A separate RPC from SEARCH because it can reach the
       * network — SEARCH never does.
       */
      case "PEER_SEARCH": {
        const query = requireQuery(message);
        requireDaemon();
        requireCap("peer_search", "peer search");
        const env = await getBridge().request("PEER_SEARCH", {
          query,
          limit: Number(message.payload?.limit) || 100,
        });
        if (env.type === "ERROR") {
          // WO-085: "you have not opted in" is an answer, not a failure. It is
          // the one PEER_SEARCH error the user can act on, and throwing would
          // flatten it into a message string — the extension-message channel
          // carries only {ok, error} for a rejection, so the code and the level
          // detail the UI needs would be lost on the way out.
          if (env.payload?.code === "contribution_required") {
            return {
              peer_search: {
                query,
                hits: [],
                progress: [],
                available: false,
                contribution_required: env.payload?.detail || {
                  capability: "distributed_search",
                  required_level: 2,
                },
                message: env.payload?.message || "",
              },
            };
          }
          throw new Error(env.payload?.message || "PEER_SEARCH failed");
        }
        return { peer_search: env.payload };
      }

      /**
       * WO-068: corpus-wide word % + nested char-token coverage. Display-only
       * telemetry (direct peer pack fetch in the daemon) — never a search axis.
       */
      case "WORD_STATS": {
        const query = requireQuery(message);
        requireDaemon();
        requireCap("word_stats", "word stats");
        return { word_stats: await relay("WORD_STATS", { query }) };
      }

      /**
       * Selector config (WO-056). Data only — the extension validates it again
       * before use and refuses the whole thing on any violation.
       */
      case "GET_SELECTORS": {
        requireDaemon();
        requireCap("selectors", "selectors");
        return { selectors: await relay("GET_SELECTORS") };
      }

      /**
       * DESIGN_v2 §7.5: the live index lives in the daemon's memory, gossiped
       * whole. The query is matched there against records this machine already
       * holds, so nothing about it reaches the network.
       */
      case "LIVE_SEARCH": {
        requireDaemon();
        return {
          live: await relay("LIVE_SEARCH", {
            query: String(message.payload?.query || ""),
            min_publishers: Number(message.payload?.min_publishers) || 1,
            limit: Number(message.payload?.limit) || 100,
          }),
        };
      }

      case "SUGGEST": {
        requireDaemon();
        return {
          suggest: await relay("SUGGEST", {
            platform: String(message.payload?.platform || "yt"),
            seed_video_id: String(message.payload?.seed_video_id || ""),
            entropy: Number(message.payload?.entropy) || 0,
            limit: Number(message.payload?.limit) || 25,
          }),
        };
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
        if (!getBridge().helloOk || !hasCap("queue")) return { advanced: false };
        const tabId = sender?.tab?.id;
        const env = await getBridge().request("QUEUE_ADVANCE", {
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
        await tabs.update(tabId, { url: href });
        return { advanced: true, next: next.video_id };
      }

      // The watch queue (WO-064). The daemon owns it — the extension stores no
      // state — so all four verbs are a straight relay and the daemon answers
      // every one of them with the resulting queue.
      case "QUEUE_ADD":
      case "QUEUE_LIST":
      case "QUEUE_REMOVE":
      case "QUEUE_REORDER": {
        requireDaemon();
        requireCap("queue", "watch queue");
        return { queue: await relay(message.type, message.payload || {}) };
      }

      case "AGGREGATE_SUMMARY":
      case "EXPORT_BUNDLE":
      case "PEERS": {
        requireDaemon();
        return { bundle: await relay(message.type, {}, message.type) };
      }

      case "IMPORT_BUNDLE":
      case "FORGET_PEER": {
        requireDaemon();
        return { bundle: await relay(message.type, message.payload || {}, message.type) };
      }

      case "SCROLL_HISTORY": {
        requireDaemon();
        requireCap("scroll_history", "scroll history");
        return {
          history: await relay("SCROLL_HISTORY", {
            platform: String(message.payload?.platform || "tt"),
            limit: Number(message.payload?.limit) || 50,
          }),
        };
      }

      case "PANEL_CONTEXT_QUERY":
        return panel.contextForPanel(message.payload?.windowId ?? null);

      case "PANEL_NOT_HERE": {
        await panel.disablePanelForTab(sender?.tab?.id);
        return {};
      }

      case "GET_CONSENT":
        return { consent: await prefs.readConsent() };

      /**
       * The affirmative action, in the order WO-089 requires.
       *
       * Two records have to move together and they are not interchangeable.
       * The daemon's is the one that governs the network — it is a separate
       * process that starts with no browser attached, so a permission living
       * only in a browser profile cannot gate it. The browser's is the one the
       * content observer reads, so it can fail closed before sending a record
       * without waiting on an RPC.
       *
       * Granting goes daemon-first and only then enables observation locally,
       * so there is no window in which this profile is recording against a
       * disclosure the daemon has not acknowledged. If the daemon refuses or is
       * unreachable, nothing is enabled and the caller is told why — a consent
       * screen that said "recording is on" while the daemon sat at its gate
       * would be the misreport this ticket exists to remove.
       *
       * Declining writes only the local decision. There is nothing to withdraw
       * from a daemon that was never granted anything, and a decline must not
       * fail because the desktop app is not installed yet.
       */
      case "SET_CONSENT": {
        const want = message.payload?.consent;
        if (want === "granted") {
          requireDaemon();
          requireCap("network_consent", "consent");
          await relay(
            "SET_NETWORK_CONSENT",
            { accepted: true, revision: CONSENT_REVISION },
            "the desktop app did not accept the disclosure"
          );
        }
        const v = await prefs.writeConsent(want);
        // Content scripts gate on this; tell them without waiting for a reload.
        await broadcastToSiteTabs({
          type: "CONSENT_CHANGED",
          payload: { consent: v },
        });
        return { consent: v };
      }

      /**
       * The daemon's own view of the gate, for the consent screen and for the
       * migration banner an existing install sees when its acceptance predates
       * the current disclosure.
       */
      case "GET_NETWORK_CONSENT": {
        requireDaemon();
        requireCap("network_consent", "consent");
        return { daemon: await relay("GET_NETWORK_CONSENT") };
      }

      /**
       * Withdrawal, and the re-acceptance path for an existing install.
       *
       * Withdrawing stops the network but deliberately leaves the local
       * recording decision alone: they are different permissions, and turning
       * one off must not silently turn the other off too.
       */
      case "SET_NETWORK_CONSENT": {
        requireDaemon();
        requireCap("network_consent", "consent");
        const accepted = message.payload?.accepted === true;
        return {
          daemon: await relay("SET_NETWORK_CONSENT", {
            accepted,
            revision: accepted ? CONSENT_REVISION : 0,
          }),
        };
      }

      // Plain daemon relays. THUMBNAIL belongs here and was once stranded
      // above, adjacent to GET_CONSENT, so every thumbnail request fell through
      // and returned a consent value instead of an image. A fall-through is
      // valid JavaScript, so nothing warned — the panel simply rendered blank
      // boxes. Grouping every no-argument relay in one labelled block is what
      // keeps that from recurring silently.
      case "THUMBNAIL":
      case "GET_CONTRIBUTION":
      case "SET_CONTRIBUTION":
      case "GET_DISK_BUDGET":
      case "SET_DISK_BUDGET": {
        requireDaemon();
        if (
          message.type === "GET_CONTRIBUTION" ||
          message.type === "SET_CONTRIBUTION"
        ) {
          requireCap("contribution_runtime", "contribution controls");
        }
        return { daemon: await relay(message.type, message.payload || {}, message.type) };
      }

      // WO-086: the Level-2 contribution-impact panel. Its own capability
      // rather than piggybacking on contribution_runtime — a daemon can
      // negotiate the level control without yet implementing the impact
      // query, and the panel must fail closed to "unavailable", never to an
      // invented zero, when this specific capability is absent.
      case "GET_CONTRIBUTION_IMPACT":
      case "RESET_CONTRIBUTION_IMPACT": {
        requireDaemon();
        requireCap("contribution_impact", "contribution impact");
        return { daemon: await relay(message.type, message.payload || {}, message.type) };
      }

      case "ANALYSIS": {
        requireDaemon();
        return { analysis: await relay("ANALYSIS") };
      }

      /** WO-018: observational funnel for a video_id (daemon query only). */
      case "EXPLAIN_VIDEO": {
        const videoId = message.payload?.video_id;
        if (typeof videoId !== "string" || !videoId) {
          throw new Error("bad video_id");
        }
        requireDaemon();
        return { explain: await relay("EXPLAIN_VIDEO", { video_id: videoId }) };
      }

      default:
        throw new Error(`unknown type ${message.type}`);
    }
  }

  return { handle, flushBuffer, onBridgeStatus, onBridgeMessage };
}

/** A search-shaped RPC's query, or a rejection. Blank is not a search. */
function requireQuery(message) {
  const query = message.payload?.query;
  if (typeof query !== "string" || !query.trim()) throw new Error("bad query");
  return query;
}
