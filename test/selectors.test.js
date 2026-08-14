// SPDX-License-Identifier: Apache-2.0
/**
 * Selector configuration — WO-056 (DESIGN_BOOTSTRAP "Option B").
 *
 * Two things are worth testing and they are different. That extraction follows
 * the config, which is the payoff: YouTube renames something, the daemon ships
 * a new selector, and the extension binary does not change. And that the
 * validator refuses anything that is not plainly data, which is the store
 * commitment: the extension may download data, never logic.
 */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseHTML } from "linkedom";

import {
  extractFromContainer,
  extractLiveSightings,
  mutationRecordsRelevant,
  observeOptions,
} from "../extension/content/extract.js";
import {
  DEFAULT_SELECTORS,
  pick,
  validateSelectorConfig,
} from "../extension/lib/selectors.js";
import {
  validateLiveSighting,
  validateLiveSightingList,
} from "../extension/lib/protocol.js";

const fixtures = join(dirname(fileURLToPath(import.meta.url)), "fixtures");

function loadHtml(name) {
  const html = readFileSync(join(fixtures, name), "utf8");
  const { document } = parseHTML(
    `<!DOCTYPE html><html><body>${html}</body></html>`
  );
  return document;
}

function ctx() {
  return {
    page_load_id: "11111111-1111-4111-8111-111111111111",
    observed_at: 1_700_000_000_000,
    surface: "WATCH_NEXT",
    context_video_id: "dQw4w9WgXcQ",
  };
}

/** Deep copy so a test cannot mutate the frozen default. */
function cloneDefault() {
  return JSON.parse(JSON.stringify(DEFAULT_SELECTORS));
}

describe("selector config drives extraction", () => {
  it("a renamed card element breaks extraction, and config alone repairs it", () => {
    // Baseline: the shipped config reads the fixture.
    const before = extractFromContainer(loadHtml("watch_next_compact.html").body, ctx());
    assert.ok(before.impressions.length > 0, "fixture yields nothing to begin with");

    // YouTube renames the card element. Nothing else about the page changes.
    const renamed = loadHtml("watch_next_compact.html");
    for (const el of [...renamed.querySelectorAll("ytd-compact-video-renderer")]) {
      const swap = renamed.createElement("ytd-brand-new-card");
      swap.innerHTML = el.innerHTML;
      el.replaceWith(swap);
    }

    const broken = extractFromContainer(renamed.body, ctx());
    assert.equal(
      broken.impressions.length,
      0,
      "renaming the card should break the shipped config — otherwise this test proves nothing"
    );

    // The daemon ships a config naming the new element. No extension change.
    const fixed = cloneDefault();
    fixed.cards = "ytd-brand-new-card";
    fixed.shapes.compact.match = "ytd-brand-new-card";

    const repaired = extractFromContainer(renamed.body, ctx(), fixed);
    assert.equal(
      repaired.impressions.length,
      before.impressions.length,
      "a config change should recover exactly what the rename broke"
    );
    assert.deepEqual(
      repaired.impressions.map((i) => i.video_id),
      before.impressions.map((i) => i.video_id)
    );
  });

  it("a field selector can be redirected without touching the engine", () => {
    const doc = loadHtml("watch_next_compact.html");
    // Move every title into an element the shipped config does not know.
    for (const t of [...doc.querySelectorAll("#video-title")]) {
      t.setAttribute("id", "renamed-title");
    }
    const cfg = cloneDefault();
    cfg.shapes.compact.title = ["#renamed-title"];

    const out = extractFromContainer(doc.body, ctx(), cfg);
    assert.ok(out.impressions.length > 0);
    assert.ok(
      out.impressions.every((i) => i.title && i.title.length > 0),
      "titles should be read from wherever the config points"
    );
  });
});

