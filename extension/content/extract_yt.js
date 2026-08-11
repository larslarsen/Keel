// SPDX-License-Identifier: Apache-2.0
/**
 * Pure ytInitialData → Impression (WATCH_NEXT).
 * Iterative bounded walk; lockupViewModel + compactVideoRenderer.
 */
import { parseDuration, parseViewCount } from "./extract.js";

/** Keep in sync with CARD_SEL / live components. */
export const RENDERER_KEYS = [
  "compactVideoRenderer",
  "videoRenderer",
  "lockupViewModel",
];

const MAX_NODES = 50_000;

/** @param {string} text @param {number} start */
export function extractBalancedObject(text, start) {
  if (text[start] !== "{") return null;
  let depth = 0;
  let inString = false;
  let escape = false;
  for (let i = start; i < text.length; i++) {
    const ch = text[i];
    if (inString) {
      if (escape) escape = false;
      else if (ch === "\\") escape = true;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === '"') {
      inString = true;
      continue;
    }
    if (ch === "{") depth += 1;
    else if (ch === "}") {
      depth -= 1;
      if (depth === 0) return text.slice(start, i + 1);
    }
  }
  return null;
}

/** @param {Document} doc */
export function parseYtInitialDataFromDom(doc) {
  for (const script of doc.querySelectorAll("script")) {
    const text = script.textContent || "";
    if (!text.includes("ytInitialData")) continue;
    for (const re of [
      /var\s+ytInitialData\s*=\s*/,
      /window\["ytInitialData"\]\s*=\s*/,
      /window\.ytInitialData\s*=\s*/,
      /ytInitialData\s*=\s*/,
    ]) {
      const m = text.match(re);
      if (!m || text[m.index + m[0].length] !== "{") continue;
      const json = extractBalancedObject(text, m.index + m[0].length);
      if (!json) continue;
      try {
        return JSON.parse(json);
      } catch {
        /* next */
      }
    }
  }
  return null;
}

/**
 * Iterative collect. Does not re-descend into matched renderers.
 * @param {unknown} root
 * @param {string[]} keys
 * @param {number} maxNodes
 * @returns {{ found: { key: string, data: object }[], visited: number, truncated: boolean }}
 */
export function collectRenderers(root, keys, maxNodes = MAX_NODES) {
  const keySet = new Set(keys);
  const found = [];
  let visited = 0;
  let truncated = false;
  const stack = [root];

  while (stack.length) {
    const node = stack.pop();
    if (node == null || typeof node !== "object") continue;
    visited += 1;
    if (visited > maxNodes) {
      truncated = true;
      break;
    }

    if (Array.isArray(node)) {
      for (let i = node.length - 1; i >= 0; i--) stack.push(node[i]);
      continue;
    }

    let matched = false;
    for (const k of keySet) {
      if (Object.prototype.hasOwnProperty.call(node, k) && node[k]) {
        found.push({ key: k, data: node[k] });
        matched = true;
      }
    }
    // Do not re-descend into matched renderer nodes
    if (matched) continue;

    for (const v of Object.values(node)) {
      if (v && typeof v === "object") stack.push(v);
    }
  }
  return { found, visited, truncated };
}

function textRuns(obj) {
  if (!obj) return "";
  if (typeof obj === "string") return obj;
  if (obj.simpleText) return obj.simpleText;
  if (obj.content) return obj.content;
  if (Array.isArray(obj.runs)) return obj.runs.map((x) => x.text).join("");
  return "";
}

function browseIdFromRuns(runs) {
  const r = runs?.[0];
  return (
    r?.navigationEndpoint?.browseEndpoint?.browseId ||
    r?.navigationEndpoint?.commandMetadata?.webCommandMetadata?.url ||
    null
  );
}

/** @param {object} r compactVideoRenderer / videoRenderer */
function fieldsFromCompact(r) {
  if (!r?.videoId || typeof r.videoId !== "string") return null;
  const title = textRuns(r.title).replace(/\s+/g, " ").trim();
  if (!title) return null;
  let channel_id =
    browseIdFromRuns(r.longBylineText?.runs) ||
    browseIdFromRuns(r.shortBylineText?.runs) ||
    r.channelId ||
    null;
  if (typeof channel_id !== "string" || !channel_id || channel_id.startsWith("/")) {
    channel_id = null;
  }
  const channel_name =
    textRuns(r.shortBylineText) || textRuns(r.longBylineText) || null;
  const badges = [];
  for (const b of r.badges || r.ownerBadges || []) {
    const label = String(
      b.metadataBadgeRenderer?.label || b.metadataBadgeRenderer?.tooltip || ""
    ).toUpperCase();
    // Only a genuine LIVE *broadcast* badge counts. Reject labels that merely
    // contain "LIVE" as part of "LIVESTREAM"/"LIVE replay"/"Live chat replay"
    // (WO-066). Require the standalone LIVE word, reject replay/chat variants.
    if (/\bLIVE\b/.test(label) && !/REPLAY|CHAT|STREAM/.test(label)) {
      badges.push("LIVE");
    }
    if (label.includes("VERIFIED") || label.includes("OFFICIAL")) {
      badges.push("VERIFIED");
    }
  }
  return {
    video_id: r.videoId,
    channel_id,
    channel_unknown: !channel_id,
    channel_name,
    title,
    duration_s: parseDuration(
      textRuns(r.lengthText) || r.lengthText?.runs?.[0]?.text
    ),
    view_count: parseViewCount(
      textRuns(r.viewCountText) || textRuns(r.shortViewCountText)
    ),
    published_at: textRuns(r.publishedTimeText) || null,
    badges: [...new Set(badges)],
  };
}

