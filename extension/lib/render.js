// SPDX-License-Identifier: Apache-2.0
/**
 * Escaping and formatting shared by both surfaces (WO-083).
 *
 * WO-083 named a `render.js` per surface. Three helpers turned out to be
 * character-for-character identical in the side panel and the full page, and
 * copying them into two new files would have re-created the duplication the
 * split exists to remove — so the identical ones live here and each surface's
 * own `render.js` holds what is genuinely its own. Recorded as a boundary
 * adjustment in the work order.
 *
 * Everything here is pure: data in, string out. No DOM, no browser API, no
 * module state. That is what makes it safe for both surfaces to share and
 * testable without a document.
 *
 * `escapeHtml` is the load-bearing one. Both surfaces build rows with
 * innerHTML from daemon-supplied titles and channel names — strings that
 * originated in a YouTube/TikTok DOM — so every interpolation of untrusted
 * text goes through it. It is deliberately *not* also used on values the code
 * itself produced, which would only obscure which strings are the dangerous
 * ones.
 */

/** HTML-escape untrusted text for innerHTML interpolation. */
export function escapeHtml(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** m:ss / h:mm:ss. Empty for a missing or non-positive duration. */
export function fmtDuration(sec) {
  if (typeof sec !== "number" || sec <= 0) return "";
  const m = Math.floor(sec / 60);
  const s = String(Math.floor(sec % 60)).padStart(2, "0");
  if (m >= 60) return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}:${s}`;
  return `${m}:${s}`;
}

/**
 * Where a video lives, per platform.
 *
 * TikTok needs an author handle for a canonical clip URL and neither surface
 * has one, so the id is handed to TikTok's own resolver.
 */
export function watchUrl(videoID, platform) {
  const id = encodeURIComponent(videoID);
  if (platform === "tt") return `https://www.tiktok.com/video/${id}`;
  return `https://www.youtube.com/watch?v=${id}`;
}
