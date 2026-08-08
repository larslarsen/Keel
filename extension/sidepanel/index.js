// SPDX-License-Identifier: Apache-2.0
/** SidePanel: daemon status + counts. No local observation storage. */
import { browser } from "../lib/browser.js";
import {
  CONSENT_KEY,
  consentGranted,
  DEFAULT_HIDE_MODE,
  coerceHideMode,
  isChannelId,
  isHideMode,
  normalizeBlocklist,
} from "../lib/prefs.js";

const STATS_MIN_INTERVAL_MS = 5000;
/** Long-lived port so the SW knows the panel is open (WO-009 with-panel). */
const SIDEPANEL_PORT = "keel-sidepanel";

const el = {
  banner: document.getElementById("daemon-banner"),
  consentBanner: document.getElementById("consent-banner"),
  back: document.getElementById("btn-back"),
  total: document.getElementById("stat-total"),
  watch: document.getElementById("stat-watch"),
  home: document.getElementById("stat-home"),
  time: document.getElementById("time-range"),
  channelNote: document.getElementById("channel-note"),
  meta: document.getElementById("suggest-meta"),
  list: document.getElementById("list"),
  entropy: document.getElementById("entropy"),
  refresh: document.getElementById("btn-refresh"),
  hideRail: document.getElementById("btn-hide-rail"),
  exportBtn: document.getElementById("btn-export"),
  wipeBtn: document.getElementById("btn-wipe"),
  dataStatus: document.getElementById("data-status"),
  wipeConfirm: document.getElementById("wipe-confirm"),
  wipeConfirmText: document.getElementById("wipe-confirm-text"),
  wipeConfirmBtn: document.getElementById("btn-wipe-confirm"),
  wipeCancelBtn: document.getElementById("btn-wipe-cancel"),
  blockList: document.getElementById("block-list"),
  blockInput: document.getElementById("block-input"),
  blockAddBtn: document.getElementById("btn-block-add"),
};

/** @type {Set<string>} */
let blocklistSet = new Set();
/** Last full page proof from SW (unfiltered). Used for the seed, not the list. */
let lastPageCache = null;

/** Focus↔Serendipity 0–100. Default must sit in the serendipity half so the
 * panel never mirrors the rail out of the box (WO-046 §Why). */
let entropy = 70;
/** key = `${pageLoadId}|${seed}|${entropy}` — re-run the walk only when it changes. */
let lastSuggestKey = "";
let lastStatsAt = 0;

async function rpc(type, payload) {
  const r = await browser.runtime.sendMessage({ type, payload });
  if (!r?.ok) throw new Error(r?.error || type);
  return r;
}

function fmt(ms) {
  if (ms == null) return "—";
  try {
    return new Date(ms).toLocaleString();
  } catch {
    return String(ms);
  }
}

function setDaemonUi(connected, detail = "") {
  if (connected) {
    el.banner.className = "banner ok";
    el.banner.textContent = "Desktop app connected.";
  } else {
    el.banner.className = "banner warn";
    el.banner.textContent =
      "Keel's desktop app isn't running. The extension stays idle until you start it." +
      (detail ? ` (${detail})` : "");
  }
}

/**
 * Thumbnails come from the daemon, never from the network (WO-040).
 *
 * The extension holds no fetching logic of its own — it asks for a video id and
 * renders whatever data URL comes back. Keeping network I/O out of here is what
 * lets the extension stay frozen while the daemon changes.
 */
const thumbCache = new Map();
async function fillThumb(img, videoID) {
  if (!img || !videoID) return;
  if (thumbCache.has(videoID)) {
    img.src = thumbCache.get(videoID);
    return;
  }
  try {
    const r = await browser.runtime.sendMessage({
      type: "THUMBNAIL",
      payload: { video_id: videoID },
    });
    const url = r?.ok ? r.daemon?.data_url : null;
    if (!url) return;
    thumbCache.set(videoID, url);
    img.src = url;
  } catch {
    /* no image is fine; the box stays blank */
  }
}

