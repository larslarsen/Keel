// SPDX-License-Identifier: Apache-2.0
/**
 * Unit tests for the three owners WO-083 extracted from `sw.js`.
 *
 * The point of the extraction was that each of these can now be constructed
 * from plain objects — no `globalThis.browser`, no imported service worker, no
 * native host. Every test below builds its subject directly, which is the
 * property being demonstrated as much as the behaviour.
 *
 * The behaviours chosen are the ones that were previously only reachable by
 * driving the whole service worker: the legacy hide-mode migration, the
 * three-variable panel-open bookkeeping WO-075 exists to fix, and the
 * dispatcher's gates — including the fall-through that once made every
 * thumbnail request return a consent value.
 */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { createPrefs } from "../extension/background/prefs.js";
import { createPanelContext } from "../extension/background/panel_context.js";
import { createRpcRouter } from "../extension/background/rpc.js";
import { createProofStore } from "../extension/background/page_proofs.js";

const YT_WATCH = "https://www.youtube.com/watch?v=aaaaaaaaaaa";
const YT_HOME = "https://www.youtube.com/";
const TT_FYP = "https://www.tiktok.com/";
const OTHER = "https://example.com/";

/** An in-memory storage.local stand-in. */
function fakeStorage(initial = {}) {
  const bag = { ...initial };
  return {
    bag,
    local: {
      get: async (key) => (key in bag ? { [key]: bag[key] } : {}),
      set: async (obj) => Object.assign(bag, obj),
    },
  };
}

describe("background/prefs.js — the control plane's one storage owner", () => {
  it("migrates a legacy three-state hide mode and persists the migration", async () => {
    const storage = fakeStorage({ hide_recommendations: "with-panel" });
    const prefs = createPrefs({ storage });
    assert.equal(await prefs.readHideMode(), "on");
    assert.equal(
      storage.bag.hide_recommendations,
      "on",
      "the migration must be written back, not recomputed on every read"
    );
  });

  it("defaults rather than throwing when storage is absent or broken", async () => {
    assert.equal(await createPrefs({}).readHideMode(), "on");
    const broken = {
      local: {
        get: async () => {
          throw new Error("quota");
        },
      },
    };
    assert.equal(await createPrefs({ storage: broken }).readHideMode(), "on");
  });

  it("throws on a failed write, where a failed read only defaults", async () => {
    // A setting the user chose and did not get has to be reportable; a missing
    // preference does not.
    await assert.rejects(
      () => createPrefs({}).writeHideMode("on"),
      /storage unavailable/
    );
  });

  it("coerces an unrecognised write to the default rather than rejecting it", async () => {
    // Pinning existing behaviour, not endorsing it: writeHideMode's
    // `isHideMode(coerceHideMode(v))` guard is unreachable, because coercion
    // already answers a valid mode for every input. So an unknown value is
    // stored as the default instead of being refused. WO-083 is a
    // behaviour-preserving split, so this is left as it is and recorded rather
    // than quietly changed — see the work order's boundary notes.
    const storage = fakeStorage();
    await createPrefs({ storage }).writeHideMode("sideways");
    assert.equal(storage.bag.hide_recommendations, "on");
  });

  it("round-trips consent and refuses anything but the two decisions", async () => {
    const storage = fakeStorage();
    const prefs = createPrefs({ storage });
    assert.equal(await prefs.readConsent(), null, "undecided reads as null");
    await prefs.writeConsent("granted");
    assert.equal(await prefs.readConsent(), "granted");
    // A junk value could not *grant* consent (consentGranted is strict), but it
    // could silently revoke one, so it is refused at the door.
    await assert.rejects(() => prefs.writeConsent("maybe"), /bad consent value/);
    assert.equal(await prefs.readConsent(), "granted");
  });
});

