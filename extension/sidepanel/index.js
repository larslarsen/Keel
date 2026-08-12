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
import { escapeHtml, fmtDuration, watchUrl } from "../lib/render.js";
import {
  fmt,
  fmtCount,
  formatBytes,
  formatExplain,
  readableChannel,
} from "./render.js";
import { errText } from "../lib/errors.js";

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
  resuggest: document.getElementById("btn-resuggest"),
  queuePanel: document.getElementById("queue-panel"),
  queueList: document.getElementById("queue-list"),
  queueCount: document.getElementById("queue-count"),
  primaryHeading: document.getElementById("primary-heading"),
  entropyRow: document.getElementById("entropy-row"),
  clusterMeta: document.getElementById("cluster-meta"),
};

/** Video ids currently queued, so a suggestion row can show which state it is
 *  in without asking the daemon per row. */
let queuedSet = new Set();

const QUEUE_ICON_ON =
  '<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 7h11"/><path d="M4 12h11"/><path d="M4 17h7"/><path d="M15 18l2.5 2.5L22 16"/></svg>';
const QUEUE_ICON_OFF =
  '<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 7h12"/><path d="M4 12h12"/><path d="M4 17h8"/><path d="M18 14v7"/><path d="M14.5 17.5h7"/></svg>';

/** @type {Set<string>} */
let blocklistSet = new Set();
/** Active-tab page proof from SW (unfiltered). Used for the seed, not the list. */
let lastPageCache = null;
/** This panel document's own window id, from windows.getCurrent (WO-080). */
let myWindowId = null;

/**
 * Active-tab context (WO-073/080): which platform this window's ACTIVE tab is
 * on, which tab it is, and whether it is a watch page. The SW's proof store
 * is per-tab, so the panel must hold the active tab's identity and ignore
 * every proof whose ids do not match it. Set from the PANEL_CONTEXT broadcast
 * and from the PANEL_CONTEXT_QUERY sent on load.
 */
let panelCtx = { windowId: null, tabId: null, platform: null, focus: false };

/** The platform the panel should answer for: the active tab's, falling back
 *  to the page proof's (and finally yt) only before any context is known. */
function panelPlatform() {
  return panelCtx.platform || lastPageCache?.platform || "yt";
}

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

function setDaemonUi(connected, detail = "", meta = {}) {
  if (connected) {
    el.banner.className = "banner ok";
    el.banner.textContent = "Desktop app connected.";
    return;
  }
  el.banner.className = "banner warn";
  if (meta?.incompatible || /desktop app update required/i.test(String(detail || ""))) {
    el.banner.textContent =
      "Desktop app update required. This extension cannot use the installed Keel host until both are updated together." +
      (detail ? ` (${detail})` : "");
    return;
  }
  el.banner.textContent =
    "Keel's desktop app isn't running. The extension stays idle until you start it." +
    (detail ? ` (${detail})` : "");
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
      box.innerHTML = `<p class="explain-body err">${escapeHtml(errText(err))}</p>`;
    });
}

