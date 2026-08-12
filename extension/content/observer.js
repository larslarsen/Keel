// SPDX-License-Identifier: Apache-2.0
/**
 * WATCH_NEXT + HOME observer (ISOLATED). In-memory only; SW owns the bridge.
 * Off-surface pages (subscriptions, channel, search, …) stay fully idle.
 *
 * Scheduler is a non-starving throttle (not a resetting debounce):
 * mutations set a dirty flag; work runs at most every THROTTLE_MS and is
 * guaranteed to run after a quiet gap of at most THROTTLE_MS once dirty.
 */
import {
  mutationSel,
  extractFromContainer,
  extractFromYtInitialData,
  parseYtInitialDataFromDom,
  platformFromUrl,
  surfaceFromUrl,
} from "./extract.js";
import { browser } from "../lib/browser.js";
import {
  DEFAULT_SELECTORS,
  pick,
  validateSelectorConfig,
} from "../lib/selectors.js";
import { CONSENT_KEY, consentGranted } from "../lib/prefs.js";
import { startHide } from "./hide.js";
import { errText } from "../lib/errors.js";

const THROTTLE_MS = 750;
const MAX_ARM_ATTEMPTS = 10;
/** Bound MO callback work — never walk subtrees per added node. */
const MAX_NODES_PER_BATCH = 32;
const LOG = "[Keel]";

let pageLoadId = crypto.randomUUID();
let lastHref = "";
let gen = 0;
/** @type {MutationObserver | null} */
let mo = null;
/** @type {ReturnType<typeof setTimeout> | null} */
let throttleTimer = null;
/** @type {ReturnType<typeof setTimeout> | null} */
let armRetryTimer = null;
let armAttempts = 0;
let dirty = false;
let scanning = false;
let armed = false;
/** @type {Set<string>} video_ids emitted for current page_load */
let emittedIds = new Set();
/** Last failures count emitted for this page_load (dedup re-reports). */
let lastEmittedFailures = 0;
/** video_id → { channel_id, channel_name } from ytInitialData (DOM owns slot_index). */
let channelByVideo = new Map();
/** Parse ytInitialData at most once per navigation. */
let initialParsed = false;
/** Rail generation: bumps when YouTube replaces the suggestion set mid-page. */
let generation = 1;
/** slot_index → video_id of the last extracted rail (change detection). */
let lastRail = new Map();

/**
 * True once this content script has been orphaned by an extension reload.
 *
 * The old script keeps running in the page but its `browser.runtime` handle is
 * dead, so every call throws "Extension context invalidated". Left alone it
 * would keep a MutationObserver and its timers alive in a tab that can never
 * report anything again — burning CPU for nothing, which is the failure WO-006
 * was about.
 */
let orphaned = false;

function isContextInvalidated(err) {
  // Only the genuinely terminal case. "Receiving end does not exist" looks
  // similar but is routine in MV3 — the service worker sleeps and a message
  // sent before it wakes rejects with exactly that. Treating it as terminal
  // would shut down a perfectly healthy observer on a transient error, and it
  // would only ever be visible as recording quietly stopping.
  return /context invalidated/i.test(errText(err));
}

/** Stop everything. Only a page reload brings this tab back. */
function shutdown(reason) {
  if (orphaned) return;
  orphaned = true;
  gen += 1; // invalidate any in-flight scan
  if (mo) {
    mo.disconnect();
    mo = null;
  }
  if (throttleTimer != null) {
    clearTimeout(throttleTimer);
    throttleTimer = null;
  }
  if (armRetryTimer != null) {
    clearTimeout(armRetryTimer);
    armRetryTimer = null;
  }
  console.info(LOG, `stopped: ${reason} — reload the page to resume`);
}

/**
 * Selectors in use. Starts as the copy shipped with the extension and is
 * replaced by the daemon's if that one validates (WO-056).
 *
 * The bundled default is the fallback rather than the source of truth: it keeps
 * extraction working before the daemon answers, and survives a daemon offering
 * something unusable. A config that fails validation is discarded whole — half
 * a schema is worse than a stale one.
 */
