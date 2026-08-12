// SPDX-License-Identifier: Apache-2.0
/**
 * Keel Bridge envelope + Impression validation.
 * Shared shape with daemon/bridge (v: 2).
 */

export const PROTOCOL_V = 2;
export const HOST_NAME = "com.keel.host";
/** Host → browser max (native messaging). */
export const MAX_HOST_TO_BROWSER = 1024 * 1024;
/** Browser → host max. */
export const MAX_BROWSER_TO_HOST = 64 * 1024 * 1024;
/** @deprecated use MAX_HOST_TO_BROWSER — was misused for outbound checks */
export const MAX_HOST_MSG = MAX_HOST_TO_BROWSER;

const SURFACES = ["WATCH_NEXT", "HOME", "SEARCH", "CHANNEL", "SHORTS"];
const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const HASH16 = /^[0-9a-f]{16}$/;

/** @returns {string} */
export function corrId() {
  return crypto.randomUUID();
}

/**
 * @param {string} type
 * @param {unknown} [payload]
 * @param {string} [id]
 */
export function envelope(type, payload = {}, id = corrId()) {
  return { v: PROTOCOL_V, id, type, payload };
}

/**
 * @param {unknown} msg
 * @returns {{ ok: true, value: object } | { ok: false, error: string }}
 */
export function validateEnvelope(msg) {
  if (!msg || typeof msg !== "object" || Array.isArray(msg)) {
    return { ok: false, error: "not an object" };
  }
  if (msg.v !== PROTOCOL_V) return { ok: false, error: "bad version" };
  if (typeof msg.id !== "string" || !msg.id) {
    return { ok: false, error: "bad id" };
  }
  if (typeof msg.type !== "string" || !msg.type) {
    return { ok: false, error: "bad type" };
  }
  if (msg.payload === undefined) {
    return { ok: false, error: "missing payload" };
  }
  return { ok: true, value: msg };
}

/**
 * @param {unknown} value
 * @returns {{ ok: true, value: object } | { ok: false, errors: string[] }}
 */
export function validateImpression(value) {
  const e = [];
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { ok: false, errors: ["not an object"] };
  }
  const r = value;
  if (typeof r.page_load_id !== "string" || !UUID_RE.test(r.page_load_id)) {
    e.push("page_load_id");
  }
  if (typeof r.observed_at !== "number" || !Number.isFinite(r.observed_at)) {
    e.push("observed_at");
  }
  if (!SURFACES.includes(r.surface)) e.push("surface");
  if (r.context_video_id !== null && typeof r.context_video_id !== "string") {
    e.push("context_video_id");
  }
  if (
    r.context_query_hash !== null &&
    (typeof r.context_query_hash !== "string" || !HASH16.test(r.context_query_hash))
  ) {
    e.push("context_query_hash");
  }
  if (
    typeof r.slot_index !== "number" ||
    !Number.isInteger(r.slot_index) ||
    r.slot_index < 0
  ) {
    e.push("slot_index");
  }
  if (typeof r.video_id !== "string" || !r.video_id) e.push("video_id");
  // channel_id may be null when the DOM omits channel links (channel_unknown)
  if (r.channel_id !== null && typeof r.channel_id !== "string") {
    e.push("channel_id");
  }
  if (typeof r.channel_id === "string" && !r.channel_id) e.push("channel_id");
  // channel_name is nullable — the DOM often omits it
  if (r.channel_name !== null && typeof r.channel_name !== "string") {
    e.push("channel_name");
  }
  if (r.channel_unknown != null && typeof r.channel_unknown !== "boolean") {
    e.push("channel_unknown");
  }
  if (typeof r.title !== "string" || !r.title) e.push("title");
  if (
    r.duration_s !== null &&
    (typeof r.duration_s !== "number" || r.duration_s < 0)
  ) {
    e.push("duration_s");
  }
  if (
    r.view_count !== null &&
    (typeof r.view_count !== "number" || r.view_count < 0)
  ) {
    e.push("view_count");
  }
  if (r.published_at !== null && typeof r.published_at !== "string") {
    e.push("published_at");
  }
  if (!Array.isArray(r.badges) || !r.badges.every((b) => typeof b === "string")) {
    e.push("badges");
  }
  if (r.surface === "WATCH_NEXT" && r.context_video_id == null) {
    e.push("WATCH_NEXT needs context_video_id");
  }
  // Optional TikTok fields (WO-063). Absent on YouTube rows.
  if (r.hashtags != null) {
    if (!Array.isArray(r.hashtags) || !r.hashtags.every((t) => typeof t === "string")) {
      e.push("hashtags");
    }
  }
  if (r.sound_id != null && typeof r.sound_id !== "string") e.push("sound_id");
  if (
    r.dwell_pct != null &&
    (typeof r.dwell_pct !== "number" || r.dwell_pct < 0 || r.dwell_pct > 1)
  ) {
    e.push("dwell_pct");
  }
  if (r.engagement != null && typeof r.engagement !== "string") e.push("engagement");
  if (e.length) return { ok: false, errors: e };
  return {
    ok: true,
    value: {
      page_load_id: r.page_load_id,
      observed_at: r.observed_at,
      surface: r.surface,
      context_video_id: r.context_video_id,
      context_query_hash: r.context_query_hash,
      slot_index: r.slot_index,
      video_id: r.video_id,
      channel_id: r.channel_id ?? null,
      channel_unknown: Boolean(
        r.channel_unknown || r.channel_id == null || r.channel_id === ""
      ),
      channel_name: r.channel_name ?? null,
      title: r.title,
      duration_s: r.duration_s,
      view_count: r.view_count,
      published_at: r.published_at,
      badges: [...r.badges],
      platform: r.platform ?? "yt",
      hashtags: Array.isArray(r.hashtags) ? [...r.hashtags] : [],
      sound_id: r.sound_id ?? null,
      dwell_pct: r.dwell_pct ?? null,
      engagement: r.engagement ?? null,
    },
  };
}

/**
 * @param {unknown[]} list
 */
export function validateImpressionList(list) {
  if (!Array.isArray(list)) {
    return { ok: false, values: [], errors: ["not array"] };
  }
  const values = [];
  const errors = [];
  list.forEach((item, i) => {
    const r = validateImpression(item);
    if (r.ok) values.push(r.value);
    else errors.push(`${i}: ${r.errors.join(",")}`);
  });
  return { ok: errors.length === 0, values, errors };
}

/** UTF-8 byte length of a JSON-serialized envelope. */
export function envelopeBytes(env) {
  return new TextEncoder().encode(JSON.stringify(env)).length;
}
