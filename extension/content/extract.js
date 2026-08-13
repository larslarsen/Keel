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
import {
  DEFAULT_SELECTORS,
  alternation,
  pick,
  pickAll,
} from "../lib/selectors.js";

/**
 * Matchers built from the config's vocabulary.
 *
 * The pattern shapes live here, compiled: a count is digits then an optional
 * magnitude, an age is a number then a unit then a trailing marker. Only the
 * words come from config, and every one is escaped on the way in — see
 * `escapeToken`. Cached per config object so a scan does not rebuild them.
 */
const matcherCache = new WeakMap();
function matchers(cfg) {
  let m = matcherCache.get(cfg);
  if (m) return m;
  const v = cfg.vocabulary || DEFAULT_SELECTORS.vocabulary;
  const ago = alternation(v.ago);
  m = {
    // "2 weeks ago", "1h ago" — a number, a unit, then the trailing marker.
    age: new RegExp(`(\\d+\\s*[a-z]{1,6}\\s+(?:${ago}))\\s*$`, "i"),
    // Words that make a metadata row worth reading at all.
    interesting: new RegExp(
      [alternation(v.views), ago, alternation(v.broadcast)]
        .filter(Boolean)
        .join("|"),
      "i",
    ),
    viewWords: new RegExp(`(?:${alternation(v.views)})`, "gi"),
    magnitudes: (v.magnitudes || []).map((x) => String(x).toLowerCase()),
    live: new RegExp(`\\b(?:${alternation(v.live)})\\b`, "i"),
    liveLoose: new RegExp(`(?:${alternation(v.live)})`, "i"),
    verified: new RegExp(`(?:${alternation(v.verified)})`, "i"),
    sponsored: new RegExp(`\\b(?:${alternation(v.sponsored)})\\b`, "i"),
    ageGated: new RegExp(`(?:${alternation(v.ageGated)})`, "i"),
  };
  matcherCache.set(cfg, m);
  return m;
}

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
export function parseViewCount(text, cfg = DEFAULT_SELECTORS) {
  if (text == null) return null;
  let s = String(text).trim().toLowerCase();
  if (!s) return 0;
  // "No views" and its equivalents: a count word with no number in front.
  if (/^no\s/i.test(s)) return 0;
  const mt = matchers(cfg);
  s = s.replace(mt.viewWords, "").replace(/,/g, "").trim();
  const magn = mt.magnitudes.join("");
  const m = s.match(new RegExp(`^([\\d.]+)\\s*([${magn}])?$`, "i"));
  if (!m) {
    const n = Number(s);
    return Number.isFinite(n) ? n : null;
  }
  let n = Number(m[1]);
  const u = (m[2] || "").toLowerCase();
  const idx = mt.magnitudes.indexOf(u);
  if (idx >= 0) n *= Math.pow(1000, idx + 1);
  return Math.round(n);
}

/** @param {string | null | undefined} href */
/**
 * The id a link points at, for one platform.
 *
 * Compiled rather than configured: an id's *shape* is a fact about the
 * platform, not a place to look, and a daemon able to redefine it could make
 * the extension record arbitrary strings as video ids.
 *
 * @param {string | null | undefined} href
 * @param {string} [platform] "yt" (default) or "tt"
 */
