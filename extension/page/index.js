// SPDX-License-Identifier: Apache-2.0
/**
 * Full-page surface (WO-022): search and data management.
 *
 * The SidePanel is the at-a-glance view while a video plays; this page is where
 * there is room to actually look at the corpus. Everything goes through the SW
 * to the daemon — this page holds no observation data of its own.
 */
import { browser } from "../lib/browser.js";

const el = {
  banner: document.getElementById("daemon-banner"),
  form: document.getElementById("search-form"),
  q: document.getElementById("q"),
  meta: document.getElementById("search-meta"),
  results: document.getElementById("results"),
  total: document.getElementById("stat-total"),
  watch: document.getElementById("stat-watch"),
  home: document.getElementById("stat-home"),
  time: document.getElementById("time-range"),
  channelNote: document.getElementById("channel-note"),
  export: document.getElementById("btn-export"),
  wipe: document.getElementById("btn-wipe"),
  dataStatus: document.getElementById("data-status"),
  wipeConfirm: document.getElementById("wipe-confirm"),
  wipeConfirmText: document.getElementById("wipe-confirm-text"),
  wipeYes: document.getElementById("btn-wipe-confirm"),
  wipeNo: document.getElementById("btn-wipe-cancel"),
  bundleBtn: document.getElementById("btn-bundle"),
  importBtn: document.getElementById("btn-import"),
  importPath: document.getElementById("import-path"),
  shareStatus: document.getElementById("share-status"),
  peerList: document.getElementById("peer-list"),
  entropy: document.getElementById("entropy"),
  suggestBtn: document.getElementById("btn-suggest"),
  suggestMeta: document.getElementById("suggest-meta"),
  suggestions: document.getElementById("suggestions"),
  liveQ: document.getElementById("live-q"),
  liveMeta: document.getElementById("live-meta"),
  liveList: document.getElementById("live-list"),
};

