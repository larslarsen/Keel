// SPDX-License-Identifier: Apache-2.0
/**
 * Port-to-search routing (WO-095 §4).
 *
 * The properties here are the ones that are invisible when they break: an event
 * delivered to the wrong page leaks one surface's activity to another, and an
 * event dropped because its route was installed too late leaves a bar that
 * never starts and no error anywhere.
 */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { createSearchSessions } from "../extension/background/search_sessions.js";

/** A fake runtime Port with the two listener hooks the module uses. */
function fakePort(name = "p") {
  const listeners = { message: [], disconnect: [] };
  return {
    name,
    posted: [],
    onMessage: { addListener: (fn) => listeners.message.push(fn) },
    onDisconnect: { addListener: (fn) => listeners.disconnect.push(fn) },
    postMessage(msg) {
      this.posted.push(msg);
    },
    send(msg) {
      for (const fn of listeners.message) fn(msg);
    },
    disconnect() {
      for (const fn of listeners.disconnect) fn();
    },
  };
}

function event(type, searchId, extra = {}) {
  return { type, payload: { search_id: searchId, seq: 1, ...extra } };
}

describe("search event routing", () => {
  it("delivers an event only to the port that claimed its search id", () => {
    const s = createSearchSessions();
    const a = fakePort("a");
    const b = fakePort("b");
    s.register(a);
    s.register(b);
    a.send({ type: "CLAIM_SEARCH", search_id: "search-a" });
    b.send({ type: "CLAIM_SEARCH", search_id: "search-b" });

    assert.equal(s.deliver(event("PEER_SEARCH_PROGRESS", "search-a")), true);
    assert.equal(a.posted.length, 1);
    assert.equal(
      b.posted.length,
      0,
      "a page received another page's search activity — this is a live feed of " +
        "what someone else is looking for"
    );
  });

  it("drops an event for a search nobody is rendering instead of broadcasting it", () => {
    const s = createSearchSessions();
    const a = fakePort("a");
    s.register(a);
    a.send({ type: "CLAIM_SEARCH", search_id: "mine" });

    // Consumed (returns true, so the caller does not fall through to the
    // owner-wide broadcast) but delivered nowhere.
    assert.equal(s.deliver(event("PEER_SEARCH_PROGRESS", "someone-elses")), true);
    assert.equal(a.posted.length, 0);
  });

  it("is not interested in envelopes that are not search events", () => {
    const s = createSearchSessions();
    assert.equal(
      s.deliver({ type: "CONTRIBUTION_STATUS", payload: {} }),
      false,
      "a non-search envelope must fall through to the owner-wide broadcast"
    );
    assert.equal(s.deliver(null), false);
  });

  it("releases the previous search when a port claims a new one", () => {
    const s = createSearchSessions();
    const a = fakePort("a");
    s.register(a);
    a.send({ type: "CLAIM_SEARCH", search_id: "first" });
    a.send({ type: "CLAIM_SEARCH", search_id: "second" });

    assert.equal(s.has("first"), false, "a replaced search kept its route");
    assert.equal(s.has("second"), true);

    // A late event from the replaced job goes nowhere, so it cannot be applied
    // as if it belonged to the current search.
    s.deliver(event("PEER_SEARCH_PROGRESS", "first"));
    assert.equal(a.posted.length, 0);
  });

  it("releases a search id on its terminal event", () => {
    const s = createSearchSessions();
    const a = fakePort("a");
    s.register(a);
    a.send({ type: "CLAIM_SEARCH", search_id: "done-soon" });

    s.deliver(event("PEER_SEARCH_COMPLETE", "done-soon", { reason: "complete" }));
    assert.equal(a.posted.length, 1);
    assert.equal(s.has("done-soon"), false, "a finished search kept its route");
  });

  it("cancels orphaned searches when a port disconnects", () => {
    const orphaned = [];
    const s = createSearchSessions({ onOrphan: (id) => orphaned.push(id) });
    const a = fakePort("a");
    s.register(a);
    a.send({ type: "CLAIM_SEARCH", search_id: "abandoned" });

    a.disconnect();
    assert.deepEqual(
      orphaned,
      ["abandoned"],
      "a page that went away left a job running on the daemon for nobody"
    );
    assert.equal(s.has("abandoned"), false);
  });

  it("survives a port that throws on postMessage", () => {
    const s = createSearchSessions();
    const a = fakePort("a");
    a.postMessage = () => {
      throw new Error("port closed");
    };
    s.register(a);
    a.send({ type: "CLAIM_SEARCH", search_id: "doomed" });

    assert.doesNotThrow(() => s.deliver(event("PEER_SEARCH_PROGRESS", "doomed")));
    assert.equal(s.has("doomed"), false, "a dead port kept its route");
  });

  it("ignores malformed port messages", () => {
    const s = createSearchSessions();
    const a = fakePort("a");
    s.register(a);
    assert.doesNotThrow(() => {
      a.send(null);
      a.send({ type: "CLAIM_SEARCH" });
      a.send({ type: "CLAIM_SEARCH", search_id: 42 });
    });
    assert.equal(s.size, 0);
  });
});