/** Compact count: 1.2K, 3.4M. */
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
  const chan =
    (typeof s.channel_name === "string" && s.channel_name) ||
    readableChannel(s.channel_id);
  const thumbBox = chan
    ? `<div class="thumb-wrap">${thumb}<span class="chan">${escapeHtml(chan)}</span></div>`
    : thumb;
  const href = watchUrl(s.video_id, panelPlatform());
  // Age rather than how many times Keel saw it. "2w ago" is what people read a
  // video's age from everywhere else, so it needs no explaining; the
  // observation count meant something only to us.
  const bits = [
    fmtDuration(s.duration_s),
    typeof s.view_count === "number" && s.view_count > 0
      ? `${fmtCount(s.view_count)} views`
      : "",
    s.published_at || "",
    typeof s.dwell_pct === "number" ? `${Math.round(s.dwell_pct * 100)}% watched` : "",
    typeof s.reason === "string" ? s.reason : "",
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

  // WO-064. Queueing is a claim on your own time, so it is one press and it is
  // reversible from here: pressing again takes it back out.
  const q = document.createElement("button");
  q.type = "button";
  q.className = "btn-icon btn-queue";
  q.dataset.vid = s.video_id;
  const setQueueState = () => {
    const on = queuedSet.has(s.video_id);
    q.title = on ? "Remove from watch queue" : "Add to watch queue";
    q.setAttribute("aria-label", q.title);
    q.setAttribute("aria-pressed", on ? "true" : "false");
    q.innerHTML = on ? QUEUE_ICON_ON : QUEUE_ICON_OFF;
  };
  setQueueState();
  q.addEventListener("click", (e) => {
    e.stopPropagation();
    const wasQueued = queuedSet.has(s.video_id);
    const call = wasQueued
      ? rpc("QUEUE_REMOVE", { index: queueIndexOf(s.video_id) })
      : rpc("QUEUE_ADD", {
          video_id: s.video_id,
          platform: panelPlatform(),
        });
    call
      .then((r) => renderQueue(r.queue))
      .catch((err) => console.warn("[Keel panel] queue", errText(err)));
  });
  actions.appendChild(q);

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
        .catch((err) => console.warn("[Keel panel] block", errText(err)));
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
            console.warn("[Keel panel] unblock", errText(err))
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

/* ---------------------------------------------------------------- WO-064:
 * the watch queue.
 *
 * The daemon owns the list and its order. Every mutation returns the whole
 * queue, so the panel never has to model what a mutation did — it draws what
 * came back. That is the same reason the blocklist works this way, and it is
 * what keeps the extension free of stored state (DESIGN_v2 §2.1).
 *
 * This is deliberately not YouTube's "Up next". Theirs is another surface
 * recommendations arrive on; this one contains only what you chose.
 */

/** @type {Array<{video_id:string,title:string,platform:string,duration_s:number}>} */
let queueItems = [];

/** Position of a video in the current queue, or -1. Removal is by position
 *  because that is what the daemon addresses and what the row represents. */
function queueIndexOf(videoID) {
  return queueItems.findIndex((it) => it.video_id === videoID);
}

/**
 * Play a queued video.
 *
 * `openVideoInActiveTab` deliberately does nothing when the active tab is not
 * YouTube or TikTok (WO-040) — but from the queue that is indistinguishable
 * from a broken button, and the queue is the one place the user has already
 * said what they want to watch. So when there is no page to repoint, open one.
 */
async function playQueued(href) {
  let tab;
  try {
    [tab] = await browser.tabs.query({ active: true, currentWindow: true });
  } catch {
    tab = null;
  }
  if (isSupportedSite(tab?.url)) return openVideoInActiveTab(href);
  if (browser.tabs?.create) await browser.tabs.create({ url: href });
}

function makeQueueLi(it, index, total) {
  const li = document.createElement("li");
  const href = watchUrl(it.video_id, it.platform);
  li.innerHTML =
    `<img class="thumb" loading="lazy" decoding="async" referrerpolicy="no-referrer"` +
    ` alt="" width="72" height="40" data-vid="${encodeURIComponent(it.video_id)}">` +
    `<div class="row-text">` +
    `<p class="title"><a href="${href}">${escapeHtml(it.title || "Untitled — no title recorded yet")}</a></p>` +
    `<div class="sub">${escapeHtml(fmtDuration(it.duration_s) || "")}</div>` +
    `<span class="row-actions"></span></div>`;

  // Play is ordinary navigation — the same thing clicking a link does. Keel
  // does not drive the player and does not touch YouTube's own queue.
  const link = li.querySelector(".title a");
  link.addEventListener("click", (e) => {
    e.preventDefault();
    playQueued(href).catch(() => {});
  });

  const actions = li.querySelector(".row-actions");
  const button = (label, svg, disabled, onClick) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn-icon btn-" + label.split(" ")[0].toLowerCase();
    b.title = label;
    b.setAttribute("aria-label", label);
    b.innerHTML = svg;
    if (disabled) b.disabled = true;
    else b.addEventListener("click", onClick);
    actions.appendChild(b);
  };
  const arrow = (d) =>
    `<svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2"><path d="${d}"/></svg>`;

  const move = (to) =>
    rpc("QUEUE_REORDER", { from: index, to })
      .then((r) => renderQueue(r.queue))
      .catch((err) => console.warn("[Keel panel] reorder", errText(err)));

  button("Move up", arrow("M12 19V5M5 12l7-7 7 7"), index === 0, () =>
    move(index - 1)
  );
  button(
    "Move down",
    arrow("M12 5v14M19 12l-7 7-7-7"),
    index === total - 1,
    () => move(index + 1)
  );
  button(
    "Remove from queue",
    arrow("M6 6l12 12M18 6L6 18"),
    false,
    () =>
      rpc("QUEUE_REMOVE", { index })
        .then((r) => renderQueue(r.queue))
        .catch((err) => console.warn("[Keel panel] unqueue", errText(err)))
  );

  const img = li.querySelector("img.thumb[data-vid]");
  if (img) fillThumb(img, decodeURIComponent(img.dataset.vid || ""));
  return li;
}

