// SPDX-License-Identifier: Apache-2.0
/**
 * Fixture-driven extract tests — WATCH_NEXT + HOME (WO-010).
 */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseHTML } from "linkedom";

import {
  extractFromContainer,
  extractFromElement,
  extractFromYtInitialData,
  parseYtInitialDataFromDom,
  parseDuration,
  parseViewCount,
  videoIdFromHref,
  surfaceFromUrl,
  extractBalancedObject,
  CARD_SEL,
} from "../extension/content/extract.js";
import { validateImpression } from "../extension/lib/protocol.js";

const __dirname = dirname(fileURLToPath(import.meta.url));
const fixtures = join(__dirname, "fixtures");

/** Reject sequential / repeated-char fabricated IDs. */
function looksFabricatedVideoId(id) {
  if (!/^[A-Za-z0-9_-]{11}$/.test(id)) return true;
  if (/^(.)\1{10}$/.test(id)) return true; // aaaaaaaaaaa
  if (/^(.)\1{4,}/.test(id) && new Set(id).size <= 2) return true;
  if (/^(?:abc|test|lockup|mixed|json|video|aaaa|bbbb|cccc)/i.test(id)) {
    return true;
  }
  // purely sequential digits/letters (e.g. 12345678901, abcdefghijk)
  if (/^(?:0123456789a|abcdefghijk|lockup\d+)/i.test(id)) return true;
  return false;
}

function looksFabricatedChannelId(id) {
  if (!/^UC[A-Za-z0-9_-]{22}$/.test(id)) return true;
  const body = id.slice(2);
  if (/(.)\1{5,}/.test(body)) return true; // long runs
  if (/lockup|mixed|json|test|aaaa|bbbb|cccc|abcdefghij/i.test(body)) {
    return true;
  }
  return false;
}

function loadHtml(name) {
  const html = readFileSync(join(fixtures, name), "utf8");
  const { document } = parseHTML(
    `<!DOCTYPE html><html><body>${html}</body></html>`
  );
  return document;
}

function ctx(extra = {}) {
  return {
    page_load_id: "11111111-1111-4111-8111-111111111111",
    observed_at: 1_700_000_000_000,
    surface: "WATCH_NEXT",
    context_video_id: "contextvid01",
    context_query_hash: null,
    ...extra,
  };
}

describe("parse helpers", () => {
  it("parseDuration", () => {
    assert.equal(parseDuration("3:32"), 212);
    assert.equal(parseDuration("1:02:03"), 3723);
    assert.equal(parseDuration("LIVE"), null);
  });

  it("parseViewCount", () => {
    assert.equal(parseViewCount("1.5B views"), 1_500_000_000);
    assert.equal(parseViewCount("No views"), 0);
  });

  it("videoIdFromHref", () => {
    assert.equal(videoIdFromHref("/watch?v=dQw4w9WgXcQ"), "dQw4w9WgXcQ");
  });

  it("surfaceFromUrl", () => {
    assert.equal(
      surfaceFromUrl("https://www.youtube.com/watch?v=contextvid01").surface,
      "WATCH_NEXT"
    );
    assert.deepEqual(surfaceFromUrl("https://www.youtube.com/"), {
      surface: "HOME",
      context_video_id: null,
    });
    assert.deepEqual(surfaceFromUrl("https://www.youtube.com"), {
      surface: "HOME",
      context_video_id: null,
    });
    // Off-surface — idle (WO-010 §3)
    assert.equal(
      surfaceFromUrl("https://www.youtube.com/feed/subscriptions").surface,
      null
    );
    assert.equal(
      surfaceFromUrl("https://www.youtube.com/@somechannel").surface,
      null
    );
    assert.equal(
      surfaceFromUrl("https://www.youtube.com/results?search_query=x").surface,
      null
    );
  });

  it("extractBalancedObject", () => {
    const text = 'var x = {"a":"}","b":{"c":1}};';
    const obj = extractBalancedObject(text, text.indexOf("{"));
    assert.deepEqual(JSON.parse(obj), { a: "}", b: { c: 1 } });
  });
});