async function rpc(type, payload) {
  const r = await browser.runtime.sendMessage({ type, payload });
  if (!r?.ok) throw new Error(r?.error || type);
  return r;
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

function fmtCount(n) {
  if (typeof n !== "number") return "";
  if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

function fmtDuration(s) {
  if (typeof s !== "number" || s <= 0) return "";
  const m = Math.floor(s / 60);
  const sec = String(Math.floor(s % 60)).padStart(2, "0");
  if (m >= 60) return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}:${sec}`;
  return `${m}:${sec}`;
}


/** Derived thumbnail URL — no API call. See WO-039. */
function thumbHtml(videoID) {
  return (
    `<img class="thumb" loading="lazy" decoding="async" referrerpolicy="no-referrer"` +
    ` alt="" width="120" height="68"` +
    ` data-vid="${encodeURIComponent(videoID)}">`
  );
}

/* ---------- WO-040: surface the panel when a video link opens ---------- */

/**
 * Intercept a full-page video-link click while the user gesture is still alive.
 *
 * The panel is tab-specific (WO-021), so `sidePanel.open` must name a tab and
 * that tab's panel must already be enabled — `open({ windowId })` only targets a
 * *global* panel. So: create the watch tab, enable the panel on it, then open
 * it. All three stay inside the click handler; user activation does not survive
 * a runtime message round-trip, which is why the earlier SW-deferred approach
 * could not work.
 */
async function openPanelOnNewWatchTab(href) {
  if (!browser.tabs?.create || !browser.sidePanel?.setOptions || !browser.sidePanel?.open) return;
  const tab = await browser.tabs.create({ url: href, active: true });
  if (tab?.id == null) return;
  await browser.sidePanel.setOptions({
    tabId: tab.id,
    path: "sidepanel/index.html",
    enabled: true,
  });
  await browser.sidePanel.open({ tabId: tab.id });
}

function armPanelOnVideoLinkClick(list) {
  list.addEventListener("click", (e) => {
    // Let modified clicks and non-left buttons keep their native behavior
    // (new tab, background tab); intercept only plain left clicks.
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target.closest?.("a[href^='https://www.youtube.com/watch?v=']");
    if (!a) return;
    e.preventDefault();
    openPanelOnNewWatchTab(a.href).catch((err) =>
      console.warn("[Keel] open panel", err?.message || err)
    );
  });
}
armPanelOnVideoLinkClick(el.results);
armPanelOnVideoLinkClick(el.suggestions);

/* ---------- tabs ---------- */

function selectTab(name) {
  for (const t of ["search", "suggest", "live", "analysis", "config"]) {
    const tab = document.getElementById(`tab-${t}`);
    const view = document.getElementById(`view-${t}`);
    const on = t === name;
    tab.setAttribute("aria-selected", String(on));
    view.hidden = !on;
  }
  if (name === "config") {
    refreshStats().catch(() => {});
    refreshPeers().catch(() => {});
    refreshAggregate().catch(() => {});
    wireDiskSlider();
    refreshDisk().catch(() => {});
    refreshConsent().catch(() => {});
    refreshContribution().catch(() => {});
  }
  if (name === "search") el.q.focus();
  if (name === "suggest" && !el.suggestions.children.length) {
    loadSuggestions().catch(() => {});
  }
  if (name === "live") {
    el.liveQ.focus();
    loadLive().catch(() => {});
  }
  if (name === "analysis") loadAnalysis().catch(() => {});
}

for (const t of ["search", "suggest", "live", "analysis", "config"]) {
  document.getElementById(`tab-${t}`).addEventListener("click", () => selectTab(t));
}

/* ---------- live ---------- */

/**
 * The live feed (DESIGN_v2 §7.5).
 *
 * Filtering happens in the daemon's memory, over an index it already holds in
 * full — so typing here sends no query anywhere. That is why this box filters as
 * you type rather than waiting for a submit: there is no request to be careful
 * about.
 */
function fmtAgo(ms) {
  if (!ms) return "";
  const secs = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (secs < 90) return "just now";
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins} min ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs} hour${hrs === 1 ? "" : "s"} ago`;
  return `${Math.floor(hrs / 24)} day${hrs < 48 ? "" : "s"} ago`;
}

function renderLive(res) {
  el.liveList.replaceChildren();
  const streams = res?.streams || [];

  if (res && res.available === false) {
    el.liveMeta.textContent = res.reason || "Not connected to the network yet.";
    return;
  }
  if (!streams.length) {
    el.liveMeta.textContent = res?.query
      ? `Nothing matching “${res.query}” among ${res.indexed || 0} known streams.`
      : "No streams reported yet. The feed fills as other nodes see livestreams.";
    return;
  }
  el.liveMeta.textContent =
    `${streams.length} stream${streams.length === 1 ? "" : "s"}` +
    (res.indexed ? ` of ${res.indexed} known` : "");

  for (const s of streams) {
    const li = document.createElement("li");
    li.innerHTML =
      `<div class="row-main">${thumbHtml(s.v)}<div class="row-text">` +
      `<p class="r-title"><a href="https://www.youtube.com/watch?v=${encodeURIComponent(s.v)}"` +
      ` target="_blank" rel="noreferrer">${escapeHtml(s.t || s.v)}</a></p>` +
      `<p class="r-sub">seen live ${escapeHtml(fmtAgo(s.last_seen))}` +
      (s.c ? ` · ${escapeHtml(s.c)}` : "") +
      `</p></div></div>`;
    el.liveList.appendChild(li);
    const im = li.querySelector("img.thumb[data-vid]");
    if (im) fillThumb(im, decodeURIComponent(im.dataset.vid || ""));
  }
}

async function loadLive() {
  try {
    const r = await rpc("LIVE_SEARCH", {
      query: el.liveQ.value.trim(),
      limit: 200,
    });
    renderLive(r.live);
  } catch (err) {
    el.liveList.replaceChildren();
    el.liveMeta.textContent = String(err?.message || err);
  }
}

let liveTimer = null;
el.liveQ.addEventListener("input", () => {
  // Debounced only to avoid re-rendering on every keystroke; there is no
  // network call to rate-limit.
  clearTimeout(liveTimer);
  liveTimer = setTimeout(() => loadLive().catch(() => {}), 120);
});

/* ---------- search ---------- */

function renderHits(res) {
  el.results.replaceChildren();
  const hits = res?.hits || [];
  if (!hits.length) {
    el.meta.textContent = res?.query
      ? `Nothing found for “${res.query}”. Keel only searches what it has seen on this device.`
      : "";
    return;
  }
  el.meta.textContent =
    `${res.total} match${res.total === 1 ? "" : "es"}` +
    (res.truncated ? ` · showing ${hits.length}` : "");

  for (const h of hits) {
    const li = document.createElement("li");
    const dur = fmtDuration(h.duration_s);
    const views = typeof h.view_count === "number" ? `${fmtCount(h.view_count)} views` : "";
    const bits = [views, h.published_at || "", dur].filter(Boolean).join(" · ");
    // seen = how many times Keel observed this being recommended. That is the
    // corpus's own signal and the reason results are ordered this way.
    li.innerHTML =
      `<div class="row-main">${thumbHtml(h.video_id)}<div class="row-text">` +
      `<p class="r-title"><a href="https://www.youtube.com/watch?v=${encodeURIComponent(h.video_id)}"` +
      ` target="_blank" rel="noreferrer">${escapeHtml(h.title || h.video_id)}</a></p>` +
      `<p class="r-sub">${escapeHtml(bits)}` +
      (bits ? " · " : "") +
      (h.seen > 0 ? `seen ${h.seen}×` : `from a shared bundle`) +
      (h.channel_id ? ` · ${escapeHtml(h.channel_id)}` : "") +
      `</p></div></div>`;
    el.results.appendChild(li);
    const im = li.querySelector("img.thumb[data-vid]");
    if (im) fillThumb(im, decodeURIComponent(im.dataset.vid || ""));
  }
}

el.form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const query = el.q.value.trim();
  if (!query) return;
  el.meta.textContent = "Searching…";
  try {
    const r = await rpc("SEARCH", { query, limit: 100 });
    renderHits(r.search);
  } catch (err) {
    el.meta.textContent = `Search failed: ${err.message}`;
  }
});

