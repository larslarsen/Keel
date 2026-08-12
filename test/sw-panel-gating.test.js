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
const TT_FYP = "https://www.tiktok.com/";
const OTHER_SITE = "https://example.com/";
const FULLPAGE_URL = "page/index.html";

let setOptionsCalls;
let setPanelBehaviorCalls;
let sidePanelOpenCalls;
let sidePanelCloseCalls;
let tabsCreateCalls;
let tabsUpdateCalls;
let tabsQueryResult;
let broadcastMessages;
let tabsGetResult;
let onUpdatedListener;
let onCreatedListener;
let onActionClickedListener;
let onActivatedListener;
let onConnectListener;
let nativeMessageListener;
// Records "setOptions"/"open" in the order the sidePanel mock methods are
// actually *invoked* (not awaited) — the only way to catch, at the unit
// level, whether something is awaited ahead of sidePanel.open() in the
// click handler. Chrome's real constraint ("open() may only be called in
// response to a user gesture") isn't otherwise reproducible in a mock; this
// is a synchronous proxy for it.
let sidePanelCallOrder;

function installBrowserStub() {
  setOptionsCalls = [];
  setPanelBehaviorCalls = [];
  sidePanelOpenCalls = [];
  sidePanelCloseCalls = [];
  tabsCreateCalls = [];
  tabsUpdateCalls = [];
  tabsQueryResult = [];
  broadcastMessages = [];
  tabsGetResult = { url: "", active: true, windowId: 1, id: 1 };
  onUpdatedListener = null;
  onCreatedListener = null;
  onActionClickedListener = null;
  onActivatedListener = null;
  onConnectListener = null;
  nativeMessageListener = null;
  sidePanelCallOrder = [];

  globalThis.browser = {
    runtime: {
      id: "test-extension-id",
      onMessage: { addListener() {} },
      onInstalled: { addListener() {} },
      onConnect: {
        addListener(fn) {
          onConnectListener = fn;
        },
      },
      sendMessage: (msg) => {
        broadcastMessages.push(msg);
        return Promise.resolve();
      },
      getURL: (p) => p,
      connectNative: () => ({
        onMessage: { addListener(fn) { nativeMessageListener = fn; } },
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
      get: (id) => Promise.resolve({ ...tabsGetResult, id }),
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
      onActivated: {
        addListener(fn) {
          onActivatedListener = fn;
        },
      },
    },
    windows: { update: () => Promise.resolve() },
    sidePanel: {
      setOptions: (opts) => {
        sidePanelCallOrder.push("setOptions");
        setOptionsCalls.push(opts);
        return Promise.resolve();
      },
      setPanelBehavior: (opts) => {
        setPanelBehaviorCalls.push(opts);
        return Promise.resolve();
      },
      open: (opts) => {
        sidePanelCallOrder.push("open");
        sidePanelOpenCalls.push(opts);
        return Promise.resolve();
      },
      close: (opts) => {
        sidePanelCloseCalls.push(opts);
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

// action.onClicked's registered listener is intentionally fire-and-forget
// (Chrome does not await it), so calling it directly only kicks off an async
// IIFE without waiting for it. A macrotask tick reliably flushes it
// regardless of how many awaits the handler chain happens to have —
// unlike chaining a fixed number of Promise.resolve()s, which breaks every
// time an await is added to or removed from the handler.
function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// Stub a connected sidepanel port (name "keel-sidepanel"), so the SW's
// panelOpen() — the port counter — reports the panel as open. Returns a
// disconnect function that tears the port back down. windowId, when given,
// simulates the panel's PANEL_HANDSHAKE telling the SW which window the
// panel document lives in (WO-075 per-window toggle); null/omitted leaves
// the port's window unknown (the conservative case).
function connectPanelPortStub(windowId) {
  let disconnect = () => {};
  let onMessage = null;
  const port = {
    name: "keel-sidepanel",
    onDisconnect: { addListener(fn) { disconnect = fn; } },
    onMessage: { addListener(fn) { onMessage = fn; } },
    postMessage() {},
  };
  onConnectListener(port);
  if (windowId != null && onMessage) {
    onMessage({ type: "PANEL_HANDSHAKE", payload: { windowId } });
  }
  return () => disconnect();
}

describe("WO-071: panel gated to watch pages, button never dead", () => {
  it("broadcasts owner-wide contribution status to extension pages (WO-079)", () => {
    broadcastMessages.length = 0;
    assert.equal(typeof nativeMessageListener, "function");
    nativeMessageListener({
      v: 2,
      id: "owner-event-1",
      type: "CONTRIBUTION_STATUS",
      payload: { stored_level: 2, effective_level: 2, transition: "idle" },
    });
    assert.deepEqual(broadcastMessages.at(-1), {
      type: "CONTRIBUTION_STATUS",
      payload: { stored_level: 2, effective_level: 2, transition: "idle" },
    });
  });

  it("disables openPanelOnActionClick so the button can be driven manually", () => {
    assert.ok(
      setPanelBehaviorCalls.some((c) => c.openPanelOnActionClick === false),
      `expected an openPanelOnActionClick:false call, got ${JSON.stringify(setPanelBehaviorCalls)}`
    );
  });

  it("registers tabs.onUpdated/onCreated/onActivated listeners for gating", () => {
    assert.equal(typeof onUpdatedListener, "function");
    assert.equal(typeof onCreatedListener, "function");
    assert.equal(typeof onActivatedListener, "function");
  });

  it("enables the panel on a YouTube watch URL", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(11, { url: YT_WATCH }, { url: YT_WATCH });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 11,
      enabled: true,
      path: "sidepanel/index.html",
    });
  });

  it("enables the panel on a TikTok watch URL", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(12, { url: TT_WATCH }, { url: TT_WATCH });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 12,
      enabled: true,
      path: "sidepanel/index.html",
    });
  });

  it("enables the panel on the TikTok For-You feed (WO-074: the FYP IS the watch page)", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(15, { url: TT_FYP }, { url: TT_FYP });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 15,
      enabled: true,
      path: "sidepanel/index.html",
    });
  });

  it("disables the panel on YouTube HOME (not a watch page)", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(13, { url: YT_HOME }, { url: YT_HOME });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 13,
      enabled: false,
      path: "sidepanel/index.html",
    });
  });

  it("disables the panel on an unrelated site", async () => {
    setOptionsCalls.length = 0;
    await onUpdatedListener(14, { url: OTHER_SITE }, { url: OTHER_SITE });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 14,
      enabled: false,
      path: "sidepanel/index.html",
    });
  });

  it("action.onClicked on the TikTok FYP opens the panel, not the full-page tab (WO-074)", async () => {
    sidePanelOpenCalls.length = 0;
    tabsCreateCalls.length = 0;
    await onActionClickedListener({ id: 25, url: TT_FYP, windowId: 1 });
    await flush();
    assert.deepEqual(sidePanelOpenCalls, [{ windowId: 1 }]);
    assert.equal(tabsCreateCalls.length, 0, "must not open the full-page tab from the TikTok FYP");
  });

  it("action.onClicked on a watch tab opens the panel, not the full-page tab", async () => {
    sidePanelOpenCalls.length = 0;
    tabsCreateCalls.length = 0;
    await onActionClickedListener({ id: 21, url: YT_WATCH, windowId: 1 });
    await flush();
    assert.deepEqual(sidePanelOpenCalls, [{ windowId: 1 }]);
    assert.equal(tabsCreateCalls.length, 0, "must not open the full-page tab from a watch page");
  });

  it("action.onClicked while the panel is open CLOSES it (toggle), and nothing else", async () => {
    const dropPort = connectPanelPortStub();
    try {
      sidePanelOpenCalls.length = 0;
      sidePanelCloseCalls.length = 0;
      tabsCreateCalls.length = 0;
      await onActionClickedListener({ id: 26, url: YT_WATCH, windowId: 5 });
      await flush();
      assert.ok(sidePanelCloseCalls.length >= 1, "expected close call(s)");
      assert.equal(sidePanelOpenCalls.length, 0, "must not also try to open");
      assert.equal(tabsCreateCalls.length, 0, "must not open the full-page tab");
    } finally {
      dropPort();
    }
  });

  it("action.onClicked toggles TikTok FYP too: close when open", async () => {
    const dropPort = connectPanelPortStub();
    try {
      sidePanelOpenCalls.length = 0;
      sidePanelCloseCalls.length = 0;
      await onActionClickedListener({ id: 27, url: TT_FYP, windowId: 5 });
      await flush();
      assert.ok(sidePanelCloseCalls.length >= 1, "expected close call(s)");
      assert.equal(sidePanelOpenCalls.length, 0, "must not also try to open");
    } finally {
      dropPort();
    }
  });

  it("action.onClicked re-asserts enabled:true after opening (not before — that consumes the click's gesture)", async () => {
    // Regression (confirmed live): a setOptions() await placed BEFORE
    // sidePanel.open() consumes the click's user-gesture context, and
    // open() then fails every time with "may only be called in response to
    // a user gesture." setOptions must come after open() succeeds —
    // fire-and-forget, a backstop for the next click, not a precondition
    // for this one.
    setOptionsCalls.length = 0;
    sidePanelOpenCalls.length = 0;
    sidePanelCallOrder.length = 0;
    await onActionClickedListener({ id: 22, url: YT_WATCH, windowId: 1 });
    await flush();
    const enableCall = setOptionsCalls.find((c) => c.tabId === 22);
    assert.ok(enableCall, "expected a setOptions call for the clicked tab");
    assert.equal(enableCall.enabled, true);
    assert.equal(sidePanelOpenCalls.at(-1).windowId, 1);
    assert.deepEqual(
      sidePanelCallOrder,
      ["open", "setOptions"],
      "open() must be invoked first, with nothing awaited ahead of it — an " +
        "awaited call before it consumes the click's user gesture and open() " +
        "fails every time in real Chrome (confirmed live, see comment above)"
    );
  });

  it("action.onClicked off a watch page opens the full-page tab, never the panel", async () => {
    sidePanelOpenCalls.length = 0;
    tabsCreateCalls.length = 0;
    tabsQueryResult = []; // no full-page tab open yet
    await onActionClickedListener({ id: 22, url: OTHER_SITE });
    await flush();
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
    await flush();
    assert.equal(tabsCreateCalls.length, 0, "must not stack a second full-page tab");
    assert.equal(tabsUpdateCalls.length, 1);
    assert.deepEqual(tabsUpdateCalls[0], { id: 777, opts: { active: true } });
  });
});

