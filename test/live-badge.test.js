// SPDX-License-Identifier: Apache-2.0
/**
 * WO-066 regression: non-live video whose text contains "live" must NOT be
 * flagged LIVE. Only a genuine YouTube LIVE broadcast badge counts.
 *
 * Fix landed in:
 *  - extension/content/extract.js  (removed loose liveLoose overlay match)
 *  - extension/content/extract_yt.js (lockup badge label + thumbnail overlay
 *    now require standalone LIVE and reject replay/chat/stream variants)
 */
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { parseHTML } from "linkedom";
import {
  extractBadges,
  extractFromYtInitialData,
} from "../extension/content/extract.js";

// Minimal card shaped like a YouTube rich/compact card so extractBadges' badge
// and overlay selectors resolve.
const CARD = `
  <div class="badge-card">
    <span class="badge">__BADGE__</span>
    <ytd-thumbnail-overlay-time-status-renderer id="time-status">__OVERLAY__</ytd-thumbnail-overlay-time-status-renderer>
  </div>`;

function badgesFor(badgeText, overlayText) {
  const html = CARD.replace("__BADGE__", badgeText).replace(
    "__OVERLAY__",
    overlayText
  );
  const { document } = parseHTML(
    `<!DOCTYPE html><html><body>${html}</body></html>`
  );
  return extractBadges(document.querySelector(".badge-card"));
}

describe("WO-066: live detection precision (DOM cards)", () => {
  it("flags a genuine standalone LIVE broadcast badge", () => {
    const b = badgesFor("LIVE", "");
    assert.ok(b.includes("LIVE"), `expected LIVE, got ${JSON.stringify(b)}`);
  });

  it("does NOT flag a non-live VOD whose overlay text contains 'live'", () => {
    // Observed case: a day-trading stream VOD whose title/description carried
    // "livestream" — never a broadcast badge.
    const b = badgesFor("", "livestream replay of the day trading session");
    assert.ok(
      !b.includes("LIVE"),
      `non-live 'livestream' overlay must not flag LIVE, got ${JSON.stringify(b)}`
    );
  });

  it("does NOT flag 'Live chat replay' thumbnail overlay", () => {
    const b = badgesFor("", "Live chat replay");
    assert.ok(
      !b.includes("LIVE"),
      `replay overlay must not flag LIVE, got ${JSON.stringify(b)}`
    );
  });

  it("does NOT flag a 'LIVE replay' thumbnail overlay", () => {
    const b = badgesFor("", "LIVE replay");
    assert.ok(
      !b.includes("LIVE"),
      `'LIVE replay' overlay must not flag LIVE, got ${JSON.stringify(b)}`
    );
  });

  it("still flags standalone LIVE in a badge even with extra text", () => {
    const b = badgesFor("● LIVE", "");
    assert.ok(b.includes("LIVE"), `expected LIVE, got ${JSON.stringify(b)}`);
  });
});

// A valid ytInitialData lockup mirroring the real fixture shape
// (test/fixtures/yt_initial_watch.json). LIVE detection for lockups reads the
// thumbnail overlay badge (extract_yt.js fieldsFromLockup overlay loop), so the
// badge label goes in thumbnailOverlayBadgeViewModel.thumbnailBadges[0].
function lockup(videoId, badgeLabel) {
  const overlayBadge = badgeLabel
    ? {
        thumbnailOverlayBadgeViewModel: {
          thumbnailBadges: [
            { thumbnailBadgeViewModel: { text: badgeLabel } },
          ],
        },
      }
    : null;
  const overlays = overlayBadge ? [overlayBadge] : [];
  return {
    lockupViewModel: {
      contentId: videoId,
      contentImage: {
        thumbnailViewModel: { overlays },
      },
      metadata: {
        lockupMetadataViewModel: {
          title: { content: `Stream ${videoId}` },
          image: {
            decoratedAvatarViewModel: {
              rendererContext: {
                commandContext: {
                  onTap: {
                    innertubeCommand: {
                      browseEndpoint: { browseId: "UC" + "a".repeat(22) },
                    },
                  },
                },
              },
            },
          },
          metadata: {
            contentMetadataViewModel: {
              metadataRows: [
                { metadataParts: [{ text: { content: "Some Channel" } }] },
              ],
            },
          },
        },
      },
    },
  };
}

const CTX = {
  page_load_id: "00000000-0000-4000-8000-000000000000",
  observed_at: 1_700_000_000_000,
  surface: "WATCH_NEXT",
  context_video_id: null,
  context_query_hash: null,
};

describe("WO-066: ytInitialData live badge", () => {
  it("flags a genuine LIVE broadcast, not a LIVE replay / livestream label", () => {
    const data = {
      secondaryResults: {
        results: [
          { itemSectionRenderer: { contents: [lockup("aZb3KpQ9LmN", "LIVE")] } },
          {
            itemSectionRenderer: {
              contents: [lockup("bYc4LqR0MnO", "LIVE replay")],
            },
          },
          {
            itemSectionRenderer: {
              contents: [lockup("cXd5MrS1NoP", "LIVESTREAM")],
            },
          },
        ],
      },
    };
    const { impressions } = extractFromYtInitialData(data, CTX);
    const live = impressions.filter((i) => i.badges.includes("LIVE"));
    assert.equal(
      live.length,
      1,
      `exactly one genuine LIVE broadcast expected, got ${JSON.stringify(
        impressions.map((i) => [i.video_id, i.badges])
      )}`
    );
    assert.equal(live[0].video_id, "aZb3KpQ9LmN");
    // replay + livestream labels must still extract, just without LIVE
    assert.equal(impressions.length, 3);
  });
});
