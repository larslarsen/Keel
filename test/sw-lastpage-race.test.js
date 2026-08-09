// SPDX-License-Identifier: Apache-2.0
/**
 * Regression for BUG S2: the SW `handle` IMPRESSIONS path mutates the shared
 * module-level `lastPage` (via rememberPage) and then `await`s sendImpressions
 * before broadcasting `{ ...result, lastPage }`. A second IMPRESSIONS arriving
 * during that await mutates `lastPage`, so the first handler resumes and
 * broadcasts its impressions under the SECOND page's `lastPage` — a stale-data
 * commit. The corpus is fine (impressions carry their own page_load_id); the
 * panel's page-proof context is wrong.
 *
 * This test drives two interleaved IMPRESSIONS messages and asserts that A's
 * broadcast carries lastPage.pageLoadId === "A".
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

describe("BUG S2: lastPage stale commit across await", () => {
  it("broadcasts A's impressions under A's lastPage, not B's", async () => {
    const msgA = {
      type: "IMPRESSIONS",
      payload: { impressions: [imp("AAAAAAAA-1111-4111-8111-111111111111", "vidA")] },
    };
    const msgB = {
      type: "IMPRESSIONS",
      payload: { impressions: [imp("BBBBBBBB-2222-4222-8222-222222222222", "vidB")] },
    };

    // Fire A (suspends at await after setting lastPage=A), then B (sets
    // lastPage=B) without awaiting A first — this is the interleaving.
    const pA = handle(msgA, {});
    const pB = handle(msgB, {});
    await pA;
    await pB;

    // Each handle() broadcasts exactly one STORE_UPDATED. A's broadcast must
    // claim lastPage.pageLoadId === A, not the B that ran during A's await.
    const storeUpdates = broadcasts.filter((m) => m.type === "STORE_UPDATED");
    assert.ok(storeUpdates.length >= 1, "expected at least one STORE_UPDATED");

    // The first broadcast is A's handler resuming; it must not have been
    // clobbered by B's lastPage.
    const aBroadcast = storeUpdates[0];
    assert.equal(
      aBroadcast.payload.lastPage.pageLoadId,
      "AAAAAAAA-1111-4111-8111-111111111111",
      "A's broadcast shows B's lastPage (stale commit across await)"
    );
  });
});
