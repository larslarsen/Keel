// SPDX-License-Identifier: Apache-2.0
/**
 * Regression for BUG S2, fixed structurally by WO-080: the SW `handle`
 * IMPRESSIONS path used to mutate ONE shared `lastPage` (via rememberPage)
 * and then `await` sendImpressions before broadcasting. A second IMPRESSIONS
 * arriving during that await mutated `lastPage`, so the first handler resumed
 * and broadcast its impressions under the SECOND page's proof — a stale-data
 * commit. The corpus was fine (impressions carry their own page_load_id); the
 * panel's page-proof context was wrong.
 *
 * WO-080 replaces the shared proof with a per-TAB store: each handler works
 * on its own tab's entry and broadcasts a pre-await snapshot, so the
 * interleaving can no longer cross-tag proofs. This test drives two
 * interleaved IMPRESSIONS messages from two tabs and asserts each broadcast
 * carries ITS OWN tab's proof with its own page_load_id.
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";

// Capture every broadcast the SW emits via browser.runtime.sendMessage.
const broadcasts = [];

// Minimal browser stub: enough for sw.js module load + the handlers under test.
// browser.js requires globalThis.browser.runtime.id to be present, else it
// throws "WebExtension API unavailable".
function installBrowserStub() {
  globalThis.browser = {
    runtime: {
      id: "test-extension-id",
      onMessage: { addListener() {} },
      onInstalled: { addListener() {} },
      sendMessage: (msg) => {
        broadcasts.push(msg);
        return Promise.resolve();
      },
      getURL: (p) => p,
      // connectNative is called synchronously by native.js (no await), so the
      // stub must return the port object directly, not a Promise.
      connectNative: () => ({
        onMessage: { addListener() {} },
        onDisconnect: { addListener() {} },
        postMessage() {},
        disconnect() {},
      }),
    },
    alarms: undefined, // native.js uses optional chaining on alarms
    storage: { local: { get: () => Promise.resolve({}), set: () => Promise.resolve() } },
    tabs: { query: () => Promise.resolve([]), sendMessage: () => Promise.resolve() },
  };
}

function imp(pid, vid) {
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

let handle;
let setBridge;

before(async () => {
  installBrowserStub();
  const sw = await import("../extension/background/sw.js");
  handle = sw.handle;
  setBridge = sw.__test_setBridge;

  // Fake bridge: connected, and request() yields a microtask so the caller's
  // await suspends — that is what lets a second handle() interleave.
  setBridge({
    get helloOk() {
      return true;
    },
    get connected() {
      return true;
    },
    request: () => Promise.resolve({ type: "IMPRESSIONS_ACK", payload: { inserted: 1 } }),
    post: () => true,
  });
  broadcasts.length = 0;
});

after(() => {
  delete globalThis.browser;
});

describe("BUG S2 (fixed by WO-080): per-tab proofs cannot cross-tag across an await", () => {
  it("each interleaved IMPRESSIONS broadcasts ITS OWN tab's proof", async () => {
    const pidA = "AAAAAAAA-1111-4111-8111-111111111111";
    const pidB = "BBBBBBBB-2222-4222-8222-222222222222";
    // Each sender's tab must first claim a proof slot.
    await handle(
      { type: "PAGE_CONTEXT", payload: { platform: "yt", pageLoadId: pidA } },
      { tab: { id: 1, windowId: 10, url: "https://www.youtube.com/watch?v=aaaaaaaaaaa" } }
    );
    await handle(
      { type: "PAGE_CONTEXT", payload: { platform: "yt", pageLoadId: pidB } },
      { tab: { id: 2, windowId: 10, url: "https://www.youtube.com/watch?v=bbbbbbbbbbb" } }
    );

    const msgA = {
      type: "IMPRESSIONS",
      payload: { impressions: [imp(pidA, "vidA")] },
    };
    const msgB = {
      type: "IMPRESSIONS",
      payload: { impressions: [imp(pidB, "vidB")] },
    };

    // Fire A (suspends at await), then B, without awaiting A first.
    const pA = handle(msgA, { tab: { id: 1, windowId: 10 } });
    const pB = handle(msgB, { tab: { id: 2, windowId: 10 } });
    await pA;
    await pB;

    const storeUpdates = broadcasts.filter((m) => m.type === "STORE_UPDATED");
    assert.ok(storeUpdates.length >= 2, "expected both handlers to broadcast");

    const byTab = (tabId) => storeUpdates.find((m) => m.payload.tab_id === tabId);
    const a = byTab(1);
    const b = byTab(2);
    assert.ok(a, "expected tab 1's broadcast");
    assert.ok(b, "expected tab 2's broadcast");
    assert.equal(
      a.payload.proof.pageLoadId,
      pidA,
      "tab A's broadcast carries A's own proof, not B's"
    );
    assert.equal(
      b.payload.proof.pageLoadId,
      pidB,
      "tab B's broadcast carries B's own proof, not A's"
    );
    assert.equal(a.payload.tab_id, 1);
    assert.equal(b.payload.tab_id, 2);
  });
});