let selectors = DEFAULT_SELECTORS;

async function loadSelectors() {
  const platform = platformFromUrl(location.href);
  if (!platform) return;
  try {
    const r = await browser.runtime.sendMessage({
      type: "GET_SELECTORS",
      payload: { platform },
    });
    const cfg = validateSelectorConfig(r?.ok ? r.selectors?.selectors : null);
    if (cfg) {
      selectors = cfg;
      console.info(LOG, `selectors v${cfg.version} for ${cfg.platform} from daemon`);
    } else if (r?.ok) {
      console.warn(LOG, "daemon selectors rejected; using the bundled set");
    }
  } catch {
    // Daemon not up yet. The bundled set is already in place.
  }
}

async function send(type, payload) {
  if (orphaned) return;
  try {
    await browser.runtime.sendMessage({ type, payload });
  } catch (err) {
    if (isContextInvalidated(err)) {
      // Expected after the extension is reloaded or updated. Say it once and
      // go quiet rather than warning on every mutation.
      shutdown("extension was reloaded");
      return;
    }
    console.warn(LOG, "sendMessage", errText(err));
  }
}

/**
 * The watch page's own title, from the tab title.
 *
 * `document.title` is "Video name - YouTube", which is the same string the user
 * sees. Read from the rendered page like everything else — no API call, no
 * player state, nothing about playback.
 */
function watchTitle() {
  const t = (document.title || "").replace(/\s*-\s*YouTube\s*$/, "").trim();
  if (!t || t === "YouTube") return null;
  return t.slice(0, 300);
}

function buildCtx() {
  // A scan queued before an SPA navigation can run after the URL has already
  // changed but before onNavigate has processed it. Building a context then
  // pairs the new page's surface with the previous page's page_load_id, which
  // files homepage rows under a watch page. Observed in the corpus: one page
  // load carrying both surfaces thirteen minutes apart.
  //
  // lastHref is the URL onNavigate last accepted, so a mismatch means the scan
  // belongs to a page that is already gone. Dropping it costs nothing — the
  // navigation about to be processed will scan the new page properly.
  if (location.href !== lastHref) return null;
  const { platform, surface, context_video_id } = surfaceFromUrl(location.href);
  if (surface === "WATCH_NEXT") {
    if (!context_video_id) return null;
    return {
      page_load_id: pageLoadId,
      observed_at: Date.now(),
      platform,
      surface: "WATCH_NEXT",
      context_video_id,
      // The title of the video being watched.
      //
      // Its id is already recorded — it is what every recommendation hangs off
      // — so the title is no further disclosure, just a public fact about an id
      // we hold. Without it the panel can only name the current video when it
      // happened to be recommended elsewhere first, which is not always: the
      // videos watched longest turn out to be the ones least likely to have
      // been captured in a rail.
      context_title: watchTitle(),
      context_query_hash: null,
    };
  }
  if (surface === "HOME") {
    return {
      page_load_id: pageLoadId,
      observed_at: Date.now(),
      platform,
      surface: "HOME",
      context_video_id: null,
      context_query_hash: null,
    };
  }
  return null;
}

/** Watch-next rail container — first configured selector that matches. */
function containerWatch() {
  return pick(document, selectors.containers.watch);
}

/** Home feed grid container — separate chain from the watch rail (WO-010). */
function containerHome() {
  return pick(document, selectors.containers.home);
}

/** Resolve observation root per surface — never documentElement. */
function container(surface) {
  const s = surface ?? surfaceFromUrl(location.href).surface;
  if (s === "WATCH_NEXT") return containerWatch();
  if (s === "HOME") return containerHome();
  return null;
}

/** Fill channel_id / channel_name from ytInitialData when the DOM omitted them. */
function enrichChannels(impressions) {
  for (const imp of impressions) {
    const ch = channelByVideo.get(imp.video_id);
    if (!ch) continue;
    if (!imp.channel_id) {
      imp.channel_id = ch.channel_id;
      imp.channel_unknown = false;
    }
    if (!imp.channel_name) imp.channel_name = ch.channel_name ?? null;
  }
  return impressions;
}

