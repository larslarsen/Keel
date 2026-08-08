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
