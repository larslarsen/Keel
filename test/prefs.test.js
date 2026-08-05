// SPDX-License-Identifier: Apache-2.0
/** Hide preference helpers + channel id validation. */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
  DEFAULT_HIDE_MODE,
  coerceHideMode,
  isChannelId,
  isHideMode,
  normalizeBlocklist,
  shouldHide,
} from "../extension/lib/prefs.js";

describe("prefs / hide_recommendations (WO-017)", () => {
  it("default is on", () => {
    assert.equal(DEFAULT_HIDE_MODE, "on");
  });

  it("isHideMode accepts on/off only", () => {
    assert.equal(isHideMode("on"), true);
    assert.equal(isHideMode("off"), true);
    assert.equal(isHideMode("never"), false);
    assert.equal(isHideMode("with-panel"), false);
    assert.equal(isHideMode("always"), false);
    assert.equal(isHideMode(null), false);
  });

  it("coerceHideMode migrates legacy three-state", () => {
    assert.equal(coerceHideMode("never"), "off");
    assert.equal(coerceHideMode("with-panel"), "on");
    assert.equal(coerceHideMode("always"), "on");
    assert.equal(coerceHideMode("on"), "on");
    assert.equal(coerceHideMode("off"), "off");
    assert.equal(coerceHideMode("bogus"), "on");
    assert.equal(coerceHideMode(undefined), "on");
  });

  it("shouldHide: on/off gated by panelOpen", () => {
    assert.equal(shouldHide("on", true), true);
    assert.equal(shouldHide("on", false), false);
    assert.equal(shouldHide("on"), false);
    assert.equal(shouldHide("off", true), false);
    assert.equal(shouldHide("off", false), false);
  });

  it("shouldHide: legacy values via coerce, gated by panelOpen", () => {
    assert.equal(shouldHide("never", true), false);
    assert.equal(shouldHide("with-panel", true), true);
    assert.equal(shouldHide("with-panel", false), false);
    assert.equal(shouldHide("always", true), true);
    assert.equal(shouldHide("always", false), false);
  });
});

describe("prefs / channel id validation (WO-016)", () => {
  const good = "UCdy1IW4I7DnkU_3v0zMWDpQ";
  const good2 = "UC4QobU6STFB0P71PMvOGN5A";

  it("isChannelId", () => {
    assert.equal(isChannelId(good), true);
    assert.equal(isChannelId("UC" + "a".repeat(22)), true);
    assert.equal(isChannelId("UCshort"), false);
    assert.equal(isChannelId("@handle"), false);
    assert.equal(isChannelId(null), false);
  });

  it("normalizeBlocklist dedupes and drops junk", () => {
    assert.deepEqual(normalizeBlocklist([good, good, "nope", good2, 3]), [
      good,
      good2,
    ]);
    assert.deepEqual(normalizeBlocklist(null), []);
  });
});
