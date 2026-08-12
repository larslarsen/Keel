# WO-063 — TikTok panel: the Mirror (no re-rank rails exist)

| | |
|---|---|
| **Addressee** | Engineer (Sr Dev lane) / reviewer |
| **Status** | **Built — video_id + extract + scroll history Mirror; dwell/engagement observers still thin** |
| **Date** | 2026-08-08 |
| **Source** | Lars, 2026-08-08 — "what features can we even offer on the tiktok panel?" Established scope: TikTok FYP serves one video at a time; there is no recommendation rail to read or re-rank, so the YouTube panel's core feature (re-rank the watch-next rail) does not transfer. The TikTok panel is a **mirror**, not a steering wheel. |
| **Depends on** | WO-057 (platform dimension + selectors wired; selectors still unverified against live TikTok). |

## The constraint that defines the scope

YouTube panel works because the watch-next rail is a *visible candidate list*:
`context_video → [recommended_video, …]` with creator/title/views. Keel builds a
co-view graph and re-ranks it. The candidate set is observable.

TikTok FYP serves one full-screen video on an infinite scroll. No sidebar, no
"up next" list, no visible candidate set. The ranker chooses the next video
*after* you finish/scroll. The only observable structure:

- the **scroll order you consumed** (previous video → next video), and
- the **connective tissue TikTok itself uses** — hashtag / sound / creator.

So TikTok edges are temporal/behavioral, not candidate-graph. The `suggest.go`
restart-walk re-ranks a rail; on TikTok there is no rail, so the walk's purpose
(the YouTube feature) does not apply. It can be *reused* to answer "videos in my
history related to this one," but never "a better feed."

## In scope — the Mirror features

All read-DOM-only, same hard rules as YouTube (no browser storage, bounded
~200 in-memory, no platform API, no MAIN world, no `tabs`).

1. **Scroll history** — what you watched, in order, with creator + caption +
   dwell %. This is the inverse of YouTube's "what's recommended": the panel
   shows *what you consumed*.
2. **Hashtag & sound clustering** — group watched videos by hashtag/sound, the
   actual substrate TikTok's ranker uses ("you've watched 14 videos on sound X").
   TikTok-native and genuinely useful.
3. **Engagement mirror** — show your own signals back: liked / commented /
   shared / rewatched / followed / not-interested. TikTok ranks on these; seeing
   your fingerprint is the control feature.
4. **Not-interested provenance** — when you hit not-interested, record which
   hashtag/sound/creator cluster it belonged to, so you see the category the
   algo is learning to suppress for you.
5. **Search→feed trace** — you searched <topic>, then saw N videos about it.
   Exposes the feedback loop TikTok hides.