describe("fixture authenticity", () => {
  it("rejects synthetic-looking video/channel IDs in fixtures", () => {
    // Active surfaces: WATCH_NEXT + HOME. search_* remain unguarded until SEARCH.
    const files = readdirSync(fixtures).filter(
      (f) =>
        /^(watch_next_|home_|yt_initial_)/.test(f) &&
        (f.endsWith(".html") || f.endsWith(".json"))
    );
    assert.ok(files.length > 0, "no active fixtures");
    assert.ok(
      files.some((f) => f.startsWith("home_")),
      "home_* fixture required (WO-010)"
    );
    const bad = [];
    for (const name of files.filter((f) => f.endsWith(".html"))) {
      const text = readFileSync(join(fixtures, name), "utf8");
      for (const m of text.matchAll(/[?&]v=([A-Za-z0-9_-]{11})/g)) {
        if (looksFabricatedVideoId(m[1])) bad.push(`${name}: video ${m[1]}`);
      }
      for (const m of text.matchAll(/\/channel\/(UC[A-Za-z0-9_-]{22})/g)) {
        if (looksFabricatedChannelId(m[1])) {
          bad.push(`${name}: channel ${m[1]}`);
        }
      }
    }
    const jsonName = "yt_initial_watch.json";
    if (files.includes(jsonName)) {
      const data = JSON.parse(readFileSync(join(fixtures, jsonName), "utf8"));
      const stack = [data];
      while (stack.length) {
        const node = stack.pop();
        if (!node || typeof node !== "object") continue;
        if (Array.isArray(node)) {
          for (const x of node) stack.push(x);
          continue;
        }
        for (const [k, v] of Object.entries(node)) {
          if (typeof v === "string") {
            if (
              (k === "videoId" || k === "contentId") &&
              looksFabricatedVideoId(v)
            ) {
              bad.push(`${jsonName}: ${k}=${v}`);
            }
            if (
              k === "browseId" &&
              v.startsWith("UC") &&
              looksFabricatedChannelId(v)
            ) {
              bad.push(`${jsonName}: browseId=${v}`);
            }
          } else if (v && typeof v === "object") stack.push(v);
        }
      }
    }
    assert.deepEqual(bad, []);
  });
});

describe("WATCH_NEXT fixtures (must find cards)", () => {
  const known = [
    "watch_next_compact.html",
    "watch_next_lockup.html",
    "watch_next_mixed.html",
  ];

  for (const name of known) {
    it(`${name} extracts ≥1 impression and fails loudly on empty`, () => {
      const doc = loadHtml(name);
      const cards = doc.querySelectorAll(CARD_SEL);
      assert.ok(
        cards.length > 0,
        `${name}: fixture has no ${CARD_SEL} nodes — fixture is broken`
      );
      const { impressions, failures, candidates } = extractFromContainer(
        doc,
        ctx()
      );
      assert.ok(
        candidates > 0,
        `${name}: candidates=0 but fixture has cards — selector drift`
      );
      assert.ok(
        impressions.length > 0,
        `${name}: extracted 0 impressions from ${candidates} candidates (${failures} failures) — parser broken`
      );
      for (const imp of impressions) {
        const v = validateImpression(imp);
        assert.equal(v.ok, true, v.errors?.join("; "));
        assert.equal(imp.surface, "WATCH_NEXT");
      }
    });
  }

  it("slot_index preserves unfiltered position (gap on middle failure)", () => {
    const doc = loadHtml("watch_next_compact.html");
    const { impressions, failures } = extractFromContainer(doc, ctx());
    assert.ok(failures >= 1);
    assert.equal(impressions[0].video_id, "dQw4w9WgXcQ");
    assert.equal(impressions[0].slot_index, 0);
    // Middle card failed → third visual card is slot 2
    assert.equal(impressions[1].video_id, "jNQXAC9IVRw");
    assert.equal(impressions[1].slot_index, 2);
  });

  it("compact cards carry the channel display name", () => {
    const doc = loadHtml("watch_next_compact.html");
    const { impressions } = extractFromContainer(doc, ctx());
    const rick = impressions.find((i) => i.video_id === "dQw4w9WgXcQ");
    assert.equal(rick.channel_name, "Rick Astley");
    for (const imp of impressions) {
      assert.equal(validateImpression(imp).ok, true, imp.video_id);
      assert.ok(
        imp.channel_name === null ||
          (typeof imp.channel_name === "string" && imp.channel_name.length > 0),
        "channel_name must be a non-empty string or null"
      );
    }
  });

  it("lockup with no channel anchor still extracts (channel_id null)", () => {
    const doc = loadHtml("watch_next_lockup.html");
    // Fixture must encode the live finding: no channel anchors in markup.
    const html = readFileSync(join(fixtures, "watch_next_lockup.html"), "utf8");
    assert.equal(
      /\/channel\/UC|\/@[\w.-]+/.test(html),
      false,
      "lockup fixture must not contain channel anchors"
    );
    const { impressions, failures, candidates } = extractFromContainer(
      doc,
      ctx()
    );
    assert.equal(failures, 0, "missing channel must not count as failure");
    // Assert against the fixture rather than a literal — a captured fixture
    // gets recaptured, and a hardcoded count makes that a test edit.
    assert.ok(candidates > 0, "fixture has no cards — selector drift");
    assert.equal(
      impressions.length,
      candidates,
      "every card in a captured fixture must extract"
    );
    for (const imp of impressions) {
      assert.equal(imp.channel_id, null);
      assert.equal(imp.channel_unknown, true);
      assert.equal(validateImpression(imp).ok, true);
    }
    // slot_index is the measurement: contiguous, in order, from zero.
    assert.deepEqual(
      impressions.map((i) => i.slot_index),
      impressions.map((_, n) => n)
    );
    // Real cards carry a duration badge and a view count. Both shapes must
    // survive extraction from a logged-out capture.
    assert.ok(
      impressions.some((i) => i.duration_s > 0),
      "no duration parsed — badge markup drifted"
    );
    assert.ok(
      impressions.some((i) => i.view_count > 0),
      "no view count parsed — metadata row markup drifted"
    );
    // One card is a genuine LIVE broadcast (Portland Andy, OUcYyd82BuQ) — the
    // only logged-in-origin video in the fixture, spliced in to satisfy the
    // LIVE-badge shape (WO-050 §Verify). Its channel anchor is stripped so the
    // no-anchor invariant holds; the channel name is read from a metadata row.
    const live = impressions.find((i) => i.badges.includes("LIVE"));
    assert.ok(live, "fixture should include a live card");
    assert.equal(live.video_id, "OUcYyd82BuQ");
    assert.equal(live.channel_name, "Portland Andy");
    assert.equal(live.channel_id, null);
    // Names come from the first icon-less metadata row, not anchors — the
    // fixture's whole point is that lockup cards carry no channel links.
    assert.ok(
      impressions.some((i) => i.channel_name),
      "lockup cards must carry a channel display name from metadata rows"
    );
    const first = impressions.find((i) => i.video_id === "BkJN4QOAqN8");
    assert.equal(first.channel_name, "Chill Mind Deep");
  });

  it("mixed compact + lockup", () => {
    const doc = loadHtml("watch_next_mixed.html");
    const { impressions } = extractFromContainer(doc, ctx());
    assert.equal(impressions.length, 3);
    assert.deepEqual(
      impressions.map((i) => i.slot_index),
      [0, 1, 2]
    );
    // lockup in the middle has no channel
    assert.equal(impressions[1].channel_unknown, true);
    assert.equal(impressions[1].channel_id, null);
  });
});

