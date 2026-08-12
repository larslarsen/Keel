/**
 * Selector configuration (WO-056, DESIGN_BOOTSTRAP "Option B").
 *
 * The extension keeps the parsing engine and holds no selectors of its own. A
 * config says *where* to look; every behaviour — read this attribute, fall back
 * to text, parse a duration, prefer the longest candidate — stays compiled in
 * here where a store reviewer can see it.
 *
 * **The line this must not cross:** the extension may download data, never
 * logic. A config is CSS selector strings and nothing else. No regexes, no
 * expressions, no branching, no field the engine does not already understand.
 * `validateSelectorConfig` enforces that and rejects the whole config on any
 * violation rather than accepting part of it.
 *
 * **What this buys, stated honestly.** YouTube renames a class or moves a node,
 * the daemon ships a new config, and the extension binary does not change. That
 * covers most breakage, because most breakage is selector-level. It does not
 * cover structural change: if cards move into shadow roots, or stop being
 * elements the compiled behaviours can walk, no config fixes it and the
 * extension has to be republished. The claim is "most breaks are config-only",
 * not "never again".
 *
 * The default below ships with the extension as data, so extraction works
 * before the daemon has answered and survives a daemon that offers nothing
 * usable.
 */

/** Fields whose value is a single selector string. */
const STRING_FIELDS = new Set(["cards", "homeItems", "match"]);

/** Fields whose value is an ordered list of candidate selectors. */
const LIST_FIELDS = new Set([
  "watch",
  "home",
  "href",
  "title",
  "channelLink",
  "duration",
  "durationScan",
  "metadata",
  "metadataRow",
  "leadingIcon",
  "links",
  // TikTok FYP: player host whose id attribute embeds the video id
  // (e.g. id="xgwrapper-0-7654326932623887630"). Where to look is data;
  // how to parse the id string is compiled in extract.js.
  "playerId",
  "hashtag",
  "sound",
  "containers",
  "overlay",
]);

const MAX_SELECTOR_LEN = 300;
const MAX_LIST_LEN = 12;

/** The YouTube selectors, as data. */
export const DEFAULT_SELECTORS = Object.freeze({
  version: 1,
  platform: "yt",

  // Card components on a watch page rail.
  cards: "ytd-compact-video-renderer, yt-lockup-view-model, ytd-rich-grid-media",
  // Home grid units. Each consumes one slot_index whether or not it yields a
  // video, so shelves and Shorts rows must be listed here too.
  homeItems:
    "ytd-rich-item-renderer, ytd-rich-section-renderer, ytd-rich-shelf-renderer",

  shapes: {
    // Legacy sidebar card.
    compact: {
      match: "ytd-compact-video-renderer",
      href: ["a#thumbnail[href]", "a[href*='watch?v=']"],
      title: ["#video-title", "a#video-title-link", "[id='video-title']"],
      channelLink: [
        "ytd-channel-name a[href]",
        "#channel-name a[href]",
        "a[href^='/channel/']",
        "a[href^='/@']",
      ],
      duration: [
        "ytd-thumbnail-overlay-time-status-renderer #text",
        "span.ytd-thumbnail-overlay-time-status-renderer",
        "#time-status #text",
        "badge-shape .yt-badge-shape__text",
      ],
      metadata: ["#metadata-line span", "#metadata-line yt-formatted-string"],
    },

    // Current watch-next and home cards.
    lockup: {
      match: "yt-lockup-view-model, ytd-rich-grid-media",
      links: ["a[href]"],
      title: [
        "h3",
        ".yt-lockup-metadata-view-model__title",
        "[class*='metadata-view-model'] a",
      ],
      metadataRow: [".ytContentMetadataViewModelMetadataRow"],
      leadingIcon: [".ytContentMetadataViewModelLeadingIcon"],
      duration: [
        "badge-shape .yt-badge-shape__text",
        "[class*='badge-shape']",
        "yt-thumbnail-overlay-badge-view-model",
      ],
      durationScan: ["span", "badge-shape"],
      metadata: [
        "span",
        "yt-formatted-string",
        "[class*='ContentMetadataViewModelMetadataRow']",
      ],
    },
  },

  // Where each surface's cards live. Ordered: the first that matches wins, so
  // the most specific comes first and the loosest is the last resort.
  containers: {
    watch: [
      "#related ytd-watch-next-secondary-results-renderer #items",
      "#related #items",
      "#related",
      "ytd-watch-next-secondary-results-renderer",
      "#secondary-inner #related",
      "#secondary-inner",
    ],
    home: [
      "ytd-browse[page-subtype='home'] ytd-rich-grid-renderer #contents",
      "ytd-rich-grid-renderer > #contents",
      "ytd-rich-grid-renderer #contents",
      "ytd-browse[page-subtype='home'] #contents",
      "ytd-rich-grid-renderer",
    ],
  },

  // Keys naming a card inside ytInitialData. Names in a JSON document, so data
  // in the same sense as a selector — they say where to look, not what to do.
  rendererKeys: ["compactVideoRenderer", "videoRenderer", "lockupViewModel"],

  // Words the parsers look for. Not patterns — the pattern shapes stay compiled
  // in extract.js, and every token here is escaped before it reaches a regex.
  //
  // This is the part that actually breaks. YouTube changing "1h ago" to "1 hour
  // ago", or a locale saying "Aufrufe" instead of "views", is a wording change,
  // and a wording change should not need a new extension.
  vocabulary: {
    // Trailing marker on a relative date: "2 weeks ago".
    ago: ["ago"],
    // Words meaning a view or concurrent-viewer count.
    views: ["views", "view", "watching"],
    // Text that marks a card as a running stream.
    live: ["live"],
    verified: ["verified", "official artist"],
    sponsored: ["sponsored", "paid", "ad"],
    ageGated: ["age", "members only", "18+"],
    // Marks a card as a past or upcoming broadcast, which reads like an age.
    broadcast: ["streamed", "premiered", "premieres"],
    // Thousands / millions / billions suffixes, in order.
    magnitudes: ["k", "m", "b"],
  },

  badges: {
    containers: [
      "ytd-badge-supported-renderer",
      ".badge",
      "[class*='badge']",
      "badge-shape",
    ],
    overlay: [
      "ytd-thumbnail-overlay-time-status-renderer",
      "#time-status",
      "badge-shape",
    ],
  },
});