/** Draw whatever the daemon last said the queue is. */
function renderQueue(res) {
  queueItems = (res && res.items) || [];
  queuedSet = new Set(queueItems.map((it) => it.video_id));
  if (el.queueCount) {
    el.queueCount.textContent = queueItems.length ? `(${queueItems.length})` : "";
  }
  if (!el.queueList) return;
  el.queueList.replaceChildren();
  if (!queueItems.length) {
    const empty = document.createElement("li");
    empty.className = "empty";
    empty.textContent = "Nothing queued. Add a suggestion to watch it later.";
    el.queueList.appendChild(empty);
    syncQueueButtons();
    return;
  }
  queueItems.forEach((it, i) =>
    el.queueList.appendChild(makeQueueLi(it, i, queueItems.length))
  );
  syncQueueButtons();
}

/**
 * Resync the add-to-queue buttons in the suggestion list.
 *
 * The two lists show the same videos from different angles, so a removal made
 * in the queue has to be visible in the suggestion row too — otherwise the row
 * goes on claiming the video is queued and pressing it removes something else.
 */
function syncQueueButtons() {
  const btns = el.list ? el.list.querySelectorAll(".btn-queue[data-vid]") : [];
  for (const b of btns) {
    const on = queuedSet.has(b.dataset.vid);
    if (b.getAttribute("aria-pressed") === String(on)) continue;
    b.setAttribute("aria-pressed", String(on));
    b.title = on ? "Remove from watch queue" : "Add to watch queue";
    b.setAttribute("aria-label", b.title);
    b.innerHTML = on ? QUEUE_ICON_ON : QUEUE_ICON_OFF;
  }
}