describe("validation keeps config to data", () => {
  it("accepts the shipped default", () => {
    assert.ok(validateSelectorConfig(cloneDefault()));
  });

  it("refuses anything that is not a selector", () => {
    for (const bad of [
      "javascript:alert(1)",
      "https://example.com/payload.js",
      "() => document.cookie",
      "<script>x</script>",
      "a[href] { }",
    ]) {
      const cfg = cloneDefault();
      cfg.shapes.compact.title = [bad];
      assert.equal(
        validateSelectorConfig(cfg),
        null,
        `should have refused ${JSON.stringify(bad)}`
      );
    }
  });

  it("refuses an unknown key rather than taking the rest", () => {
    const cfg = cloneDefault();
    cfg.shapes.compact.onExtract = "a";
    assert.equal(validateSelectorConfig(cfg), null);
  });

  it("refuses a config missing a shape the engine reads", () => {
    const cfg = cloneDefault();
    delete cfg.shapes.lockup;
    assert.equal(validateSelectorConfig(cfg), null);
  });

  it("refuses an unknown version", () => {
    const cfg = cloneDefault();
    cfg.version = 2;
    assert.equal(validateSelectorConfig(cfg), null);
  });

  it("refuses a malformed CSS selector", () => {
    const cfg = cloneDefault();
    cfg.cards = "ytd-card[[[";
    assert.equal(validateSelectorConfig(cfg), null);
  });
});

describe("containers and renderer keys are config too", () => {
  it("the child combinator is a selector, not an expression", () => {
    const cfg = cloneDefault();
    cfg.cards = "ytd-rich-grid-renderer > #contents";
    assert.ok(validateSelectorConfig(cfg), "`>` must be allowed");

    cfg.cards = "() => fetch('/x')";
    assert.equal(validateSelectorConfig(cfg), null, "`=>` must still be refused");
  });

  it("refuses a renderer key that is not a plain identifier", () => {
    for (const bad of ["a.b", "key with space", "__proto__.x", "a[0]", ""]) {
      const cfg = cloneDefault();
      cfg.rendererKeys = [bad];
      assert.equal(
        validateSelectorConfig(cfg),
        null,
        `should have refused ${JSON.stringify(bad)}`
      );
    }
  });

  it("refuses a config with no container chain for a surface", () => {
    const cfg = cloneDefault();
    delete cfg.containers.home;
    assert.equal(validateSelectorConfig(cfg), null);
  });
});

describe("vocabulary is data, patterns are not", () => {
  it("a wording change is a config change", async () => {
    const { parseAge, parseViewCount } = await import(
      "../extension/content/extract.js"
    );

    // What ships today.
    assert.equal(parseAge("Streamed 2 weeks ago"), "2 weeks ago");
    assert.equal(parseViewCount("1.2K views"), 1200);

    // YouTube starts saying something else. Only words change.
    const cfg = cloneDefault();
    cfg.vocabulary.ago = ["previously"];
    cfg.vocabulary.views = ["plays"];

    assert.equal(parseAge("2 weeks previously", cfg), "2 weeks previously");
    assert.equal(parseViewCount("1.2K plays", cfg), 1200);
    assert.equal(
      parseAge("2 weeks ago", cfg),
      null,
      "the old wording should stop matching once config replaces it"
    );
  });

  it("magnitudes come from config, in order", async () => {
    const { parseViewCount } = await import("../extension/content/extract.js");
    assert.equal(parseViewCount("3M views"), 3_000_000);
    const cfg = cloneDefault();
    cfg.vocabulary.magnitudes = ["t"]; // a locale with one suffix meaning 10^3
    assert.equal(parseViewCount("3T views", cfg), 3000);
  });

  it("a token carrying regex syntax is refused, and escaped if it ever arrived", async () => {
    const { escapeToken } = await import("../extension/lib/selectors.js");
    const cfg = cloneDefault();
    cfg.vocabulary.live = ["(?:.*)"];
    assert.equal(
      validateSelectorConfig(cfg),
      null,
      "a token that is really a pattern must be refused"
    );
    // Belt and braces: even a token that passed validation is escaped at use.
    assert.equal(escapeToken("a.*b"), "a\\.\\*b");
  });
});