function escapeHtml(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function fmtWhen(ms) {
  if (ms == null) return "—";
  try {
    return new Date(ms).toLocaleDateString();
  } catch {
    return String(ms);
  }
}

/** Observational copy only — never "because you watched" (WO-018). */
function formatExplain(ex) {
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
        typeof c.median_slot_index === "number"
          ? c.median_slot_index
          : "?";
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

function toggleExplain(li, videoId) {
  const existing = li.querySelector(".explain");
  if (existing) {
    existing.remove();
    return;
  }
  const box = document.createElement("div");
  box.className = "explain";
  box.innerHTML = `<p class="explain-body">Loading…</p>`;
  li.appendChild(box);
  rpc("EXPLAIN_VIDEO", { video_id: videoId })
    .then((r) => {
      box.innerHTML = formatExplain(r.explain);
    })
    .catch((err) => {
      box.innerHTML = `<p class="explain-body err">${escapeHtml(err.message || String(err))}</p>`;
    });
}

/** Compact count: 1.2K, 3.4M. */
function fmtCount(n) {
  if (typeof n !== "number" || n <= 0) return "";
  if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

/** m:ss / h:mm:ss. */
function fmtDuration(sec) {
  if (typeof sec !== "number" || sec <= 0) return "";
  const m = Math.floor(sec / 60);
  const s = String(Math.floor(sec % 60)).padStart(2, "0");
  if (m >= 60) return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}:${s}`;
  return `${m}:${s}`;
}

/**
 * A channel is worth showing only if a human can read it. `@handle` is a name;
 * `UC…` is a database key and belongs in the block button's dataset, not on
 * screen (WO-041).
 */
function readableChannel(id) {
  return typeof id === "string" && id.startsWith("@") ? id : "";
}

/**
 * One row of our own ranking (WO-046): the daemon's graph walk, not YouTube's
 * rail. Suggestions carry a channel key but no display name yet, so the label
 * under the thumbnail only appears when the key is already human-readable.
 */
function makeSuggestionLi(s) {
  const li = document.createElement("li");
  const thumb =
    `<img class="thumb" loading="lazy" decoding="async" referrerpolicy="no-referrer"` +
    ` alt="" width="96" height="54"` +
    ` data-vid="${encodeURIComponent(s.video_id)}">`;
  const chan = readableChannel(s.channel_id);
  const thumbBox = chan
    ? `<div class="thumb-wrap">${thumb}<span class="chan">${escapeHtml(chan)}</span></div>`
    : thumb;
  const href = `https://www.youtube.com/watch?v=${encodeURIComponent(s.video_id)}`;
  // Age rather than how many times Keel saw it. "2w ago" is what people read a
  // video's age from everywhere else, so it needs no explaining; the
  // observation count meant something only to us.
  const bits = [
    fmtDuration(s.duration_s),
    typeof s.view_count === "number" && s.view_count > 0
      ? `${fmtCount(s.view_count)} views`
      : "",
    s.published_at || "",
  ].filter(Boolean);
  li.innerHTML =
    `<div class="row-main">${thumbBox}<div class="row-text">` +
    `<p class="title"><a href="${href}" target="_blank" rel="noreferrer">${escapeHtml(s.title || "Untitled — no title recorded yet")}</a></p>` +
    `<div class="sub">${escapeHtml(bits.join(" · "))}` +
    (s.via_title ? ` · appeared after ${escapeHtml(s.via_title)}` : "") +
    `</div></div><span class="row-actions"></span></div>`;

  const actions = li.querySelector(".row-actions");

  const why = document.createElement("button");
  why.type = "button";
  why.className = "btn-icon btn-why";
  why.title = "Why was this suggested?";
  why.setAttribute("aria-label", "Why was this suggested?");
  why.innerHTML =
    '<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M12 16v-1"/><path d="M12 13a2.5 2.5 0 1 0-1.5-4.5"/></svg>';
  why.addEventListener("click", (e) => {
    e.stopPropagation();
    toggleExplain(li, s.video_id);
  });
  actions.appendChild(why);

  if (s.channel_id && isChannelId(s.channel_id)) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn-icon btn-block";
    const blocked = blocklistSet.has(s.channel_id);
    btn.title = blocked ? "Unblock this channel" : "Block this channel";
    btn.setAttribute("aria-label", btn.title);
    btn.innerHTML = blocked
      ? '<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2"><path d="M5 12h14"/></svg>'
      : '<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M5.6 5.6l12.8 12.8"/></svg>';
    btn.dataset.channelId = s.channel_id;
    btn.dataset.action = blocked ? "unblock" : "block";
    btn.addEventListener("click", () => {
      const id = btn.dataset.channelId;
      const act = btn.dataset.action;
      if (!id) return;
      const type = act === "unblock" ? "UNBLOCK_CHANNEL" : "BLOCK_CHANNEL";
      rpc(type, { channel_id: id })
        .then((r) => {
          renderBlocklist(r.blocklist);
        })
        .catch((err) => console.warn("[Keel panel] block", err?.message || err));
    });
    actions.appendChild(btn);
  }
  const img = li.querySelector("img.thumb[data-vid]");
  if (img) fillThumb(img, decodeURIComponent(img.dataset.vid || ""));
  return li;
}

function renderBlocklist(list) {
  const ids = normalizeBlocklist(list);
  blocklistSet = new Set(ids);
  if (!el.blockList) return;
  el.blockList.replaceChildren();
  if (!ids.length) {
    const empty = document.createElement("li");
    empty.className = "meta";
    empty.style.border = "none";
    empty.style.background = "transparent";
    empty.textContent = "No channels blocked.";
    el.blockList.appendChild(empty);
  } else {
    for (const id of ids) {
      const li = document.createElement("li");
      const span = document.createElement("span");
      span.textContent = id;
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn";
      btn.textContent = "Unblock";
      btn.addEventListener("click", () => {
        rpc("UNBLOCK_CHANNEL", { channel_id: id })
          .then((r) => renderBlocklist(r.blocklist))
          .catch((err) =>
            console.warn("[Keel panel] unblock", err?.message || err)
          );
      });
      li.appendChild(span);
      li.appendChild(btn);
      el.blockList.appendChild(li);
    }
  }
  // The daemon excludes blocked channels from the walk; re-run so the change
  // shows up in the list (WO-046).
  refreshSuggestions({ force: true }).catch(() => {});
}

async function loadBlocklist() {
  try {
    const r = await rpc("GET_BLOCKLIST");
    renderBlocklist(r.blocklist);
  } catch {
    renderBlocklist([]);
  }
}

/**
 * The panel's primary list is our ranking, not YouTube's rail (WO-046). The
 * rail is never rendered as a browsable list — even in fallback paths, the
 * panel says it has nothing rather than mirror the page behind it.
 */

/** Seed the walk with the video currently being watched (context_video_id is
 * on every impression; they all share it on one page). Empty on non-watch
 * pages — the daemon then falls back to its last-watch context. */
function currentSeed() {
  const imps = lastPageCache?.impressions || [];
  for (const imp of imps) if (imp.context_video_id) return imp.context_video_id;
  return "";
}

function renderSuggestions(res, seed) {
  const list = (res && res.suggestions) || [];
  if (!list.length) {
    el.list.replaceChildren();
    el.meta.textContent =
      "Nothing to suggest yet — Keel needs to have seen a few watch pages first.";
    return;
  }
  // Never fall back to the raw video id.
  //
  // Usually there is a title: the video being watched was itself recommended
  // somewhere Keel captured, so its title is already in the corpus. But not
  // always — a video reached from search, subscriptions, a channel page or a
  // link was never offered on a captured surface, so nothing here knows what it
  // is called. Printing the id in that case is the "meaningless hash" of
  // WO-041.
  const from = res.seed_title
    ? `From ${escapeHtml(res.seed_title)}`
    : seed
      ? "From the video you're watching"
      : "From your recent watching";
  el.meta.textContent =
    from +
    ` · graph ${res.graph_nodes} node(s), ${res.graph_edges} edge(s)` +
    (seed ? "" : " · no watch page open");
  el.list.replaceChildren();
  for (const s of list) el.list.appendChild(makeSuggestionLi(s));
}

/** Re-run the walk only when the watched video or the entropy changed — never
 * on every scan tick (WO-046). */
async function refreshSuggestions({ force = false } = {}) {
  const seed = currentSeed();
  const pageId = lastPageCache?.pageLoadId ?? "";
  const key = `${pageId}|${seed}|${entropy}`;
  if (!force && key === lastSuggestKey) return;
  lastSuggestKey = key;
  el.meta.textContent = "Walking the graph…";
  try {
    const r = await rpc("SUGGEST", {
      seed_video_id: seed,
      entropy,
      limit: 25,
    });
    renderSuggestions(r.suggest || {}, seed);
  } catch (err) {
    el.list.replaceChildren();
    el.meta.textContent =
      "Suggestions unavailable — start Keel's desktop app and open a watch page.";
  }
}

/** Absorb a page proof from the SW; re-run the walk on a new page_load_id. */
function absorbLastPage(lastPage) {
  const changed =
    !lastPageCache || lastPageCache.pageLoadId !== lastPage.pageLoadId;
  lastPageCache = lastPage;
  if (changed) refreshSuggestions({ force: true }).catch(() => {});
}

function renderStats(stats) {
  if (!stats) {
    el.total.textContent = "—";
    el.watch.textContent = "—";
    if (el.home) el.home.textContent = "—";
    el.time.textContent = "Counts appear when the desktop app is running.";
    if (el.channelNote) {
      el.channelNote.textContent =
        "Channel IDs are reliable for first-paint rail cards only. Cards loaded by " +
        "scrolling often have no channel in the page markup, so channel analysis " +
        "is incomplete unless you account for unknowns.";
    }
    return;
  }
  el.total.textContent = String(stats.total ?? 0);
  el.watch.textContent = String(stats.by_surface?.WATCH_NEXT ?? 0);
  if (el.home) el.home.textContent = String(stats.by_surface?.HOME ?? 0);
  el.time.textContent = `First: ${fmt(stats.first_observed_at)} · Last: ${fmt(
    stats.last_observed_at
  )}`;
  // Live unknown/known so the tiles never look like complete channel data (WO-013).
  if (el.channelNote && typeof stats.channel_unknown === "number") {
    const unk = stats.channel_unknown ?? 0;
    const known = stats.channel_known ?? 0;
    const total = stats.total ?? unk + known;
    el.channelNote.textContent =
      `Channel ID known: ${known} · unknown: ${unk}` +
      (total > 0 ? ` (${Math.round((unk / total) * 100)}% unknown).` : ".") +
      " First-paint rail cards usually have a channel; scrolled cards usually do not " +
      "(YouTube omits channel links in the lockup DOM).";
  }
}

/** Bump visible counts from an insert ACK until the next STATS refresh. */
function bumpCounts(inserted) {
  if (typeof inserted !== "number" || inserted <= 0) return;
  const t = Number(el.total.textContent);
  if (Number.isFinite(t)) el.total.textContent = String(t + inserted);
  const w = Number(el.watch.textContent);
  if (Number.isFinite(w)) el.watch.textContent = String(w + inserted);
}

/**
 * WO-040: a panel link loads the video in the active tab instead of a new one —
 * that is the point of a companion panel. Side-panel frames cannot navigate to
 * a web URL, so the active tab is repointed via tabs.update (host permission
 * covers youtube.com; no "tabs" permission needed).
 */
async function openVideoInActiveTab(href) {
  if (!browser.tabs?.query || !browser.tabs?.update) return;
  let tab;
  try {
    [tab] = await browser.tabs.query({ active: true, currentWindow: true });
  } catch {
    return;
  }
  const url = tab?.url || "";
  if (!/^https:\/\/www\.youtube\.com\//.test(url)) return;
  await browser.tabs.update(tab.id, { url: href });
}

/**
 * Ask the watched page to go back.
 *
 * The browser's own Back button does not work after a panel link: Chromium's
 * history manipulation intervention prunes entries created without user
 * activation, and an extension navigation counts as none. That was hardened
 * further in Chrome 142, so it will not come back.
 *
 * The carve-out is documented — the intervention "only impacts the browser
 * back/forward buttons and not the history.back()/forward() APIs" — so asking
 * the page to do it from inside traverses the real history, extension entries
 * included. tabs.goBack() is not usable: it behaves like the pruned button.
 */
async function goBackInActiveTab() {
  if (!browser.tabs?.query || !browser.tabs?.sendMessage) return;
  let tab;
  try {
    [tab] = await browser.tabs.query({ active: true, currentWindow: true });
  } catch {
    return;
  }
  if (!/^https:\/\/www\.youtube\.com\//.test(tab?.url || "")) return;
  try {
    await browser.tabs.sendMessage(tab.id, { type: "GO_BACK" });
  } catch {
    // No content script on that tab yet; nothing to go back through either.
  }
}

if (el.back) {
  el.back.addEventListener("click", () => {
    goBackInActiveTab().catch(() => {});
  });
}

el.list.addEventListener("click", (e) => {
  if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const a = e.target.closest?.("a[href^='https://www.youtube.com/watch?v=']");
  if (!a) return;
  e.preventDefault();
  openVideoInActiveTab(a.href).catch((err) =>
    console.warn("[Keel panel] open", err?.message || err)
  );
});

/**
 * Live path for STORE_UPDATED: apply lastPage from the SW payload.
 * No GET_STATS — that is reserved for open / manual / periodic refresh.
 */
function applyStoreUpdate(payload) {
  if (!payload || typeof payload !== "object") return;
  if (payload.lastPage) absorbLastPage(payload.lastPage);
  if (payload.stats) {
    renderStats(payload.stats);
    return;
  }
  bumpCounts(payload.inserted);
}

function setDataStatus(text, isError = false) {
  if (!el.dataStatus) return;
  el.dataStatus.textContent = text || "";
  el.dataStatus.classList.toggle("err", Boolean(isError));
}

function hideWipeConfirm() {
  if (el.wipeConfirm) el.wipeConfirm.hidden = true;
}

function showWipeConfirm(rowCount) {
  if (!el.wipeConfirm || !el.wipeConfirmText) return;
  const n = Number.isFinite(rowCount) ? rowCount : "?";
  el.wipeConfirmText.textContent =
    `This will permanently delete all ${n} stored recommendation(s) on this device. ` +
    `It cannot be undone. It does not change anything on YouTube or what YouTube knows about you.`;
  el.wipeConfirm.hidden = false;
}

async function doExport() {
  setDataStatus("Exporting…");
  hideWipeConfirm();
  try {
    const r = await rpc("EXPORT");
    const x = r.export || {};
    setDataStatus(
      `Exported ${x.rows ?? "?"} row(s) (${formatBytes(x.bytes)}) to:\n${x.path || "?"}`
    );
  } catch (err) {
    setDataStatus(err.message || String(err), true);
  }
}

function formatBytes(n) {
  if (typeof n !== "number" || !Number.isFinite(n)) return "? bytes";
  if (n < 1024) return `${n} bytes`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

async function doWipe() {
  setDataStatus("Deleting…");
  hideWipeConfirm();
  try {
    const r = await rpc("WIPE");
    const deleted = r.wipe?.deleted ?? 0;
    renderStats({
      total: 0,
      by_surface: { WATCH_NEXT: 0, HOME: 0 },
      first_observed_at: null,
      last_observed_at: null,
    });
    // No corpus left and no rail to fall back to (WO-046).
    lastPageCache = null;
    lastSuggestKey = "";
    refreshSuggestions({ force: true }).catch(() => {});
    setDataStatus(`Deleted ${deleted} row(s). Corpus is empty.`);
  } catch (err) {
    setDataStatus(err.message || String(err), true);
  }
}

/**
 * Daemon STATS round-trip. Throttled to ≥ STATS_MIN_INTERVAL_MS unless forced
 * (panel open, manual Refresh, daemon reconnect, periodic timer).
 */
async function refreshStats({ force = false } = {}) {
  const now = Date.now();
  if (!force && now - lastStatsAt < STATS_MIN_INTERVAL_MS) return;
  lastStatsAt = now;
  try {
    const st = await rpc("GET_STATS");
    setDaemonUi(Boolean(st.connected));
    renderStats(st.stats);
    if (st.lastPage) absorbLastPage(st.lastPage);
  } catch (err) {
    setDaemonUi(false, err.message);
    try {
      const s = await rpc("GET_STATUS");
      if (s.lastPage) absorbLastPage(s.lastPage);
    } catch {
      /* ignore */
    }
  }
}

/** Manual / initial: always hit STATS. */
async function refresh() {
  await refreshStats({ force: true });
}

browser.runtime.onMessage.addListener((msg) => {
  if (msg?.type === "DAEMON_STATUS") {
    setDaemonUi(Boolean(msg.payload?.connected), msg.payload?.detail || "");
    if (msg.payload?.connected) refreshStats({ force: true }).catch(() => {});
  }
  if (msg?.type === "STORE_UPDATED") {
    applyStoreUpdate(msg.payload || {});
    // Periodic catch-up for Counts only — not every emission.
    refreshStats({ force: false }).catch(() => {});
  }
});

/**
 * Signal panel open for with-panel hide; disconnect on close restores the rail.
 * Reconnect if the SW restarts while the panel is still open (MV3 eviction).
 */
function connectPanelPort() {
  try {
    const port = browser.runtime.connect({ name: SIDEPANEL_PORT });
    port.onDisconnect.addListener(() => {
      // lastError is set on unexpected disconnect; still retry while visible.
      setTimeout(connectPanelPort, 300);
    });
  } catch (err) {
    console.warn("[Keel panel] port", err?.message || err);
    setTimeout(connectPanelPort, 1000);
  }
}

/** @type {import('../lib/prefs.js').HideMode} */
let hideMode = DEFAULT_HIDE_MODE;

function paintHideButton() {
  if (!el.hideRail) return;
  const on = hideMode === "on";
  el.hideRail.setAttribute("aria-pressed", on ? "true" : "false");
  el.hideRail.title = on
    ? "Show YouTube's suggestions"
    : "Hide YouTube's suggestions";
  el.hideRail.setAttribute(
    "aria-label",
    on ? "Show YouTube's suggestions" : "Hide YouTube's suggestions"
  );
}

async function loadHideMode() {
  try {
    const r = await rpc("GET_HIDE_STATE");
    hideMode = coerceHideMode(r.mode);
  } catch {
    hideMode = DEFAULT_HIDE_MODE;
  }
  paintHideButton();
}

if (el.hideRail) {
  el.hideRail.addEventListener("click", () => {
    const next = hideMode === "on" ? "off" : "on";
    hideMode = next;
    paintHideButton();
    rpc("SET_HIDE_MODE", { mode: next }).catch((err) => {
      console.warn("[Keel panel] SET_HIDE_MODE", err?.message || err);
    });
  });
}

if (el.entropy) {
  el.entropy.addEventListener("input", () => {
    entropy = Number(el.entropy.value) || 0;
    refreshSuggestions({ force: true }).catch(() => {});
  });
}

el.refresh.addEventListener("click", () => refresh());

if (el.blockAddBtn && el.blockInput) {
  el.blockAddBtn.addEventListener("click", () => {
    const id = (el.blockInput.value || "").trim();
    if (!isChannelId(id)) {
      el.blockInput.setCustomValidity("Need a UC… channel id (24 chars after UC).");
      el.blockInput.reportValidity();
      return;
    }
    el.blockInput.setCustomValidity("");
    rpc("BLOCK_CHANNEL", { channel_id: id })
      .then((r) => {
        el.blockInput.value = "";
        renderBlocklist(r.blocklist);
      })
      .catch((err) => console.warn("[Keel panel] add block", err?.message || err));
  });
}

if (el.exportBtn) {
  el.exportBtn.addEventListener("click", () => {
    doExport().catch(() => {});
  });
}
if (el.wipeBtn) {
  el.wipeBtn.addEventListener("click", () => {
    const n = Number(el.total.textContent);
    showWipeConfirm(Number.isFinite(n) ? n : 0);
  });
}
if (el.wipeCancelBtn) {
  el.wipeCancelBtn.addEventListener("click", () => hideWipeConfirm());
}
if (el.wipeConfirmBtn) {
  el.wipeConfirmBtn.addEventListener("click", () => {
    doWipe().catch(() => {});
  });
}

setInterval(() => {
  refreshStats({ force: true }).catch(() => {});
}, STATS_MIN_INTERVAL_MS);
connectPanelPort();
loadHideMode();
loadBlocklist();
refresh();

/**
 * Surface an outstanding consent decision.
 *
 * Recording is gated on consent, so without this the panel would sit empty with
 * no explanation and no way forward — the consent screen otherwise appears only
 * once, at install, and anyone who closed it would have no route back.
 */
async function refreshConsentBanner() {
  let missing = false;
  try {
    const bag = await browser.storage?.local?.get(CONSENT_KEY);
    missing = !consentGranted(bag?.[CONSENT_KEY]);
  } catch {
    missing = false; // never nag on a storage error
  }
  if (el.consentBanner) el.consentBanner.hidden = !missing;
}

refreshConsentBanner().catch(() => {});
browser.storage?.onChanged?.addListener?.((changes, area) => {
  if (area === "local" && CONSENT_KEY in changes) refreshConsentBanner().catch(() => {});
});
