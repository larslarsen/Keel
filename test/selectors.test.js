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

import { extractFromContainer } from "../extension/content/extract.js";
import {
  DEFAULT_SELECTORS,
  validateSelectorConfig,
} from "../extension/lib/selectors.js";

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
