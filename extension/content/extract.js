// SPDX-License-Identifier: Apache-2.0
/**
 * Pure DOM extractors for WATCH_NEXT and HOME.
 * Supports ytd-compact-video-renderer, yt-lockup-view-model, ytd-rich-grid-media.
 * ytInitialData path (WATCH_NEXT enrichment): extract_yt.js. SEARCH is out of scope.
 */
export {
  extractBalancedObject,
  parseYtInitialDataFromDom,
  extractFromYtInitialData,
} from "./extract_yt.js";

/** Video card selectors — keep in sync with RENDERER_KEYS in extract_yt.js */
import { DEFAULT_SELECTORS, pick, pickAll } from "../lib/selectors.js";

/**
 * Selectors are configuration, not constants (WO-056).
 *
 * Every reader takes a config and defaults to the one shipped with the
 * extension, so a caller that has fetched a newer config from the daemon can
 * pass it and everything else keeps working. Behaviours stay here; the config
 * only says where to look.
 */
export const CARD_SEL = DEFAULT_SELECTORS.cards;

/**
 * HOME grid units that each consume one slot_index (row-major).
 * Non-video units (section/shelf) consume a slot without emitting.
 */
export const HOME_ITEM_SEL = DEFAULT_SELECTORS.homeItems;

/**
 * MutationObserver relevance: cards plus home grid units.
 * O(1) matches() only in the callback — never querySelector a subtree.
 */
export const MUTATION_CARD_SEL = `${CARD_SEL}, ${HOME_ITEM_SEL}`;

/** Card selector for a given config. */
export function cardSel(cfg = DEFAULT_SELECTORS) {
  return cfg.cards;
}
/** Home grid unit selector for a given config. */
export function homeItemSel(cfg = DEFAULT_SELECTORS) {
  return cfg.homeItems;
}
/** What a MutationObserver should consider relevant for a given config. */
export function mutationSel(cfg = DEFAULT_SELECTORS) {
  return `${cfg.cards}, ${cfg.homeItems}`;
}

/** @param {string | null | undefined} text */
export function parseDuration(text) {
  if (text == null) return null;
  const s = String(text).trim();
  if (!s || !/^[\d:]+$/.test(s)) return null;
  const p = s.split(":").map(Number);
  if (p.some((n) => !Number.isFinite(n))) return null;
  if (p.length === 2) return p[0] * 60 + p[1];
  if (p.length === 3) return p[0] * 3600 + p[1] * 60 + p[2];
  return p.length === 1 ? p[0] : null;
}

/** @param {string | null | undefined} text */
export function parseViewCount(text) {
  if (text == null) return null;
  let s = String(text).trim().toLowerCase();
  if (!s || s === "no views") return 0;
  s = s.replace(/views?|watching/g, "").replace(/,/g, "").trim();
  const m = s.match(/^([\d.]+)\s*([kmb])?$/i);
  if (!m) {
    const n = Number(s);
    return Number.isFinite(n) ? n : null;
  }
  let n = Number(m[1]);
  const u = (m[2] || "").toLowerCase();
  if (u === "k") n *= 1e3;
  else if (u === "m") n *= 1e6;
  else if (u === "b") n *= 1e9;
  return Math.round(n);
}

/** @param {string | null | undefined} href */
export function videoIdFromHref(href) {
  if (!href) return null;
  try {
    const u = href.startsWith("http")
      ? new URL(href)
      : new URL(href, "https://www.youtube.com");
    if (u.pathname === "/watch") {
      const v = u.searchParams.get("v");
      if (v && /^[\w-]{11}$/.test(v)) return v;
    }
  } catch {
    /* fall through */
  }
  const m = String(href).match(/[?&]v=([\w-]{11})/);
  return m ? m[1] : null;
}