describe("WO-075: the toolbar toggle is per-window", () => {
  it("a panel open in another window does not block opening in the clicked window", async () => {
    const dropPort = connectPanelPortStub(4); // panel open in window 4
    try {
      sidePanelOpenCalls.length = 0;
      sidePanelCloseCalls.length = 0;
      tabsCreateCalls.length = 0;
      await onActionClickedListener({ id: 28, url: TT_FYP, windowId: 5 });
      await flush();
      assert.deepEqual(sidePanelOpenCalls, [{ windowId: 5 }]);
      assert.equal(sidePanelCloseCalls.length, 0, "must not close window 5's closed panel");
      assert.equal(tabsCreateCalls.length, 0, "must not open the full-page tab");
    } finally {
      dropPort();
    }
  });

  it("toggle closes only when the clicked window ITSELF has the panel open", async () => {
    const dropPort = connectPanelPortStub(5);
    try {
      sidePanelOpenCalls.length = 0;
      sidePanelCloseCalls.length = 0;
      await onActionClickedListener({ id: 29, url: YT_WATCH, windowId: 5 });
      await flush();
      assert.ok(sidePanelCloseCalls.length >= 1, "expected close call(s)");
      assert.equal(sidePanelOpenCalls.length, 0, "must not also try to open");
    } finally {
      dropPort();
    }
  });

  it("toggle opens again once the window's own panel port disconnects (real close flow)", async () => {
    const dropPort = connectPanelPortStub(5);
    dropPort(); // panel document closed → port gone → window no longer counts open
    sidePanelOpenCalls.length = 0;
    sidePanelCloseCalls.length = 0;
    await onActionClickedListener({ id: 30, url: TT_FYP, windowId: 5 });
    await flush();
    assert.deepEqual(sidePanelOpenCalls, [{ windowId: 5 }]);
    assert.equal(sidePanelCloseCalls.length, 0, "must not close after the panel is gone");
  });

  it("a port that never handshakes stays conservative: toggle closes, never double-opens", async () => {
    const dropPort = connectPanelPortStub(null);
    try {
      sidePanelOpenCalls.length = 0;
      sidePanelCloseCalls.length = 0;
      await onActionClickedListener({ id: 31, url: TT_FYP, windowId: 5 });
      await flush();
      assert.ok(sidePanelCloseCalls.length >= 1, "expected close call(s)");
      assert.equal(sidePanelOpenCalls.length, 0, "must not double-open on an unknown window");
    } finally {
      dropPort();
    }
  });
});

