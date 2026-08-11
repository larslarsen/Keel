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
  searchNetwork: document.getElementById("search-network"),
  peerProgress: document.getElementById("peer-progress"),
  peerProgressCaption: document.getElementById("peer-progress-caption"),
  wordCorpus: document.getElementById("word-corpus"),
  wordCorpusMeta: document.getElementById("word-corpus-meta"),
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
    startLiveRefresh();
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

export function renderLive(res) {
  el.liveList.replaceChildren();
  const table = document.getElementById("live-table");
  const streams = res?.streams || [];
  if (table) table.hidden = true;

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
  const at = new Date().toLocaleTimeString();
  el.liveMeta.textContent =
    `${streams.length} stream${streams.length === 1 ? "" : "s"}` +
    (res.indexed ? ` of ${res.indexed} known` : "") +
    ` · updated ${at}`;
  if (table) table.hidden = false;

  // One table across platforms rather than a tab per platform.
  //
  // The side panel is scoped to the site you are on, because a suggestion has
  // to belong to somewhere. This page is different: it is opened on its own,
  // with no site behind it, and "what is live right now" is a question that
  // spans platforms. A column keeps them distinguishable without a click.
  for (const s of streams) {
    const tr = document.createElement("tr");
    const platform = s.p || "yt";
    tr.innerHTML =
      `<td class="live-thumb">${thumbHtml(s.v)}</td>` +
      `<td class="live-where">${escapeHtml(platformLabel(platform))}</td>` +
      `<td><a href="${escapeHtml(liveUrl(s.v, platform))}" target="_blank" rel="noreferrer">` +
      `${escapeHtml(s.t || s.v)}</a>` +
      (s.c ? `<span class="r-sub"> · ${escapeHtml(s.c)}</span>` : "") +
      `</td>` +
      `<td>${escapeHtml(liveFor(s))}</td>` +
      `<td>${escapeHtml(fmtAgo(s.s ?? s.last_seen))}</td>`;
    el.liveList.appendChild(tr);
    const img = tr.querySelector("img.thumb[data-vid]");
    fillThumb(img, s.v);
  }
}

/** Human name for a platform code. */
function platformLabel(p) {
  return p === "tt" ? "TikTok" : "YouTube";
}

/** Where a live stream lives, per platform. */
function liveUrl(id, platform) {
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
 */
function liveFor(s) {
  // Duration the stream has been live: from when it started (StartedAt if the
  // peer reported it, else our first sighting) to NOW. Measuring to last_seen
  // instead collapses to ~0 for a stream we saw only once (first_seen ==
  // last_seen), which reads as "just started" next to a "seen 3h ago" column.
  const began = s.b || s.first_seen || Date.now();
  const mins = Math.max(0, Math.round((Date.now() - began) / 60000));
  if (mins >= 60) {
    const hrs = Math.floor(mins / 60);
    return `${hrs}+ hour${hrs === 1 ? "" : "s"}`;
  }
  if (mins >= 5) return `${mins}+ min`;
  return "just started";
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

/**
 * Keep the live feed live.
 *
 * It used to load when the tab was opened and never again, so a page left open
 * showed streams that had ended hours earlier — indistinguishable from the
 * daemon reporting stale data, and the cause of several bug reports that were
 * really a page nobody had refreshed. A feed whose entire subject is "right
 * now" cannot be a snapshot.
 *
 * Only while the tab is on screen: polling a hidden tab spends the daemon's
 * time to update something nobody is looking at.
 */
const LIVE_REFRESH_MS = 30_000;
let liveRefreshTimer = null;

function liveTabVisible() {
  const view = document.getElementById("view-live");
  return Boolean(view && !view.hidden && document.visibilityState === "visible");
}

function startLiveRefresh() {
  if (liveRefreshTimer) return;
  liveRefreshTimer = setInterval(() => {
    if (liveTabVisible()) loadLive().catch(() => {});
  }, LIVE_REFRESH_MS);
}

document.addEventListener("visibilitychange", () => {
  // Coming back to the tab should show current data, not what was true when it
  // was hidden.
  if (liveTabVisible()) loadLive().catch(() => {});
});

let liveTimer = null;
el.liveQ.addEventListener("input", () => {
  // Debounced only to avoid re-rendering on every keystroke; there is no
  // network call to rate-limit.
  clearTimeout(liveTimer);
  liveTimer = setTimeout(() => loadLive().catch(() => {}), 120);
});

/* ---------- search ---------- */

// hitRow builds one result <li>. provenance is the trailing "how do we know
// this" text — local search and peer search mean different things by it, so
// the caller decides rather than this function guessing from the hit shape.
function hitRow(h, provenance) {
  const li = document.createElement("li");
  const dur = fmtDuration(h.duration_s);
  const views = typeof h.view_count === "number" ? `${fmtCount(h.view_count)} views` : "";
  const bits = [views, h.published_at || "", dur].filter(Boolean).join(" · ");
  li.innerHTML =
    `<div class="row-main">${thumbHtml(h.video_id)}<div class="row-text">` +
    `<p class="r-title"><a href="https://www.youtube.com/watch?v=${encodeURIComponent(h.video_id)}"` +
    ` target="_blank" rel="noreferrer">${escapeHtml(h.title || h.video_id)}</a></p>` +
    `<p class="r-sub">${escapeHtml(bits)}` +
    (bits ? " · " : "") +
    provenance +
    (h.channel_id ? ` · ${escapeHtml(h.channel_id)}` : "") +
    `</p></div></div>`;
  const im = li.querySelector("img.thumb[data-vid]");
  if (im) fillThumb(im, decodeURIComponent(im.dataset.vid || ""));
  return li;
}

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
    // seen = how many times Keel observed this being recommended. That is
    // the corpus's own signal and the reason local results are ordered this
    // way.
    const provenance = h.seen > 0 ? `seen ${h.seen}×` : `from a shared bundle`;
    el.results.appendChild(hitRow(h, provenance));
  }
}

