// SPDX-License-Identifier: Apache-2.0
/**
 * WO-107's selector-source boundary.
 *
 * These tests deliberately exercise the object the observer uses, rather than
 * validating independent JSON values. A syntactically valid config from the
 * wrong platform is still unsafe at this boundary.
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
  pick,
  validateSelectorConfig,
} from "../extension/lib/selectors.js";
import { SELECTORS_TT } from "../extension/lib/selectors_tt.js";
import {
  applyDaemonSelectorReply,
  initialSelectorsForHref,
} from "../extension/content/selector_source.js";

const here = dirname(fileURLToPath(import.meta.url));
const fixtures = join(here, "fixtures");

function daemonReply(selectors) {
  return { ok: true, selectors: { platform: selectors.platform, selectors } };
}

describe("platform-correct bundled selector fallback (WO-107)", () => {
  it("chooses each page's bundle before any daemon response", () => {
    const tt = initialSelectorsForHref("https://www.tiktok.com/explore");
    const yt = initialSelectorsForHref("https://www.youtube.com/watch?v=aaaaaaaaaaa");
    const other = initialSelectorsForHref("https://example.com/");
    assert.equal(tt.platform, "tt");
    assert.equal(tt.selectors, SELECTORS_TT);
    assert.equal(yt.platform, "yt");
    assert.equal(yt.selectors, DEFAULT_SELECTORS);
    assert.deepEqual(other, { platform: null, selectors: null });
    assert.ok(validateSelectorConfig(tt.selectors));
    assert.ok(validateSelectorConfig(yt.selectors));
  });

  it("retains TikTok's bundle for missing, malformed, and YouTube daemon replies", () => {
    const current = initialSelectorsForHref("https://www.tiktok.com/explore").selectors;
    const malformed = { version: 1, platform: "tt" };
    for (const reply of [undefined, { ok: false }, daemonReply(malformed), daemonReply(DEFAULT_SELECTORS)]) {
      const next = applyDaemonSelectorReply(current, "tt", reply);
      assert.equal(next.selectors, SELECTORS_TT);
      assert.equal(next.source, "bundled");
    }
  });

  it("retains YouTube's bundle for a valid TikTok daemon reply", () => {
    const current = initialSelectorsForHref("https://www.youtube.com/").selectors;
    const next = applyDaemonSelectorReply(current, "yt", daemonReply(SELECTORS_TT));
    assert.equal(next.selectors, DEFAULT_SELECTORS);
    assert.equal(next.source, "bundled");
    assert.match(next.reason, /platform tt, page is yt/);
  });

  it("accepts a validated matching daemon config", () => {
    const override = structuredClone(SELECTORS_TT);
    override.containers.explore = ["[data-e2e=\"daemon-explore\"]"];
    const next = applyDaemonSelectorReply(SELECTORS_TT, "tt", daemonReply(override));
    assert.equal(next.source, "daemon");
    assert.equal(next.selectors, override);
  });

  it("keeps the checked-in TikTok bundle semantically closed against the daemon embed", () => {
    const daemon = JSON.parse(
      readFileSync(join(here, "..", "daemon", "selectors_tt.json"), "utf8"),
    );
    assert.deepEqual(SELECTORS_TT, daemon);
  });

  it("extracts the current Explore fixture using only the bundled TikTok fallback", () => {
    const html = readFileSync(join(fixtures, "tiktok_explore.html"), "utf8");
    const { document } = parseHTML(`<!doctype html><html><body>${html}</body></html>`);
    const initial = initialSelectorsForHref("https://www.tiktok.com/explore");
    const root = pick(document, initial.selectors.containers.explore);
    assert.ok(root, "the bundled tt config finds the Explore root");
    const out = extractFromContainer(root, {
      page_load_id: "11111111-1111-4111-8111-111111111111",
      observed_at: 1_700_000_000_000,
      platform: "tt",
      surface: "EXPLORE",
      context_video_id: null,
    }, initial.selectors);
    assert.equal(out.impressions.length, 15);
    assert.ok(out.impressions.every((row) => row.platform === "tt" && row.surface === "EXPLORE"));
  });
});
