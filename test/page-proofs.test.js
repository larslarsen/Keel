// SPDX-License-Identifier: Apache-2.0
/**
 * WO-080: page proof is owned per TAB, not extension-global.
 *
 * The store is a pure module (no browser APIs) so the whole defect class can
 * be pinned at unit level: two same-platform tabs cannot overwrite one
 * another, a background tab's proof can never become the "active" one, late
 * messages from a previous document are dropped, and nothing is persisted.
 */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { createProofStore } from "../extension/background/page_proofs.js";

function imp(pid, vid, slot = 0) {
  return {
    page_load_id: pid,
    observed_at: 1700000000000,
    surface: "WATCH_NEXT",
    context_video_id: "ctx-" + vid,
    context_query_hash: null,
    slot_index: slot,
    video_id: vid,
    channel_id: null,
    channel_name: null,
    channel_unknown: false,
    title: "Title " + vid,
    duration_s: null,
    view_count: null,
    published_at: null,
    badges: [],
  };
}

function ctx(tab, win, pid, platform = "yt", focus = true) {
  return { tabId: tab, windowId: win, pageLoadId: pid, platform, surface: "WATCH_NEXT", focus };
}

describe("WO-080: per-tab proof store", () => {
  it("keeps two same-platform tabs as separate proofs (the core defect)", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pidA", "yt"));
    store.observeContext(ctx(2, 10, "pidB", "yt"));
    store.observeImpressions({ tabId: 1, values: [imp("pidA", "v1")] });
    store.observeImpressions({ tabId: 2, values: [imp("pidB", "v2")] });

    assert.equal(store.get(1).impressions.length, 1);
    assert.equal(store.get(1).impressions[0].video_id, "v1");
    assert.equal(store.get(2).impressions[0].video_id, "v2");
    assert.equal(store.get(1).pageLoadId, "pidA");
    assert.equal(store.get(2).pageLoadId, "pidB");
  });

  it("keeps a TikTok and a YouTube tab apart in one window", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pidY", "yt"));
    store.observeContext(ctx(2, 10, "pidT", "tt"));
    assert.equal(store.get(1).platform, "yt");
    assert.equal(store.get(2).platform, "tt");
  });

  it("tracks the window a tab's proof belongs to (multi-window)", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pidA"));
    store.observeContext(ctx(2, 11, "pidB"));
    assert.equal(store.get(1).windowId, 10);
    assert.equal(store.get(2).windowId, 11);
  });

  it("a new pageLoadId REPLACES the proof wholesale (navigation invalidates)", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "oldPage"));
    store.observeImpressions({ tabId: 1, values: [imp("oldPage", "vOld")] });
    assert.equal(store.get(1).impressions.length, 1);

    store.observeContext(ctx(1, 10, "newPage"));
    assert.equal(store.get(1).impressions.length, 0, "new document starts empty");
    assert.equal(store.get(1).pageLoadId, "newPage");
  });

  it("a rail-generation update merges into the SAME proof without resetting it", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pid", "tt"));
    store.observeImpressions({ tabId: 1, values: [imp("pid", "v1")], railGeneration: 1 });
    store.observeImpressions({ tabId: 1, values: [imp("pid", "v2")], railGeneration: 2 });
    const p = store.get(1);
    assert.equal(p.platform, "tt", "platform must survive the generation reset");
    assert.equal(p.pageLoadId, "pid");
    assert.equal(p.railGeneration, 2);
    assert.equal(p.impressions.length, 2);
  });

  it("late impressions from the PREVIOUS document are dropped, not restored", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "oldPage"));
    store.observeContext(ctx(1, 10, "newPage"));
    const { accepted, stale } = store.observeImpressions({
      tabId: 1,
      values: [imp("oldPage", "vLate")],
    });
    assert.equal(accepted.length, 0, "the stale batch must not reach the daemon");
    assert.equal(stale, true);
    assert.equal(store.get(1).impressions.length, 0);
    assert.equal(store.get(1).pageLoadId, "newPage");
  });

  it("considers a message without a page_load_id unattributable and drops it", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pid"));
    const { accepted, stale } = store.observeImpressions({
      tabId: 1,
      values: [{ ...imp("pid", "v1"), page_load_id: null }],
    });
    assert.equal(accepted.length, 0);
    assert.equal(stale, true);
  });

  it("unknown sender tab: context write is refused, impressions no-op", () => {
    const store = createProofStore();
    assert.equal(store.observeContext({ ...ctx(1, 10, "pid"), tabId: undefined }), null);
    const r = store.observeImpressions({ tabId: 99, values: [imp("pid", "v1")] });
    assert.equal(r.accepted.length, 0);
    assert.equal(r.proof, null);
  });

  it("a missing pageLoadId in a context write refuses the proof (off-surface pages)", () => {
    const store = createProofStore();
    assert.equal(store.observeContext({ tabId: 1, windowId: 10, pageLoadId: "" }), null);
    assert.equal(store.size(), 0);
  });

  it("remove(tabId) drops the proof (tab closed)", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pidA"));
    store.observeContext(ctx(2, 10, "pidB"));
    store.remove(1);
    assert.equal(store.get(1), null);
    assert.ok(store.get(2), "other tabs keep their proofs");
  });

  it("clear() drops every proof (wipe / SW restart keeps nothing)", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pidA"));
    store.clear();
    assert.equal(store.size(), 0);
  });

  it("a fresh store instance is empty — SW restart never restores proofs", () => {
    const a = createProofStore();
    const b = createProofStore();
    a.observeContext(ctx(1, 10, "pidA"));
    assert.equal(a.size(), 1);
    assert.equal(b.size(), 0);
  });

  it("bounded: evicts the oldest proof beyond the bound", () => {
    const store = createProofStore(2);
    store.observeContext(ctx(1, 10, "pid1"));
    store.observeContext(ctx(2, 10, "pid2"));
    store.observeContext(ctx(3, 10, "pid3"));
    assert.equal(store.size(), 2);
    assert.equal(store.get(1), null, "oldest entry evicted");
    assert.ok(store.get(2) && store.get(3));
  });

  it("merges by (video_id, slot_index) and sorts by slot", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pid"));
    store.observeImpressions({ tabId: 1, values: [imp("pid", "v1", 0), imp("pid", "v3", 2)] });
    store.observeImpressions({ tabId: 1, values: [imp("pid", "v2", 1), imp("pid", "v1", 0)] });
    const p = store.get(1);
    assert.deepEqual(
      p.impressions.map((i) => i.video_id),
      ["v1", "v2", "v3"]
    );
    assert.equal(p.impressions.filter((i) => i.video_id === "v1").length, 1);
  });

  it("accumulates extraction failures on the tab's proof", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pid"));
    store.observeImpressions({ tabId: 1, values: [], failures: 3 });
    assert.equal(store.get(1).failures, 3);
  });

  it("get() returns a copy — callers cannot mutate the held proof", () => {
    const store = createProofStore();
    store.observeContext(ctx(1, 10, "pid"));
    store.observeImpressions({ tabId: 1, values: [imp("pid", "v1")] });
    const p = store.get(1);
    p.impressions.push(imp("pid", "vX"));
    assert.equal(store.get(1).impressions.length, 1);
  });
});