// appendPeerHits adds network-found rows after whatever local search already
// rendered, tagged distinctly. A peer-search hit is not a claim this device
// watched or even has seen the video — it may not even have a title (WO-059:
// the daemon does not fetch one just to label a search result, since that
// would bind a catalogue fetch to this exact query).
function appendPeerHits(hits) {
  for (const h of hits || []) {
    el.results.appendChild(hitRow(h, "found on the network"));
  }
}

// Validated dark-mode categorical palette (dataviz skill), in its fixed
// CVD-safe adjacency order. Cycled by dictionary index for queries with
// more distinct tokens than colors — an accepted departure from "never
// cycle" here, because these segments carry no user-facing identity to
// protect long-term the way a tracked chart series would (WO-067).
const PEER_PROGRESS_COLORS = [
  "#3987e5", "#d95926", "#199e70", "#c98500",
  "#d55181", "#008300", "#9085e9", "#e66767",
];

// renderPeerProgress draws one segment per query token the daemon walked,
// color-coded and shuffled — deliberately not labeled and not in query
// order, so the bar itself cannot be read back into the query's structure
// (WO-067). progress entries carry only an opaque token_index, never the
// token text; the daemon does not send that either.
function renderPeerProgress(progress) {
  if (!el.peerProgress) return;
  el.peerProgress.replaceChildren();
  if (!progress || !progress.length) {
    el.peerProgress.hidden = true;
    el.peerProgress.setAttribute("aria-hidden", "true");
    if (el.peerProgressCaption) el.peerProgressCaption.hidden = true;
    return;
  }

  const order = progress.map((_, i) => i);
  for (let i = order.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [order[i], order[j]] = [order[j], order[i]];
  }

  const fills = [];
  for (const i of order) {
    const p = progress[i];
    const color = PEER_PROGRESS_COLORS[Math.abs(p.token_index || 0) % PEER_PROGRESS_COLORS.length];
    const seg = document.createElement("div");
    seg.style.setProperty("--seg-color", color);
    if (p.known) {
      const pct = p.target > 0 ? Math.min(100, Math.round((p.fetched / p.target) * 100)) : 100;
      const fill = document.createElement("div");
      fill.className = "fill";
      fill.style.width = "0%";
      seg.className = "seg";
      seg.appendChild(fill);
      seg.title = `~${pct}% of the network's estimated coverage for this part of the search`;
      fills.push([fill, pct]);
    } else {
      seg.className = "seg unknown";
      seg.title = "No network estimate yet for this part of the search";
    }
    el.peerProgress.appendChild(seg);
  }
  el.peerProgress.hidden = false;
  el.peerProgress.setAttribute("aria-hidden", "false");
  if (el.peerProgressCaption) el.peerProgressCaption.hidden = false;
  // Width starts at 0 so the CSS transition animates to the final fraction
  // on the next frame rather than painting already-full.
  requestAnimationFrame(() => {
    for (const [fill, pct] of fills) fill.style.width = pct + "%";
  });
}