async function emit(impressions, failures) {
  enrichChannels(impressions);
  const fresh = impressions.filter((i) => {
    if (emittedIds.has(i.video_id)) return false;
    emittedIds.add(i.video_id);
    return true;
  });
  // Report failure delta only — raw re-sends inflate the diagnosis metric.
  const failDelta = Math.max(0, failures - lastEmittedFailures);
  if (failDelta > 0) lastEmittedFailures = failures;
  if (!fresh.length && !failDelta) return;
  await send("IMPRESSIONS", {
    impressions: fresh,
    failures: failDelta,
    generation,
  });
}

async function observeDom() {
  const ctx = buildCtx();
  if (!ctx) return;
  const root = container(ctx.surface);
  if (!root) return;
  const { impressions, failures } = extractFromContainer(root, ctx, selectors);

  // Rail change detection: when an occupied slot now holds a different
  // video, YouTube has replaced the suggestion set (e.g. the ~2s post-load
  // refresh). Bump the generation and re-emit the whole rail so the panel
  // shows the current set instead of an append-only pile of colliding slots.
  const rail = new Map();
  for (const imp of impressions) {
    if (!rail.has(imp.slot_index)) rail.set(imp.slot_index, imp.video_id);
  }
  let changed = false;
  if (lastRail.size > 0) {
    for (const [slot, video_id] of rail) {
      if (lastRail.get(slot) !== video_id) {
        changed = true;
        break;
      }
    }
  }
  if (changed) {
    generation++;
    emittedIds.clear();
  }
  lastRail = rail;

  await emit(impressions, failures);
}

async function observeInitial() {
  if (initialParsed) return;
  initialParsed = true;
  const ctx = buildCtx();
  // ytInitialData enrichment is WATCH_NEXT-only (secondaryResults lockups).
  if (!ctx || ctx.surface !== "WATCH_NEXT") return;
  const data = parseYtInitialDataFromDom(document);
  if (!data) return;
  const { impressions, failures } = extractFromYtInitialData(data, ctx, selectors);
  channelByVideo = new Map();
  for (const imp of impressions) {
    if (imp.channel_id && !imp.channel_unknown) {
      channelByVideo.set(imp.video_id, {
        channel_id: imp.channel_id,
        channel_name: imp.channel_name ?? null,
      });
    }
  }
  // JSON supplies channel_id; DOM owns slot_index. Do not emit JSON rows —
  // they would race DOM with a walk-order slot. Enrichment only.
  void failures;
}

/** Non-starving throttle: coalesce mutations, always flush within THROTTLE_MS. */
function schedule() {
  dirty = true;
  if (throttleTimer != null || scanning) return;
  throttleTimer = setTimeout(async () => {
    throttleTimer = null;
    if (!dirty) return;
    dirty = false;
    scanning = true;
    try {
      await observeDom();
    } catch (e) {
      console.warn(LOG, e);
    } finally {
      scanning = false;
      // If mutations arrived during the scan, schedule another pass
      if (dirty) schedule();
    }
  }, THROTTLE_MS);
}

/**
 * Only schedule when card-like nodes appear.
 * O(1) per node: matches() only — never querySelector a subtree in the
 * MO callback (that path pins the renderer at ~1,400 mutations/s).
 * Cap examined nodes per batch; if the cap is hit, schedule anyway —
 * a no-op observeDom pass is cheap; unbounded callback work is not.
 */
function mutationsRelevant(records) {
  let examined = 0;
  for (const r of records) {
    for (const n of r.addedNodes) {
      if (n.nodeType !== 1) continue;
      const el = /** @type {Element} */ (n);
      if (el.matches?.(mutationSel(selectors))) return true;
      if (++examined >= MAX_NODES_PER_BATCH) return true;
    }
  }
  return false;
}