/** Brackets and quotes must balance — catches a truncated or mangled selector. */
function balanced(s) {
  const pairs = { "[": "]", "(": ")" };
  const stack = [];
  let quote = null;
  for (const ch of s) {
    if (quote) {
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") quote = ch;
    else if (pairs[ch]) stack.push(pairs[ch]);
    else if (ch === "]" || ch === ")") {
      if (stack.pop() !== ch) return false;
    }
  }
  return !quote && stack.length === 0;
}

/**
 * A selector must be a plain, bounded CSS string.
 *
 * Checked structurally rather than by asking the browser, because this also
 * runs under the fixture tests where there is no global document. Where a DOM
 * is available the browser's own parser is consulted as well — it is stricter
 * than anything worth reimplementing here.
 */
function validSelector(v) {
  if (typeof v !== "string") return false;
  const s = v.trim();
  if (!s || s.length > MAX_SELECTOR_LEN) return false;
  // Reject anything that is not a selector. A config carrying a URL, a script
  // or an expression is not a config we asked for, whatever it claims.
  // `>` is the child combinator and has to be allowed; `=>` is still rejected
  // explicitly below. `<` and braces have no meaning in a selector at all.
  if (/[<{}]|javascript:|https?:|=>|\bfunction\b/i.test(s)) return false;
  if (!balanced(s)) return false;
  if (typeof document !== "undefined" && document.createDocumentFragment) {
    try {
      document.createDocumentFragment().querySelector(s);
    } catch {
      return false;
    }
  }
  return true;
}

function validList(v) {
  return (
    Array.isArray(v) &&
    v.length > 0 &&
    v.length <= MAX_LIST_LEN &&
    v.every(validSelector)
  );
}

function validFieldSet(obj) {
  if (!obj || typeof obj !== "object" || Array.isArray(obj)) return false;
  for (const [k, v] of Object.entries(obj)) {
    if (STRING_FIELDS.has(k)) {
      if (!validSelector(v)) return false;
    } else if (LIST_FIELDS.has(k)) {
      if (!validList(v)) return false;
    } else {
      return false; // unknown key: reject the whole config
    }
  }
  return true;
}

/**
 * Check a config from the daemon.
 *
 * Returns the config if every part of it is acceptable, otherwise null. There
 * is no partial acceptance: a config with one unknown key is a config written
 * against a different understanding of this engine, and taking half of it would
 * mean extracting with a mixture of two schemas.
 *
 * @param {unknown} cfg
 * @returns {object | null}
 */
export function validateSelectorConfig(cfg) {
  if (!cfg || typeof cfg !== "object" || Array.isArray(cfg)) return null;
  if (cfg.version !== 1) return null;
  if (typeof cfg.platform !== "string" || !/^[a-z]{2,8}$/.test(cfg.platform)) {
    return null;
  }
  if (!validSelector(cfg.cards) || !validSelector(cfg.homeItems)) return null;

  if (!cfg.shapes || typeof cfg.shapes !== "object") return null;
  for (const shape of Object.values(cfg.shapes)) {
    if (!validFieldSet(shape)) return null;
  }
  if (!cfg.badges || !validFieldSet(cfg.badges)) return null;
  if (!cfg.containers || !validFieldSet(cfg.containers)) return null;
  if (!validVocabulary(cfg.vocabulary)) return null;
  if (!cfg.containers.watch || !cfg.containers.home) return null;

  // Renderer keys are JSON property names, not selectors: plain identifiers,
  // bounded, and never anything that could be read as an expression.
  if (
    !Array.isArray(cfg.rendererKeys) ||
    cfg.rendererKeys.length === 0 ||
    cfg.rendererKeys.length > MAX_LIST_LEN ||
    !cfg.rendererKeys.every(
      (k) => typeof k === "string" && /^[A-Za-z][A-Za-z0-9_]{0,63}$/.test(k),
    )
  ) {
    return null;
  }

  // Every shape the engine knows how to read must be present. A config missing
  // one would silently stop extracting that card type.
  for (const name of ["compact", "lockup"]) {
    if (!cfg.shapes[name]) return null;
  }
  return cfg;
}

/**
 * Vocabulary is words, never patterns.
 *
 * A token that reached a regex unescaped would be logic: `.*` or `(?:` supplied
 * by the daemon changes what the parser *does*, not what it looks for. So a
 * token must be plain — letters, digits, spaces, and the handful of characters
 * that appear in real words like "18+" — and `escapeToken` is applied at every
 * use site regardless.
 */
const VOCAB_FIELDS = new Set([
  "ago",
  "views",
  "live",
  "verified",
  "sponsored",
  "ageGated",
  "broadcast",
  "magnitudes",
]);

function validVocabulary(v) {
  if (!v || typeof v !== "object" || Array.isArray(v)) return false;
  for (const [k, list] of Object.entries(v)) {
    if (!VOCAB_FIELDS.has(k)) return false;
    if (!Array.isArray(list) || list.length === 0 || list.length > MAX_LIST_LEN) {
      return false;
    }
    for (const t of list) {
      if (typeof t !== "string") return false;
      const s = t.trim();
      if (!s || s.length > 40) return false;
      if (!/^[\p{L}\p{N} '+.-]+$/u.test(s)) return false;
    }
  }
  for (const need of VOCAB_FIELDS) if (!v[need]) return false;
  return true;
}

/** Make a vocabulary token safe to place inside a pattern. */
export function escapeToken(t) {
  return String(t).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** An alternation of escaped tokens, for building a matcher. */
export function alternation(tokens) {
  return (Array.isArray(tokens) ? tokens : [])
    .map((t) => escapeToken(String(t).trim()))
    .filter(Boolean)
    .join("|");
}

/** First element matching any candidate, in order. */
export function pick(el, list) {
  if (!el || !Array.isArray(list)) return null;
  for (const sel of list) {
    const found = el.querySelector(sel);
    if (found) return found;
  }
  return null;
}

/** Every element matching any candidate, in order, de-duplicated. */
export function pickAll(el, list) {
  if (!el || !Array.isArray(list)) return [];
  const seen = new Set();
  const out = [];
  for (const sel of list) {
    for (const n of el.querySelectorAll(sel)) {
      if (seen.has(n)) continue;
      seen.add(n);
      out.push(n);
    }
  }
  return out;
}

/** Join a candidate list into one selector, for matches() and MO relevance. */
export function joined(list) {
  return Array.isArray(list) ? list.join(", ") : String(list || "");
}
