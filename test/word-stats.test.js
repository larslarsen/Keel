// SPDX-License-Identifier: Apache-2.0
/**
 * WO-068: WORD_STATS is a distinct RPC from SEARCH/PEER_SEARCH — display-only
 * corpus telemetry. The SW must relay the daemon payload unaltered.
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

describe("WORD_STATS (WO-068)", () => {
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
    await assert.rejects(() => handle({ type: "WORD_STATS", payload: { query: "  " } }, {}));
  });

  it("relays word bars and nested token coverage", async () => {
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
          type: "WORD_STATS_RESULT",
          payload: {
            distinct_words: 1200,
            distinct_graphs: 400,
            peers: 2,
            available: true,
            words: [
              {
                word: "trading",
                pct: 12.5,
                tokens: [{ token_index: 7, estimate: 40, known: true }],
              },
            ],
          },
        });
      },
      post: () => true,
    });

    const res = await handle({ type: "WORD_STATS", payload: { query: "trading" } }, {});

    assert.equal(lastRequest.type, "WORD_STATS");
    assert.equal(lastRequest.payload.query, "trading");
    assert.equal(res.word_stats.available, true);
    assert.equal(res.word_stats.distinct_words, 1200);
    assert.equal(res.word_stats.words[0].word, "trading");
    assert.equal(res.word_stats.words[0].pct, 12.5);
    assert.equal(res.word_stats.words[0].tokens[0].token_index, 7);
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
    await assert.rejects(() => handle({ type: "WORD_STATS", payload: { query: "x" } }, {}));
  });
});