/** Tear down observer + timers so off-surface pages leave nothing running. */
function disarm() {
  if (mo) {
    mo.disconnect();
    mo = null;
  }
  if (armRetryTimer != null) {
    clearTimeout(armRetryTimer);
    armRetryTimer = null;
  }
  if (throttleTimer != null) {
    clearTimeout(throttleTimer);
    throttleTimer = null;
  }
  dirty = false;
  scanning = false;
  armAttempts = 0;
}

function armMo() {
  if (orphaned) return;
  if (mo) mo.disconnect();
  mo = null;
  if (armRetryTimer != null) {
    clearTimeout(armRetryTimer);
    armRetryTimer = null;
  }
  const ctx = buildCtx();
  if (!ctx) {
    disarm();
    return;
  }
  const root = container(ctx.surface);
  if (!root) {
    armAttempts++;
    if (armAttempts > MAX_ARM_ATTEMPTS) {
      console.warn(LOG, "armMo: container not ready after", MAX_ARM_ATTEMPTS, "attempts");
      return;
    }
    armRetryTimer = setTimeout(() => {
      armRetryTimer = null;
      armMo();
    }, THROTTLE_MS);
    return;
  }
  armAttempts = 0;
  mo = new MutationObserver((records) => {
    if (mutationsRelevant(records)) schedule();
  });
  mo.observe(root, { childList: true, subtree: true });
  schedule();
}

async function onNavigate({ force = false } = {}) {
  if (orphaned) return;
  const href = location.href;
  if (!force && href === lastHref && armed) return;
  lastHref = href;
  const g = ++gen;
  pageLoadId = crypto.randomUUID();
  emittedIds = new Set();
  lastEmittedFailures = 0;
  channelByVideo = new Map();
  initialParsed = false;
  generation = 1;
  lastRail = new Map();
  dirty = false;
  armAttempts = 0;
  // Drop any prior arm/throttle before deciding the new surface.
  if (armRetryTimer != null) {
    clearTimeout(armRetryTimer);
    armRetryTimer = null;
  }
  if (throttleTimer != null) {
    clearTimeout(throttleTimer);
    throttleTimer = null;
  }
  if (mo) {
    mo.disconnect();
    mo = null;
  }

  const ctx = buildCtx();
  await send("PAGE_CONTEXT", {
    platform: platformFromUrl(href),
    surface: ctx?.surface ?? null,
    pageLoadId,
    href,
  });

  // Off-surface: fully idle — no MO, no ytInitialData parse, no retry timers.
  if (!ctx) return;

  await observeInitial();
  if (g !== gen) return;
  await observeDom();
  if (g !== gen) return;
  armMo();
}

function listenSpa() {
  document.addEventListener("yt-navigate-finish", () => {
    onNavigate().catch((e) => console.warn(LOG, e));
  });
  const wrap = (method) => {
    const orig = history[method];
    if (typeof orig !== "function") return;
    history[method] = function (...args) {
      const ret = orig.apply(this, args);
      setTimeout(() => {
        if (location.href !== lastHref) onNavigate().catch(() => {});
      }, 100);
      return ret;
    };
  };
  wrap("pushState");
  wrap("replaceState");
  window.addEventListener("popstate", () => {
    setTimeout(() => {
      if (location.href !== lastHref) onNavigate().catch(() => {});
    }, 100);
  });
}


/**
 * Whether the user has agreed to recording.
 *
 * Required by Chrome Web Store policy, which as of 1 August 2026 requires all
 * data collection to be prominently disclosed and affirmatively consented to
 * inside the product's own interface, before collection begins. The definition
 * of user data has no carve-out for data that never leaves the device.
 */
async function consented() {
  try {
    const bag = await browser.storage?.local?.get(CONSENT_KEY);
    return consentGranted(bag?.[CONSENT_KEY]);
  } catch {
    return false; // unreadable means no
  }
}