/** @param {string | null | undefined} href */
export function channelIdFromHref(href) {
  if (!href) return null;
  try {
    const u = href.startsWith("http")
      ? new URL(href)
      : new URL(href, "https://www.youtube.com");
    const ch = u.pathname.match(/^\/channel\/(UC[\w-]{22})/);
    if (ch) return ch[1];
    const h = u.pathname.match(/^\/@([\w.-]+)/);
    if (h) return `@${h[1]}`;
    const user = u.pathname.match(/^\/(?:user|c)\/([\w.-]+)/);
    if (user) return user[0].slice(1);
  } catch {
    /* fall through */
  }
  return null;
}

/** @param {string} href */
export function surfaceFromUrl(href) {
  try {
    const u = new URL(href, "https://www.youtube.com");
    if (u.pathname === "/watch") {
      const v = u.searchParams.get("v");
      return {
        surface: "WATCH_NEXT",
        context_video_id: v && /^[\w-]{11}$/.test(v) ? v : null,
      };
    }
    // HOME only at the exact root (WO-010). /feed/*, /@*, /results* stay idle.
    if (u.pathname === "/" || u.pathname === "") {
      return { surface: "HOME", context_video_id: null };
    }
  } catch {
    /* ignore */
  }
  return { surface: null, context_video_id: null };
}

/**
 * Pull just the age out of a metadata line.
 *
 * YouTube packs several facts into one row, and which ones vary by surface, so
 * taking the whole string stored things like "Liberal Hivemind 22K 1h ago" and
 * "Streamed 2h ago" as if they were dates — 27% of rows on a live corpus. That
 * was invisible while the field was not displayed and obviously wrong once it
 * was.
 *
 * Anchoring to the end takes the age and nothing else. The "Streamed" marker is
 * dropped: a past livestream is already identifiable from its badges, and the
 * value here should be one comparable thing.
 *
 * @param {string} text
 * @returns {string | null} e.g. "2w ago", "15 min ago"
 */
export function parseAge(text) {
  const m = String(text || "").match(/(\d+\s*[a-z]{1,6}\s+ago)\s*$/i);
  return m ? m[1].replace(/\s+/g, " ").trim() : null;
}

/** @param {Element} el */
export function extractBadges(el, cfg = DEFAULT_SELECTORS) {
  const out = new Set();
  for (const n of pickAll(el, cfg.badges.containers)) {
    const t = (n.textContent || "").toUpperCase();
    if (/\bLIVE\b/.test(t)) out.add("LIVE");
    if (/VERIFIED|OFFICIAL ARTIST/.test(t)) out.add("VERIFIED");
    if (/SPONSORED|PAID|\bAD\b/.test(t)) out.add("SPONSORED");
    if (/AGE|MEMBERS ONLY|18\+/.test(t)) out.add("AGE_GATED");
  }
  const overlay = pick(el, cfg.badges.overlay);
  if (/LIVE/i.test(overlay?.textContent || "")) out.add("LIVE");
  return [...out];
}

/**
 * ytd-compact-video-renderer shape (legacy sidebar card).
 * @param {Element} el
 */
function readCompactFields(el, cfg = DEFAULT_SELECTORS) {
  const c = cfg.shapes.compact;
  const thumb = pick(el, c.href);
  const video_id = videoIdFromHref(thumb?.getAttribute("href"));
  if (!video_id) return null;

  const titleEl = pick(el, c.title);
  const title = (
    titleEl?.getAttribute("title") ||
    titleEl?.textContent ||
    thumb?.getAttribute("title") ||
    ""
  )
    .replace(/\s+/g, " ")
    .trim();
  if (!title) return null;

  const chA = pick(el, c.channelLink);
  const channel_id = channelIdFromHref(chA?.getAttribute("href"));
  const channel_name =
    (chA?.textContent || "").replace(/\s+/g, " ").trim() || null;
  // Live cards may omit channel links; null is ok (channel_unknown).

  const durEl = pick(el, c.duration);
  let view_count = null;
  let published_at = null;
  for (const span of pickAll(el, c.metadata)) {
    const t = (span.textContent || "").trim();
    if (/view|watching/i.test(t)) view_count = parseViewCount(t);
    else if (published_at == null) published_at = parseAge(t);
  }

  return {
    video_id,
    channel_id: channel_id || null,
    channel_unknown: !channel_id,
    channel_name,
    title,
    duration_s: parseDuration(durEl?.textContent),
    view_count,
    published_at,
    badges: extractBadges(el, cfg),
  };
}

