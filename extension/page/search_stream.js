// SPDX-License-Identifier: Apache-2.0
/**
 * The search page's streaming distributed-search client (WO-095).
 *
 * # Two bars that mean deliberately different things
 *
 * A **token bar** is a schematic animation of one logical peer response. It
 * resets when work starts against another peer, animates while the response is
 * pending, and snaps to full when that response terminates and validates. It is
 * not a count, not a byte meter, and not a coverage estimate — the daemon does
 * not know how much of a shard it is about to receive, and a bar that pretended
 * to would be inventing the one number this feature exists to be honest about.
 *
 * A **word bar** is completion for this search: how many distinct candidates
 * from this query have been confirmed, locally, to contain that word, against
 * the frozen global estimate. It is allowed to pass 100% — the target is an
 * overlap-adjusted sketch estimate, not a ceiling — and it keeps the marker at
 * 100 when it does, because "we found more than expected" and "we found exactly
 * what was expected" are different facts.
 *
 * # Nothing here tokenizes, colors, or counts
 *
 * The daemon sends a render plan: normalized words with opaque ids, token
 * occurrences with character ranges and colour slots, and the word fragments
 * each token covers. This page slices strings it was given and paints the
 * colours it was told. It used to chop the query into three-character blocks
 * itself and hash them for colour, which meant the interface's idea of the
 * query and the daemon's could disagree — and after WO-097 they would have,
 * because a real token can straddle a space and belong to two words at once.
 *
 * # Staleness is guarded three ways
 *
 * A submission mints a fresh `search_id` and bumps a page generation. An event
 * is applied only when its search id is current, its generation is current, and
 * its sequence number is ahead of the last one applied. Any one of those alone
 * would leave a hole: ids repeat across nothing, generations do not survive a
 * replaced job's in-flight events, and sequence alone cannot tell two searches
 * apart.
 */

import { errText } from "../lib/errors.js";
import { PEER_SEARCH_REV_STREAMING } from "../lib/protocol.js";

/** Port name; must match background/search_sessions.js. */
const SEARCH_PORT = "keel-search";

/**
 * Colour slots. A token's `color_slot` indexes this directly — the daemon
 * assigns one slot per distinct token value, so a repeated token keeps its
 * colour for free and the page never hashes anything.
 */
export const TOKEN_COLORS = [
  "#4f9dde",
  "#e0803c",
  "#5fb37a",
  "#c96ec4",
  "#d4b03a",
  "#6f7ae0",
  "#d1615d",
  "#3fa9a3",
];

export function colorForSlot(slot) {
  const n = Number(slot);
  if (!Number.isFinite(n)) return TOKEN_COLORS[0];
  return TOKEN_COLORS[Math.abs(n) % TOKEN_COLORS.length];
}

/**
 * @param {{
 *   browser: object,
 *   rpc: (type: string, payload?: object) => Promise<object>,
 *   el: Record<string, HTMLElement|null>,
 *   hitRow: (hit: object, provenance: string) => HTMLElement,
 *   hasStreaming: () => boolean,
 *   onContributionRequired?: (detail: object) => void,
 *   log?: (...a: unknown[]) => void,
 * }} deps
 */