// The panel's Back control. Chromium prunes extension-created history entries
// from the Back button, but not from history.back() called inside the page —
// see WO-042. Registered at module scope so it works regardless of consent:
// navigating is not observing.
browser.runtime.onMessage.addListener((msg) => {
  if (msg?.type === "GO_BACK") history.back();
});

/**
 * Scan immediately when a recommendation is clicked.
 *
 * Scans are throttled to THROTTLE_MS, and YouTube's rail loads lazily as you
 * scroll. A card that appears and is clicked inside that window is never
 * scanned — so the recommendation the user actually *took* is the one most
 * likely to be missing. Measured on a live corpus: nine videos were watched but
 * never captured as recommendations, and the four watched longest were among
 * them. Those are precisely the ones clicked promptly.
 *
 * pointerdown rather than click, because it fires before navigation starts.
 * The extraction is synchronous DOM work; only the send is async, and it is
 * fire-and-forget.
 *
 * This records the whole rail, not just the clicked card, so slot indices stay
 * consistent with every other scan.
 */
document.addEventListener(
  "pointerdown",
  (e) => {
    if (orphaned || e.button !== 0) return;
    const t = e.target;
    if (!t || typeof t.closest !== "function") return;
    if (!t.closest('a[href*="/watch?v="]')) return;
    observeDom().catch(() => {});
  },
  true,
);

/**
 * Queue autoplay (WO-064): tell the worker when the watched video finishes.
 *
 * This is the one place Keel reads playback state, and it is worth being clear
 * about why that is allowed here when `watchTitle` explicitly refuses to. The
 * `ended` event is not recorded, not counted, and never leaves the machine — it
 * is a trigger for a list the user built by hand. Nothing about the player is
 * driven: no control is clicked, no autoplay toggle is touched, no API is
 * called. The daemon decides whether the finished video was queued, and the
 * only action taken is an ordinary tab navigation, the same one the Play button
 * performs.
 *
 * `ended` does not bubble, so it is caught in the capture phase at the document
 * — one listener that survives YouTube replacing the <video> element on every
 * SPA navigation, which a listener bound to the element would not.
 */
let lastEndedId = "";
function watchForEnd() {
  document.addEventListener(
    "ended",
    (e) => {
      if (orphaned) return;
      if (!(e.target instanceof HTMLMediaElement)) return;
      const { platform, surface, context_video_id } = surfaceFromUrl(location.href);
      if (surface !== "WATCH_NEXT" || !context_video_id) return;
      // Media elements can fire `ended` more than once for one playthrough, and
      // a second one would consume the video we just navigated to.
      if (context_video_id === lastEndedId) return;
      lastEndedId = context_video_id;
      send("VIDEO_ENDED", { video_id: context_video_id, platform });
    },
    true,
  );
}

async function start() {
  if (armed) return;
  armed = true;
  lastHref = "";
  // Hide is independent of surface: CSS is scoped to watch #secondary +
  // home grid; off-surface pages are unaffected. Start before arming so it
  // applies without waiting for the first scan.
  startHide().catch((e) => console.warn(LOG, "hide", errText(e)));
  await loadSelectors();

  if (!(await consented())) {
    console.info(LOG, "no consent — not recording");
    // Tell the worker this is a YouTube tab anyway, so the side panel can be
    // opened from the toolbar. Without this the extension deadlocks: the
    // control for granting consent lives inside the panel, and the panel was
    // only enabled for tabs that had reported themselves.
    //
    // This reports the page as off-surface and observes nothing. Enabling a
    // surface is not collecting data.
    await send("PAGE_CONTEXT", {
      platform: platformFromUrl(location.href),
      surface: null,
      pageLoadId: null,
      href: location.href,
    });
    browser.runtime.onMessage.addListener((msg) => {
      if (msg?.type === "CONSENT_CHANGED" && consentGranted(msg.payload?.consent)) {
        armed = false;
        start().catch(() => {});
      }
    });
    return;
  }

  listenSpa();
  watchForEnd();
  await onNavigate({ force: true });
  console.info(LOG, "observer armed");
}

start().catch((e) => console.error(LOG, e));