/**
 * yt-lockup-view-model shape (current watch-next cards).
 * @param {Element} el
 */
function readLockupFields(el, cfg = DEFAULT_SELECTORS) {
  const c = cfg.shapes.lockup;
  // Prefer any watch href on the lockup
  let video_id = null;
  let title = "";
  for (const a of pickAll(el, c.links)) {
    const id = videoIdFromHref(a.getAttribute("href"));
    if (!id) continue;
    if (!video_id) video_id = id;
    const t = (a.getAttribute("title") || a.textContent || "")
      .replace(/\s+/g, " ")
      .trim();
    // Prefer longer title-like text over bare thumbnails
    if (t && t.length > title.length && !/^[\d:]+$/.test(t)) title = t;
  }
  if (!video_id) return null;

  if (!title) {
    const h = pick(el, c.title);
    title = (h?.getAttribute("title") || h?.textContent || "")
      .replace(/\s+/g, " ")
      .trim();
  }
  if (!title) return null;

  let channel_id = null;
  let channel_name = null;
  for (const a of pickAll(el, c.links)) {
    const href = a.getAttribute("href") || "";
    if (href.includes("watch?v=")) continue;
    const id = channelIdFromHref(href);
    if (id) {
      channel_id = id;
      channel_name = (a.textContent || "").replace(/\s+/g, " ").trim() || null;
      break;
    }
  }
  // Real lockup cards have no channel link in the DOM; the display name is the
  // first metadata row with no leading icon (row 2 is "1.2K views · 3 days
  // ago"). Capture it so the panel can show who the video is from.
  if (!channel_name) {
    for (const row of pickAll(el, c.metadataRow)) {
      if (pick(row, c.leadingIcon)) continue;
      const t = (row.textContent || "").replace(/\s+/g, " ").trim();
      // Count/date rows ("578 watching", "21K") are never channel names.
      if (!t || /^[\d.,]+\s*[kmb]?\b/i.test(t)) continue;
      channel_name = t;
      break;
    }
  }

  const durEl = pick(el, c.duration);
  let duration_s = parseDuration(durEl?.textContent);
  // Some lockups put duration in a span with aria or text like "10:32"
  if (duration_s == null) {
    for (const n of pickAll(el, c.durationScan)) {
      const d = parseDuration((n.textContent || "").trim());
      if (d != null && d > 0) {
        duration_s = d;
        break;
      }
    }
  }

  // Lockup metadata rows are divs, not spans, and the rail omits the word
  // "views" entirely: rows read "578 watching", "21K 1h ago", sometimes
  // "1.2K views • 3 days ago". Scanning spans for /view/ left view_count null
  // on every non-live card.
  let view_count = null;
  let published_at = null;
  for (const n of pickAll(el, c.metadata)) {
    const t = (n.textContent || "").replace(/\s+/g, " ").trim();
    if (!t || !/view|watching|ago|streamed|premier/i.test(t)) continue;
    if (view_count == null) {
      const num = t.match(/^([\d.,]+\s*[kmb]?)\b/i);
      if (num) view_count = parseViewCount(num[1]);
    }
    if (published_at == null) published_at = parseAge(t);
  }

  return {
    video_id,
    channel_id: channel_id || null,
    channel_unknown: !channel_id,
    channel_name,
    title,
    duration_s,
    view_count,
    published_at,
    badges: extractBadges(el, cfg),
  };
}

