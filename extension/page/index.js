// SPDX-License-Identifier: Apache-2.0
/**
 * Full-page surface (WO-022): search and data management.
 *
 * The SidePanel is the at-a-glance view while a video plays; this page is where
 * there is room to actually look at the corpus. Everything goes through the SW
 * to the daemon — this page holds no observation data of its own.
 */
import { browser } from "../lib/browser.js";
import { PEER_SEARCH_REV_RECIPROCAL } from "../lib/protocol.js";
import { escapeHtml, fmtDuration } from "../lib/render.js";
import {
  analysisTable,
  fmtAgo,
  fmtCount,
  liveFor,
  liveUrl,
  platformLabel,
  thumbHtml,
  tile,
} from "./render.js";
import { errText } from "../lib/errors.js";

const el = {
  banner: document.getElementById("daemon-banner"),
  form: document.getElementById("search-form"),
  q: document.getElementById("q"),
  meta: document.getElementById("search-meta"),
  results: document.getElementById("results"),
  searchNetwork: document.getElementById("search-network"),
  searchNetworkNote: document.getElementById("search-network-note"),
  searchNetworkReason: document.getElementById("search-network-reason"),
  searchNetworkRoute: document.getElementById("search-network-route"),
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
  liveNote: document.getElementById("live-note"),
  liveReason: document.getElementById("live-reason"),
  liveRoute: document.getElementById("live-route"),
  liveMeta: document.getElementById("live-meta"),
  liveList: document.getElementById("live-list"),
  contribImpactNote: document.getElementById("contrib-impact-note"),
  contribImpactReason: document.getElementById("contrib-impact-reason"),
  contribImpact: document.getElementById("contrib-impact"),
  contribImpactActions: document.getElementById("contrib-impact-actions"),
  contribImpactReset: document.getElementById("contrib-impact-reset"),
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
      console.warn("[Keel] open panel", errText(err))
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

// WO-085 requires a direct route, not just an explanation: the setting that
// would enable this control lives in another tab, and telling someone to go
// find it is how a disabled control becomes a dead end.
el.liveRoute?.addEventListener("click", async () => {
  selectTab("config");
  await refreshContribution().catch(() => {});
  const levels = document.getElementById("contrib-levels");
  levels?.scrollIntoView?.({ block: "center" });
  levels?.querySelector('input[name="contrib"][value="2"]')?.focus?.();
});

el.searchNetworkRoute?.addEventListener("click", async () => {
  selectTab("config");
  // Awaited rather than fired alongside selectTab's own call: refreshContribution
  // replaces the radios, so anything focused before it finishes is thrown away.
  await refreshContribution().catch(() => {});
  const levels = document.getElementById("contrib-levels");
  levels?.scrollIntoView?.({ block: "center" });
  levels?.querySelector('input[name="contrib"][value="2"]')?.focus?.();
});

// WO-086: local-only counters, so resetting them is a plain click — nothing
// destructive of the observation corpus is at stake, unlike Wipe below.
el.contribImpactReset?.addEventListener("click", async () => {
  try {
    await rpc("RESET_CONTRIBUTION_IMPACT");
    await refreshContributionImpact();
  } catch {
    /* daemon down; the banner already says so */
  }
});

/* ---------- live ---------- */

export function renderLive(res) {
  el.liveList.replaceChildren();
  const table = document.getElementById("live-table");
  const streams = res?.streams || [];
  if (table) table.hidden = true;

  if (res && res.available === false) {
    // Two different unavailabilities (WO-089). "Live starts at Broad sharing"
    // is a setting the reader can change; "not connected" is a machine state
    // they cannot, and offering a route to the contribution page for the second
    // one would send them somewhere that cannot help.
    const gated = res.code === "contribution_required";
    el.liveMeta.textContent = gated ? "" : res.reason || "Not connected to the network yet.";
    if (el.liveNote && el.liveReason) {
      el.liveReason.textContent = gated ? res.reason || "" : "";
      el.liveNote.hidden = !gated;
    }
    if (el.liveRoute) el.liveRoute.hidden = !gated;
    if (el.liveQ) el.liveQ.disabled = gated;
    return;
  }
  // Back to available: clear the gate copy rather than leaving it under a
  // working table.
  if (el.liveNote) el.liveNote.hidden = true;
  if (el.liveRoute) el.liveRoute.hidden = true;
  if (el.liveQ) el.liveQ.disabled = false;
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
async function loadLive() {
  try {
    const r = await rpc("LIVE_SEARCH", {
      query: el.liveQ.value.trim(),
      limit: 200,
    });
    renderLive(r.live);
  } catch (err) {
    el.liveList.replaceChildren();
    el.liveMeta.textContent = errText(err);
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

  // One segment per distinct three-gram token. The daemon can report the same
  // token_index more than once as a walk progresses, and a repeated token would
  // be drawn as two bars — which reads as more of the query than there is.
  const seen = new Set();
  progress = progress.filter((p) => {
    const k = Number(p.token_index) || 0;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });

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

/**
 * The query word, with each three-gram's first character in that three-gram's
 * colour.
 *
 * No tokenizer here: the daemon sends one entry per three-gram in word order,
 * so the nth character starts the nth three-gram and the two line up by index.
 * Counting characters is the whole algorithm.
 *
 * Three-grams overlap, so a character belongs to up to three of them; giving
 * each three-gram the character it starts at gives every one exactly one, left
 * to right.
 *
 * @param {string} word
 * @param {Array<{token_index?: number}>} tokens in word order
 * @returns {DocumentFragment}
 */
function colorizedWord(word, tokens) {
  const frag = document.createDocumentFragment();
  const text = String(word ?? "");
  [...text].forEach((ch, i) => {
    const t = tokens[i];
    if (!t) {
      frag.appendChild(document.createTextNode(ch));
      return;
    }
    const span = document.createElement("span");
    span.textContent = ch;
    span.className = "tok-char";
    span.style.color =
      PEER_PROGRESS_COLORS[Math.abs(Number(t.token_index) || 0) % PEER_PROGRESS_COLORS.length];
    frag.appendChild(span);
  });
  return frag;
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
    // The word is written a character at a time, each tinted with the colour of
    // the three-gram that starts there — the same colour as that three-gram's
    // bar below, because the daemon sends them in word order and the two line
    // up by index.
    label.appendChild(colorizedWord(w.word, w.tokens || []));
    label.appendChild(document.createTextNode(` — ${pctText}`));
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
    // One bar per three-gram, in word order — the daemon sends them that way,
    // so the nth bar is the nth three-gram and matches the nth coloured letter
    // above it. No sorting or de-duplicating here: a word with a repeated
    // three-gram really does have two of them.
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
    if (r.peer_search?.contribution_required) {
      // The daemon is the authority, and it just told us the checkbox was
      // wrong — a level change this page missed, or a modified/stale client.
      // Correct the control from the answer rather than leave it inviting a
      // request that will be refused again (WO-085).
      const d = r.peer_search.contribution_required;
      searchEntitlement = {
        known: true,
        allowed: false,
        level: Number(d.effective_level) || 1,
        minLevel: Number(d.required_level) || 2,
      };
      applyCapabilityUi();
    } else if (r.peer_search?.available) {
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
      "Your recordings and recommendation trail stay here — Keel never " +
      "serves them or any cached graph/catalogue/search block to another " +
      "peer, and nothing derived from what you were shown leaves this " +
      "computer at all. You still get the personal product: local search, " +
      "suggestions and graph pre-walk. Keel downloads the starter dataset, " +
      "broad groups of shared recommendation data and the global " +
      "word-popularity statistic, and uses them here. Asking for those does " +
      "tell the peer answering your address, the time and a coarse slice of " +
      "the catalogue — never the video you wanted. Two things are held back " +
      "for the level that supplies them: searching other people\u2019s " +
      "recommendations, and the shared Live feed.",
  },
  {
    n: 2,
    name: "Broad sharing",
    body:
      "Unlocks the shared Live feed and searching other people\u2019s " +
      "recommendations, because from here your machine also supplies both: it " +
      "publishes the livestreams it sees, contributes its word-popularity " +
      "aggregate, and answers other people\u2019s searches. A livestream " +
      "notice carries no sender field, but a peer you are connected to can " +
      "still infer that it started with you from timing alone. " +
      "Your computer starts answering other peers, with two things at once: " +
      "data other people published that it is passing on, and aggregated " +
      "recommendation blocks built from what you were shown. So something " +
      "derived from your own recording does leave — counts of which video " +
      "appeared alongside which, by rough position and day. No timestamps, " +
      "titles, searches, page visits or watch order. It is sent as whole " +
      "buckets of thousands of videos, never one video on request. Each " +
      "neighbourhood has an opaque claim key that is unlinkable to your other " +
      "neighbourhoods; updates preserve that key so they replace rather than " +
      "multiply the claim. Whoever you answer still sees your address, timing " +
      "and the whole bucket you returned. Set the disk limit below.",
  },
  {
    n: 3,
    name: "Cohort aggregator",
    body:
      "Would add a different kind of report — measurements under threshold " +
      "encryption, sealed unless enough other people report the same thing, " +
      "so anything only you saw can never be read. What it would earn is the " +
      "comparison: how your feed differs from other people's, which needs a " +
      "protected group to compare against and cannot exist without one. Not " +
      "the level where sharing begins; broad sharing above already " +
      "contributes blocks. Not built.",
  },
  {
    n: 4,
    name: "Transparency contributor",
    body:
      "Would publish your full recommendation trails, attributed to you. " +
      "YouTube already knows what it showed you — what changes is that " +
      "everyone else would too, and that YouTube would know you are the one " +
      "publishing it. It unlocks nothing extra, deliberately — being public is " +
      "the whole point of choosing it, and a perk here would attract people " +
      "who had not thought about permanence. Researchers running similar " +
      "projects on other platforms have been retaliated against. Cannot be " +
      "withdrawn once copied. Not built.",
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
    // No livestream exception here (WO-090): the shared Live feed starts at
    // Broad sharing, so at the default setting there is nothing this row can
    // point to as the thing a peer connection would have been carrying.
    row.textContent =
      "No peer connection at the moment. That is normal at the default " +
      "setting, and expected if this machine has no route out.";
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
  let daemonPending = false;
  try {
    const r = await rpc("GET_NETWORK_CONSENT");
    daemonPending = r.daemon?.consent_required === true;
  } catch {
    // Daemon unreachable: the local recording decision is still the thing
    // this row can honestly report.
  }
  const on = v === "granted";
  if (daemonPending) {
    row.textContent =
      "Keel is waiting for you to accept what it records and downloads. " +
      "The desktop app will not open a network connection until that " +
      "answer is stored there — an older go-ahead in this browser is not enough.";
    if (actions) actions.hidden = false;
    return;
  }
  row.textContent = on
    ? "Keel is recording the recommendations YouTube shows you, to this device only. To stop it, remove the extension or the desktop app — Wipe, below, erases what it already holds."
    : "Keel is not recording. It asks first, and has not been given the go-ahead on this browser.";
  if (actions) actions.hidden = on;
}

async function refreshContribution() {
  const wrap = document.getElementById("contrib-levels");
  const note = document.getElementById("contrib-note");
  if (!wrap) return;
  if (!hasCap("contribution_runtime")) {
    applyCapabilityUi();
    return;
  }
  let level = 1;
  let maxImpl = 1;
  let stored = null;
  let transition = "idle";
  let detail = "";
  try {
    const r = await rpc("GET_CONTRIBUTION");
    // The checked radio follows the EFFECTIVE level — the policy the daemon is
    // actually enforcing — not the stored choice (WO-077). Showing the stored
    // one while a different policy runs is precisely the misreport this is
    // about: the old build let the control read "Strictly Personal" while the
    // node went on serving until the next restart.
    level = r.daemon?.effective_level ?? r.daemon?.level ?? 1;
    stored = r.daemon?.stored_level ?? null;
    transition = r.daemon?.transition ?? "idle";
    detail = r.daemon?.detail ?? "";
    maxImpl = r.daemon?.max_implemented ?? 1;
    // The search control follows the same effective level these radios do
    // (WO-085), so it is refreshed from the same answer rather than from a
    // second RPC that could disagree with this one.
    setSearchEntitlement(r.daemon);
    applyLiveEntitlement(r.daemon);
    applyContributionImpactEntitlement(r.daemon);
  } catch {
    return;
  }
  const disagree = stored != null && stored !== level;
  note.textContent =
    maxImpl < 2
      ? "Keel does not yet serve or publish anything for other people. " +
        "Higher contribution levels are not available yet."
      : "Choose how much this node contributes.";
  if (transition === "starting" || transition === "stopping") {
    note.textContent = "Applying your choice to the network…";
  } else if (disagree) {
    // Never silently show the stored value as though it were in force.
    note.textContent =
      `Level ${level} is what this node is currently enforcing` +
      (stored != null ? `, not the level ${stored} on record` : "") +
      (detail ? ` — ${detail}` : ".");
  }
  renderContributionRows(wrap, { level, maxImpl, interactive: true });
}

/**
 * Render the four Level 1–4 rows.
 *
 * Shared by refreshContribution (capability present, real daemon state) and
 * applyCapabilityUi (capability absent). WO-088: a control whose bridge
 * capability is unavailable stays visible and disabled with a reason — it
 * must never disappear, and it must never be built from guessed state.
 * `interactive: false` renders every row disabled, nothing checked, and
 * attaches no change listener, so an incompatible daemon can neither have a
 * level invented for it nor receive a SET_CONTRIBUTION it never negotiated.
 */
function renderContributionRows(wrap, { level, maxImpl, interactive }) {
  wrap.replaceChildren();
  for (const l of LEVELS) {
    const avail = interactive && l.n <= maxImpl;
    const row = document.createElement("label");
    row.className = "contrib" + (avail ? "" : " contrib-off");
    row.innerHTML =
      `<input type="radio" name="contrib" value="${l.n}">` +
      `<span><strong>${escapeHtml(l.name)}</strong>` +
      `<span class="meta">${escapeHtml(l.body)}` +
      (avail
        ? ""
        : interactive
          ? " <em>Not available yet.</em>"
          : " <em>Unavailable until the desktop app is updated.</em>") +
      `</span></span>`;
    // Set as properties, not HTML-string attributes: `checked`/`disabled` as
    // markup only sets the initial reflected state, which is easy to get
    // wrong across environments. The property assignment is unambiguous.
    const input = row.querySelector("input");
    input.disabled = !avail;
    input.checked = interactive && l.n === level;
    wrap.appendChild(row);
  }
  if (!interactive) return;
  wrap.querySelectorAll('input[name="contrib"]').forEach((el) => {
    el.addEventListener("change", async () => {
      try {
        await rpc("SET_CONTRIBUTION", { level: Number(el.value) });
      } catch (err) {
        console.warn("[Keel] contribution", errText(err));
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
      console.warn("[Keel] disk budget", errText(err));
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

/** @type {Record<string, number>} */
let bridgeCaps = Object.create(null);

function hasCap(name, minRev = 1) {
  const n = bridgeCaps[name];
  return Number.isFinite(n) && n >= minRev;
}

function setDaemonUi(ok, detail = "", meta = {}) {
  if (ok && meta?.capabilities && typeof meta.capabilities === "object") {
    bridgeCaps = { ...meta.capabilities };
  } else if (!ok) {
    bridgeCaps = Object.create(null);
  }
  applyCapabilityUi();
  el.banner.className = ok ? "banner ok" : "banner warn";
  if (ok) {
    el.banner.textContent =
      "Desktop app connected. Your recordings stay on this device.";
    return;
  }
  if (meta?.incompatible || /desktop app update required/i.test(String(detail || ""))) {
    el.banner.textContent =
      "Desktop app update required. Update the Keel desktop app to match this extension." +
      (detail ? ` (${detail})` : "");
    return;
  }
  el.banner.textContent =
    "Keel's desktop app isn't running." + (detail ? ` (${detail})` : "");
}

/**
 * Reciprocal distributed search (WO-085).
 *
 * `allowed` is what the daemon says its effective policy permits. `known` is
 * whether we have been told yet — until GET_CONTRIBUTION or a
 * CONTRIBUTION_STATUS broadcast arrives, the control is left as the negotiated
 * capability alone decides it, because guessing "off" would flash a
 * "you have not opted in" explanation at Level-2 users on every page load.
 *
 * @type {{ known: boolean, allowed: boolean, level: number, minLevel: number }}
 */
let searchEntitlement = { known: false, allowed: false, level: 1, minLevel: 2 };

/**
 * Ask the daemon what the search control should look like right now.
 *
 * The search view is the first thing this page shows and most users never open
 * the config tab, so the entitlement cannot wait for refreshContribution to be
 * called from there. Seeding the negotiated capability map first matters for
 * the same reason: without it a freshly loaded page knows nothing about the
 * peer_search revision and would render the control as though the daemon were
 * pre-WO-085.
 */
async function syncSearchControl() {
  try {
    const st = await rpc("GET_STATUS");
    if (st?.capabilities && typeof st.capabilities === "object") {
      bridgeCaps = { ...st.capabilities };
      applyCapabilityUi();
    }
  } catch {
    return; // the banner already says the daemon is unreachable
  }
  await refreshSearchEntitlement();
}

/** Refresh only the level-derived half, after a status broadcast. */
async function refreshSearchEntitlement() {
  if (!hasCap("contribution_runtime")) return;
  try {
    const r = await rpc("GET_CONTRIBUTION");
    setSearchEntitlement(r.daemon);
  } catch {
    /* the banner already says the daemon is unreachable */
  }
}

/**
 * Mirror the search-control pattern for Live (WO-089): a contribution
 * broadcast has to be able to disable the feed without waiting for the next
 * LIVE_SEARCH, or an already-open Live tab would keep looking available.
 */
function applyLiveEntitlement(d) {
  if (!d || typeof d.live !== "boolean") return;
  if (d.live === false) {
    renderLive({
      available: false,
      code: "contribution_required",
      required_level: Number(d.live_min_level) || 2,
      reason:
        "Live starts at Broad sharing: the shared feed is built " +
        "from livestream sightings people publish, so it is available " +
        "to the levels that publish them.",
      streams: [],
    });
    return;
  }
  loadLive().catch(() => {});
}

/**
 * Mirror the search-control / Live pattern once more (WO-086): a
 * contribution broadcast has to be able to gate the impact panel without
 * waiting for the config tab to be reselected.
 */
function applyContributionImpactEntitlement(d) {
  if (!d || typeof d.effective_level !== "number") return;
  if (d.effective_level < 2) {
    renderContributionImpact(null, {
      reason:
        "Your impact starts at Broad sharing: this panel shows evidence " +
        "that your copy is doing useful serving work, which only exists " +
        "once your node answers requests for other people.",
    });
    return;
  }
  refreshContributionImpact().catch(() => {});
}

/** Fetch and render the WO-086 contribution-impact panel. */
async function refreshContributionImpact() {
  if (!hasCap("contribution_impact")) {
    renderContributionImpact(null, {
      reason: "Your impact requires a desktop app that negotiates contribution_impact.",
    });
    return;
  }
  try {
    const r = await rpc("GET_CONTRIBUTION_IMPACT");
    if (r.daemon?.available === false) {
      renderContributionImpact(null, {
        reason: "The peer network isn't connected right now.",
      });
      return;
    }
    renderContributionImpact(r.daemon, null);
  } catch {
    /* daemon down; the banner already says so */
  }
}

/**
 * Render the WO-086 panel. `d` is a populated ContributionImpactPayload, or
 * null while gated — the two are mutually exclusive so a caller can never
 * show stale numbers under a gate note left over from a prior render.
 */
function renderContributionImpact(d, gate) {
  const root = el.contribImpact;
  if (!root) return;
  if (el.contribImpactNote && el.contribImpactReason) {
    el.contribImpactReason.textContent = gate?.reason || "";
    el.contribImpactNote.hidden = !gate;
  }
  if (el.contribImpactActions) el.contribImpactActions.hidden = !d;
  if (!d) {
    root.replaceChildren();
    root.className = "";
    return;
  }
  const claims = (d.graph_claims_local ?? 0) + (d.graph_claims_peer_cached ?? 0);
  const catalogue = (d.catalogue_local ?? 0) + (d.catalogue_peer_cached ?? 0);
  const bytesServed = d.bytes_served ?? 0;
  const rows = [
    [String(d.requests_answered ?? 0), "broad requests answered"],
    [
      bytesServed >= MB
        ? `${(bytesServed / MB).toFixed(1)} MB`
        : `${(bytesServed / 1024).toFixed(1)} KB`,
      "sent to other people",
    ],
    [String(claims), "recommendation claims eligible to serve"],
    [String(catalogue), "catalogue entries eligible to serve"],
    [String((d.buckets_announced ?? 0) + (d.shards_announced ?? 0)), "buckets/shards announced"],
    [String(d.keel_peers ?? 0), "Keel peers connected right now"],
  ];
  root.className = "stats";
  root.innerHTML = rows
    .map(
      ([n, label]) =>
        `<div><strong>${escapeHtml(n)}</strong><span>${escapeHtml(label)}</span></div>`
    )
    .join("");
}

/** Record what the daemon reported, from either the RPC or the broadcast. */
function setSearchEntitlement(d) {
  if (!d || typeof d !== "object") return;
  if (typeof d.distributed_search !== "boolean") return;
  searchEntitlement = {
    known: true,
    allowed: d.distributed_search,
    level: Number(d.effective_level ?? d.level ?? 1) || 1,
    minLevel: Number(d.distributed_search_min_level) || 2,
  };
  applyCapabilityUi();
}

function applyCapabilityUi() {
  const net = el.searchNetwork;
  if (net) {
    // Two separate reasons the control can be unavailable, and they need two
    // different sentences: the desktop app cannot do this (update it), or it
    // can and this node has not opted in (change a setting). Collapsing them
    // into one greyed-out box would leave the second group with no route.
    const negotiated = Number(bridgeCaps["peer_search"]) || 0;
    const reciprocal = negotiated >= PEER_SEARCH_REV_RECIPROCAL;
    // An older daemon has no level rule, so presenting one would be a
    // UI-only restriction of a daemon that would have answered.
    const gated = reciprocal && searchEntitlement.known && !searchEntitlement.allowed;
    const on = negotiated >= 1 && !gated;

    net.disabled = !on;
    if (!on) net.checked = false;
    // Disabled with a reason, never hidden. A control that vanishes reads as
    // "this feature was removed"; one that is greyed out with a tooltip reads
    // as "your desktop app is behind", which is the actionable message and the
    // whole point of negotiating rather than failing on an unknown RPC.
    const row = net.closest("label");
    let reason = "";
    if (negotiated < 1) {
      reason = "Peer search requires a desktop app that negotiates peer_search.";
    } else if (gated) {
      reason =
        `Searching other people's recommendations needs Broad sharing ` +
        `(level ${searchEntitlement.minLevel}). Those searches run on the ` +
        `machines that also answer them, so the level that serves is the ` +
        `level that can ask. Local search, suggestions, graph pre-walk and ` +
        `the downloaded global word statistics all work at level ` +
        `${searchEntitlement.level}. The shared Live feed and distributed ` +
        `peer search both start at Broad sharing.`;
    }
    if (row) row.title = reason;
    if (el.searchNetworkNote && el.searchNetworkReason) {
      el.searchNetworkReason.textContent = reason;
      el.searchNetworkNote.hidden = !reason;
    }
    if (el.searchNetworkRoute) el.searchNetworkRoute.hidden = !gated;
  }
  const contrib = document.getElementById("contrib-levels");
  const contribNote = document.getElementById("contrib-note");
  const contribHead = contrib?.previousElementSibling;
  if (contrib && !hasCap("contribution_runtime")) {
    // Same rule as the search control above: disabled and visible, never
    // hidden, and never showing a guessed level for a daemon that did not
    // negotiate the state schema (WO-088).
    renderContributionRows(contrib, { level: null, maxImpl: 0, interactive: false });
    if (contribNote) {
      contribNote.textContent =
        "Current level unavailable until the desktop app is updated to " +
        "support contribution_runtime. The levels below are shown for " +
        "reference and cannot be changed here yet.";
    }
    if (contribHead && contribHead.tagName === "H2") contribHead.hidden = false;
    // Losing contribution_runtime entirely means the daemon cannot even
    // report which level is running, so the impact panel — which depends on
    // that level — cascades to the same disabled state rather than being
    // left showing stale or invented numbers.
    renderContributionImpact(null, {
      reason: "Your impact requires a desktop app that negotiates contribution_runtime.",
    });
  }
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
    setDaemonUi(Boolean(st.connected), "", { capabilities: bridgeCaps });
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
    setDaemonUi(
      Boolean(msg.payload?.connected),
      msg.payload?.detail || "",
      msg.payload || {}
    );
    // A reconnect can be to a different daemon build, or to the same one after
    // a level change this page never saw. Re-ask rather than keep the old
    // answer (WO-085).
    if (msg.payload?.connected) refreshSearchEntitlement().catch(() => {});
  }
  if (msg?.type === "CONTRIBUTION_STATUS") {
    // Applied straight from the broadcast, not only after the refresh's
    // round trip (WO-085/WO-079): a level change made in another browser
    // profile has to reach an already-open search view, and the payload
    // carries everything the control needs.
    setSearchEntitlement(msg.payload);
    applyLiveEntitlement(msg.payload);
    applyContributionImpactEntitlement(msg.payload);
    refreshContribution().catch(() => {});
    refreshConsent().catch(() => {});
  }
});

selectTab("search");
refreshStats().catch(() => {});
syncSearchControl().catch(() => {});

// The full page shows everything the side panel does, with more room, so a
// panel open beside it is redundant. Ask for it to be hidden on this tab only.
browser.runtime.sendMessage({ type: "PANEL_NOT_HERE" }).catch(() => {});