/* ---------- suggestions ---------- */

async function loadSuggestions() {
  el.suggestMeta.textContent = "Walking the graph…";
  el.suggestions.replaceChildren();
  try {
    const r = await rpc("SUGGEST", {
      entropy: Number(el.entropy.value),
      limit: 25,
    });
    const res = r.suggest || {};
    const list = res.suggestions || [];
    if (!list.length) {
      el.suggestMeta.textContent =
        "Nothing to suggest yet — Keel needs to have seen a few watch pages first.";
      return;
    }
    el.suggestMeta.textContent =
      `From ${escapeHtml(res.seed_title || res.seed_video_id || "your last watch")}` +
      ` · graph ${res.graph_nodes} node(s), ${res.graph_edges} edge(s)`;
    for (const s of list) {
      const li = document.createElement("li");
      const views =
        typeof s.view_count === "number" ? `${fmtCount(s.view_count)} views` : "";
      const bits = [views, fmtDuration(s.duration_s)].filter(Boolean).join(" · ");
      li.innerHTML =
        `<div class="row-main">${thumbHtml(s.video_id)}<div class="row-text">` +
        `<p class="r-title"><a href="https://www.youtube.com/watch?v=${encodeURIComponent(s.video_id)}"` +
        ` target="_blank" rel="noreferrer">${escapeHtml(s.title || s.video_id)}</a></p>` +
        `<p class="r-sub">${escapeHtml(bits)}` +
        (bits ? " · " : "") +
        `seen ${s.seen}×` +
        (s.via_title ? ` · appeared after ${escapeHtml(s.via_title)}` : "") +
        (s.from_peer ? ` · via shared bundle` : "") +
        `</p></div></div>`;
      el.suggestions.appendChild(li);
      const im = li.querySelector("img.thumb[data-vid]");
      if (im) fillThumb(im, decodeURIComponent(im.dataset.vid || ""));
    }
  } catch (err) {
    el.suggestMeta.textContent = `Suggest failed: ${err.message}`;
  }
}