/**
 * One interface for card component shapes.
 * @param {Element} el
 * @returns {object | null}
 */
export function readCardFields(el, cfg = DEFAULT_SELECTORS) {
  if (!el) return null;
  const lockup = cfg.shapes.lockup;
  const compact = cfg.shapes.compact;
  // Which shape this is comes from the config's `match` selectors, not from a
  // hardcoded tag name — that is the whole point of Option B. matches() is
  // cheap and never walks the subtree.
  const is = (sel) => {
    try {
      return typeof el.matches === "function" && el.matches(sel);
    } catch {
      return false;
    }
  };
  if (is(lockup.match)) return readLockupFields(el, cfg);
  if (is(compact.match)) return readCompactFields(el, cfg);

  // A grid item wraps the real card; look one level in before giving up.
  const nestedLockup = el.querySelector?.(lockup.match);
  if (nestedLockup) return readLockupFields(nestedLockup, cfg);
  const nestedCompact = el.querySelector?.(compact.match);
  if (nestedCompact) return readCompactFields(nestedCompact, cfg);

  // Neither shape matched. Try both anyway: a card whose wrapper was renamed
  // still parses if its innards are recognisable, which is the difference
  // between degraded extraction and none.
  return readCompactFields(el, cfg) || readLockupFields(el, cfg);
}

const OBSERVED_SURFACES = new Set(["WATCH_NEXT", "HOME"]);

/**
 * @param {Element} el
 * @param {object} ctx
 * @param {object | null} [fields] pre-parsed fields (avoid double parse)
 */
export function extractFromElement(el, ctx, fields = undefined, cfg = DEFAULT_SELECTORS) {
  if (!el || !ctx || typeof ctx.slot_index !== "number" || ctx.slot_index < 0) {
    return null;
  }
  if (!ctx.page_load_id || !OBSERVED_SURFACES.has(ctx.surface)) return null;
  const f = fields !== undefined ? fields : readCardFields(el, cfg);
  if (!f) return null;
  return {
    page_load_id: ctx.page_load_id,
    observed_at: ctx.observed_at ?? Date.now(),
    surface: ctx.surface,
    context_video_id: ctx.context_video_id ?? null,
    context_title: ctx.context_title ?? null,
    context_query_hash: null,
    slot_index: ctx.slot_index,
    video_id: f.video_id,
    channel_id: f.channel_id ?? null,
    channel_unknown: Boolean(f.channel_unknown || !f.channel_id),
    channel_name: f.channel_name ?? null,
    title: f.title,
    duration_s: f.duration_s ?? null,
    view_count: f.view_count ?? null,
    published_at: f.published_at ?? null,
    badges: f.badges || [],
  };
}

/**
 * HOME grid extraction.
 * slot_index is row-major over grid children (left→right, then down): each
 * ytd-rich-item-renderer / ytd-rich-section-renderer / shelf consumes one
 * index whether or not it yields a video. Non-video units (Shorts rows,
 * shelves, ads, playlist/channel cards) must still advance the index so
 * every video below them keeps a stable position (WO-010 / WO-004 §4).
 * @param {ParentNode} root
 * @param {object} ctx
 */