export function videoIdFromHref(href, platform = "yt") {
  if (!href) return null;
  const base = platform === "tt" ? "https://www.tiktok.com" : "https://www.youtube.com";
  if (platform === "tt") {
    // /@author/video/<digits>, and the id-only form the panel links to.
    const m = String(href).match(/\/video\/(\d{15,25})(?:[/?#]|$)/);
    return m ? m[1] : null;
  }
  try {
    const u = href.startsWith("http") ? new URL(href) : new URL(href, base);
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

/**
 * TikTok FYP embeds the real snowflake id on the player host:
 *   <div id="xgwrapper-0-7654326932623887630">
 * Confirmed 2026-08-11 against live capture (WO-063): matches
 * /api/item/availability/?itemIds=… and is stable per video across scroll.
 * Shape is compiled; the host element is found via config `playerId`.
 *
 * @param {string | null | undefined} idAttr
 * @param {string} [platform]
 */
export function videoIdFromPlayerId(idAttr, platform = "yt") {
  if (!idAttr || platform !== "tt") return null;
  const m = String(idAttr).match(/^xgwrapper-\d+-(\d{15,25})$/);
  return m ? m[1] : null;
}

/**
 * Sound id from a TikTok /music/<slug>-<digits> href. Shape compiled.
 * @param {string | null | undefined} href
 */
export function soundIdFromHref(href) {
  if (!href) return null;
  const m = String(href).match(/\/music\/[^/?#]*-(\d{10,25})(?:[/?#]|$)/);
  return m ? m[1] : null;
}

/**
 * Hashtag slug from /tag/<name>. Shape compiled.
 * @param {string | null | undefined} href
 */
export function hashtagFromHref(href) {
  if (!href) return null;
  try {
    const u = href.startsWith("http")
      ? new URL(href)
      : new URL(href, "https://www.tiktok.com");
    const m = u.pathname.match(/^\/tag\/([^/]+)$/);
    if (!m) return null;
    return decodeURIComponent(m[1]).replace(/^#/, "").toLowerCase() || null;
  } catch {
    return null;
  }
}

/** @param {string | null | undefined} href */
/**
 * The channel or author a link points at.
 *
 * TikTok has only handles, so the `/@name` branch already covers it — but the
 * YouTube-only `/channel/UC…` and `/user/` forms must not fire on a TikTok URL
 * that happens to contain them.
 *
 * @param {string | null | undefined} href
 * @param {string} [platform]
 */
export function channelIdFromHref(href, platform = "yt") {
  if (!href) return null;
  if (platform === "tt") {
    const h = String(href).match(/^\/?@([\w.-]+)/) ||
      String(href).match(/tiktok\.com\/@([\w.-]+)/);
    return h ? `@${h[1]}` : null;
  }
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
/**
 * Which platform a URL belongs to.
 *
 * Compiled in rather than configured, like the surface rules below: knowing
 * that tiktok.com is TikTok is not a selector, and a daemon that could redefine
 * it could point the extension at a site the user never agreed to.
 */
export function platformFromUrl(href) {
  try {
    const h = new URL(href, "https://www.youtube.com").hostname;
    if (/(^|\.)youtube\.com$/.test(h)) return "yt";
    if (/(^|\.)tiktok\.com$/.test(h)) return "tt";
  } catch {
    /* not a URL we can read */
  }
  return null;
}

/**
 * Which surface a URL is, per platform.
 *
 * WATCH_NEXT means "a single item with recommendations beside it", HOME means
 * "an unprompted feed". The names are YouTube's but the ideas are not: TikTok's
 * For You page is the same kind of object as YouTube's homepage — what the
 * recommender serves you unasked — and that is the comparison the whole project
 * exists to make.
 */
export function surfaceFromUrl(href) {
  const platform = platformFromUrl(href);
  try {
    const u = new URL(href, "https://www.youtube.com");
    if (platform === "yt") {
      if (u.pathname === "/watch") {
        const v = u.searchParams.get("v");
        return {
          platform,
          surface: "WATCH_NEXT",
          context_video_id: v && /^[\w-]{11}$/.test(v) ? v : null,
        };
      }
      // HOME only at the exact root (WO-010). /feed/*, /@*, /results* stay idle.
      if (u.pathname === "/" || u.pathname === "") {
        return { platform, surface: "HOME", context_video_id: null };
      }
    }
    if (platform === "tt") {
      // /@author/video/<id> is a single clip with a recommendation column.
      const m = u.pathname.match(/^\/@[^/]+\/video\/(\d{15,25})$/);
      if (m) {
        return { platform, surface: "WATCH_NEXT", context_video_id: m[1] };
      }
      // A live room reads as a watch page whose id is the room.
      const live = u.pathname.match(/^\/@[^/]+\/live$/);
      if (live) {
        return { platform, surface: "WATCH_NEXT", context_video_id: null };
      }
      // The For You feed, and the explicit /foryou path.
      if (u.pathname === "/" || u.pathname === "" || u.pathname === "/foryou") {
        return { platform, surface: "HOME", context_video_id: null };
      }
    }
  } catch {
    /* ignore */
  }
  return { platform, surface: null, context_video_id: null };
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
export function parseAge(text, cfg = DEFAULT_SELECTORS) {
  const m = String(text || "").match(matchers(cfg).age);
  return m ? m[1].replace(/\s+/g, " ").trim() : null;
}

/**
 * Is this element's text a LIVE *badge*, as opposed to text mentioning live?
 *
 * `mt.live` is `\blive\b` over an element's entire textContent, and the badge
 * containers include `[class*='badge']`, which matches wrappers as well as the
 * badge itself. So any wrapped text with the word "live" in it — "Streamed live
 * 12 hours ago" under a finished stream, a description, a title — reads as a
 * live broadcast.
 *
 * Confirmed against YouTube: a stream that ENDED at 20:55 was recorded by Keel
 * as LIVE at 22:17, 86 minutes later. YouTube's own player response for it says
 * `isLiveContent: true` with `isLiveNow: false` — livestream content, not live.
 *
 * A genuine badge is the word itself, sometimes with a dot: "LIVE", "● LIVE",
 * "EN DIRECT". It is never a sentence, and never carries a timestamp. So the
 * text must BE the label rather than contain it: bounded length, starting at
 * the word, and rejecting anything with a relative-time phrase in it. Genuine
 * badges are unaffected, which is the requirement WO-066 set — precision over
 * recall, because a false LIVE is a claim about the world that is simply untrue.
 *
 * @param {string} t trimmed textContent of a badge container
 * @param {ReturnType<typeof matchers>} mt
 */
export function isLiveBadgeText(t, mt) {
  if (!t || !mt.live.test(t)) return false;
  // A badge is a label. Anything long enough to be a sentence is not one.
  if (t.length > 24) return false;
  // "Streamed live 12 hours ago", "was live 3 days ago": a relative-time phrase
  // means this is metadata about a past broadcast, not a live indicator.
  if (mt.age.test(t) || /\bago\b/i.test(t)) return false;
  // "LIVE replay", "Live chat replay": YouTube's labels for an ended broadcast.
  // Both ytInitialData paths already reject these; the DOM path never did.
  if (/\b(?:replay|chat)\b/i.test(t)) return false;
  // The label must BEGIN with the word, after an optional dot or bullet — not
  // merely contain it somewhere.
  const head = t.replace(/^[\s\u2022\u25cf\u2219.\u00b7-]+/, "");
  return mt.live.test(head.slice(0, 12));
}

/** @param {Element} el */
export function extractBadges(el, cfg = DEFAULT_SELECTORS) {
  const out = new Set();
  const mt = matchers(cfg);
  for (const n of pickAll(el, cfg.badges.containers)) {
    const t = (n.textContent || "").trim();
    if (isLiveBadgeText(t, mt)) out.add("LIVE");
    if (mt.verified.test(t)) out.add("VERIFIED");
    if (mt.sponsored.test(t)) out.add("SPONSORED");
    if (mt.ageGated.test(t)) out.add("AGE_GATED");
  }
  // NOTE: only the word-bounded `mt.live` (above, on badge containers) sets
  // LIVE. A loose substring match on arbitrary overlay/description text
  // (the old `mt.liveLoose`) flagged non-live VODs whose title/description
  // contained "live"/"livestream"/"live chat" as LIVE — see WO-066. The live
  // badge is the only authoritative signal; precision over recall here.
  return [...out];
}

/**
 * ytd-compact-video-renderer shape (legacy sidebar card).
 * @param {Element} el
 */
function readCompactFields(el, cfg = DEFAULT_SELECTORS) {
  const c = cfg.shapes.compact;
  const thumb = pick(el, c.href);
  const video_id = videoIdFromHref(thumb?.getAttribute("href"), cfg.platform);
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
  const channel_id = channelIdFromHref(chA?.getAttribute("href"), cfg.platform);
  const channel_name =
    (chA?.textContent || "").replace(/\s+/g, " ").trim() || null;
  // Live cards may omit channel links; null is ok (channel_unknown).

  const durEl = pick(el, c.duration);
  let view_count = null;
  let published_at = null;
  for (const span of pickAll(el, c.metadata)) {
    const t = (span.textContent || "").trim();
    if (matchers(cfg).interesting.test(t) && /\d/.test(t)) {
      if (view_count == null) view_count = parseViewCount(t, cfg);
    }
    if (published_at == null) published_at = parseAge(t, cfg);
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
    hashtags: [],
    sound_id: null,
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
    const id = videoIdFromHref(a.getAttribute("href"), cfg.platform);
    if (!id) continue;
    if (!video_id) video_id = id;
    const t = (a.getAttribute("title") || a.textContent || "")
      .replace(/\s+/g, " ")
      .trim();
    // Prefer longer title-like text over bare thumbnails
    if (t && t.length > title.length && !/^[\d:]+$/.test(t)) title = t;
  }
  // TikTok FYP has no /video/<id> href — id lives on the player host
  // (config `playerId`). Shape of the id string is compiled; host is data.
  if (!video_id && c.playerId) {
    for (const n of pickAll(el, c.playerId)) {
      const id = videoIdFromPlayerId(n.getAttribute("id") || n.id, cfg.platform);
      if (id) {
        video_id = id;
        break;
      }
    }
  }
  if (!video_id) return null;

  if (!title) {
    const h = pick(el, c.title);
    title = (h?.getAttribute("title") || h?.textContent || h?.getAttribute("alt") || "")
      .replace(/\s+/g, " ")
      .trim();
  }
  if (!title) return null;

  let channel_id = null;
  let channel_name = null;
  const channelCandidates = c.channelLink
    ? pickAll(el, c.channelLink)
    : pickAll(el, c.links);
  for (const a of channelCandidates) {
    const href = a.getAttribute("href") || "";
    if (href.includes("watch?v=")) continue;
    const id = channelIdFromHref(href, cfg.platform);
    if (id) {
      channel_id = id;
      channel_name = (a.textContent || a.getAttribute("alt") || "")
        .replace(/\s+/g, " ")
        .trim() || null;
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
    if (!t || !matchers(cfg).interesting.test(t)) continue;
    if (view_count == null) {
      const num = t.match(/^([\d.,]+\s*[kmb]?)\b/i);
      if (num) view_count = parseViewCount(num[1]);
    }
    if (published_at == null) published_at = parseAge(t, cfg);
  }

    let hashtags = [];
  if (c.hashtag) {
    const seen = new Set();
    for (const a of pickAll(el, c.hashtag)) {
      const tag = hashtagFromHref(a.getAttribute("href"));
      if (tag && !seen.has(tag)) {
        seen.add(tag);
        hashtags.push(tag);
      }
    }
  }
  let sound_id = null;
  if (c.sound) {
    for (const a of pickAll(el, c.sound)) {
      sound_id = soundIdFromHref(a.getAttribute("href"));
      if (sound_id) break;
    }
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
    hashtags,
    sound_id,
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
    platform: ctx.platform ?? "yt",
    context_query_hash: ctx.context_query_hash ?? null,
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
    hashtags: Array.isArray(f.hashtags) ? f.hashtags : [],
    sound_id: f.sound_id ?? null,
    dwell_pct: f.dwell_pct ?? null,
    engagement: f.engagement ?? null,
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