describe("background/panel_context.js — panel policy without a browser", () => {
  function makePanel({ tabs, sidePanel } = {}) {
    const broadcasts = [];
    const closes = [];
    const proofs = createProofStore();
    const panel = createPanelContext({
      tabs,
      sidePanel: sidePanel || {
        setOptions: async () => {},
        close: async (o) => closes.push(o),
      },
      windows: { update: async () => {} },
      runtime: { getURL: (p) => p },
      proofs,
      broadcast: (m) => broadcasts.push(m),
    });
    return { panel, broadcasts, closes, proofs };
  }

  it("gates the panel per platform (WO-071/074)", () => {
    const { panel } = makePanel();
    assert.equal(panel.panelAllowedFor(YT_WATCH), true);
    assert.equal(
      panel.panelAllowedFor(YT_HOME),
      false,
      "YouTube's feed is a feed, not a watch page"
    );
    assert.equal(
      panel.panelAllowedFor(TT_FYP),
      true,
      "on TikTok the FYP *is* the watch page"
    );
    assert.equal(panel.panelAllowedFor(OTHER), false);
    assert.equal(panel.panelAllowedFor(undefined), false);
  });

  it("counts a panel as open everywhere until its window handshakes (WO-075)", () => {
    const { panel } = makePanel();
    const port = makePort();
    panel.registerPanelPort(port);

    assert.equal(
      panel.panelOpen(7),
      true,
      "before the handshake the window is unknown, so the toggle must not assume it is elsewhere"
    );
    port.fire({ type: "PANEL_HANDSHAKE", payload: { windowId: 7 } });
    assert.equal(panel.panelOpen(7), true);
    assert.equal(
      panel.panelOpen(9),
      false,
      "once the window is known, another window's toggle must still open"
    );

    port.disconnect();
    assert.equal(panel.panelOpen(7), false);
    assert.equal(panel.panelOpen(), false);
  });

  it("does not double-decrement when an un-handshaked port disconnects", () => {
    // The bookkeeping is three variables that must agree; this is the path
    // where they came apart.
    const { panel } = makePanel();
    const a = makePort();
    const b = makePort();
    panel.registerPanelPort(a);
    panel.registerPanelPort(b);
    b.fire({ type: "PANEL_HANDSHAKE", payload: { windowId: 3 } });
    a.disconnect(); // never handshaked
    assert.equal(panel.panelOpen(3), true, "the surviving panel is still open");
    b.disconnect();
    assert.equal(panel.panelOpen(3), false);
    assert.equal(panel.panelOpen(), false);
  });

  it("offers only the ACTIVE tab's proof, never a background tab's (WO-080)", async () => {
    const tabs = {
      query: async ({ windowId }) => [{ id: 11, windowId: windowId ?? 1, url: YT_WATCH }],
    };
    const { panel, proofs } = makePanel({ tabs });
    proofs.observeContext({
      tabId: 11,
      windowId: 1,
      pageLoadId: "11111111-1111-4111-8111-111111111111",
      platform: "yt",
      surface: "WATCH_NEXT",
      focus: true,
      railGeneration: null,
    });
    proofs.observeContext({
      tabId: 22,
      windowId: 1,
      pageLoadId: "22222222-2222-4222-8222-222222222222",
      platform: "yt",
      surface: "WATCH_NEXT",
      focus: true,
      railGeneration: null,
    });

    const proof = await panel.activeProofForWindow(1);
    assert.equal(proof?.pageLoadId, "11111111-1111-4111-8111-111111111111");
    assert.equal(proof?.tabId, 11, "tab 22's proof must not be reachable from here");

    const ctx = await panel.contextForPanel(1);
    assert.equal(ctx.tab_id, 11);
    assert.equal(ctx.focus, true);
    assert.equal(ctx.platform, "yt");
  });

  it("closes the panel and broadcasts a defocused context when a tab leaves the gate", async () => {
    const { panel, broadcasts, closes } = makePanel();
    await panel.evalActivePanelContext({ id: 5, url: OTHER }, 2);
    assert.deepEqual(closes, [{ windowId: 2 }, { tabId: 5 }]);
    const ctx = broadcasts.at(-1);
    assert.equal(ctx.type, "PANEL_CONTEXT");
    assert.equal(ctx.payload.focus, false);
    assert.equal(ctx.payload.tab_id, 5);
  });

  it("focuses an already-open full-page tab instead of stacking duplicates", async () => {
    const updates = [];
    const created = [];
    const tabs = {
      query: async () => [{ id: 42, windowId: 1 }],
      update: async (id, o) => updates.push({ id, ...o }),
      create: async (o) => created.push(o),
    };
    const { panel } = makePanel({ tabs });
    await panel.openFullpageTab();
    assert.deepEqual(updates, [{ id: 42, active: true }]);
    assert.deepEqual(created, [], "an existing tab must be focused, not duplicated");
  });
});