describe("WO-073: panel context comes from the ACTIVE tab, not the last page reported", () => {
  function activeTab(tab) {
    tabsQueryResult = [tab];
    tabsGetResult = tab;
  }

  it("PANEL_CONTEXT_QUERY returns the active tab's platform, surface, focus and proof", async () => {
    activeTab({ id: 31, windowId: 7, url: YT_WATCH, active: true });
    const ctx = await handle(
      { type: "PANEL_CONTEXT_QUERY", payload: { windowId: 7 } },
      {}
    );
    assert.equal(ctx.window_id, 7);
    assert.equal(ctx.tab_id, 31);
    assert.equal(ctx.platform, "yt");
    assert.equal(ctx.surface, "WATCH_NEXT");
    assert.equal(ctx.focus, true);
    assert.equal(ctx.proof, null, "no proof while nothing has reported");
  });

  it("PANEL_CONTEXT_QUERY reports focus:false off a watch page", async () => {
    activeTab({ id: 32, windowId: 7, url: OTHER_SITE, active: true });
    const ctx = await handle(
      { type: "PANEL_CONTEXT_QUERY", payload: { windowId: 7 } },
      {}
    );
    assert.equal(ctx.focus, false);
    assert.equal(ctx.platform, null);
  });

  it("PANEL_CONTEXT_QUERY scopes to lastFocusedWindow when no windowId is given", async () => {
    activeTab({ id: 33, windowId: 8, url: TT_WATCH, active: true });
    const ctx = await handle({ type: "PANEL_CONTEXT_QUERY", payload: {} }, {});
    assert.equal(ctx.window_id, 8);
    assert.equal(ctx.platform, "tt");
    assert.equal(ctx.focus, true);
  });

  it("PANEL_CONTEXT_QUERY treats the TikTok FYP as a focused surface (WO-074)", async () => {
    activeTab({ id: 34, windowId: 8, url: TT_FYP, active: true });
    const ctx = await handle({ type: "PANEL_CONTEXT_QUERY", payload: { windowId: 8 } }, {});
    assert.equal(ctx.platform, "tt");
    assert.equal(ctx.surface, "HOME");
    assert.equal(ctx.focus, true, "the FYP is the TikTok watch page — it must be a focused surface");
  });

  it("onActivated on a YT watch tab enables the panel and broadcasts its context", async () => {
    setOptionsCalls.length = 0;
    broadcastMessages.length = 0;
    sidePanelCloseCalls.length = 0;
    activeTab({ id: 41, windowId: 9, url: YT_WATCH, active: true });
    await onActivatedListener({ tabId: 41, windowId: 9 });
    await flush();
    const ctx = broadcastMessages.find((m) => m.type === "PANEL_CONTEXT");
    assert.ok(ctx, "expected a PANEL_CONTEXT broadcast");
    assert.deepEqual(ctx.payload, {
      windowId: 9,
      tab_id: 41,
      platform: "yt",
      surface: "WATCH_NEXT",
      focus: true,
    });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 41,
      enabled: true,
      path: "sidepanel/index.html",
    });
    assert.equal(sidePanelCloseCalls.length, 0, "must not close on a watch page");
  });

  it("onActivated off a watch page closes the panel and broadcasts focus:false", async () => {
    setOptionsCalls.length = 0;
    broadcastMessages.length = 0;
    sidePanelCloseCalls.length = 0;
    activeTab({ id: 42, windowId: 9, url: YT_HOME, active: true });
    await onActivatedListener({ tabId: 42, windowId: 9 });
    await flush();
    const ctx = broadcastMessages.find((m) => m.type === "PANEL_CONTEXT");
    assert.deepEqual(ctx.payload, {
      windowId: 9,
      tab_id: 42,
      platform: "yt",
      surface: "HOME",
      focus: false,
    });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 42,
      enabled: false,
      path: "sidepanel/index.html",
    });
    assert.equal(sidePanelCloseCalls.length, 2, "close both window and tab forms");
  });

  it("onActivated on the TikTok FYP enables the panel and broadcasts tt focus:true (WO-074)", async () => {
    setOptionsCalls.length = 0;
    broadcastMessages.length = 0;
    sidePanelCloseCalls.length = 0;
    activeTab({ id: 45, windowId: 9, url: TT_FYP, active: true });
    await onActivatedListener({ tabId: 45, windowId: 9 });
    await flush();
    const ctx = broadcastMessages.find((m) => m.type === "PANEL_CONTEXT");
    assert.ok(ctx, "expected a PANEL_CONTEXT broadcast");
    assert.deepEqual(ctx.payload, {
      windowId: 9,
      tab_id: 45,
      platform: "tt",
      surface: "HOME",
      focus: true,
    });
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 45,
      enabled: true,
      path: "sidepanel/index.html",
    });
    assert.equal(sidePanelCloseCalls.length, 0, "must not close on the FYP");
  });

  it("onUpdated on the ACTIVE tab runs the full gate (close-on-leave, not just disable)", async () => {
    setOptionsCalls.length = 0;
    broadcastMessages.length = 0;
    sidePanelCloseCalls.length = 0;
    await onUpdatedListener(43, { url: OTHER_SITE }, { id: 43, windowId: 9, active: true, url: OTHER_SITE });
    await flush();
    const ctx = broadcastMessages.find((m) => m.type === "PANEL_CONTEXT");
    assert.equal(ctx.payload.focus, false, "navigating the active tab away must close the panel");
    assert.ok(sidePanelCloseCalls.length > 0);
  });

  it("onUpdated on a BACKGROUND tab only syncs enable/disable, never closes or broadcasts", async () => {
    setOptionsCalls.length = 0;
    broadcastMessages.length = 0;
    sidePanelCloseCalls.length = 0;
    await onUpdatedListener(44, { url: OTHER_SITE }, { id: 44, windowId: 9, active: false, url: OTHER_SITE });
    assert.equal(sidePanelCloseCalls.length, 0, "a background tab must not close the window's panel");
    assert.equal(broadcastMessages.length, 0, "a background tab must not re-scope the panel");
    assert.deepEqual(setOptionsCalls.at(-1), {
      tabId: 44,
      enabled: false,
      path: "sidepanel/index.html",
    });
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
    // WO-080: proof writes are attributed to the SENDER's tab, which is what
    // makes the per-tab isolation real — the payload carries no tab identity.
    const sender = { tab: { id: 51, windowId: 9, url: TT_FYP } };
    await handle(
      {
        type: "PAGE_CONTEXT",
        payload: { platform: "tt", pageLoadId, surface: "HOME" },
      },
      sender
    );
    // First impression batch establishes generation 1 on the same page.
    await handle(
      { type: "IMPRESSIONS", payload: { impressions: [imp(pageLoadId, "v1")], generation: 1 } },
      sender
    );
    // YouTube-style rail swap: same pageLoadId, new generation -> the proof
    // merges into the same per-tab entry. Before the fix this silently
    // dropped `platform`.
    await handle(
      { type: "IMPRESSIONS", payload: { impressions: [imp(pageLoadId, "v2")], generation: 2 } },
      sender
    );
    // GET_STATUS resolves the proof of window 9's ACTIVE tab — the reporting
    // tab, so its proof must come back.
    tabsQueryResult = [{ id: 51, windowId: 9, url: TT_FYP, active: true }];
    const status = await handle({ type: "GET_STATUS", payload: { windowId: 9 } }, {});
    assert.equal(
      status.proof.platform,
      "tt",
      "platform must survive the rail-generation reset, not silently fall back to yt"
    );
    assert.equal(status.proof.tabId, 51, "the proof must be attributed to the reporting tab");
    assert.equal(status.proof.impressions.length, 2);
  });
});
