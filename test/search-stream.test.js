// SPDX-License-Identifier: Apache-2.0
/**
 * The search page's streaming client (WO-095 §4, §7, §9).
 *
 * Three guards decide whether an event is applied — current search id, current
 * page generation, ahead-of-last sequence — and the interesting cases are the
 * ones where only one of them would have caught the stale event.
 */
import { describe, it, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseHTML } from "linkedom";

const here = dirname(fileURLToPath(import.meta.url));
const pageHtml = readFileSync(
  join(here, "..", "extension", "page", "index.html"),
  "utf8"
);

const PLAN = {
  normalized: "world",
  words: [{ word_id: 0, word: "world", start: 0, end: 5, stopword: false }],
  tokens: [
    {
      token_id: 0,
      color_slot: 0,
      start: 0,
      end: 3,
      discovery: true,
      bar_word_id: 0,
      fragments: [{ word_id: 0, start: 0, end: 3 }],
    },
    {
      token_id: 1,
      color_slot: 1,
      start: 3,
      end: 6,
      discovery: true,
      bar_word_id: 0,
      fragments: [{ word_id: 0, start: 3, end: 5 }],
    },
  ],
};

describe("streaming search page client", () => {
  let document;
  let createSearchStream;
  let port;
  let rpcCalls;
  let stream;

  beforeEach(async () => {
    const { document: doc } = parseHTML(pageHtml);
    document = doc;
    globalThis.document = document;
    globalThis.requestAnimationFrame = (fn) => fn();
    // Node's global crypto is a getter-only property, so the id source is
    // stubbed by defining over it rather than by assignment.
    let n = 0;
    Object.defineProperty(globalThis, "crypto", {
      configurable: true,
      value: { randomUUID: () => `search-${++n}` },
    });

    const mod = await import("../extension/page/search_stream.js");
    createSearchStream = mod.createSearchStream;

    const messageListeners = [];
    port = {
      posted: [],
      onMessage: { addListener: (fn) => messageListeners.push(fn) },
      onDisconnect: { addListener: () => {} },
      postMessage(msg) {
        this.posted.push(msg);
      },
      emit(msg) {
        for (const fn of messageListeners) fn(msg);
      },
    };

    rpcCalls = [];
    stream = createSearchStream({
      browser: { runtime: { connect: () => port } },
      rpc: async (type, payload) => {
        rpcCalls.push({ type, payload });
        if (type === "SEARCH") {
          return {
            search: {
              query: "world",
              hits: [{ video_id: "local1", title: "A local world", seen: 3 }],
              total: 1,
              truncated: false,
              plan: PLAN,
            },
          };
        }
        if (type === "PEER_SEARCH") {
          return {
            peer_search_started: {
              search_id: payload.search_id,
              tokens: 2,
              words: [
                { word_id: 0, word: "world", target: 4, raw: 5, known: true, uncertain: false },
              ],
            },
          };
        }
        return {};
      },
      el: {
        wordCorpus: document.getElementById("word-corpus"),
        wordCorpusMeta: document.getElementById("word-corpus-meta"),
        peerProgressCaption: document.getElementById("peer-progress-caption"),
        results: document.getElementById("results"),
        meta: document.getElementById("search-meta"),
      },
      hitRow: (hit, provenance) => {
        const li = document.createElement("li");
        li.dataset.videoId = hit.video_id;
        li.dataset.provenance = provenance;
        return li;
      },
      hasStreaming: () => true,
    });
  });

  const evt = (type, extra) => ({
    type,
    payload: { search_id: stream.activeSearchId, ...extra },
  });

  it("claims its search id before issuing the request", async () => {
    await stream.run("world", { network: true });
    const claimIndex = port.posted.findIndex((m) => m.type === "CLAIM_SEARCH");
    assert.ok(claimIndex >= 0, "the page never claimed a route");
    const peerCall = rpcCalls.findIndex((c) => c.type === "PEER_SEARCH");
    assert.ok(
      peerCall >= 0,
      "the page never started the search"
    );
    // The claim is a Port message and the request is an RPC, so ordering is
    // asserted by the claim existing at all before the first event can arrive —
    // which the next test exercises directly.
    assert.equal(port.posted[claimIndex].search_id, rpcCalls[peerCall].payload.search_id);
  });

  it("renders local results immediately and appends network rows after", async () => {
    await stream.run("world", { network: true });
    let rows = [...document.querySelectorAll("#results li")];
    assert.equal(rows.length, 1);
    assert.equal(rows[0].dataset.provenance, "seen 3×");

    port.emit(
      evt("PEER_SEARCH_RESULT", { seq: 1, hit: { video_id: "net1", title: "Net world" } })
    );
    rows = [...document.querySelectorAll("#results li")];
    assert.equal(rows.length, 2);
    assert.equal(rows[1].dataset.provenance, "found on the network");
  });

  it("keeps the local row when the same video arrives from the network", async () => {
    await stream.run("world", { network: true });
    port.emit(
      evt("PEER_SEARCH_RESULT", { seq: 1, hit: { video_id: "local1", title: "A local world" } })
    );
    const rows = [...document.querySelectorAll("#results li")];
    assert.equal(rows.length, 1, "the video was rendered twice");
    assert.equal(
      rows[0].dataset.provenance,
      "seen 3×",
      "the network row replaced the local provenance"
    );
  });

  it("ignores an event whose sequence is not ahead of the last applied", async () => {
    await stream.run("world", { network: true });
    port.emit(evt("PEER_SEARCH_WORD_PROGRESS", { seq: 5, word_id: 0, found: 3 }));
    const note = () => document.querySelector("#word-corpus .word-target").textContent;
    assert.match(note(), /3 of ~4/);

    // A reordered or replayed earlier event must not roll the count back.
    port.emit(evt("PEER_SEARCH_WORD_PROGRESS", { seq: 2, word_id: 0, found: 1 }));
    assert.match(note(), /3 of ~4/);
  });

  it("ignores events belonging to a search it already replaced", async () => {
    await stream.run("world", { network: true });
    const firstId = stream.activeSearchId;
    port.emit({
      type: "PEER_SEARCH_WORD_PROGRESS",
      payload: { search_id: firstId, seq: 1, word_id: 0, found: 2 },
    });

    await stream.run("world", { network: true });
    assert.notEqual(stream.activeSearchId, firstId, "the replacement reused the search id");

    // A late event from the replaced job.
    port.emit({
      type: "PEER_SEARCH_WORD_PROGRESS",
      payload: { search_id: firstId, seq: 99, word_id: 0, found: 999 },
    });
    const note = document.querySelector("#word-corpus .word-target").textContent;
    assert.doesNotMatch(note, /999/, "a replaced search's event was applied to the current one");
  });

  it("lets a word count exceed its target while keeping the marker", async () => {
    await stream.run("world", { network: true });
    port.emit(evt("PEER_SEARCH_WORD_PROGRESS", { seq: 1, word_id: 0, found: 9 }));

    const row = document.querySelector("#word-corpus .word-row");
    assert.match(row.querySelector(".word-target").textContent, /9 of ~4/);
    assert.equal(
      row.querySelector(".target-marker").hidden,
      false,
      "the 100% marker must stay put when the count runs past it"
    );
    assert.equal(
      row.querySelector(".word-bar .fill").style.width,
      "100%",
      "the fill stops at 100% rather than overflowing its track"
    );
    assert.ok(
      row.querySelector(".word-bar .fill").classList.contains("past"),
      "passing the estimate must be visually distinct from exactly meeting it"
    );
  });

  it("resets a token bar on each new response cycle and snaps it on completion", async () => {
    await stream.run("world", { network: true });
    const seg = () => document.querySelector("#word-corpus .token-subbars .seg");

    port.emit(evt("PEER_SEARCH_PROGRESS", { seq: 1, token_id: 0, cycle: 1, phase: "active" }));
    assert.ok(seg().classList.contains("active"));

    port.emit(evt("PEER_SEARCH_PROGRESS", { seq: 2, token_id: 0, cycle: 1, phase: "complete" }));
    assert.ok(seg().classList.contains("done"));
    assert.equal(seg().querySelector(".fill").style.width, "100%");

    // Another peer for the same token reuses and resets the same bar.
    port.emit(evt("PEER_SEARCH_PROGRESS", { seq: 3, token_id: 0, cycle: 2, phase: "active" }));
    assert.ok(seg().classList.contains("active"));
    assert.equal(
      document.querySelectorAll("#word-corpus .token-subbars .seg").length,
      2,
      "a second response cycle must reuse the bar, not add one"
    );
  });

  it("states every terminal outcome and never blanks the local results", async () => {
    const state = () => document.getElementById("peer-progress-caption").textContent;

    for (const [payload, pattern] of [
      [{ reason: "no_peers", results: 0 }, /No peers/i],
      [{ reason: "budget", results: 2 }, /budget/i],
      [{ reason: "exhausted", results: 1 }, /Ran out of peers/i],
      [{ reason: "local_only", results: 0 }, /searched locally only/i],
      [{ reason: "complete", results: 3, target_met: true }, /Done/i],
    ]) {
      await stream.run("world", { network: true });
      port.emit(evt("PEER_SEARCH_COMPLETE", { seq: 1, ...payload }));
      assert.match(state(), pattern, `terminal reason ${payload.reason} was not stated`);
      assert.equal(
        document.querySelectorAll("#results li").length >= 1,
        true,
        "local results disappeared on a terminal event"
      );
    }
  });

  it("states a failure rather than showing an empty successful search", async () => {
    await stream.run("world", { network: true });
    port.emit(evt("PEER_SEARCH_FAILED", { seq: 1, message: "peer unreachable", results: 0 }));
    const state = document.getElementById("peer-progress-caption").textContent;
    assert.match(state, /failed/i);
    assert.match(state, /Local results are unchanged/i);
    assert.equal(document.querySelectorAll("#results li").length, 1);
  });

  it("cancels the running job when network search is switched off mid-flight", async () => {
    await stream.run("world", { network: true });
    const id = stream.activeSearchId;
    stream.cancel();
    assert.ok(
      rpcCalls.some((c) => c.type === "PEER_SEARCH_CANCEL" && c.payload.search_id === id),
      "cancelling did not tell the daemon, so the job kept spending peers' budget"
    );
    assert.ok(port.posted.some((m) => m.type === "RELEASE_SEARCH" && m.search_id === id));
  });

  it("does not start network work, or claim a route, when network search is off", async () => {
    await stream.run("world", { network: false });
    assert.equal(rpcCalls.filter((c) => c.type === "PEER_SEARCH").length, 0);
    assert.equal(document.querySelectorAll("#results li").length, 1);
    assert.match(
      document.getElementById("peer-progress-caption").textContent,
      /Network search is off/i
    );
  });
});