describe("ytInitialData", () => {
  it("real lockupViewModel capture: 20 unique + browseId channels", () => {
    const data = JSON.parse(
      readFileSync(join(fixtures, "yt_initial_watch.json"), "utf8")
    );
    const { impressions, candidates, failures } = extractFromYtInitialData(
      data,
      ctx()
    );
    // Logged-out secondaryResults: 20 lockupViewModels (each listed once).
    assert.equal(candidates, 20);
    assert.equal(failures, 0);
    assert.equal(impressions.length, 20);
    assert.equal(impressions[0].slot_index, 0);
    assert.equal(impressions[19].slot_index, 19);
    // Known first contentId from the 2026-08-04 logged-out capture
    assert.equal(impressions[0].video_id, "BkJN4QOAqN8");
    assert.equal(
      impressions[0].title,
      "Avicii, Dua Lipa, Coldplay, Martin Garrix & Kygo, The Chainsmokers Style - Summer Vibes #13"
    );
    const withCh = impressions.filter((i) => i.channel_id && !i.channel_unknown);
    assert.equal(
      withCh.length,
      20,
      `expected browseId on all lockups, got ${withCh.length}`
    );
    assert.equal(impressions[0].channel_id, "UCOSCW_h0Zy-A_5L8ui1THfQ");
    assert.match(impressions[0].channel_id, /^UC[\w-]{22}$/);
    // Display name read off the card's metadata row (channel-name-in-panel).
    assert.equal(impressions[0].channel_name, "Chill Mind Deep");
    assert.ok(
      impressions.every(
        (i) =>
          i.channel_name === null ||
          (typeof i.channel_name === "string" && i.channel_name.length > 0)
      ),
      "channel_name must be a non-empty string or null"
    );
    for (const imp of impressions) {
      assert.equal(validateImpression(imp).ok, true, imp.video_id);
      assert.equal(imp.channel_unknown, false);
    }
  });

  it("parse from script text only", () => {
    const payload = { contents: { hello: true } };
    const { document } = parseHTML(
      `<html><head><script>var ytInitialData = ${JSON.stringify(
        payload
      )};</script></head><body></body></html>`
    );
    assert.deepEqual(parseYtInitialDataFromDom(document), payload);
  });
});