describe("a second platform runs on the same engine (WO-057)", () => {
  /**
   * Fixture rebuilt 2026-08-11 from live FYP capture (WO-063): xgwrapper player
   * id, data-e2e fields, empty virtualized shell. Proves engine + shipped
   * selectors_tt.json against real markup shape (ids fabricated for the test).
   */
  const tiktok = JSON.parse(
    readFileSync(join(fixtures, "..", "..", "daemon", "selectors_tt.json"), "utf8")
  );

  it("the shipped TikTok config is valid", () => {
    assert.ok(validateSelectorConfig(tiktok));
  });

  it("rejects a partial TikTok configuration rather than disabling a surface", () => {
    const partial = structuredClone(tiktok);
    delete partial.containers.liveRoom;
    assert.equal(validateSelectorConfig(partial), null);
    const missingEvidence = structuredClone(tiktok);
    delete missingEvidence.live.active;
    assert.equal(validateSelectorConfig(missingEvidence), null);
  });

  it("extracts clips using only the TikTok config", () => {
    const doc = loadHtml("tiktok_feed.html");
    const out = extractFromContainer(doc.body, {
      page_load_id: "11111111-1111-4111-8111-111111111111",
      observed_at: 1_700_000_000_000,
      platform: "tt",
      surface: "WATCH_NEXT",
      context_video_id: "7300000000000000000",
    }, tiktok);

    assert.equal(out.impressions.length, 3, "three clips in the fixture");
    assert.deepEqual(
      out.impressions.map((i) => i.video_id),
      [
        "7300000000000000001",
        "7300000000000000002",
        "7300000000000000003",
      ]
    );
    assert.ok(
      out.impressions.every((i) => i.platform === "tt"),
      "every record must carry the platform it came from"
    );
    assert.ok(
      out.impressions.every((i) => i.title && i.title.length > 0),
      "titles come from the configured field"
    );
    assert.ok(
      out.impressions[2].badges.includes("LIVE"),
      "the live room should be badged"
    );
    assert.deepEqual(out.impressions[0].hashtags, ["sourdough"]);
    assert.equal(out.impressions[0].sound_id, "7300000000000000099");
    assert.equal(out.impressions[0].channel_id, "@firstcreator");
    assert.equal(out.impressions[1].sound_id, "7300000000000000098");
    // Empty virtualized shell must not produce a row.
    assert.equal(out.impressions.length, 3);
  });

  it("the YouTube config finds nothing in a TikTok page", () => {
    const doc = loadHtml("tiktok_feed.html");
    const out = extractFromContainer(doc.body, {
      page_load_id: "11111111-1111-4111-8111-111111111111",
      observed_at: 1,
      platform: "tt",
      surface: "WATCH_NEXT",
      context_video_id: "7300000000000000000",
    });
    assert.equal(
      out.impressions.length,
      0,
      "platforms must not silently extract each other's pages"
    );
  });

  it("walks Explore and Following containers without collapsing their surfaces", () => {
    const doc = loadHtml("tiktok_discovery.html");
    const base = { page_load_id: "11111111-1111-4111-8111-111111111111", observed_at: Date.now(), platform: "tt", context_video_id: null };
    const explore = extractFromContainer(doc.querySelector('[data-e2e="explore-list"]'), { ...base, surface: "EXPLORE" }, tiktok);
    const following = extractFromContainer(doc.querySelector('[data-e2e="following-feed"]'), { ...base, surface: "FOLLOWING" }, tiktok);
    assert.deepEqual(explore.impressions.map((x) => [x.surface, x.slot_index]), [["EXPLORE", 0], ["EXPLORE", 2]]);
    assert.deepEqual(following.impressions.map((x) => x.surface), ["FOLLOWING"]);
  });

  it("emits all four Live-wall cards through the real captured ancestor shape", () => {
    const doc = loadHtml("tiktok_live_wall.html");
    const ctx = { page_load_id: "11111111-1111-4111-8111-111111111111", observed_at: Date.now(), platform: "tt", surface: "LIVE" };
    const wall = extractLiveSightings(doc.querySelector("main#tiktok-live-main-container-id"), ctx, tiktok);
    assert.deepEqual(wall.map((x) => [x.live_locator, x.slot_index]), [
      ["@alpha.live/live", 0], ["@bravo.live/live", 1], ["@charlie.live/live", 2], ["@delta.live/live", 3],
    ]);
    assert.deepEqual(wall.map((x) => x.title), [
      "Sanitized first Live title", "Sanitized second Live title", "Sanitized third Live title", "Sanitized fourth Live title",
    ]);
  });

  it("does not enable a Live room from the former synthetic fixture", () => {
    const rooms = loadHtml("tiktok_live_room.html");
    const room = extractLiveSightings(rooms.querySelector("main"), { ...ctx(), platform: "tt", surface: "LIVE_ROOM" }, tiktok);
    assert.equal(room.length, 0, "the old synthetic room still lacks the room-scoped playing video");
  });
});

