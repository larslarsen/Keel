// SPDX-License-Identifier: Apache-2.0
/**
 * The side panel's pure rendering helpers (WO-083).
 *
 * Same boundary as `page/render.js`: data in, string out, no `el`, no `rpc`,
 * no browser API. The panel's element builders (`makeSuggestionLi`,
 * `makeQueueLi`, `toggleExplain`) wire click handlers and call the daemon, so
 * they are controller work by definition and stay there — importing from here.
 *
 * Shared escaping, duration formatting and watch URLs live in `lib/render.js`.
 */
import { escapeHtml } from "../lib/render.js";

/**
 * Compact view counts, blanking zero.
 *
 * The full page's version does not blank zero — it renders "0 views" where the
 * panel renders nothing. The divergence predates WO-083 and both readings are
 * defensible, so the behaviour-preserving split kept each surface's own rather
 * than picking a winner. Recorded in the work order.
 */
export function fmtCount(n) {
  if (typeof n !== "number" || n <= 0) return "";
  if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

/** Full local date+time, for the corpus's first/last observation stamps. */
export function fmt(ms) {
  if (ms == null) return "—";
  try {
    return new Date(ms).toLocaleString();
  } catch {
    return String(ms);
  }
}

/** Local date only, for the funnel inspector's per-sighting lines. */
export function fmtWhen(ms) {
  if (ms == null) return "—";
  try {
    return new Date(ms).toLocaleDateString();
  } catch {
    return String(ms);
  }
}

/**
 * A channel is worth showing only if a human can read it. `@handle` is a name;
 * `UC…` is a database key and belongs in the block button's dataset, not on
 * screen (WO-041).
 */
export function readableChannel(id) {
  return typeof id === "string" && id.startsWith("@") ? id : "";
}

export function formatBytes(n) {
  if (typeof n !== "number" || !Number.isFinite(n)) return "? bytes";
  if (n < 1024) return `${n} bytes`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * The funnel inspector's body (WO-018).
 *
 * Observational copy only — never "because you watched". Every claim here is
 * a count of what this device saw, and the closing line says so, because the
 * one thing this feature must not become is a guess at YouTube's reasons.
 */
export function formatExplain(ex) {
  if (!ex || !ex.total_impressions) {
    return `<p class="explain-body">Not in the local corpus yet (seen only on this page, or not stored).</p>`;
  }
  const total = ex.total_impressions;
  const once = total === 1;
  let html = `<p class="explain-body"><strong>Seen ${total} time${once ? "" : "s"}</strong>`;
  if (ex.first_observed_at != null) {
    html += ` · first ${fmtWhen(ex.first_observed_at)}`;
  }
  if (ex.last_observed_at != null && ex.last_observed_at !== ex.first_observed_at) {
    html += ` · last ${fmtWhen(ex.last_observed_at)}`;
  }
  html += `.</p>`;

  const ctxs = ex.contexts || [];
  if (ctxs.length) {
    html += `<p class="explain-label">Appeared after (watch-page rail):</p><ul class="explain-ctx">`;
    for (const c of ctxs) {
      const label = c.title
        ? escapeHtml(c.title)
        : `<span class="unknown">${escapeHtml(c.context_video_id)}</span>`;
      const med =
        typeof c.median_slot_index === "number" ? c.median_slot_index : "?";
      html += `<li>${label} · ${c.count}× · median slot ${med}</li>`;
    }
    html += `</ul>`;
  } else if (ex.home_impressions > 0 && total === ex.home_impressions) {
    html += `<p class="explain-body">Only observed on the home feed (no watch-page context).</p>`;
  } else if (!ctxs.length) {
    html += `<p class="explain-body">No watch-page co-occurrence in the corpus yet.</p>`;
  }

  if (ex.home_impressions > 0 && total !== ex.home_impressions) {
    html += `<p class="explain-body">Also on home feed: ${ex.home_impressions}×.</p>`;
  }

  const hist = ex.slot_histogram || [];
  if (hist.length) {
    const bits = hist
      .slice(0, 8)
      .map((b) => `slot ${b.slot}: ${b.count}`)
      .join(" · ");
    html += `<p class="explain-meta">Slots: ${bits}</p>`;
  }
  html += `<p class="explain-meta">Co-occurrence only — not YouTube’s stated reason.</p>`;
  return html;
}