async function loadQueue() {
  try {
    const r = await rpc("QUEUE_LIST", {});
    renderQueue(r.queue);
  } catch {
    renderQueue(null);
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
/** Seed the walk with the ACTIVE tab's video, not any tab's (WO-073). The
 *  proof cache is window-global — whichever tab last reported — so a YT proof
 *  must not seed a walk on a TikTok tab. */
function currentSeed() {
  // No watch page open in this window: nothing to seed a walk from. The SW
  // closes the panel outright when it can (Chrome 141+); on engines without
  // sidePanel.close this is what renders the honest "no watch page open" state.
  if (!panelCtx.focus) return "";
  // Window-global proof must not seed across platforms (WO-073): a YT tab's
  // proof is not a seed for a TikTok walk.
  if (lastPageCache?.platform && panelCtx.platform && lastPageCache.platform !== panelCtx.platform) {
    return "";
  }
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

/** TikTok Mirror (WO-063): scroll history + local hashtag/sound clusters. */
function renderScrollHistory(res) {
  const items = (res && res.items) || [];
  const tags = res?.hashtag_counts || {};
  const sounds = res?.sound_counts || {};
  if (!items.length) {
    el.list.replaceChildren();
    el.meta.textContent =
      "No scroll history yet — open TikTok's For You feed and scroll a few clips.";
    if (el.clusterMeta) {
      el.clusterMeta.hidden = true;
      el.clusterMeta.textContent = "";
    }
    return;
  }
  el.meta.textContent = `${items.length} clip(s) in your recent scroll history`;
  const topTags = Object.entries(tags)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 8)
    .map(([t, n]) => `#${t} (${n})`);
  const topSounds = Object.entries(sounds)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 5)
    .map(([id, n]) => `sound ${id.slice(-6)} (${n})`);
  if (el.clusterMeta) {
    const parts = [];
    if (topTags.length) parts.push("Hashtags: " + topTags.join(" · "));
    if (topSounds.length) parts.push("Sounds: " + topSounds.join(" · "));
    el.clusterMeta.textContent = parts.join("\n") || "";
    el.clusterMeta.hidden = !parts.length;
  }
  el.list.replaceChildren();
  for (const it of items) {
    el.list.appendChild(
      makeSuggestionLi({
        video_id: it.video_id,
        title: it.title,
        channel_name: it.channel_name || it.channel_id || "",
        channel_id: it.channel_id,
        reason: (it.hashtags || []).slice(0, 4).map((t) => `#${t}`).join(" ") ||
          (it.sound_id ? `sound …${String(it.sound_id).slice(-6)}` : ""),
        dwell_pct: it.dwell_pct,
      })
    );
  }
}

function applyPlatformChrome() {
  const tt = panelPlatform() === "tt";
  if (el.primaryHeading) {
    el.primaryHeading.textContent = tt ? "Scroll history" : "Our suggestions";
  }
  if (el.entropyRow) el.entropyRow.hidden = tt;
  if (el.clusterMeta && !tt) {
    el.clusterMeta.hidden = true;
    el.clusterMeta.textContent = "";
  }
}

/** Re-run the walk only when the watched video or the entropy changed — never
 * on every scan tick (WO-046). On TikTok the Mirror shows scroll history
 * instead (WO-063) — there is no rail to re-rank. */
async function refreshSuggestions({ force = false } = {}) {
  applyPlatformChrome();
  const plat = panelPlatform();
  if (plat === "tt") {
    const key = `tt|history|${lastPageCache?.pageLoadId ?? ""}`;
    if (!force && key === lastSuggestKey) return;
    lastSuggestKey = key;
    el.meta.textContent = "Loading scroll history…";
    try {
      const r = await rpc("SCROLL_HISTORY", { platform: "tt", limit: 50 });
      renderScrollHistory(r.history || {});
    } catch {
      el.list.replaceChildren();
      el.meta.textContent =
        "History unavailable — start Keel's desktop app and scroll TikTok.";
    }
    return;
  }
  const seed = currentSeed();
  const pageId = lastPageCache?.pageLoadId ?? "";
  const key = `${plat}|${pageId}|${seed}|${entropy}`;
  if (!force && key === lastSuggestKey) return;
  lastSuggestKey = key;
  el.meta.textContent = "Walking the graph…";
  try {
    const r = await rpc("SUGGEST", {
      // Scoped, never blended: a TikTok clip is not an answer to "what next"
      // after a YouTube video, and the two graphs are built by different
      // systems (WO-057). The platform is the ACTIVE tab's (WO-073), not the
      // last tab that reported a page proof.
      platform: plat,
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

/** Absorb a page proof from the SW when it belongs to THIS window's ACTIVE
 *  tab (WO-080); re-run the walk on a new page_load_id. */
function absorbLastPage(lastPage) {
  if (!lastPage || typeof lastPage !== "object") return;
  // A proof whose platform is not the active tab's cannot seed this panel
  // (WO-073: a TikTok tab's proof is not a seed for a YouTube walk).
  if (lastPage.platform && panelCtx.platform && lastPage.platform !== panelCtx.platform) {
    return;
  }
  // WO-080: the proof must be the ACTIVE tab's — window first, then tab id.
  // A background tab's STORE_UPDATED broadcast must never seed the panel, and
  // two same-platform tabs must never cross-seed. Unknown ids (before any
  // context is known) fall back to the platform check only.
  if (
    lastPage.windowId != null &&
    panelCtx.windowId != null &&
    lastPage.windowId !== panelCtx.windowId
  ) {
    return;
  }
  if (lastPage.tabId != null && panelCtx.tabId != null && lastPage.tabId !== panelCtx.tabId) {
    return;
  }
  const changed =
    !lastPageCache || lastPageCache.pageLoadId !== lastPage.pageLoadId;
  lastPageCache = lastPage;
  if (changed) {
    refreshSuggestions({ force: true }).catch(() => {});
    // Autoplay drains the queue in the daemon, and the navigation it causes is
    // the only sign the panel gets. Re-read rather than model it here.
    loadQueue().catch(() => {});
  }
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
/** Sites Keel runs on. Kept beside the navigation helpers that guard on it. */
function isSupportedSite(url) {
  return /^https:\/\/www\.(youtube|tiktok)\.com\//.test(String(url || ""));
}

async function openVideoInActiveTab(href) {
  if (!browser.tabs?.query || !browser.tabs?.update) return;
  let tab;
  try {
    [tab] = await browser.tabs.query({ active: true, currentWindow: true });
  } catch {
    return;
  }
  if (!isSupportedSite(tab?.url)) return;
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
  if (!isSupportedSite(tab?.url)) return;
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
    console.warn("[Keel panel] open", errText(err))
  );
});

/**
 * Live path for STORE_UPDATED: absorb only the ACTIVE tab's proof from the SW
 * payload (WO-080); refresh counts from the insert ack. No GET_STATS — that is
 * reserved for open / manual / periodic refresh.
 */
function applyStoreUpdate(payload) {
  if (!payload || typeof payload !== "object") return;
  if (payload.proof) absorbLastPage(payload.proof);
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
    setDataStatus(errText(err), true);
  }
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
    setDataStatus(errText(err), true);
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
    const st = await rpc("GET_STATS", { windowId: myWindowId });
    setDaemonUi(Boolean(st.connected));
    renderStats(st.stats);
    if (st.proof) absorbLastPage(st.proof);
  } catch (err) {
    setDaemonUi(false, err.message);
    try {
      const s = await rpc("GET_STATUS", { windowId: myWindowId });
      if (s.proof) absorbLastPage(s.proof);
    } catch {
      /* ignore */
    }
  }
}

/** Manual / initial: always hit STATS. */
async function refresh() {
  await refreshStats({ force: true });
}

/**
 * Track the active tab's context (WO-073/080): the window's active tab decides
 * which platform the panel answers for, and its TAB ID decides which proof
 * may seed the panel. Swapping YT→TT in the same window previously kept the
 * YouTube suggestions because the proof was window-global; the id check is
 * what stops two same-platform tabs from cross-seeding each other.
 */
function applyPanelContext(ctx) {
  if (!ctx || typeof ctx !== "object") return;
  // Panel is per-window; ignore another window's context broadcasts (the
  // SW may not know every panel's window id on engines with no sidePanel.close).
  if (panelCtx.windowId != null && ctx.window_id != null && ctx.window_id !== panelCtx.windowId) {
    return;
  }
  const platformChanged = ctx.platform != null && ctx.platform !== panelCtx.platform;
  const tabChanged = ctx.tab_id != null && ctx.tab_id !== panelCtx.tabId;
  panelCtx = {
    windowId: ctx.window_id ?? panelCtx.windowId,
    tabId: ctx.tab_id ?? panelCtx.tabId,
    platform: ctx.platform ?? panelCtx.platform,
    focus: ctx.focus ?? panelCtx.focus,
  };
  if (platformChanged || tabChanged) {
    // The active tab moved (to another platform, or to another tab): the
    // cached proof belongs to the previous active tab and must not seed or
    // label this one (WO-080 — same-platform tabs are the whole point).
    lastPageCache = null;
    lastSuggestKey = "";
    refreshSuggestions({ force: true }).catch(() => {});
  }
}

/** Ask the SW which context this window's active tab has. Sent on load: the
 * SW may have been evicted and missed the tabs.onActivated broadcast, and the
 * panel never sees the moment it was opened. */
async function queryPanelContext() {
  try {
    applyPanelContext(await rpc("PANEL_CONTEXT_QUERY", { windowId: myWindowId }));
  } catch {
    // Best effort; a broadcast will catch up after the SW wakes.
  }
}

browser.runtime.onMessage.addListener((msg) => {
  if (msg?.type === "PANEL_CONTEXT") {
    applyPanelContext(msg.payload);
  }
  if (msg?.type === "DAEMON_STATUS") {
    setDaemonUi(Boolean(msg.payload?.connected), msg.payload?.detail || "", msg.payload || {});
    if (msg.payload?.connected) refreshStats({ force: true }).catch(() => {});
    refreshConsentBanner().catch(() => {});
  }
  if (msg?.type === "CONTRIBUTION_STATUS") {
    refreshConsentBanner().catch(() => {});
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
    // Tell the SW which window this panel lives in (WO-075): the toolbar
    // toggle is per-window, so a panel open in another window must not block
    // opening this one. Coming from an extension page, windows.getCurrent
    // needs no permission. A null window id stays conservative (the SW then
    // treats the port as "open everywhere"). The same id scopes the panel's
    // proof/context queries (WO-080).
    browser.windows
      ?.getCurrent?.()
      .then((w) => {
        myWindowId = w?.id ?? null;
        port.postMessage({ type: "PANEL_HANDSHAKE", payload: { windowId: myWindowId } });
      })
      .catch(() => {
        port.postMessage({ type: "PANEL_HANDSHAKE", payload: { windowId: null } });
      });
  } catch (err) {
    console.warn("[Keel panel] port", errText(err));
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
      console.warn("[Keel panel] SET_HIDE_MODE", errText(err));
    });
  });
}

