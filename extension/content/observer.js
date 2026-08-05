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
  MUTATION_CARD_SEL,
  extractFromContainer,
  extractFromYtInitialData,
  parseYtInitialDataFromDom,
  surfaceFromUrl,
} from "./extract.js";
import { browser } from "../lib/browser.js";
import { startHide } from "./hide.js";

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

async function send(type, payload) {
  try {
    await browser.runtime.sendMessage({ type, payload });
  } catch (err) {
    console.warn(LOG, "sendMessage", err?.message || err);
  }
}

function buildCtx() {
  const { surface, context_video_id } = surfaceFromUrl(location.href);
  if (surface === "WATCH_NEXT") {
    if (!context_video_id) return null;
    return {
      page_load_id: pageLoadId,
      observed_at: Date.now(),
      surface: "WATCH_NEXT",
      context_video_id,
      context_query_hash: null,
    };
  }
  if (surface === "HOME") {
    return {
      page_load_id: pageLoadId,
      observed_at: Date.now(),
      surface: "HOME",
      context_video_id: null,
      context_query_hash: null,
    };
  }
  return null;
}

/** Watch-next rail container. */
function containerWatch() {
  return (
    document.querySelector(
      "#related ytd-watch-next-secondary-results-renderer #items"
    ) ||
    document.querySelector("#related #items") ||
    document.querySelector("#related") ||
    document.querySelector("ytd-watch-next-secondary-results-renderer") ||
    document.querySelector("#secondary-inner #related") ||
    document.querySelector("#secondary-inner") ||
    null
  );
}

/** Home feed grid container — separate chain from the watch rail (WO-010). */
function containerHome() {
  return (
    document.querySelector(
      "ytd-browse[page-subtype='home'] ytd-rich-grid-renderer #contents"
    ) ||
    document.querySelector("ytd-rich-grid-renderer > #contents") ||
    document.querySelector("ytd-rich-grid-renderer #contents") ||
    document.querySelector("ytd-browse[page-subtype='home'] #contents") ||
    document.querySelector("ytd-rich-grid-renderer") ||
    null
  );
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
  const { impressions, failures } = extractFromContainer(root, ctx);

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
  const { impressions, failures } = extractFromYtInitialData(data, ctx);
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
      if (el.matches?.(MUTATION_CARD_SEL)) return true;
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

async function start() {
  if (armed) return;
  armed = true;
  lastHref = "";
  // Hide is independent of surface: CSS is scoped to watch #secondary +
  // home grid; off-surface pages are unaffected. Start before arming so
  // with-panel / always apply without waiting for the first scan.
  startHide().catch((e) => console.warn(LOG, "hide", e?.message || e));
  listenSpa();
  await onNavigate({ force: true });
  console.info(LOG, "observer armed");
}

start().catch((e) => console.error(LOG, e));