/**
 * lockupViewModel (current watch-next JSON).
 * @param {object} r
 */
function fieldsFromLockup(r) {
  if (!r || typeof r !== "object") return null;

  let video_id =
    r.contentId ||
    r.rendererContext?.commandContext?.onTap?.innertubeCommand?.watchEndpoint
      ?.videoId ||
    r.rendererContext?.commandContext?.onTap?.innertubeCommand
      ?.reelWatchEndpoint?.videoId ||
    null;

  // Nested metadata
  const meta =
    r.metadata?.lockupMetadataViewModel ||
    r.metadata ||
    r.lockupMetadataViewModel ||
    {};
  const title = textRuns(meta.title || r.title).replace(/\s+/g, " ").trim();

  if (!video_id) {
    // scan shallow for watchEndpoint
    const stack = [r];
    let n = 0;
    while (stack.length && n < 200) {
      const node = stack.pop();
      n += 1;
      if (!node || typeof node !== "object") continue;
      if (node.videoId && typeof node.videoId === "string" && !video_id) {
        video_id = node.videoId;
      }
      if (node.watchEndpoint?.videoId) video_id = node.watchEndpoint.videoId;
      if (Array.isArray(node)) {
        for (const x of node) stack.push(x);
      } else {
        for (const v of Object.values(node)) {
          if (v && typeof v === "object") stack.push(v);
        }
      }
    }
  }
  if (!video_id || typeof video_id !== "string") return null;
  if (!title) return null;

  // Live lockups put browseId on the avatar tap target, not metadataRows.
  let channel_id =
    meta.image?.decoratedAvatarViewModel?.rendererContext?.commandContext
      ?.onTap?.innertubeCommand?.browseEndpoint?.browseId ||
    null;
  let channel_name = null;
  if (!channel_id || typeof channel_id !== "string") {
    const rows =
      meta.metadata?.contentMetadataViewModel?.metadataRows ||
      meta.contentMetadataViewModel?.metadataRows ||
      [];
    for (const row of rows) {
      for (const part of row.metadataParts || []) {
        const bid =
          part.text?.commandRuns?.[0]?.onTap?.innertubeCommand?.browseEndpoint
            ?.browseId ||
          part.text?.runs?.[0]?.navigationEndpoint?.browseEndpoint?.browseId;
        if (bid && typeof bid === "string") {
          channel_id = bid;
          channel_name = textRuns(part?.text) || null;
          break;
        }
      }
      if (channel_id) break;
    }
  }
  // Live cards set browseId on the avatar; the name is the first metadata
  // row's text (rows after it carry counts/dates).
  if (!channel_name) {
    const rows =
      meta.metadata?.contentMetadataViewModel?.metadataRows ||
      meta.contentMetadataViewModel?.metadataRows ||
      [];
    outer: for (const row of rows) {
      for (const part of row.metadataParts || []) {
        const c = textRuns(part?.text).trim();
        if (c && !part?.leadingIcon) {
          channel_name = c;
          break outer;
        }
      }
    }
  }
  if (!channel_id || typeof channel_id !== "string") {
    // Prefer metadata subtree (avatar lives there); contentImage is large and
    // used to exhaust a tight walk budget before browseId was reached.
    const stack = [meta, r].filter(Boolean);
    let n = 0;
    while (stack.length && n < 2_000 && !channel_id) {
      const node = stack.pop();
      n += 1;
      if (!node || typeof node !== "object") continue;
      if (node.browseId && /^UC[\w-]{22}$/.test(node.browseId)) {
        channel_id = node.browseId;
        break;
      }
      if (Array.isArray(node)) for (const x of node) stack.push(x);
      else for (const v of Object.values(node)) {
        if (v && typeof v === "object") stack.push(v);
      }
    }
  }
  if (typeof channel_id !== "string" || !/^UC[\w-]{22}$/.test(channel_id)) {
    channel_id = null;
  }
  // channel_id may stay null — still a valid impression (channel_unknown)

  let duration_s = null;
  const badges = [];
  // thumbnail badges often hold duration
  const overlays =
    r.contentImage?.thumbnailViewModel?.overlays ||
    r.thumbnailViewModel?.overlays ||
    [];
  for (const o of overlays) {
    const badge =
      o.thumbnailOverlayBadgeViewModel?.thumbnailBadges?.[0]
        ?.thumbnailBadgeViewModel?.text ||
      o.animatedThumbnailOverlayViewModel?.text;
    const d = parseDuration(badge);
    if (d != null) duration_s = d;
    // Only a genuine LIVE *broadcast* badge counts. YouTube also renders
    // "LIVE replay" / "Live chat replay" thumbnails for ended/premiere VODs;
    // those must NOT be flagged live (WO-066). Require the standalone LIVE
    // word and reject replay/chat variants.
    if (/\bLIVE\b/i.test(badge || "") && !/replay|chat/i.test(badge || "")) {
      badges.push("LIVE");
    }
  }

  return {
    video_id,
    channel_id: channel_id || null,
    channel_unknown: !channel_id,
    channel_name: channel_name || null,
    title,
    duration_s,
    view_count: null,
    published_at: null,
    badges: [...new Set(badges)],
  };
}