export function createSearchStream({
  browser,
  rpc,
  el,
  hitRow,
  hasStreaming,
  onContributionRequired = () => {},
  log = () => {},
}) {
  let port = null;
  let generation = 0;
  /** @type {null | object} */
  let active = null;

  /* ---------- Port ---------- */

  function ensurePort() {
    if (port) return port;
    try {
      port = browser.runtime.connect({ name: SEARCH_PORT });
    } catch (err) {
      log("search port connect", errText(err));
      port = null;
      return null;
    }
    port.onMessage.addListener(onEvent);
    port.onDisconnect.addListener(() => {
      port = null;
      // The service worker was evicted or the port broke. Nothing is
      // recovered: job state lives in memory on both sides by design
      // (DESIGN_v2 §2.1), so a search interrupted this way is reported as
      // interrupted rather than silently resumed.
      if (active) {
        setNetState("The background link dropped; local results are unchanged.");
        finishActive();
      }
    });
    return port;
  }

  /* ---------- lifecycle ---------- */

  /**
   * Cancel whatever is running and mark the page generation advanced.
   *
   * Called on replacement, explicit cancel, switching network search off, and
   * page teardown. The daemon is told as well as the page: a job nobody is
   * rendering must stop spending peers' serving budget.
   */
  function cancel() {
    generation++;
    if (!active) return;
    const { searchId } = active;
    active = null;
    try {
      port?.postMessage({ type: "RELEASE_SEARCH", search_id: searchId });
    } catch {
      /* the port is gone; the SW cancels orphans on disconnect */
    }
    rpc("PEER_SEARCH_CANCEL", { search_id: searchId }).catch(() => {});
  }

  function finishActive() {
    if (!active) return;
    try {
      port?.postMessage({ type: "RELEASE_SEARCH", search_id: active.searchId });
    } catch {
      /* nothing to release */
    }
    active = null;
  }

  /* ---------- rendering ---------- */

  function setNetState(text) {
    if (!el.peerProgressCaption) return;
    el.peerProgressCaption.textContent = text;
    el.peerProgressCaption.hidden = !text;
  }

  function clearProgress() {
    if (el.wordCorpus) {
      el.wordCorpus.replaceChildren();
      el.wordCorpus.hidden = true;
      el.wordCorpus.setAttribute("aria-hidden", "true");
    }
    if (el.wordCorpusMeta) el.wordCorpusMeta.hidden = true;
    setNetState("");
  }

  /**
   * Build one row per distinct non-stopword query word.
   *
   * Stopwords get no row: they are still required by the daemon's matcher, and
   * they have no target and no distributed work, so a bar for one could only
   * ever sit at zero and read as a failure (WO-095 §7).
   */
  function renderPlan(plan, targets) {
    if (!el.wordCorpus || !plan) return { words: new Map(), tokens: new Map() };
    el.wordCorpus.replaceChildren();

    const words = new Map();
    const tokens = new Map();
    const targetByWord = new Map();
    for (const t of targets || []) targetByWord.set(t.word_id, t);

    const seenWord = new Set();
    for (const w of plan.words || []) {
      if (w.stopword || seenWord.has(w.word_id)) continue;
      seenWord.add(w.word_id);

      const row = document.createElement("div");
      row.className = "word-row";
      row.dataset.wordId = String(w.word_id);

      const label = document.createElement("div");
      label.className = "word-label";
      label.appendChild(colorizeWord(plan, w));
      const targetNote = document.createElement("span");
      targetNote.className = "word-target";
      label.appendChild(targetNote);
      row.appendChild(label);

      const bar = document.createElement("div");
      bar.className = "word-bar";
      const fill = document.createElement("div");
      fill.className = "fill";
      fill.style.width = "0%";
      bar.appendChild(fill);
      // The 100% marker stays put when the count runs past it, which is what
      // makes "past the estimate" legible instead of just full.
      const marker = document.createElement("div");
      marker.className = "target-marker";
      bar.appendChild(marker);
      row.appendChild(bar);

      const sub = document.createElement("div");
      sub.className = "token-subbars";
      row.appendChild(sub);

      el.wordCorpus.appendChild(row);

      const t = targetByWord.get(w.word_id);
      const entry = {
        word: w.word,
        found: 0,
        target: Number(t?.target) || 0,
        known: Boolean(t?.known),
        uncertain: Boolean(t?.uncertain),
        fill,
        marker,
        note: targetNote,
        sub,
      };
      words.set(w.word_id, entry);
      paintWord(entry);
    }

    // Token bars go under the word the daemon chose, in query order. The
    // placement is presentation and carries no search meaning — a token
    // straddling a space belongs to two words and still gets exactly one bar.
    for (const tok of plan.tokens || []) {
      const home = words.get(tok.bar_word_id);
      if (!home) continue;
      const seg = document.createElement("div");
      seg.className = "seg" + (tok.discovery ? "" : " local-only");
      seg.style.setProperty("--seg-color", colorForSlot(tok.color_slot));
      const segFill = document.createElement("div");
      segFill.className = "fill";
      segFill.style.width = "0%";
      seg.appendChild(segFill);
      seg.title = tok.discovery
        ? "Waiting for a peer to answer for this part of your search"
        : "This part of your search is common enough that it is not asked of peers";
      home.sub.appendChild(seg);
      if (tok.discovery) {
        // Repeated occurrences of one token value share live state, because
        // they share one fetch. Several segments, one entry.
        const prior = tokens.get(tok.token_id);
        if (prior) prior.segs.push({ seg, fill: segFill });
        else tokens.set(tok.token_id, { segs: [{ seg, fill: segFill }], cycle: 0 });
      }
    }

    el.wordCorpus.hidden = words.size === 0;
    el.wordCorpus.setAttribute("aria-hidden", words.size === 0 ? "true" : "false");
    return { words, tokens };
  }

  /**
   * The word, painted from the plan's token fragments.
   *
   * Every character of the word is covered by exactly one fragment — the query
   * grid tiles the whole normalized string — so this walks the fragments that
   * intersect this occurrence and slices the normalized text the plan already
   * carries. No chopping, no hashing, no tokenizer.
   */
  function colorizeWord(plan, word) {
    const frag = document.createDocumentFragment();
    const text = String(plan.normalized ?? "");
    const pieces = [];
    for (const tok of plan.tokens || []) {
      for (const f of tok.fragments || []) {
        if (f.word_id !== word.word_id) continue;
        if (f.start < word.start || f.end > word.end) continue;
        pieces.push({ start: f.start, end: f.end, slot: tok.color_slot });
      }
    }
    pieces.sort((a, b) => a.start - b.start);
    if (!pieces.length) {
      frag.appendChild(document.createTextNode(word.word));
      return frag;
    }
    for (const p of pieces) {
      const span = document.createElement("span");
      span.className = "tok-char";
      span.textContent = text.slice(p.start, p.end);
      span.style.color = colorForSlot(p.slot);
      frag.appendChild(span);
    }
    return frag;
  }

  /**
   * Apply the frozen targets from PEER_SEARCH_STARTED to render state that
   * already exists (WO-099 §2).
   *
   * The page claims its route before issuing PEER_SEARCH, deliberately, so
   * daemon events may arrive before the acknowledgement resumes. The first
   * implementation then called renderPlan() a second time when the targets
   * landed, which replaced the DOM rows and silently discarded every count,
   * token phase and cycle already applied — the acknowledgement erased the
   * progress it was supposed to annotate.
   *
   * So targets update entries in place. A bar is allowed to paint against an
   * unknown target briefly; the acknowledgement supplies the denominator
   * without rolling the numerator back.
   */
  function applyTargets(targets) {
    if (!active) return;
    for (const t of targets || []) {
      const entry = active.words.get(t.word_id);
      if (!entry) continue;
      entry.target = Number(t.target) || 0;
      entry.known = Boolean(t.known);
      entry.uncertain = Boolean(t.uncertain);
      paintWord(entry);
    }
  }

  function paintWord(entry) {
    if (entry.known && entry.target > 0) {
      const pct = (entry.found / entry.target) * 100;
      entry.fill.style.width = Math.min(100, pct) + "%";
      entry.fill.classList.toggle("past", entry.found > entry.target);
      entry.marker.hidden = false;
      entry.note.textContent =
        ` — ${entry.found} of ~${entry.target}` + (entry.uncertain ? " (rough)" : "");
    } else {
      // No fake marker and no invented denominator: a count and an honest
      // statement that the size of the world is unknown (WO-095 §7).
      entry.fill.style.width = entry.found > 0 ? "100%" : "0%";
      entry.marker.hidden = true;
      entry.note.textContent = ` — ${entry.found} found · target unknown`;
    }
  }

  function paintToken(entry, phase) {
    for (const { seg, fill } of entry.segs) {
      seg.classList.remove("active", "done", "failed");
      switch (phase) {
        case "active":
          // Reset to zero, then animate on the next frame so the CSS
          // transition actually runs rather than painting already-full.
          fill.style.width = "0%";
          seg.classList.add("active");
          requestAnimationFrame(() => {
            fill.style.width = "70%";
          });
          break;
        case "complete":
          seg.classList.add("done");
          fill.style.width = "100%";
          break;
        case "failed":
          seg.classList.add("failed");
          fill.style.width = "0%";
          break;
        case "done":
          seg.classList.add("done");
          break;
        default:
          fill.style.width = "0%";
      }
    }
  }

  /* ---------- events ---------- */

  function onEvent(msg) {
    if (!msg || !active) return;
    const p = msg.payload || {};
    if (p.search_id !== active.searchId) return;
    const seq = Number(p.seq) || 0;
    if (seq && seq <= active.lastSeq) return;
    if (seq) active.lastSeq = seq;

    switch (msg.type) {
      case "PEER_SEARCH_PROGRESS": {
        const entry = active.tokens.get(p.token_id);
        if (entry) {
          entry.cycle = Number(p.cycle) || 0;
          paintToken(entry, p.phase);
        }
        break;
      }
      case "PEER_SEARCH_WORD_PROGRESS": {
        const entry = active.words.get(p.word_id);
        if (entry) {
          entry.found = Number(p.found) || 0;
          paintWord(entry);
        }
        break;
      }
      case "PEER_SEARCH_RESULT": {
        appendResult(p.hit);
        break;
      }
      case "PEER_SEARCH_COMPLETE":
        setNetState(completionText(p));
        finishActive();
        break;
      case "PEER_SEARCH_CANCELLED":
        setNetState(cancelledText(p.reason));
        finishActive();
        break;
      case "PEER_SEARCH_FAILED":
        // Never rendered as an empty successful search: local results stay,
        // and the failure is stated (WO-095 §9).
        setNetState(`Network search failed: ${p.message || "unknown error"}. Local results are unchanged.`);
        finishActive();
        break;
      default:
        break;
    }
  }

  /**
   * A cancellation the user did not ask for needs explaining. "Stopped" alone
   * would leave someone whose contribution level changed in another tab with no
   * idea why their search ended.
   */
  function cancelledText(reason) {
    switch (reason) {
      case "contribution_downgrade":
        return "Network search stopped: this device's contribution level no longer includes it. Local results are unchanged.";
      case "consent_withdrawn":
        return "Network search stopped: network sharing was turned off. Local results are unchanged.";
      case "shutdown":
        return "Network search stopped: the desktop app is shutting down. Local results are unchanged.";
      default:
        return "Network search stopped. Local results are unchanged.";
    }
  }

  function completionText(p) {
    const n = Number(p.results) || 0;
    switch (p.reason) {
      case "local_only":
        return "Nothing in this query is specific enough to ask peers for — searched locally only.";
      case "no_peers":
        return "No peers answered yet. Local results are unchanged.";
      case "budget":
        return `Stopped at this session's network budget after ${n} network result${n === 1 ? "" : "s"}.`;
      case "exhausted":
        return `Ran out of peers with ${n} network result${n === 1 ? "" : "s"} — less than the estimate suggests exists.`;
      case "saturated":
        return `Done — ${n} network result${n === 1 ? "" : "s"}, and peers stopped adding new ones.`;
      default:
        return p.target_met
          ? `Done — ${n} network result${n === 1 ? "" : "s"}, matching the global estimate.`
          : `Done — ${n} network result${n === 1 ? "" : "s"}.`;
    }
  }

  function appendResult(hit) {
    if (!hit?.video_id || !active) return;
    // Local rows win: the same video found locally keeps its local provenance
    // and its position (WO-095 §9). Network-only rows append in arrival order.
    if (active.seen.has(hit.video_id)) return;
    active.seen.add(hit.video_id);
    el.results?.appendChild(hitRow(hit, "found on the network"));
  }

  /* ---------- entry point ---------- */

  /**
   * Run one submission: local results immediately, then network work if it is
   * selected and entitled.
   *
   * @returns {Promise<void>}
   */
  async function run(query, { network }) {
    cancel();
    const myGeneration = generation;
    clearProgress();

    let local;
    try {
      local = await rpc("SEARCH", { query, limit: 100 });
    } catch (err) {
      if (myGeneration !== generation) return;
      el.meta.textContent = `Search failed: ${errText(err)}`;
      return;
    }
    if (myGeneration !== generation) return;

    const plan = local.search?.plan || null;
    const localHits = local.search?.hits || [];
    renderLocal(local.search);
    const seen = new Set(localHits.map((h) => h.video_id));

    if (!network) {
      setNetState("Network search is off — showing only what this device has seen.");
      return;
    }
    if (!hasStreaming()) {
      // A daemon that only speaks the atomic contract. Falling back is fine;
      // pretending to stream is not (WO-095 §3).
      await runAtomic(query, myGeneration, seen);
      return;
    }

    const searchId = crypto.randomUUID();
    const p = ensurePort();
    if (!p) {
      setNetState("Could not reach the background worker; local results only.");
      return;
    }
    // Claimed BEFORE the request is issued, so an event that overtakes the
    // acknowledgement still has a route.
    p.postMessage({ type: "CLAIM_SEARCH", search_id: searchId });

    // Built ONCE. Everything after this updates it in place.
    const { words, tokens } = renderPlan(plan, []);
    active = { searchId, generation: myGeneration, words, tokens, seen, lastSeq: 0 };
    setNetState("Asking peers…");

    let started;
    try {
      started = await rpc("PEER_SEARCH", { query, limit: 100, search_id: searchId });
    } catch (err) {
      if (myGeneration !== generation) return;
      const text = errText(err);
      setNetState(
        /already running/i.test(text)
          ? "Too many searches are running at once. Close one and try again; local results are unchanged."
          : `Network search unavailable: ${text}. Local results are unchanged.`
      );
      finishActive();
      return;
    }
    if (myGeneration !== generation) return;

    if (started.peer_search?.contribution_required) {
      onContributionRequired(started.peer_search.contribution_required);
      setNetState(started.peer_search.message || "Network search needs a higher contribution level.");
      finishActive();
      return;
    }
    // The frozen targets arrive with the acknowledgement. Applied in place, and
    // only if this search is still the active one: a terminal event that won
    // the race has already cleared `active`, and a late acknowledgement must
    // not recreate live state for a search that has finished (WO-099 §2).
    if (started.peer_search_started?.words) {
      applyTargets(started.peer_search_started.words);
    }
  }

  /** Revision-2 fallback: one reply, shown as one reply. */
  async function runAtomic(query, myGeneration, seen) {
    setNetState("Asking peers… (this desktop app answers in one go — update for live progress)");
    try {
      const r = await rpc("PEER_SEARCH", { query, limit: 100 });
      if (myGeneration !== generation) return;
      if (r.peer_search?.contribution_required) {
        onContributionRequired(r.peer_search.contribution_required);
        setNetState(r.peer_search.message || "Network search needs a higher contribution level.");
        return;
      }
      if (r.peer_search?.available === false) {
        setNetState("The peer network is not running. Local results are unchanged.");
        return;
      }
      for (const h of r.peer_search?.hits || []) {
        if (seen.has(h.video_id)) continue;
        seen.add(h.video_id);
        el.results?.appendChild(hitRow(h, "found on the network"));
      }
      setNetState(`Done — ${r.peer_search?.hits?.length || 0} network result(s).`);
    } catch (err) {
      if (myGeneration !== generation) return;
      setNetState(`Network search failed: ${errText(err)}. Local results are unchanged.`);
    }
  }

  function renderLocal(res) {
    el.results.replaceChildren();
    const hits = res?.hits || [];
    if (!hits.length) {
      el.meta.textContent = res?.query
        ? `Nothing found locally for “${res.query}”.`
        : "";
    } else {
      el.meta.textContent =
        `${res.total} local match${res.total === 1 ? "" : "es"}` +
        (res.truncated ? ` · showing ${hits.length}` : "");
    }
    for (const h of hits) {
      const provenance = h.seen > 0 ? `seen ${h.seen}×` : `from a shared bundle`;
      el.results.appendChild(hitRow(h, provenance));
    }
  }

  return {
    run,
    cancel,
    renderPlan,
    applyTargets,
    colorizeWord,
    get activeSearchId() {
      return active?.searchId || null;
    },
  };
}

export { PEER_SEARCH_REV_STREAMING, SEARCH_PORT };