el.suggestBtn.addEventListener("click", () => loadSuggestions().catch(() => {}));

/* ---------- sharing ---------- */

const MB = 1024 * 1024;

const LEVELS = [
  {
    n: 1,
    name: "Strictly personal",
    body:
      "Your recordings stay here. You get the whole product — search, " +
      "suggestions, blocking, analysis. Keel still announces livestreams it " +
      "sees, with no sender attached, so the Live tab works for everyone.",
  },
  {
    n: 2,
    name: "Mirror",
    body:
      "Lends disk space to store and pass on data other people published — " +
      "the recommendation graph and titles that let suggestions reach past " +
      "what you have seen yourself. Nothing you recorded is published. " +
      "Requests ask for a bucket of thousands of videos at once and filter " +
      "on your machine, and your computer uses a different network identity " +
      "each session, so a peer answering cannot tell which video you wanted " +
      "or link your requests together. Set the disk limit below.",
  },
  {
    n: 3,
    name: "Cohort aggregator",
    body:
      "Would add counts of which videos were recommended after which, " +
      "grouped by rough position and day, under threshold encryption: your " +
      "report stays sealed unless enough other people report the same thing, " +
      "so anything only you saw can never be read. Not built.",
  },
  {
    n: 4,
    name: "Transparency contributor",
    body:
      "Would publish your full recommendation trails, attributed to you. " +
      "YouTube already knows what it showed you — what changes is that " +
      "everyone else would too, and that YouTube would know you are the one " +
      "publishing it. Researchers running similar projects on other platforms " +
      "have been retaliated against. Cannot be withdrawn once copied. Not built.",
  },
];

async function refreshConsent() {
  const row = document.getElementById("consent-row");
  const actions = document.getElementById("consent-actions");
  if (!row) return;
  let v = null;
  try {
    v = (await rpc("GET_CONSENT")).consent;
  } catch {
    return;
  }
  const on = v === "granted";
  row.textContent = on
    ? "Keel is recording the recommendations YouTube shows you, to this device only. To stop it, remove the extension or the desktop app — Wipe, below, erases what it already holds."
    : "Keel is not recording. It asks first, and has not been given the go-ahead on this browser.";
  if (actions) actions.hidden = on;
}

async function refreshContribution() {
  const wrap = document.getElementById("contrib-levels");
  const note = document.getElementById("contrib-note");
  if (!wrap) return;
  let level = 1;
  let maxImpl = 1;
  try {
    const r = await rpc("GET_CONTRIBUTION");
    level = r.daemon?.level ?? 1;
    maxImpl = r.daemon?.max_implemented ?? 1;
  } catch {
    return;
  }
  note.textContent =
    maxImpl < 2
      ? "Keel sends nothing anywhere today. The levels below describe what " +
        "contributing would mean when it exists; only the first is available."
      : "Choose how much this node contributes.";
  wrap.replaceChildren();
  for (const l of LEVELS) {
    const avail = l.n <= maxImpl;
    const row = document.createElement("label");
    row.className = "contrib" + (avail ? "" : " contrib-off");
    row.innerHTML =
      `<input type="radio" name="contrib" value="${l.n}"` +
      `${l.n === level ? " checked" : ""}${avail ? "" : " disabled"}>` +
      `<span><strong>${escapeHtml(l.name)}</strong>` +
      `<span class="meta">${escapeHtml(l.body)}` +
      (avail ? "" : " <em>Not available yet.</em>") +
      `</span></span>`;
    wrap.appendChild(row);
  }
  wrap.querySelectorAll('input[name="contrib"]').forEach((el) => {
    el.addEventListener("change", async () => {
      try {
        await rpc("SET_CONTRIBUTION", { level: Number(el.value) });
      } catch (err) {
        console.warn("[Keel] contribution", err?.message || err);
      }
      await refreshContribution();
    });
  });
}

