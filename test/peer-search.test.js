// SPDX-License-Identifier: Apache-2.0
/**
 * WO-059: the SW's PEER_SEARCH case is a distinct RPC from SEARCH — it can
 * reach the network, SEARCH never does — and must relay the daemon's
 * PEER_SEARCH_RESULT payload (including a false `available`, which the UI
 * relies on to distinguish "no swarm running" from "network searched, found
 * nothing").
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";

function installBrowserStub() {
  globalThis.browser = {
    runtime: {
      id: "test-extension-id",
      onMessage: { addListener() {} },
      onInstalled: { addListener() {} },
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
    tabs: { query: () => Promise.resolve([]), sendMessage: () => Promise.resolve() },
  };
}

let handle;
let setBridge;
let lastRequest;

before(async () => {
  installBrowserStub();
  const sw = await import("../extension/background/sw.js");
  handle = sw.handle;
  setBridge = sw.__test_setBridge;
});

after(() => {
  delete globalThis.browser;
});

describe("PEER_SEARCH (WO-059)", () => {
  it("rejects without hitting the daemon when the query is blank", async () => {
    setBridge({
      get helloOk() {
        return true;
      },
      get connected() {
        return true;
      },
      request: () => {
        throw new Error("must not call the daemon for a blank query");
      },
      post: () => true,
    });
    await assert.rejects(() => handle({ type: "PEER_SEARCH", payload: { query: "  " } }, {}));
  });

  it("relays a found result, including untitled hits", async () => {
    setBridge({
      get helloOk() {
        return true;
      },
      get connected() {
        return true;
      },
      request: (type, payload) => {
        lastRequest = { type, payload };
        return Promise.resolve({
          type: "PEER_SEARCH_RESULT",
          payload: {
            query: "sourdough",
            available: true,
            hits: [
              { video_id: "titledvid01", title: "A sourdough tutorial" },
              { video_id: "untitledvid1", title: "" },
            ],
          },
        });
      },
      post: () => true,
    });

    const res = await handle(
      { type: "PEER_SEARCH", payload: { query: "sourdough", limit: 50 } },
      {}
    );

    assert.equal(lastRequest.type, "PEER_SEARCH");
    assert.equal(lastRequest.payload.query, "sourdough");
    assert.equal(lastRequest.payload.limit, 50);
    assert.equal(res.peer_search.available, true);
    assert.equal(res.peer_search.hits.length, 2);
    assert.equal(res.peer_search.hits[1].title, "", "an untitled hit must survive the relay, not be dropped");
  });

  it("relays available=false when no swarm is running, rather than throwing", async () => {
    setBridge({
      get helloOk() {
        return true;
      },
      get connected() {
        return true;
      },
      request: () =>
        Promise.resolve({
          type: "PEER_SEARCH_RESULT",
          payload: { query: "x", available: false, hits: [] },
        }),
      post: () => true,
    });

    const res = await handle({ type: "PEER_SEARCH", payload: { query: "x" } }, {});
    assert.equal(res.peer_search.available, false);
    assert.deepEqual(res.peer_search.hits, []);
  });

  it("throws when the daemon is not connected", async () => {
    setBridge({
      get helloOk() {
        return false;
      },
      get connected() {
        return false;
      },
      request: () => {
        throw new Error("must not call the daemon while disconnected");
      },
      post: () => true,
    });
    await assert.rejects(() => handle({ type: "PEER_SEARCH", payload: { query: "x" } }, {}));
  });
});