function fieldsFromFound(item) {
  if (item.key === "lockupViewModel") return fieldsFromLockup(item.data);
  return fieldsFromCompact(item.data);
}

/**
 * Prefer secondary results path; fall back to full walk.
 * @param {object} data
 * @param {object} ctx
 */
export function extractFromYtInitialData(data, ctx, cfg = undefined) {
  if (!data || typeof data !== "object") {
    return { impressions: [], failures: 0, candidates: 0 };
  }

  // Refuse data that describes a different page than the one we are on.
  //
  // `window.ytInitialData` is written once when the document loads and is never
  // refreshed by YouTube's in-page navigation. So after an hour of clicking
  // around one tab, this still holds the *first* page's rail — and re-parsing it
  // on every navigation records those videos again, stamped with the current
  // time, in the current slots.
  //
  // Measured on a live corpus: eight videos appeared in twelve consecutive page
  // loads spanning eleven hours, one of them carrying a LIVE badge the whole
  // time. That is how a stream that ended half a day ago kept being reported as
  // live "just now".
  //
  // The endpoint below is the video this data was built for. If it disagrees
  // with the video actually open, the data is stale and the DOM scan — which
  // always reflects the real page — is the only honest source.
  const builtFor =
    data?.currentVideoEndpoint?.watchEndpoint?.videoId ||
    data?.playerOverlays?.playerOverlayRenderer?.videoDetails
      ?.playerOverlayVideoDetailsRenderer?.videoId ||
    null;
  if (builtFor && ctx?.context_video_id && builtFor !== ctx.context_video_id) {
    return { impressions: [], failures: 0, candidates: 0, stale: true };
  }

  let seed =
    data?.contents?.twoColumnWatchNextResults?.secondaryResults
      ?.secondaryResults?.results ||
    data?.contents?.twoColumnWatchNextResults?.secondaryResults ||
    null;

  // Which JSON keys name a card is configuration, like a selector (WO-056).
  const keys =
    Array.isArray(cfg?.rendererKeys) && cfg.rendererKeys.length
      ? cfg.rendererKeys
      : RENDERER_KEYS;

  let { found, truncated } = seed
    ? collectRenderers(seed, keys)
    : { found: [], truncated: false };

  if (!found.length) {
    const full = collectRenderers(data, keys);
    found = full.found;
    truncated = full.truncated;
    if (truncated) {
      console.warn(
        "[Keel] ytInitialData walk truncated at",
        MAX_NODES,
        "nodes"
      );
    }
  }

  const impressions = [];
  let failures = 0;
  const seen = new Set();

  // slot_index = position in unfiltered found list
  for (let slot_index = 0; slot_index < found.length; slot_index++) {
    const f = fieldsFromFound(found[slot_index]);
    if (!f) {
      failures += 1;
      continue;
    }
    if (seen.has(f.video_id)) continue;
    if (ctx.context_video_id && f.video_id === ctx.context_video_id) continue;
    seen.add(f.video_id);
    impressions.push({
      page_load_id: ctx.page_load_id,
      observed_at: ctx.observed_at ?? Date.now(),
      surface: "WATCH_NEXT",
      context_video_id: ctx.context_video_id ?? null,
      context_query_hash: null,
      slot_index,
      video_id: f.video_id,
      channel_id: f.channel_id ?? null,
      channel_unknown: Boolean(f.channel_unknown || !f.channel_id),
      channel_name: f.channel_name ?? null,
      title: f.title,
      duration_s: f.duration_s ?? null,
      view_count: f.view_count ?? null,
      published_at: f.published_at ?? null,
      badges: f.badges || [],
    });
  }
  return { impressions, failures, candidates: found.length };
}
