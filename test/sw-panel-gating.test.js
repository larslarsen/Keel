// SPDX-License-Identifier: Apache-2.0
/**
 * WO-071: the side panel is gated to YouTube/TikTok watch pages only —
 * `syncPanelForTab` (driven by tabs.onUpdated/onCreated + a startup sweep)
 * enables/disables the panel per tab, and the toolbar button (action.onClicked,
 * with openPanelOnActionClick:false) opens the panel on a watch page or the
 * full-page tab everywhere else, never a dead click.
 *
 * Also covers the defect 2 regression: rememberPage's rail-generation reset
 * used to drop `platform` from lastPage, which read as "yt" on a TikTok tab
 * once YouTube's ~2s rail-swap-shaped reset fired.
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";

const YT_WATCH = "https://www.youtube.com/watch?v=aaaaaaaaaaa";
const YT_HOME = "https://www.youtube.com/";
const TT_WATCH = "https://www.tiktok.com/@someone/video/1234567890123456";
const OTHER_SITE = "https://example.com/";
const FULLPAGE_URL = "page/index.html";

let setOptionsCalls;
let setPanelBehaviorCalls;
let sidePanelOpenCalls;
let tabsCreateCalls;
let tabsUpdateCalls;
let tabsQueryResult;
let onUpdatedListener;
let onCreatedListener;
let onActionClickedListener;

function installBrowserStub() {
  setOptionsCalls = [];
  setPanelBehaviorCalls = [];
  sidePanelOpenCalls = [];
  tabsCreateCalls = [];
  tabsUpdateCalls = [];
  tabsQueryResult = [];
  onUpdatedListener = null;
  onCreatedListener = null;
  onActionClickedListener = null;

  globalThis.browser = {
    runtime: {
      id: "test-extension-id",
      onMessage: { addListener() {} },
      onInstalled: { addListener() {} },
      onConnect: { addListener() {} },
      sendMessage: () => Promise.resolve(),
      getURL: (p) => p,
      connectNative: () => ({
        onMessage: { addListener() {} },
        onDisconnect: { addListener() {} },
        postMessage() {},
        disconnect() {},
      }),
    },
    alarms: undefined,
    storage: { local: { get: () => Promise.resolve({}), set: () => Promise.resolve() } },
    tabs: {
      query: () => Promise.resolve(tabsQueryResult),
      sendMessage: () => Promise.resolve(),
      create: (opts) => {
        tabsCreateCalls.push(opts);
        return Promise.resolve({ id: 999, ...opts });
      },
      update: (id, opts) => {
        tabsUpdateCalls.push({ id, opts });
        return Promise.resolve({ id });
      },
      get: () => Promise.resolve({}),
      onUpdated: {
        addListener(fn) {
          onUpdatedListener = fn;
        },
      },
      onCreated: {
        addListener(fn) {
          onCreatedListener = fn;
        },
      },
    },
    windows: { update: () => Promise.resolve() },
    sidePanel: {
      setOptions: (opts) => {
        setOptionsCalls.push(opts);
        return Promise.resolve();
      },
      setPanelBehavior: (opts) => {
        setPanelBehaviorCalls.push(opts);
        return Promise.resolve();
      },
      open: (opts) => {
        sidePanelOpenCalls.push(opts);
        return Promise.resolve();
      },
    },
    action: {
      onClicked: {
        addListener(fn) {
          onActionClickedListener = fn;
        },
      },
    },
  };
}

let handle;

before(async () => {
  installBrowserStub();
  const sw = await import("../extension/background/sw.js");
  handle = sw.handle;
});

after(() => {
  delete globalThis.browser;
});

describe("WO-071: panel gated to watch pages, button never dead", () => {
  it("disables openPanelOnActionClick so the button can be driven manually", () => {
    assert.ok(
      setPanelBehaviorCalls.some((c) => c.openPanelOnActionClick === false),
      `expected an openPanelOnActionClick:false call, got ${JSON.stringify(setPanelBehaviorCalls)}`
    );
  });

  it("registers tabs.onUpdated/onCreated listeners for gating", () => {
    assert.equal(typeof onUpdatedListener, "function");
    assert.equal(typeof onCreatedListener, "function");
  });

  it("enables the panel on a YouTube watch URL", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(11, { url: YT_WATCH }, { url: YT_WATCH });
    assert.deepEqual(setOptionsCalls.at(-1), { tabId: 11, enabled: true });
  });

  it("enables the panel on a TikTok watch URL", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(12, { url: TT_WATCH }, { url: TT_WATCH });
    assert.deepEqual(setOptionsCalls.at(-1), { tabId: 12, enabled: true });
  });

  it("disables the panel on YouTube HOME (not a watch page)", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(13, { url: YT_HOME }, { url: YT_HOME });
    assert.deepEqual(setOptionsCalls.at(-1), { tabId: 13, enabled: false });
  });

  it("disables the panel on an unrelated site", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(14, { url: OTHER_SITE }, { url: OTHER_SITE });
    assert.deepEqual(setOptionsCalls.at(-1), { tabId: 14, enabled: false });
  });

  it("action.onClicked on a watch tab opens the panel, not the full-page tab", async () => {
    sidePanelOpenCalls.length = 0;
    tabsCreateCalls.length = 0;
    await onActionClickedListener({ id: 21, url: YT_WATCH });
    assert.deepEqual(sidePanelOpenCalls, [{ tabId: 21 }]);
    assert.equal(tabsCreateCalls.length, 0, "must not open the full-page tab from a watch page");
  });

  it("action.onClicked off a watch page opens the full-page tab, never the panel", async () => {
    sidePanelOpenCalls.length = 0;
    tabsCreateCalls.length = 0;
    tabsQueryResult = []; // no full-page tab open yet
    await onActionClickedListener({ id: 22, url: OTHER_SITE });
    assert.equal(sidePanelOpenCalls.length, 0, "must not open the panel off a watch page");
    assert.equal(tabsCreateCalls.length, 1);
    assert.ok(
      String(tabsCreateCalls[0].url).includes(FULLPAGE_URL),
      `expected the full-page URL, got ${tabsCreateCalls[0].url}`
    );
  });

  it("action.onClicked focuses an already-open full-page tab instead of stacking a duplicate", async () => {
    sidePanelOpenCalls.length = 0;
    tabsCreateCalls.length = 0;
    tabsUpdateCalls.length = 0;
    tabsQueryResult = [{ id: 777, windowId: 1, url: FULLPAGE_URL }];
    await onActionClickedListener({ id: 23, url: OTHER_SITE });
    assert.equal(tabsCreateCalls.length, 0, "must not stack a second full-page tab");
    assert.equal(tabsUpdateCalls.length, 1);
    assert.deepEqual(tabsUpdateCalls[0], { id: 777, opts: { active: true } });
  });
});

describe("WO-071 defect 2: TikTok platform must survive a rail-generation reset", () => {
  function imp(pid, vid, generation) {
    return {
      page_load_id: pid,
      observed_at: 1700000000000,
      surface: "WATCH_NEXT",
      context_video_id: "ctx-" + vid,
      context_query_hash: null,
      slot_index: 0,
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

  it("keeps platform:'tt' across the same-page rail-generation reset", async () => {
    const pageLoadId = "cccccccc-3333-4333-8333-333333333333";
    await handle(
      {
        type: "PAGE_CONTEXT",
        payload: { platform: "tt", pageLoadId, surface: "WATCH_NEXT" },
      },
      {}
    );
    // First impression batch establishes generation 1 on the same page.
    await handle(
      { type: "IMPRESSIONS", payload: { impressions: [imp(pageLoadId, "v1")], generation: 1 } },
      {}
    );
    // YouTube-style rail swap: same pageLoadId, new generation -> rememberPage
    // resets lastPage. Before the fix this silently dropped `platform`.
    await handle(
      { type: "IMPRESSIONS", payload: { impressions: [imp(pageLoadId, "v2")], generation: 2 } },
      {}
    );
    const status = await handle({ type: "GET_STATUS" }, {});
    assert.equal(
      status.lastPage.platform,
      "tt",
      "platform must survive the rail-generation reset, not silently fall back to yt"
    );
  });
});
