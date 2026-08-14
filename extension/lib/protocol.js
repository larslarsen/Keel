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

/** Extension semantic version (diagnostic only; WO-081 uses capabilities). */
export const CLIENT_VERSION = "0.1.0";
/** Inclusive API range this extension speaks. */
export const CLIENT_API = Object.freeze({ min: 1, max: 1 });
/**
 * Required capability revisions (fail closed if missing).
 *
 * `network_consent` is required, not optional (WO-089). A daemon without it
 * predates the consent gate, which means two things at once: it would start its
 * network before anyone had accepted the corrected disclosure, and it would run
 * Live and answer the word protocol at the default level. Neither is something
 * the extension can compensate for from its side, so the session fails closed
 * with "desktop app update required" rather than connecting to a daemon whose
 * defaults contradict what this build's screens say.
 */
export const CLIENT_REQUIRED = Object.freeze({ core: 1, network_consent: 1 });

/**
 * The network-data disclosure this build's consent screen renders.
 *
 * Sent with an acceptance so the daemon records *which* wording was agreed to,
 * and refused if it names a revision the daemon does not know. Must match
 * store.NetworkConsentRevision in the daemon, and must be raised in the same
 * change as the screen's text.
 */
export const CONSENT_REVISION = 2;
/**
 * WO-110: shown when the daemon's returned gate state says consent is still
 * required after an affirmative grant — whether because the daemon itself
 * refused a stale revision (consent_rejected) or, defensively, because a
 * structurally valid reply simply did not say the gate opened. Kept as one
 * exported string so the consent screen's mismatch copy and the check that
 * triggers it cannot drift apart the way a re-typed message would.
 */
export const STALE_CONSENT_MESSAGE =
  "the browser extension and desktop app do not agree on the disclosure " +
  "version — update the browser extension; pressing Accept again will not " +
  "fix it.";
/** Optional capability ceilings; negotiated map may omit any of these. */
export const CLIENT_OPTIONAL = Object.freeze({
  selectors: 1,
  tiktok: 1,
  scroll_history: 1,
  // 2 = reciprocal distributed search (WO-085): the daemon refuses PEER_SEARCH
  // below contribution level 2 and says so with a contribution_required code.
  // A negotiated 1 means the daemon predates that rule and still answers at
  // level 1, so the control must not be presented as level-gated — see
  // PEER_SEARCH_REV_RECIPROCAL.
  // 3 = streaming job (WO-095): PEER_SEARCH starts work and is acknowledged
  // immediately; progress, results and the terminal state arrive as events. A
  // negotiated 2 means the daemon still answers atomically, and this build
  // falls back to that visibly rather than fabricating progress it is not
  // receiving — see PEER_SEARCH_REV_STREAMING.
  peer_search: 3,
  word_stats: 1,
  queue: 1,
  contribution_runtime: 1,
  contribution_impact: 1,
  // WO-093: GET_STATS carries typed peer-network health (state, reason,
  // bounded failure count) beside the raw counts. Optional, and its absence is
  // the signal that matters: a daemon that cannot say *why* keel_peers is zero
  // must be reported as needing an update, never rendered as the ambiguous
  // zero this capability exists to abolish.
  network_status: 1,
  // 2 = titleless LIVE_ROOM is a valid sighting (WO-104). Revision 1 still
  // requires a non-empty title on every surface. Gossip already permitted an
  // omitted `t`; this revision is bridge-only.
  live_sightings: 2,
});

/** live_sightings revision that accepts an empty title on LIVE_ROOM only. */
export const LIVE_SIGHTINGS_REV_TITLELESS_ROOM = 2;

/** peer_search revision at which distributed search became Level-2+. */
export const PEER_SEARCH_REV_RECIPROCAL = 2;

/**
 * peer_search revision at which the RPC became a streaming job (WO-095).
 *
 * Below this the daemon answers PEER_SEARCH once, with everything it found, and
 * the page must present that as the one-shot result it is. Fabricating animated
 * progress against an atomic reply would show the user work that is not
 * happening — the interface would be lying about the network in the one place
 * this feature exists to be honest about it.
 */
export const PEER_SEARCH_REV_STREAMING = 3;

/**
 * Envelope-id prefix the daemon puts on unsolicited events (WO-095 §3).
 *
 * An id carrying this can never be a reply, so it can never resolve a pending
 * request. Mirrors bridge.EventIDPrefix in the daemon.
 */
export const EVENT_ID_PREFIX = "evt-";

const SURFACES = ["WATCH_NEXT", "HOME", "SEARCH", "CHANNEL", "SHORTS", "EXPLORE", "FOLLOWING"];
const LIVE_SURFACES = ["LIVE", "LIVE_ROOM"];
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
  if ((r.surface === "EXPLORE" || r.surface === "FOLLOWING") && r.platform !== "tt") {
    e.push("TikTok surface needs tt platform");
  }
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

/** Validate the separate ephemeral Live wire shape (WO-098 / WO-104). */
export function validateLiveSighting(value, rev = LIVE_SIGHTINGS_REV_TITLELESS_ROOM) {
  const r = value || {};
  const e = [];
  if (typeof r.page_load_id !== "string" || !UUID_RE.test(r.page_load_id)) e.push("page_load_id");
  if (!Number.isFinite(r.observed_at) || r.observed_at <= 0) e.push("observed_at");
  if (!LIVE_SURFACES.includes(r.surface)) e.push("surface");
  if (!Number.isInteger(r.slot_index) || r.slot_index < 0) e.push("slot_index");
  if (r.platform !== "tt") e.push("platform");
  if (typeof r.live_locator !== "string" || !/^@[a-z0-9_.-]{1,64}\/live$/.test(r.live_locator)) e.push("live_locator");
  if (typeof r.channel_id !== "string" || !/^@[a-z0-9_.-]{1,64}$/i.test(r.channel_id)) e.push("channel_id");
  const title = typeof r.title === "string" ? r.title : "";
  const titlelessRoom = rev >= LIVE_SIGHTINGS_REV_TITLELESS_ROOM && r.surface === "LIVE_ROOM";
  if (r.title != null && typeof r.title !== "string") e.push("title");
  else if (title.length > 300 || (!title && !titlelessRoom)) e.push("title");
  if (!Array.isArray(r.badges) || !r.badges.every((b) => typeof b === "string")) e.push("badges");
  if (e.length) return { ok: false, errors: e };
  return { ok: true, value: { page_load_id: r.page_load_id, observed_at: r.observed_at, surface: r.surface, slot_index: r.slot_index, platform: "tt", live_locator: r.live_locator, channel_id: r.channel_id, channel_name: typeof r.channel_name === "string" ? r.channel_name : "", title, badges: [...r.badges] } };
}

export function validateLiveSightingList(list, rev = LIVE_SIGHTINGS_REV_TITLELESS_ROOM) {
  if (!Array.isArray(list)) return { ok: false, values: [], errors: ["not array"] };
  const values = [], errors = [];
  list.forEach((v, i) => { const r = validateLiveSighting(v, rev); if (r.ok) values.push(r.value); else errors.push(`${i}: ${r.errors.join(",")}`); });
  return { ok: !errors.length, values, errors };
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