async function refreshDisk() {
  const slider = document.getElementById("disk-budget");
  const label = document.getElementById("disk-label");
  if (!slider || !label) return;
  try {
    const r = await rpc("GET_DISK_BUDGET");
    const d = r.daemon || {};
    slider.value = String(Math.round((d.budget_bytes || 0) / MB));
    label.textContent =
      `${slider.value} MB budget · ${(d.used_bytes / MB).toFixed(1)} MB used` +
      ` (${d.items || 0} thumbnail(s))`;
  } catch {
    /* daemon down; banner already says so */
  }
}

function wireDiskSlider() {
  const slider = document.getElementById("disk-budget");
  if (!slider || slider.dataset.wired) return;
  slider.dataset.wired = "1";
  slider.addEventListener("change", async () => {
    try {
      await rpc("SET_DISK_BUDGET", { bytes: Number(slider.value) * MB });
      await refreshDisk();
    } catch (err) {
      console.warn("[Keel] disk budget", err?.message || err);
    }
  });
}

async function refreshPeers() {
  try {
    const r = await rpc("PEERS");
    const p = r.bundle || {};
    el.peerList.replaceChildren();
    for (const peer of p.peers || []) {
      const li = document.createElement("li");
      li.innerHTML =
        `<span class="an-label">${escapeHtml(peer.source)}</span>` +
        `<span class="an-count">${peer.edges} edge(s) ` +
        `<button type="button" class="btn" data-forget="${escapeHtml(peer.source)}">Forget</button></span>`;
      el.peerList.appendChild(li);
    }
    if (!(p.peers || []).length) {
      el.peerList.innerHTML = `<li><span class="an-label">No imported bundles.</span></li>`;
    }
  } catch {
    /* daemon down; banner already says so */
  }
}

el.peerList.addEventListener("click", async (e) => {
  const src = e.target?.dataset?.forget;
  if (!src) return;
  try {
    await rpc("FORGET_PEER", { source: src });
    el.shareStatus.textContent = `Forgot ${src}.`;
    await refreshPeers();
  } catch (err) {
    el.shareStatus.textContent = `Could not forget: ${err.message}`;
  }
});

el.bundleBtn.addEventListener("click", async () => {
  el.shareStatus.textContent = "Building bundle…";
  try {
    const r = await rpc("EXPORT_BUNDLE");
    const b = r.bundle || {};
    el.shareStatus.textContent =
      `Wrote ${b.edges} edge(s) and ${b.catalogue} catalogue entr(ies) to ${b.path}`;
  } catch (err) {
    el.shareStatus.textContent = `Bundle failed: ${err.message}`;
  }
});

el.importBtn.addEventListener("click", async () => {
  const path = el.importPath.value.trim();
  if (!path) {
    el.shareStatus.textContent = "Give the path to a bundle file.";
    return;
  }
  el.shareStatus.textContent = "Importing…";
  try {
    const r = await rpc("IMPORT_BUNDLE", { path });
    const b = r.bundle || {};
    el.shareStatus.textContent =
      `Imported ${b.edges} edge(s) from ${b.node_id}. Suggestions now draw on them.`;
    await refreshPeers();
  } catch (err) {
    el.shareStatus.textContent = `Import failed: ${err.message}`;
  }
});

/* ---------- analysis ---------- */

function tile(value, label) {
  return `<div><strong>${escapeHtml(String(value))}</strong><span>${escapeHtml(label)}</span></div>`;
}