6. **Self-analytics** — completion % and dwell by topic ("you watch cooking to
   90% but news to 20%").
7. **Hashtag/sound mute nudge** — the TikTok analog of WO-009 (hide
   recommendations): "you've watched 20 cooking videos today" rather than hiding
   a specific next video (which TikTok won't let you — it's already loaded).

## Out of scope — explicit non-goals

- **No re-ranked "better for you" rail.** There is no candidate list to re-rank.
  Keel can reflect and cluster your history; it cannot propose alternatives the
  way it does on YouTube's rail. Do not promise this.
- **No steering of the live feed.** TikTok's next-video decision is server-side
  and post-consumption; the panel cannot intervene before the video loads.
- **No thin/leaky peer graph sharing.** YouTube shares video-ID co-view edges
  (not identifying). On TikTok the useful edges are hashtag/sound co-occurrence,
  which *are* identifying, so peer sharing stays at video-ID level only — most
  of the value is lost. The privacy math that makes YouTube sharing safe does
  not transfer. If sharing is attempted on TikTok, it MUST use video-ID-only
  edges (consistent with §2.1; hashtag/sound clusters are local-only, never
  exported). Reviewer call whether TikTok sharing ships at all; default proposal
  = video-ID edges only, same as YouTube, no cluster export.

## Data the daemon must capture (new TikTok record fields)

WO-057 added `impressions.platform` (`yt` | `tt`) and keyed live/sightings by
platform+id. TikTok needs more than YouTube's fields:

- `dwell_pct` (0–1): fraction of the video watched before scroll/skip.
- `engagement`: liked / commented / shared / rewatched / followed / not_interested
  (nullable, may be set post-hoc by a separate observer pass).
- `hashtags[]`, `sound_id`: parsed from the caption / sound element by
  `extract.js` (TikTok config in `daemon/selectors_tt.json`, per WO-057).
- `search_query_hash`: when a watch follows a search, link it (hash the query
  per AGENTS.md §hard-rules — never store raw search text).

These ride on the existing `impressions` table (or a TikTok sibling) — bounded,
in-memory, flushed on reconnect, never in browser storage. Same constraint as
YouTube.

## Selectors — the real risk

TikTok hashes and rotates class names; the WO-057 config leans on `data-e2e`
attributes. **The selectors are unverified against live TikTok (WO-057 caveat).**
Until a capture from a logged-out session exists (per `test/README.md`), the
Mirror panel has nothing to read. First engineering step is the capture + fixture,
mirroring the YouTube fixture discipline — not building UI on a hypothesis config.

## Acceptance

- [x] TikTok capture from a logged-out session exists; `selectors_tt.json`
      fields (creator, caption, hashtags, sound_id, video_id via `xgwrapper`)
      verified against it; fixture + test pass. Dwell/engagement/not-interested
      are schema-ready but not yet observed from DOM clicks.
- [x] New TikTok fields on `impressions` (`hashtags_json`, `sound_id`,
      `dwell_pct`, `engagement`), never browser storage. Search query hash
      still null until a search surface is observed.
- [x] Panel on `tt`: scroll history + local hashtag/sound cluster counts
      (via `SCROLL_HISTORY`). No re-rank rail. Engagement mirror /
      not-interested provenance / search→feed / mute nudge: deferred (need
      click observers + more UI).
- [x] Peer sharing unchanged: video-ID edges only; hashtag/sound counts are
      local-only in `SCROLL_HISTORY_RESULT` and never exported.
- [x] No re-rank rail feature exists or is promised; panel is a mirror on `tt`.
- [x] `go test ./daemon/...` and `npm test` pass (2026-08-11).

---

## Engineer notes (fill on build)

### Blocker found 2026-08-11 — no video_id source for the scrolling FYP feed

Before writing any extraction code, got a real logged-out capture from Lars (View Source, then a live post-hydration DOM via `document.documentElement.outerHTML`) to verify `daemon/selectors_tt.json`'s selector hypothesis against actual TikTok markup, per this ticket's own instruction not to build UI on a hypothesis config.

**What's confirmed real** (good news — most of the DOM-selector hypothesis holds):
- `data-e2e="recommend-list-item-container"` on each card — real, current.
- `data-e2e="video-desc"` (caption) — real.
- Hashtags: `<a data-e2e="search-common-link" href="/tag/<name>">#<Name></a>` inside the caption — real, one anchor per hashtag.
- `data-e2e="video-author-avatar"` with `href="/@<uniqueId>"` — real.
- `data-e2e="video-music"` with `href="/music/<slug>-<id>"` — real, sound_id is derivable.
- `data-e2e="like-count"` / `comment-count` / `favorite-count` / `share-count` — real, as text content.
- The feed is virtualized: of 9 `<article>` slots in the capture, only 3 had real content; the rest were empty shells the extractor must skip, not error on.

**video_id was missing from hrefs — found on the player host (resolved 2026-08-11).**
No card carries a `/@user/video/<id>` href; FYP routes via client JS. The earlier
`shapes.*.href: ["a[href*=\"/video/\"]"]` assumption is wrong for this surface.

**Source (confirmed against `~/Downloads/keel-wo063-tt-debug.jsonl`, 316 feed
snapshots + resource URLs from the temporary `DEBUG_CAPTURE` probe):**

```html
<div id="xgwrapper-0-7654326932623887630" class="xgplayer-container tiktok-web-player">
```

- 22 distinct snowflake ids across the scroll session; each maps 1:1 to one
  author+caption (stable key).
- Exact match with `/api/item/availability/?itemIds=…` query params observed in
  resource timing (same three ids on first paint).
- Present only on hydrated cards; empty virtualized shells have no `xgwrapper`
  (extractor must skip those).
- Not on cover CDN paths, not in music hrefs (those are sound ids), not in
  `__UNIVERSAL_DATA_FOR_REHYDRATION__` after scroll.

**Ruled out (still true):**

- `__UNIVERSAL_DATA_FOR_REHYDRATION__` / `webapp.updated-items` — first-paint SSR
  seed only (`fyp_fetch_count`); length stays 3 across scroll.
- `window.SIGI_STATE` — no readable script tag; live object would need MAIN world.
- Fetch *bodies* — off the table (no MAIN-world XHR intercept). Resource-timing
  *URLs* were enough: `itemIds` corroborated the DOM ids.

**Landed (WO-056 split):**

- Config (data): `shapes.lockup.playerId: ["[id^=\"xgwrapper-\"]"]` in
  `daemon/selectors_tt.json` (+ channelLink / like-count metadata).
- Engine (logic): `videoIdFromPlayerId` parses `^xgwrapper-\d+-(\d{15,25})$`;
  `readLockupFields` falls back to `playerId` when no `/video/` href.
- Fixture: `test/fixtures/tiktok_feed.html` rebuilt from live shape (incl. empty
  shell). Debug probe left in place until live QA signs off.

**Landed after video_id (2026-08-11 cont.):**

- Extract: hashtags + sound_id via config `hashtag`/`sound` selectors;
  `videoIdFromPlayerId` + lockup `playerId`.
- Daemon: columns + `SCROLL_HISTORY` RPC; cluster counts local-only.
- Panel: on `tt`, primary list is scroll history (not SUGGEST walk); entropy
  slider hidden. Debug probe removed.
- Still deferred: dwell_pct/engagement from live media + click observers,
  not-interested provenance, search→feed trace, mute nudge UI.