describe("background/rpc.js — dispatch, validation and capability gates", () => {
  function makeRouter({
    caps = {},
    helloOk = true,
    reply,
    prefs,
    openConsentPage,
  } = {}) {
    const sent = [];
    const broadcasts = [];
    const siteBroadcasts = [];
    const opened = [];
    const bridge = {
      helloOk,
      lastHelloFailure: null,
      hasCapability: (n) => Boolean(caps[n]),
      request: async (type, payload) => {
        sent.push({ type, payload });
        return reply
          ? reply(type, payload)
          : { type: `${type}_RESULT`, payload: { echoed: type } };
      },
    };
    const proofs = createProofStore();
    const router = createRpcRouter({
      getBridge: () => bridge,
      proofs,
      prefs: prefs || createPrefs({ storage: fakeStorage() }),
      panel: {
        panelAllowedFor: () => true,
        panelOpen: () => false,
        activeProofForWindow: async () => null,
        contextForPanel: async () => ({ tab_id: null, focus: false }),
        disablePanelForTab: async () => {},
      },
      tabs: { update: async () => {} },
      broadcast: (m) => broadcasts.push(m),
      broadcastToSiteTabs: async (m) => siteBroadcasts.push(m),
      onHideModeChanged: async () => {},
      openConsentPage: openConsentPage || (() => opened.push(true)),
    });
    return { router, bridge, sent, broadcasts, siteBroadcasts, proofs, opened };
  }

  it("refuses an un-negotiated optional RPC with actionable copy, not a crash", async () => {
    const { router, sent } = makeRouter({ caps: {} });
    await assert.rejects(
      () => router.handle({ type: "PEER_SEARCH", payload: { query: "x" } }, {}),
      /peer search unavailable — desktop app update required/
    );
    assert.deepEqual(sent, [], "a gated RPC must never reach the daemon");
  });

  it("validates before it gates, so a bad query is a bad query at any capability", async () => {
    const { router } = makeRouter({ caps: { peer_search: 1 } });
    await assert.rejects(
      () => router.handle({ type: "PEER_SEARCH", payload: { query: "   " } }, {}),
      /bad query/
    );
  });

  it("rejects a malformed channel id before touching the daemon", async () => {
    const { router, sent } = makeRouter();
    await assert.rejects(
      () => router.handle({ type: "BLOCK_CHANNEL", payload: { channel_id: "nope" } }, {}),
      /bad channel_id/
    );
    assert.deepEqual(sent, []);
  });

  it("keeps THUMBNAIL a thumbnail — the fall-through defect, as a test", async () => {
    // THUMBNAIL once sat adjacent to GET_CONSENT and fell through to it, so
    // every thumbnail request returned a consent value and the panel rendered
    // blank boxes. A fall-through is valid JavaScript; only a test catches it.
    const { router } = makeRouter({
      reply: (type) => ({ type: "OK", payload: { data_url: `data:${type}` } }),
    });
    const r = await router.handle(
      { type: "THUMBNAIL", payload: { video_id: "v" } },
      {}
    );
    assert.equal(r.daemon.data_url, "data:THUMBNAIL");
    assert.equal(r.consent, undefined, "a thumbnail must not answer with consent");
  });

  it("buffers impressions while disconnected, bounded, and flushes on reconnect", async () => {
    const { router, bridge, sent, broadcasts } = makeRouter({ helloOk: false });
    const PID = "33333333-3333-4333-8333-333333333333";
    // A tab only owns a proof after it has announced its page (WO-080), and
    // impressions are matched against that proof's page_load_id.
    await router.handle(
      { type: "PAGE_CONTEXT", payload: { pageLoadId: PID, platform: "yt" } },
      { tab: { id: 1, windowId: 1, url: "https://www.youtube.com/watch?v=aaaaaaaaaaa" } }
    );
    const batch = (n) =>
      Array.from({ length: n }, (_, i) => ({
        page_load_id: PID,
        observed_at: 1,
        surface: "HOME",
        context_video_id: null,
        context_query_hash: null,
        slot_index: i,
        video_id: `vid${i}`,
        channel_id: null,
        channel_name: null,
        title: `t${i}`,
        duration_s: null,
        view_count: null,
        published_at: null,
        badges: [],
      }));

    for (let i = 0; i < 3; i++) {
      await router.handle(
        { type: "IMPRESSIONS", payload: { impressions: batch(100) } },
        { tab: { id: 1, windowId: 1 } }
      );
    }
    const status = await router.handle({ type: "GET_STATUS", payload: {} }, {});
    assert.equal(
      status.buffered,
      200,
      "the disconnected buffer is bounded at 200 (DESIGN_v2 §2.1)"
    );

    bridge.helloOk = true;
    router.flushBuffer();
    await new Promise((r) => setTimeout(r, 0));
    assert.equal(sent.at(-1)?.type, "IMPRESSIONS");
    const flushed = broadcasts.at(-1);
    assert.equal(flushed.type, "STORE_UPDATED");
    assert.equal(
      flushed.payload.proof,
      null,
      "a flush is a multi-tab batch and carries no tab's proof (WO-080)"
    );
  });

  it("derives Live proof from the sender and gates sibling-valid sightings by capability and level", async () => {
    const PID = "99999999-9999-4999-8999-999999999999";
    let live = false;
    const { router, sent } = makeRouter({
      caps: { live_sightings: 1 },
      reply: (type) => {
        if (type === "GET_CONTRIBUTION") {
          return { type: "CONTRIBUTION_RESULT", payload: { live, transition: "idle" } };
        }
        return { type: `${type}_RESULT`, payload: {} };
      },
    });
    const sender = { tab: { id: 7, windowId: 1, url: "https://www.tiktok.com/live" } };
    await router.handle({ type: "PAGE_CONTEXT", payload: { pageLoadId: PID, platform: "yt", surface: "HOME" } }, sender);
    const valid = {
      page_load_id: PID, observed_at: Date.now(), surface: "LIVE_ROOM", slot_index: 0,
      platform: "tt", live_locator: "@creator/live", channel_id: "@creator",
      channel_name: "Creator", title: "Live now", badges: ["LIVE"],
    };
    const first = await router.handle(
      { type: "LIVE_SIGHTINGS", payload: { sightings: [valid, { ...valid, live_locator: "/bad" }] } },
      sender,
    );
    assert.equal(first.result.dropped, 1, "Level 1 refuses Live even with the negotiated RPC");
    assert.equal(sent.some((x) => x.type === "LIVE_SIGHTINGS"), false);

    live = true;
    await router.handle({ type: "LIVE_SIGHTINGS", payload: { sightings: [valid] } }, sender);
    const publish = sent.find((x) => x.type === "LIVE_SIGHTINGS");
    assert.equal(publish.payload.sightings.length, 1, "one malformed sibling cannot poison a valid sighting");
    assert.equal(publish.payload.sightings[0].surface, "LIVE", "the sender URL, not the payload, owns the surface");
  });

  it("keeps one bounded tagged FIFO, preserves adjacent-kind order, and purges Live on downgrade", async () => {
    const PID = "88888888-8888-4888-8888-888888888888";
    const { router, bridge, sent } = makeRouter({
      helloOk: false,
      caps: { live_sightings: 1 },
      reply: (type) => type === "GET_CONTRIBUTION"
        ? { type: "CONTRIBUTION_RESULT", payload: { live: true, transition: "idle" } }
        : { type: `${type}_RESULT`, payload: {} },
    });
    const sender = { tab: { id: 8, windowId: 1, url: "https://www.tiktok.com/live" } };
    await router.handle({ type: "PAGE_CONTEXT", payload: { pageLoadId: PID } }, sender);
    const sighting = (n) => ({ page_load_id: PID, observed_at: Date.now(), surface: "LIVE", slot_index: n, platform: "tt", live_locator: `@live${n}/live`, channel_id: `@live${n}`, title: `Live ${n}`, badges: [] });
    const impression = { page_load_id: PID, observed_at: Date.now(), surface: "HOME", context_video_id: null, context_query_hash: null, slot_index: 0, video_id: "queuedvideo", channel_id: null, channel_name: null, title: "Queued impression", duration_s: null, view_count: null, published_at: null, badges: [] };
    await router.handle({ type: "LIVE_SIGHTINGS", payload: { sightings: [sighting(1)] } }, sender);
    await router.handle({ type: "IMPRESSIONS", payload: { impressions: [impression] } }, sender);
    await router.handle({ type: "LIVE_SIGHTINGS", payload: { sightings: [sighting(2)] } }, sender);
    assert.equal((await router.handle({ type: "GET_STATUS", payload: {} }, {})).buffered, 3);
    bridge.helloOk = true;
    router.flushBuffer();
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.deepEqual(
      sent.filter((x) => x.type === "LIVE_SIGHTINGS" || x.type === "IMPRESSIONS").map((x) => x.type),
      ["LIVE_SIGHTINGS", "IMPRESSIONS", "LIVE_SIGHTINGS"],
      "adjacent kinds group, but alternating evidence never reorders"
    );

    bridge.helloOk = false;
    for (let i = 0; i < 201; i++) {
      await router.handle({ type: "LIVE_SIGHTINGS", payload: { sightings: [sighting(i + 10)] } }, sender);
    }
    assert.equal((await router.handle({ type: "GET_STATUS", payload: {} }, {})).buffered, 200, "the cap is global across tagged records");
    router.onBridgeMessage({ type: "CONTRIBUTION_STATUS", payload: { live: false, transition: "idle" } });
    assert.equal((await router.handle({ type: "GET_STATUS", payload: {} }, {})).buffered, 0, "downgrade purges only queued Live evidence immediately");
  });

  it("will not let a page claim a proof slot it has no browser-attributed tab for", async () => {
    const { router, proofs } = makeRouter();
    await router.handle(
      {
        type: "PAGE_CONTEXT",
        payload: { pageLoadId: "44444444-4444-4444-8444-444444444444", platform: "yt" },
      },
      {} // no sender.tab
    );
    assert.equal(proofs.get(1), null);
    assert.equal(proofs.size?.() ?? 0, 0);
  });

  it("grants the daemon's disclosure before enabling local recording (WO-089)", async () => {
    const { router, sent, siteBroadcasts } = makeRouter({
      caps: { network_consent: 1 },
    });
    const r = await router.handle(
      { type: "SET_CONSENT", payload: { consent: "granted" } },
      {}
    );
    assert.equal(r.consent, "granted");
    // Daemon first. The reverse order would leave a window in which this
    // profile records against a disclosure the daemon has not acknowledged.
    assert.equal(sent[0]?.type, "SET_NETWORK_CONSENT");
    assert.equal(sent[0]?.payload?.accepted, true);
    assert.equal(
      sent[0]?.payload?.revision,
      2,
      "the client must name the disclosure revision it actually rendered"
    );
    assert.equal(siteBroadcasts.at(-1)?.type, "CONSENT_CHANGED");
  });

  it("does not enable recording when the daemon refuses the disclosure", async () => {
    const { router, siteBroadcasts } = makeRouter({
      caps: { network_consent: 1 },
      reply: (type) =>
        type === "SET_NETWORK_CONSENT"
          ? { type: "ERROR", payload: { message: "consent revision 1 is newer" } }
          : { type: "OK", payload: {} },
    });
    await assert.rejects(
      () => router.handle({ type: "SET_CONSENT", payload: { consent: "granted" } }, {}),
      /consent revision/
    );
    assert.deepEqual(
      siteBroadcasts,
      [],
      "a refused grant must not switch the observer on anyway"
    );
  });

  it("refuses to grant against a daemon that predates the consent gate", async () => {
    // An old daemon would start its network without acknowledging anything and
    // would run Live at the default level. Failing closed is the only honest
    // answer the extension can give.
    const { router, sent } = makeRouter({ caps: {} });
    await assert.rejects(
      () => router.handle({ type: "SET_CONSENT", payload: { consent: "granted" } }, {}),
      /consent unavailable — desktop app update required/
    );
    assert.deepEqual(sent, []);
  });

  it("re-asks an existing grant when the daemon still needs current consent", async () => {
    const opened = [];
    const { router, broadcasts } = makeRouter({
      caps: { network_consent: 1 },
      prefs: createPrefs({ storage: fakeStorage({ consent: "granted" }) }),
      openConsentPage: () => opened.push("consent"),
      reply: (type) =>
        type === "GET_NETWORK_CONSENT"
          ? { type: "NETWORK_CONSENT_RESULT", payload: { consent_required: true } }
          : { type: "OK", payload: {} },
    });
    router.onBridgeStatus(true, "", { capabilities: { network_consent: 1 } });
    for (let i = 0; i < 8; i++) await Promise.resolve();
    assert.deepEqual(opened, ["consent"]);
    assert.equal(
      broadcasts.some((m) => m.type === "NETWORK_CONSENT_STATUS" && m.payload?.consent_required),
      true,
      "extension pages may hear disclosure migration state under its own type"
    );
  });

  it("never presents network-consent payloads to content scripts as contribution state", async () => {
    const { router, siteBroadcasts } = makeRouter({
      caps: { network_consent: 1, contribution_runtime: 1 },
      reply: (type) => {
        if (type === "GET_NETWORK_CONSENT") {
          return { type: "NETWORK_CONSENT_RESULT", payload: { consent_required: true } };
        }
        if (type === "GET_CONTRIBUTION") {
          return { type: "CONTRIBUTION_RESULT", payload: { live: true, transition: "idle" } };
        }
        return { type: "OK", payload: {} };
      },
    });
    router.onBridgeStatus(true, "", { capabilities: { network_consent: 1, contribution_runtime: 1 } });
    for (let i = 0; i < 8; i++) await Promise.resolve();
    assert.deepEqual(
      siteBroadcasts.filter((m) => m.type === "CONTRIBUTION_STATUS").map((m) => m.payload),
      [{ live: true, transition: "idle" }],
      "an open Live observer must only receive effective policy state"
    );
    assert.equal(siteBroadcasts.some((m) => m.type === "NETWORK_CONSENT_STATUS"), false);
  });

  it("does not open a second consent tab for a first-run profile", async () => {
    // onInstalled already opened the screen. Connecting must not duplicate it.
    const opened = [];
    const { router } = makeRouter({
      caps: { network_consent: 1 },
      prefs: createPrefs({ storage: fakeStorage() }),
      openConsentPage: () => opened.push("consent"),
      reply: (type) =>
        type === "GET_NETWORK_CONSENT"
          ? { type: "NETWORK_CONSENT_RESULT", payload: { consent_required: true } }
          : { type: "OK", payload: {} },
    });
    router.onBridgeStatus(true, "", { capabilities: { network_consent: 1 } });
    for (let i = 0; i < 8; i++) await Promise.resolve();
    assert.deepEqual(opened, []);
  });

  it("lets a decline through without needing the daemon at all", async () => {
    // There is nothing to withdraw from a daemon that was never granted
    // anything, and declining must not fail because the desktop app is missing.
    const { router, sent, siteBroadcasts } = makeRouter({ helloOk: false });
    const r = await router.handle(
      { type: "SET_CONSENT", payload: { consent: "declined" } },
      {}
    );
    assert.equal(r.consent, "declined");
    assert.deepEqual(sent, []);
    assert.equal(siteBroadcasts.at(-1)?.payload?.consent, "declined");
    await assert.rejects(
      () => router.handle({ type: "SET_CONSENT", payload: { consent: "sure" } }, {}),
      /bad consent value/
    );
  });

  it("rejects an unknown type rather than answering something plausible", async () => {
    const { router } = makeRouter();
    await assert.rejects(
      () => router.handle({ type: "NOT_A_REAL_RPC" }, {}),
      /unknown type NOT_A_REAL_RPC/
    );
    await assert.rejects(() => router.handle(null, {}), /bad message/);
  });

  it("mirrors the negotiated capability map into DAEMON_STATUS and clears it on drop", () => {
    const { router, broadcasts } = makeRouter();
    router.onBridgeStatus(true, "ok", { capabilities: { core: 1, queue: 1 } });
    assert.deepEqual(broadcasts.at(-1).payload.capabilities, { core: 1, queue: 1 });
    router.onBridgeStatus(false, "not running");
    assert.deepEqual(
      broadcasts.at(-1).payload.capabilities,
      {},
      "a dropped connection has negotiated nothing"
    );
  });
});

/** A stand-in for a runtime port with the listener plumbing panels use. */
function makePort() {
  const msg = [];
  const gone = [];
  return {
    onMessage: { addListener: (fn) => msg.push(fn) },
    onDisconnect: { addListener: (fn) => gone.push(fn) },
    fire: (m) => msg.forEach((fn) => fn(m)),
    disconnect: () => gone.forEach((fn) => fn()),
  };
}