// renderWordCorpus draws WO-068's two-tier bars: top = per-word global % of
// observed graphs; bottom = per ShardK char-token coverage from gossiped
// token sketches. Separate from renderPeerProgress (query-scoped fetch
// coverage). Word strings shown on the top tier are the user's own query
// words; token sub-bars stay unlabeled (color only).
function renderWordCorpus(stats) {
  if (!el.wordCorpus) return;
  el.wordCorpus.replaceChildren();
  if (!stats || !stats.words?.length) {
    el.wordCorpus.hidden = true;
    el.wordCorpus.setAttribute("aria-hidden", "true");
    if (el.wordCorpusMeta) el.wordCorpusMeta.hidden = true;
    return;
  }

  const maxTok = Math.max(
    1,
    ...stats.words.flatMap((w) => (w.tokens || []).map((t) => (t.known ? Number(t.estimate) || 0 : 0)))
  );

  for (const w of stats.words) {
    const row = document.createElement("div");
    row.className = "word-row";

    const label = document.createElement("div");
    label.className = "word-label";
    const pctText =
      typeof w.pct === "number" ? `~${w.pct}% of observed graphs` : "no estimate yet";
    label.textContent = `${w.word} — ${pctText}`;
    row.appendChild(label);

    const bar = document.createElement("div");
    bar.className = "word-bar";
    const fill = document.createElement("div");
    fill.className = "fill";
    fill.style.width = "0%";
    bar.appendChild(fill);
    row.appendChild(bar);

    const sub = document.createElement("div");
    sub.className = "token-subbars";
    const fills = [[fill, typeof w.pct === "number" ? Math.min(100, w.pct) : 0]];
    for (const t of w.tokens || []) {
      const color =
        PEER_PROGRESS_COLORS[Math.abs(t.token_index || 0) % PEER_PROGRESS_COLORS.length];
      const seg = document.createElement("div");
      seg.style.setProperty("--seg-color", color);
      if (t.known) {
        const est = Number(t.estimate) || 0;
        const pct = Math.round((est / maxTok) * 100);
        const past = est > maxTok; // defensive; maxTok is max of estimates
        const tf = document.createElement("div");
        tf.className = "fill" + (past ? " past" : "");
        tf.style.width = "0%";
        seg.className = "seg";
        seg.appendChild(tf);
        seg.title = past
          ? `est. ${est} graphs (past peer scale)`
          : `est. ${est} graphs for this part of the word`;
        fills.push([tf, Math.min(100, pct)]);
      } else {
        seg.className = "seg unknown";
        seg.title = "No network estimate yet for this part of the word";
      }
      sub.appendChild(seg);
    }
    row.appendChild(sub);
    el.wordCorpus.appendChild(row);

    requestAnimationFrame(() => {
      for (const [f, pct] of fills) f.style.width = pct + "%";
    });
  }

  el.wordCorpus.hidden = false;
  el.wordCorpus.setAttribute("aria-hidden", "false");
  if (el.wordCorpusMeta) {
    const bits = [];
    if (stats.distinct_words > 0) {
      bits.push(`~${stats.distinct_words.toLocaleString()} distinct words in the swarm's view`);
    }
    if (stats.peers > 0) bits.push(`${stats.peers} peer pack(s)`);
    else if (stats.available === false) bits.push("local catalogue only");
    el.wordCorpusMeta.textContent = bits.join(" · ");
    el.wordCorpusMeta.hidden = !bits.length;
  }
}