export function extractFromHomeContainer(root, ctx, cfg = DEFAULT_SELECTORS) {
  // Prefer the rich-grid's own #contents (direct parent of rich-items).
  // An outer ytd-browse #contents also exists and must not be used — it has
  // a single child (the grid) and would collapse every page to one slot.
  //
  // Order matters (WO-010 live QA): shelves nest their own
  // ytd-rich-grid-renderer. If root is already the outer grid #contents,
  // descending with querySelector finds a shelf's inner grid first and
  // returns ~3 candidates / 0 impressions. Check "root is already
  // contents" before any descendant search.
  /** @type {ParentNode | null} */
  let contents = null;
  if (root && root.nodeType === 1) {
    const el = /** @type {Element} */ (root);
    const isGridContents =
      el.id === "contents" &&
      (el.parentElement?.tagName || "").toLowerCase() ===
        "ytd-rich-grid-renderer";
    if (isGridContents) {
      contents = el;
    } else if (el.matches?.("ytd-rich-grid-renderer")) {
      // Direct child only — never a shelf nested deeper in this grid.
      contents =
        el.querySelector(":scope > #contents") || el.querySelector("#contents");
    } else {
      // Root is document / browse shell: take the first grid #contents in
      // tree order (the page grid). Prefer direct-child combinator so a
      // stray match cannot jump into a shelf when the shell is the grid.
      contents =
        el.querySelector?.(":scope > ytd-rich-grid-renderer > #contents") ||
        el.querySelector?.("ytd-rich-grid-renderer > #contents") ||
        el.querySelector?.("ytd-rich-grid-renderer #contents") ||
        null;
    }
  }
  if (!contents) {
    contents =
      root.querySelector?.("ytd-rich-grid-renderer > #contents") ||
      root.querySelector?.("ytd-rich-grid-renderer #contents") ||
      root;
  }

  /** @type {Element[]} */
  let items = [];
  if (contents && contents.children && contents.children.length) {
    items = [...contents.children].filter((n) => n.nodeType === 1);
  } else {
    items = [...root.querySelectorAll(cfg.homeItems)];
  }

  const impressions = [];
  let failures = 0;
  const seen = new Set();

  for (let slot_index = 0; slot_index < items.length; slot_index++) {
    const item = items[slot_index];
    const tag = (item.tagName || "").toLowerCase();

    // Section / shelf / nudge: consume slot, no impression, not a failure.
    if (
      tag === "ytd-rich-section-renderer" ||
      tag === "ytd-rich-shelf-renderer" ||
      tag === "ytd-feed-nudge-renderer"
    ) {
      continue;
    }

    const card =
      (item.matches?.(cfg.cards) ? item : null) ||
      item.querySelector?.(cfg.cards) ||
      item.querySelector?.("ytd-rich-grid-media") ||
      item;

    const f = readCardFields(card, cfg);
    if (!f) {
      // Video-looking card that failed to parse counts as a failure; pure
      // non-video rich-items (channel/playlist tiles) just consume the slot.
      if (item.querySelector?.("a[href*='watch?v=']")) failures += 1;
      continue;
    }
    if (seen.has(f.video_id)) continue;
    seen.add(f.video_id);
    const imp = extractFromElement(
      card,
      { ...ctx, surface: "HOME", context_video_id: null, slot_index },
      f,
      cfg
    );
    if (imp) impressions.push(imp);
    else failures += 1;
  }
  return { impressions, failures, candidates: items.length };
}

/**
 * Extract impressions. slot_index = unfiltered candidate position (gaps for skips).
 * @param {ParentNode} root
 * @param {object} ctx
 */
export function extractFromContainer(root, ctx, cfg = DEFAULT_SELECTORS) {
  if (ctx?.surface === "HOME") return extractFromHomeContainer(root, ctx);

  const cards = [...root.querySelectorAll(cfg.cards)];
  const impressions = [];
  let failures = 0;
  const seen = new Set();

  for (let slot_index = 0; slot_index < cards.length; slot_index++) {
    const card = cards[slot_index];
    const f = readCardFields(card, cfg);
    if (!f) {
      failures += 1;
      continue;
    }
    if (seen.has(f.video_id)) continue; // gap: no second row
    if (ctx.context_video_id && f.video_id === ctx.context_video_id) continue;
    seen.add(f.video_id);
    const imp = extractFromElement(
      card,
      { ...ctx, surface: "WATCH_NEXT", slot_index },
      f,
      cfg
    );
    if (imp) impressions.push(imp);
    else failures += 1;
  }
  return { impressions, failures, candidates: cards.length };
}