if (el.entropy) {
  el.entropy.addEventListener("input", () => {
    entropy = Number(el.entropy.value) || 0;
    refreshSuggestions({ force: true }).catch(() => {});
  });
}

// WO-065. Force, always: the point of pressing it is to get a different answer
// out of the same seed, and the walk is random, so the cache key that normally
// suppresses a re-run is exactly what has to be bypassed.
if (el.resuggest) {
  el.resuggest.addEventListener("click", () => {
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
      .catch((err) => console.warn("[Keel panel] add block", errText(err)));
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
queryPanelContext();
loadHideMode();
loadBlocklist();
loadQueue();
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
  let daemonPending = false;
  try {
    const bag = await browser.storage?.local?.get(CONSENT_KEY);
    missing = !consentGranted(bag?.[CONSENT_KEY]);
  } catch {
    missing = false; // never nag on a storage error
  }
  try {
    const r = await rpc("GET_NETWORK_CONSENT");
    daemonPending = r?.daemon?.consent_required === true;
  } catch {
    daemonPending = false;
  }
  if (el.consentBanner) el.consentBanner.hidden = !(missing || daemonPending);
  const text = document.getElementById("consent-banner-text");
  if (text) {
    text.textContent = daemonPending
      ? "Keel needs you to accept what it records and downloads before the network starts."
      : "Keel is not recording yet — it needs your go-ahead first.";
  }
}

refreshConsentBanner().catch(() => {});
browser.storage?.onChanged?.addListener?.((changes, area) => {
  if (area === "local" && CONSENT_KEY in changes) refreshConsentBanner().catch(() => {});
});