describe("interactive TikTok Explore and Live-room captures (WO-104)", () => {
  const tiktok = JSON.parse(
    readFileSync(join(fixtures, "..", "..", "daemon", "selectors_tt.json"), "utf8")
  );

  function liveCtx(extra = {}) {
    return {
      page_load_id: "11111111-1111-4111-8111-111111111111",
      observed_at: 1_700_000_000_000,
      platform: "tt",
      surface: "LIVE_ROOM",
      context_video_id: null,
      live_locator: "@creator001/live",
      ...extra,
    };
  }

  it("the shipped TikTok config remains bounded CSS data after the room selectors", () => {
    assert.ok(validateSelectorConfig(tiktok));
    const executable = structuredClone(tiktok);
    executable.live.onExtract = "() => fetch('/x')";
    assert.equal(validateSelectorConfig(executable), null);
  });

  it("Explore's real root yields 15/15 impressions with titles from the configured field", () => {
    const doc = loadHtml("tiktok_explore.html");
    assert.ok(doc.querySelector(tiktok.containers.explore[0]), "public Explore root is first");
    const root = pick(doc, tiktok.containers.explore);
    const out = extractFromContainer(root, {
      page_load_id: "11111111-1111-4111-8111-111111111111",
      observed_at: 1_700_000_000_000,
      platform: "tt",
      surface: "EXPLORE",
      context_video_id: null,
    }, tiktok);
    assert.equal(out.candidates, 15);
    assert.equal(out.failures, 0);
    assert.equal(out.impressions.length, 15);
    assert.deepEqual(
      out.impressions.map((i) => i.slot_index),
      [...Array(15).keys()],
    );
    assert.ok(out.impressions.every((i) => i.surface === "EXPLORE"));
    const counts = [...doc.querySelectorAll("[data-e2e='explore-card-like-container'] span")]
      .map((n) => (n.textContent || "").trim());
    assert.equal(counts.length, 15);
    assert.ok(
      out.impressions.every((i) => i.title === "SANITIZED"),
      "titles come from the configured image alternative, not the like overlay",
    );
    assert.ok(
      out.impressions.every((i) => !counts.includes(i.title)),
      "no extracted title is a like-count placeholder",
    );
    const names = [...doc.querySelectorAll("[data-e2e='explore-card-user-unique-id']")]
      .map((n) => (n.textContent || "").trim());
    assert.equal(names.length, 15);
    assert.deepEqual(
      out.impressions.map((i) => i.channel_id),
      Array.from({ length: 15 }, (_, i) => `@creator${String(i + 1).padStart(3, "0")}`),
    );
    assert.deepEqual(out.impressions.map((i) => i.channel_name), names);
    assert.ok(
      out.impressions.every((i) => !counts.includes(i.channel_name)),
      "no creator name is a like-count placeholder",
    );
  });

  it("does not index a Following creator wall or its hover preview", () => {
    const { document } = parseHTML(`<!DOCTYPE html><html><body>
      <section data-e2e="following-feed">
        <article class="creator-card">
          <a href="/@alice">@alice</a>
          <button>Follow</button>
          <video></video>
        </article>
        <article class="creator-card">
          <a href="/@bob">@bob</a>
          <button>Follow</button>
        </article>
      </section>
    </body></html>`);
    const out = extractFromContainer(document.querySelector("[data-e2e=following-feed]"), {
      page_load_id: "11111111-1111-4111-8111-111111111111",
      observed_at: 1,
      platform: "tt",
      surface: "FOLLOWING",
      context_video_id: null,
    }, tiktok);
    assert.equal(out.candidates, 0);
    assert.equal(out.impressions.length, 0);
  });

  it("active room yields exactly one titleless LIVE_ROOM sighting from the header", () => {
    const doc = loadHtml("tiktok_live_room_active.html");
    const root = pick(doc, tiktok.containers.liveRoom);
    const out = extractLiveSightings(root, liveCtx(), tiktok);
    assert.equal(out.length, 1);
    assert.equal(out[0].slot_index, 0);
    assert.equal(out[0].surface, "LIVE_ROOM");
    assert.equal(out[0].live_locator, "@creator001/live");
    assert.equal(out[0].channel_id, "@creator001");
    assert.equal(out[0].channel_name, "SANITIZED_TEXT_62");
    assert.equal(out[0].title, "");
    assert.ok(validateLiveSighting(out[0], 2).ok);
    assert.equal(validateLiveSighting(out[0], 1).ok, false);
    assert.ok(
      !out.some((s) => s.title && s.title.startsWith("SANITIZED_TEXT_")),
      "replacement-card titles must not label the current room",
    );
  });

  it("removing the room-scoped video or playing state yields none", () => {
    const goneVideo = loadHtml("tiktok_live_room_active.html");
    goneVideo.querySelectorAll("video").forEach((n) => n.remove());
    assert.equal(
      extractLiveSightings(pick(goneVideo, tiktok.containers.liveRoom), liveCtx(), tiktok).length,
      0,
    );

    const gonePlaying = loadHtml("tiktok_live_room_active.html");
    for (const n of gonePlaying.querySelectorAll(".xgplayer-playing")) {
      n.classList.remove("xgplayer-playing");
    }
    assert.equal(
      extractLiveSightings(pick(gonePlaying, tiktok.containers.liveRoom), liveCtx(), tiktok).length,
      0,
    );
  });

  it("missing, malformed, or mismatched header identity yields none", () => {
    const missing = loadHtml("tiktok_live_room_active.html");
    missing.querySelectorAll("[data-e2e='live-header-container'] a[href^='/@']").forEach((n) => n.remove());
    assert.equal(extractLiveSightings(pick(missing, tiktok.containers.liveRoom), liveCtx(), tiktok).length, 0);

    const malformed = loadHtml("tiktok_live_room_active.html");
    malformed.querySelectorAll("[data-e2e='live-header-container'] a[href^='/@']").forEach((n) => {
      n.setAttribute("href", "/not-a-handle");
    });
    assert.equal(extractLiveSightings(pick(malformed, tiktok.containers.liveRoom), liveCtx(), tiktok).length, 0);

    const nameless = loadHtml("tiktok_live_room_active.html");
    nameless.querySelectorAll("[data-e2e='room-header-anchor-name']").forEach((n) => {
      n.textContent = "";
    });
    assert.equal(extractLiveSightings(pick(nameless, tiktok.containers.liveRoom), liveCtx(), tiktok).length, 0);
  });

  it("the inactive countdown fixture yields none despite its replacement cards", () => {
    const doc = loadHtml("tiktok_live_room_inactive.html");
    assert.equal(doc.querySelectorAll("[data-e2e='discover_category-list-live-card']").length, 42);
    assert.equal(doc.querySelectorAll("video").length, 0);
    assert.equal(doc.querySelectorAll(".xgplayer-playing").length, 0);
    const out = extractLiveSightings(pick(doc, tiktok.containers.liveRoom), liveCtx(), tiktok);
    assert.equal(out.length, 0);
  });

  it("active-room replacement cards do not produce sibling sightings", () => {
    const doc = loadHtml("tiktok_live_room_active.html");
    for (const a of doc.querySelectorAll("[data-e2e='discover_category-list-live-card'] a[href$='/live']")) {
      a.setAttribute("href", "/@other.live/live");
    }
    const out = extractLiveSightings(pick(doc, tiktok.containers.liveRoom), liveCtx(), tiktok);
    assert.equal(out.length, 1);
    assert.equal(out[0].live_locator, "@creator001/live");
    assert.equal(out[0].title, "");
  });

  it("LIVE wall cards still require and retain their real titles", () => {
    const doc = loadHtml("tiktok_live_wall.html");
    const wall = extractLiveSightings(
      doc.querySelector("main#tiktok-live-main-container-id"),
      { ...liveCtx(), surface: "LIVE" },
      tiktok,
    );
    assert.equal(wall.length, 4);
    assert.ok(wall.every((s) => s.title && s.title.length > 0));
    assert.ok(validateLiveSightingList(wall, 2).ok);
    assert.ok(validateLiveSightingList(wall.map((s) => ({ ...s, title: "" })), 2).ok === false);
  });

  it("SPA replacement cannot pair one creator header with another room's player", () => {
    const leftover = loadHtml("tiktok_live_room_inactive.html");
    leftover.querySelector("[data-e2e='live-header-container'] a[href^='/@']")
      .setAttribute("href", "/@alice.room");
    leftover.querySelector("[data-e2e='room-header-anchor-name']").textContent = "Alice";
    assert.equal(
      extractLiveSightings(
        pick(leftover, tiktok.containers.liveRoom),
        liveCtx({ live_locator: "@alice.room/live" }),
        tiktok,
      ).length,
      0,
      "countdown page must not emit the departing room",
    );

    const next = loadHtml("tiktok_live_room_active.html");
    assert.equal(
      extractLiveSightings(
        pick(next, tiktok.containers.liveRoom),
        liveCtx({ live_locator: "@alice.room/live" }),
        tiktok,
      ).length,
      0,
      "new player with the old route emits nothing",
    );

    for (const a of next.querySelectorAll("[data-e2e='live-header-container'] a[href^='/@']")) {
      a.setAttribute("href", "/@bob.room");
    }
    next.querySelector("[data-e2e='room-header-anchor-name']").textContent = "Bob";
    assert.equal(
      extractLiveSightings(
        pick(next, tiktok.containers.liveRoom),
        liveCtx({ live_locator: "@alice.room/live" }),
        tiktok,
      ).length,
      0,
      "old route with the new header/player emits nothing",
    );
    assert.equal(
      extractLiveSightings(
        pick(next, tiktok.containers.liveRoom),
        liveCtx({ live_locator: "@bob.room/live" }),
        tiktok,
      ).length,
      1,
      "matching route, header and player emit the new room",
    );
  });

  it("replacement-card badges never cross onto the current room", () => {
    const doc = loadHtml("tiktok_live_room_active.html");
    const card = doc.querySelector("[data-e2e='discover_category-list-live-card']");
    const badge = doc.createElement("span");
    badge.className = "Badge";
    badge.textContent = "Verified";
    card.prepend(badge);
    const out = extractLiveSightings(pick(doc, tiktok.containers.liveRoom), liveCtx(), tiktok);
    assert.equal(out.length, 1);
    assert.deepEqual(out[0].badges, []);
  });

  it("schedules a room scan when the player subtree arrives or later gains playing state", () => {
    assert.deepEqual(observeOptions("LIVE_ROOM").attributeFilter, ["class"]);
    assert.equal(observeOptions("LIVE").attributes, undefined);

    const inactive = loadHtml("tiktok_live_room_inactive.html");
    const active = loadHtml("tiktok_live_room_active.html");
    const dest = inactive.querySelector("[data-e2e='live-room-content']");
    const player = active.querySelector(".xgplayer-playing").cloneNode(true);
    assert.ok(player);

    assert.equal(
      extractLiveSightings(pick(inactive, tiktok.containers.liveRoom), liveCtx(), tiktok).length,
      0,
    );
    assert.equal(
      mutationRecordsRelevant([{ type: "childList", addedNodes: [player] }], tiktok),
      true,
      "inserting the already-playing host must schedule without a subtree walk",
    );
    dest.append(player);
    assert.equal(
      extractLiveSightings(pick(inactive, tiktok.containers.liveRoom), liveCtx(), tiktok).length,
      1,
    );

    const late = loadHtml("tiktok_live_room_inactive.html");
    const lateDest = late.querySelector("[data-e2e='live-room-content']");
    const latePlayer = active.querySelector(".xgplayer-playing").cloneNode(true);
    latePlayer.classList.remove("xgplayer-playing");
    lateDest.append(latePlayer);
    assert.equal(
      extractLiveSightings(pick(late, tiktok.containers.liveRoom), liveCtx(), tiktok).length,
      0,
    );
    assert.equal(
      mutationRecordsRelevant(
        [{ type: "childList", addedNodes: [latePlayer] }],
        tiktok,
      ),
      false,
      "a video wrapper without playing state is not the active predicate",
    );
    latePlayer.classList.add("xgplayer-playing");
    assert.equal(
      mutationRecordsRelevant(
        [{ type: "attributes", attributeName: "class", target: latePlayer }],
        tiktok,
      ),
      true,
      "gaining xgplayer-playing after insertion must schedule",
    );
    assert.equal(
      extractLiveSightings(pick(late, tiktok.containers.liveRoom), liveCtx(), tiktok).length,
      1,
    );
  });
});