describe("HOME fixtures", () => {
  function homeCtx(extra = {}) {
    return {
      page_load_id: "22222222-2222-4222-8222-222222222222",
      observed_at: 1_700_000_000_000,
      surface: "HOME",
      context_video_id: null,
      context_query_hash: null,
      ...extra,
    };
  }

  /** Outer page grid #contents — same node observer.js containerHome() hands extract. */
  function outerGridContents(doc) {
    const nodes = [
      ...doc.querySelectorAll("ytd-rich-grid-renderer > #contents"),
    ];
    // First in tree order is the page grid; later ones are shelf nests.
    assert.ok(nodes.length >= 2, "fixture must nest a shelf grid for regression");
    return nodes[0];
  }

  it("home_grid.html extracts videos; section consumes a slot", () => {
    const doc = loadHtml("home_grid.html");
    const { impressions, failures, candidates } = extractFromContainer(
      doc,
      homeCtx()
    );
    // 5 rich-items + 1 section = 6 grid units
    assert.equal(candidates, 6, "grid children should be 6 (5 items + section)");
    assert.equal(failures, 0);
    assert.equal(impressions.length, 5);
    assert.equal(impressions[0].surface, "HOME");
    assert.equal(impressions[0].context_video_id, null);
    // Section at index 2 → video slots 0,1,3,4,5
    assert.deepEqual(
      impressions.map((i) => i.slot_index),
      [0, 1, 3, 4, 5]
    );
    assert.equal(impressions[0].video_id, "BkJN4QOAqN8");
    assert.equal(impressions[4].video_id, "TMBN8hj5Jy0");
    for (const imp of impressions) {
      const v = validateImpression(imp);
      assert.equal(v.ok, true, v.errors?.join("; "));
      assert.equal(imp.surface, "HOME");
      assert.equal(imp.context_video_id, null);
    }
  });

  it("candidates equals outer #contents direct children (nested shelf regression)", () => {
    // Live failure: observer passes ytd-rich-grid-renderer #contents; extract
    // used to querySelector a nested shelf grid first → candidates=3, imps=0.
    const doc = loadHtml("home_grid.html");
    const root = outerGridContents(doc);
    const direct = [...root.children].filter((n) => n.nodeType === 1);
    assert.equal(direct.length, 6, "fixture outer grid has 6 direct children");

    const nested = root.querySelector(
      "ytd-rich-section-renderer ytd-rich-grid-renderer > #contents"
    );
    assert.ok(nested, "fixture must include nested shelf grid");
    assert.equal(
      [...nested.children].filter((n) => n.nodeType === 1).length,
      3,
      "nested shelf has 3 children (the wrong-root trap)"
    );

    const { impressions, failures, candidates } = extractFromContainer(
      root,
      homeCtx()
    );
    assert.equal(
      candidates,
      direct.length,
      `candidates (${candidates}) must equal direct children (${direct.length}); ` +
        "fewer means extract descended into a shelf nested grid"
    );
    assert.notEqual(
      candidates,
      3,
      "candidates=3 is the live-QA failure (nested shelf only)"
    );
    assert.equal(failures, 0);
    assert.equal(impressions.length, 5);
    assert.deepEqual(
      impressions.map((i) => i.slot_index),
      [0, 1, 3, 4, 5]
    );
  });

  it("HOME impressions validate without context_video_id", () => {
    const doc = loadHtml("home_grid.html");
    const { impressions } = extractFromContainer(doc, homeCtx());
    assert.ok(impressions.length >= 1);
    const v = validateImpression(impressions[0]);
    assert.equal(v.ok, true, v.errors?.join("; "));
  });
});

describe("purity / channel_id", () => {
  it("null when incomplete", () => {
    const { document } = parseHTML(
      `<div id="c"><a id="thumbnail" href="/watch?v=zzzzzzzzzzz"></a></div>`
    );
    assert.equal(
      extractFromElement(document.querySelector("#c"), {
        ...ctx(),
        slot_index: 0,
      }),
      null
    );
  });

  it("compact without channel link yields impression + channel_unknown", () => {
    const { document } = parseHTML(`
      <ytd-compact-video-renderer>
        <a id="thumbnail" href="/watch?v=zzzzzzzzzz1"></a>
        <a id="video-title" title="T">T</a>
        <ytd-channel-name>Only Text No Link</ytd-channel-name>
      </ytd-compact-video-renderer>`);
    const { impressions, failures } = extractFromContainer(document, ctx());
    assert.equal(failures, 0);
    assert.equal(impressions.length, 1);
    assert.equal(impressions[0].channel_id, null);
    assert.equal(impressions[0].channel_unknown, true);
  });
});