el.form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const query = el.q.value.trim();
  if (!query) return;
  el.meta.textContent = "Searching…";
  renderPeerProgress(null);
  renderWordCorpus(null);
  try {
    const r = await rpc("SEARCH", { query, limit: 100 });
    renderHits(r.search);
  } catch (err) {
    el.meta.textContent = `Search failed: ${err.message}`;
    return;
  }
  // Corpus bars are independent of network-search checkbox: telemetry only.
  try {
    const r = await rpc("WORD_STATS", { query });
    if (r.word_stats) renderWordCorpus(r.word_stats);
  } catch {
    // Optional display; local search already succeeded.
  }
  if (!el.searchNetwork?.checked) return;
  try {
    const r = await rpc("PEER_SEARCH", { query, limit: 100 });
    if (r.peer_search?.available) {
      appendPeerHits(r.peer_search.hits);
      renderPeerProgress(r.peer_search.progress);
    }
  } catch {
    // Network search is a bonus on top of local results already shown;
    // failing quietly here is the right default rather than replacing a
    // working local result with an error.
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

/**
 * Show whether the peer network is doing anything.
 *
 * A peer-to-peer feature that silently connects to nobody looks exactly like
 * one that works, so this is the first question anyone will have.
 */
/**
 * Which build is answering.
 *
 * The browser keeps the desktop app alive across extension reloads, so a
 * freshly built binary is not necessarily the one running. Without this, the
 * only way to tell was to notice that a fixed bug was still happening.
 */
function renderDaemon(d) {
  const row = document.getElementById("daemon-build");
  if (!row) return;
  row.textContent = d?.built_at
    ? `Desktop app ${d.version || ""} · built ${d.built_at}`
    : "";
}

function renderSwarm(sw) {
  const row = document.getElementById("swarm-row");
  if (!row) return;
  if (!sw || !sw.up) {
    row.textContent =
      "Not connected to the peer network. That is normal at the default " +
      "setting for everything except livestreams, and expected if this " +
      "machine has no route out.";
    return;
  }
  // Headline the count of other Keel installs, never the raw libp2p figure.
  //
  // Joining the public IPFS DHT connects you to dozens of strangers who are not
  // running this software, and that number churns — 53 one minute, 11 the next.
  // Shown as "peers" it reads as a busy network of people that does not exist.
  const keel = Number(sw.keel_peers) || 0;
  const dht = Number(sw.peers) || 0;
  const live = Number(sw.live_indexed) || 0;

  const parts = [
    keel === 0
      ? "No other Keel users connected."
      : `Connected to ${keel} other Keel user${keel === 1 ? "" : "s"}.`,
  ];
  if (live) {
    // With no Keel peers the index can only hold this machine's own sightings,
    // so saying "known" without qualification would overstate it.
    parts.push(
      keel === 0
        ? `${live} livestream${live === 1 ? "" : "s"} indexed, all from your own browsing.`
        : `${live} livestream${live === 1 ? "" : "s"} known.`,
    );
  }
  // Kept for diagnosis, named for what it is: routing traffic, not people.
  parts.push(`${dht} DHT connection${dht === 1 ? "" : "s"} (network plumbing).`);
  if (sw.id) parts.push(`This node: ${String(sw.id).slice(-8)}`);
  row.textContent = parts.join(" ");
  renderUpdateNotice(sw.versions);
}

/**
 * Tell the user when the network has moved on without them (WO-061).
 *
 * Two different messages, because they need two different responses. A newer
 * version among peers is worth knowing and can wait. Peers on a different key
 * scheme cannot be exchanged with at all — that node is alone on the network
 * however many peers it can see, and without this line the symptom is
 * indistinguishable from nobody else being online (WO-058).
 *
 * Shown here rather than in the side panel on purpose: this is where network
 * state lives, and the panel was decluttered for a reason (WO-041). Nothing is
 * blocked either way — Keel's local recording and suggestions work whatever the
 * rest of the network is running.
 */
function renderUpdateNotice(v) {
  const row = document.getElementById("update-row");
  if (!row) return;
  if (!v || (!v.update_advised && !v.update_required)) {
    row.hidden = true;
    row.textContent = "";
    return;
  }
  const latest = v.latest_seen ? ` (peers are on ${v.latest_seen})` : "";
  row.hidden = false;
  row.className = v.update_required ? "notice warn" : "notice";
  row.textContent = v.update_required
    ? `Most Keel nodes you can see are running an incompatible version${latest}. ` +
      "They compute storage keys differently, so nothing can be exchanged with " +
      "them until this copy is updated. Recording and your own suggestions are " +
      "unaffected."
    : `A newer Keel is out${latest}. Yours still works and still connects — ` +
      "updating gets you whatever the newer version added.";
}

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
    renderSwarm(s.swarm);
    renderDaemon(s.daemon);
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
