// SPDX-License-Identifier: Apache-2.0
/**
 * The full page's pure rendering helpers (WO-083).
 *
 * Everything here takes data and returns a string. Nothing reads `el`, calls
 * `rpc`, or touches a browser API — which is the boundary the ticket draws:
 * a helper that needed transport would not be a rendering helper. `hitRow` and
 * the `render*` functions stay in the controller for exactly that reason, and
 * import from here.
 *
 * Shared escaping and duration formatting live in `lib/render.js`; this file
 * holds what is the full page's own.
 */
import { escapeHtml } from "../lib/render.js";

/**
 * Compact view counts.
 *
 * Deliberately not shared with the side panel, whose version also blanks zero.
 * The two surfaces disagree — the page renders "0 views" where the panel
 * renders nothing — and both readings are defensible, so WO-083's
 * behaviour-preserving split kept each one rather than picking a winner and
 * silently changing a surface. Recorded in the work order.
 */
export function fmtCount(n) {
  if (typeof n !== "number") return "";
  if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

/** Coarse "how long ago", for sighting timestamps. */
export function fmtAgo(ms) {
  if (!ms) return "";
  const secs = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (secs < 90) return "just now";
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins} min ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs} hour${hrs === 1 ? "" : "s"} ago`;
  return `${Math.floor(hrs / 24)} day${hrs < 48 ? "" : "s"} ago`;
}

/**
 * Derived thumbnail URL — no API call. See WO-039.
 *
 * Emits an `<img>` with the id in a data attribute and no `src`: the actual
 * image comes from the daemon via the controller's `fillThumb`, so this stays
 * pure and the extension never fetches from the network itself.
 */
export function thumbHtml(videoID) {
  return (
    `<img class="thumb" loading="lazy" decoding="async" referrerpolicy="no-referrer"` +
    ` alt="" width="120" height="68"` +
    ` data-vid="${encodeURIComponent(videoID)}">`
  );
}

export function platformLabel(p) {
  return p === "tt" ? "TikTok" : "YouTube";
}

/** Where a live stream lives, per platform. */
export function liveUrl(id, platform) {
  const v = encodeURIComponent(id);
  if (platform === "tt") return `https://www.tiktok.com/video/${v}`;
  return `https://www.youtube.com/watch?v=${v}`;
}

/**
 * How long it has been running — the fact a viewer can act on.
 *
 * "Seen just now" is true of a stream that started this morning, because
 * whoever is watching keeps reporting it. The span between first and last
 * sighting is what distinguishes a fresh stream from an all-day one.
 *
 * Duration is measured from when it started (StartedAt if the peer reported
 * it, else our first sighting) to NOW. Measuring to last_seen instead
 * collapses to ~0 for a stream we saw only once (first_seen == last_seen),
 * which reads as "just started" next to a "seen 3h ago" column.
 */
export function liveFor(s) {
  const began = s.b || s.first_seen || Date.now();
  const mins = Math.max(0, Math.round((Date.now() - began) / 60000));
  if (mins >= 60) {
    const hrs = Math.floor(mins / 60);
    return `${hrs}+ hour${hrs === 1 ? "" : "s"}`;
  }
  if (mins >= 5) return `${mins}+ min`;
  return "just started";
}

/** One stat tile in the config view's summary row. */
export function tile(value, label) {
  return `<div><strong>${escapeHtml(String(value))}</strong><span>${escapeHtml(label)}</span></div>`;
}

/** One titled count list in the analysis view. Empty string for no rows. */
export function analysisTable(title, rows, note) {
  if (!rows?.length) return "";
  let h = `<h2>${escapeHtml(title)}</h2>`;
  if (note) h += `<p class="meta">${escapeHtml(note)}</p>`;
  h += `<ol class="an-list">`;
  for (const r of rows) {
    h +=
      `<li><span class="an-label">${escapeHtml(r.label || r.key)}</span>` +
      `<span class="an-count">${r.count}×` +
      (r.extra ? ` · ${escapeHtml(r.extra)}` : "") +
      `</span></li>`;
  }
  return h + `</ol>`;
}
