# WO-013 — `channel_id` is missing for everything past the initial rail

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | **Done — 2026-08-03** (no DOM source; gap made visible) |
| **Date** | 2026-08-03 |
| **Source** | Live QA after WO-012; measured against the corpus |

Not a regression. A boundary in what the page gives us, which nobody had measured until now.

---

## Measured

One watch page, scrolled:

| Slots | Rows | `channel_unknown` |
|---|---|---|
| 0–19 | 20 | **0** |
| 20–59 | 40 | **40 (100%)** |

The first ~20 cards always resolve a channel. Everything loaded by scrolling never does. Overall
that page was **67% unknown**, and the ratio gets worse the more the user scrolls.

## Why

`observer.js` builds `channelByVideo` once per navigation, from `ytInitialData` — which contains only
the cards present at first paint. Cards past that arrive from YouTube's continuation requests, and:

- We do not intercept `fetch`/XHR and never will (`DESIGN_v2.md` §4.1, hard rule).
- We never call the Data API (§3.2, hard rule).
- Live `yt-lockup-view-model` cards carry **no channel anchor and no `UC…` string anywhere in their
  markup** — established in WO-005 and re-confirmed since. There is nothing in the DOM to read.

So for scrolled cards there is currently no legitimate source for `channel_id`.

## Why it matters

`slot_index` and `video_id` are correct for every row, so the core measurement is intact. But
channel-level analysis over this corpus is silently biased: it would only ever see the top of each
rail. Anyone querying "which channels get recommended after channel X" gets an answer skewed toward
first-paint positions, with no indication that two thirds of the data was excluded.

## Investigation results (step 1) — 2026-08-03

### Q1: Does YouTube write continuation payloads anywhere readable in the DOM?

**No (for our purposes).**

Evidence:

1. **`parseYtInitialDataFromDom`** only reads inline `<script>` text containing `ytInitialData`. That
   blob is first navigation paint. Continuation is not appended to those scripts; YouTube loads more
   rail items via innertube `/youtubei/v1/next` (see chip `apiUrl` in the same initial JSON) — network
   we do not observe.
2. **Captured `yt_initial_watch.json`:** 20 unique `contentId`s with matching `browseId` UC values
   (40 browseId occurrences, walk order duplicates). That is exactly the first-paint enrichment set
   `channelByVideo` is built from — not an unbounded scroll history.
3. No second `ytInitialData` mutation path exists in the content script; re-running the parser after
   scroll would re-read the same first-paint script nodes.

### Q2: Do later lockup cards ever carry a channel anchor?

**No — same shape as first-paint DOM lockups.**

Evidence:

1. **WO-005** live capture + fixture authenticity: `watch_next_lockup.html` has no `/channel/UC` and
   no `/@…` channel hrefs. Channel name is plain text; avatar is a button with `aria-label` only.
2. Continuation cards use the same `yt-lockup-view-model` component. DOM extract
   (`readLockupFields` / channel href walk) returns `channel_id: null` for that markup; enrichment
   only succeeds when `channelByVideo` already has the video from first-paint JSON.
3. Live corpus measurement in this WO (slots 20–59 → 100% unknown) matches “DOM has no channel +
   map miss”, not a separate renderer we failed to parse.

**Conclusion:** no legitimate ISOLATED-world source for scrolled `channel_id`. Step 2 only — do not
invent a workaround.

## Implementation (step 2)

| Surface | Change |
|---|---|
| Export header | `channel_unknown_count`, `channel_known_count` next to `row_count` |
| `STATS` | `channel_unknown`, `channel_known` |
| SidePanel Counts | Live known/unknown line under the tiles + permanent first-paint caveat |
| `DESIGN_v2.md` §4.2 | Limitation + rationale (hard rules cited) |

No network interception, no Data API, no display-name → channel_id. Rows without channel still emit.

---

## Acceptance

- [x] Both investigation questions answered with evidence, recorded above.
- [x] No legitimate DOM source — gap surfaced (export header, panel, design note).
- [x] No network interception, no Data API, no display-name fallback.
- [x] Daemon tests assert export channel counts (6/6 on 12-row seed).

## Pushback invited

If the investigation shows the continuation data is reachable but only through something that skirts
§4.1, say so and stop. The hard rule wins; a 67% gap that is documented is better than complete data
obtained by a method that breaks the project's own terms.

**Recorded:** continuation is only on the network path. Hard rule stands.