function analysisTable(title, rows, note) {
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

async function loadAnalysis() {
  const body = document.getElementById("analysis-body");
  const stats = document.getElementById("an-stats");
  body.innerHTML = `<p class="meta">Loading…</p>`;
  try {
    const r = await rpc("ANALYSIS");
    const a = r.analysis || {};
    stats.innerHTML =
      tile(a.total_impressions ?? 0, "impressions") +
      tile(a.distinct_videos ?? 0, "distinct videos") +
      tile(a.distinct_channels ?? 0, "distinct channels") +
      tile(a.watched_videos ?? 0, "videos you watched") +
      (a.peer_edges
        ? tile(a.peer_edges, `imported edges (${a.peer_sources} source)`)
        : "");
    body.innerHTML =
      analysisTable(
        "Pushed hardest",
        a.top_videos,
        "Videos recommended to you most often, with their average position."
      ) +
      analysisTable("Channels seen most", a.top_channels) +
      analysisTable(
        "Strongest pairs",
        a.top_edges,
        "After the first video, the second one appeared this often."
      );
    if (!a.total_impressions) {
      body.innerHTML = `<p class="meta">Nothing recorded yet.</p>`;
    }
  } catch (err) {
    body.innerHTML = `<p class="meta">Analysis failed: ${escapeHtml(err.message)}</p>`;
  }
}

/* ---------- data ---------- */

function setDaemonUi(ok, detail = "") {
  el.banner.className = ok ? "banner ok" : "banner warn";
  el.banner.textContent = ok
    ? "Desktop app connected. Your recordings stay on this device."
    : "Keel's desktop app isn't running." + (detail ? ` (${detail})` : "");
}

async function refreshAggregate() {
  const note = document.getElementById("aggregate-note");
  if (!note) return;
  try {
    const r = await rpc("AGGREGATE_SUMMARY");
    const a = r.bundle || r.aggregate || {};
    if (!a.note) return;
    note.textContent = a.note;
  } catch {
    /* daemon down; the banner already says so */
  }
}

async function refreshStats() {
  try {
    const st = await rpc("GET_STATS");
    setDaemonUi(Boolean(st.connected));
    const s = st.stats;
    if (!s) return;
    el.total.textContent = String(s.total ?? 0);
    el.watch.textContent = String(s.by_surface?.WATCH_NEXT ?? 0);
    el.home.textContent = String(s.by_surface?.HOME ?? 0);
    if (typeof s.channel_unknown === "number") {
      const unk = s.channel_unknown ?? 0;
      const known = s.channel_known ?? 0;
      const tot = unk + known;
      el.channelNote.textContent =
        `Channel known: ${known} · unknown: ${unk}` +
        (tot ? ` (${Math.round((unk / tot) * 100)}% unknown).` : ".") +
        " Channel is only recorded for first-paint cards.";
    }
  } catch (err) {
    setDaemonUi(false, err.message);
  }
}

el.export.addEventListener("click", async () => {
  el.dataStatus.textContent = "Exporting…";
  try {
    const r = await rpc("EXPORT");
    const p = r.export || {};
    el.dataStatus.textContent = `Exported ${p.rows} row(s) to ${p.path}`;
  } catch (err) {
    el.dataStatus.textContent = `Export failed: ${err.message}`;
  }
});

el.wipe.addEventListener("click", async () => {
  const total = Number(el.total.textContent) || 0;
  el.wipeConfirmText.textContent =
    `This permanently deletes all ${total} stored observation(s) on this device. ` +
    "It cannot be undone. It does not change anything YouTube knows about you.";
  el.wipeConfirm.hidden = false;
});

el.wipeNo.addEventListener("click", () => {
  el.wipeConfirm.hidden = true;
});

el.wipeYes.addEventListener("click", async () => {
  el.wipeConfirm.hidden = true;
  el.dataStatus.textContent = "Wiping…";
  try {
    const r = await rpc("WIPE");
    el.dataStatus.textContent = `Deleted ${r.wipe?.deleted ?? 0} row(s).`;
    await refreshStats();
  } catch (err) {
    el.dataStatus.textContent = `Wipe failed: ${err.message}`;
  }
});

browser.runtime.onMessage.addListener((msg) => {
  if (msg?.type === "DAEMON_STATUS") {
    setDaemonUi(Boolean(msg.payload?.connected), msg.payload?.detail || "");
  }
});

selectTab("search");
refreshStats().catch(() => {});

// The full page shows everything the side panel does, with more room, so a
// panel open beside it is redundant. Ask for it to be hidden on this tab only.
browser.runtime.sendMessage({ type: "PANEL_NOT_HERE" }).catch(() => {});
