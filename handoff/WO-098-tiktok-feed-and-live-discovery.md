# WO-098 — Capture TikTok Explore, Following, and Live discovery correctly

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Ready to code — independent of WO-095** |
| **Date** | 2026-08-13 |
| **Supersedes** | WO-076 — mapping all three routes to `HOME` |
| **Source** | Lars's live observation of the three feeds plus rendered-DOM review of `/live` |

## Outcome

Keel observes the recommendation cards TikTok actually displays on:

- `/explore` — an ordered wall of ordinary video cards;
- `/following` — an ordered personalized feed of ordinary video cards;
- `/live` — an ordered discovery wall of active livestream cards; and
- `/@creator/live` — the active livestream room when opened.

Explore and Following enter the existing TikTok observation/history path.
At contribution Level 2+, every valid card on `/live` enters Keel's local Live
index immediately and follows the existing Live-gossip path so TikTok's much
larger livestream population appears in Keel's Live page. At Level 1, WO-089's
existing rule remains exact: there is no Live index, extraction/publication,
topic, snapshot, relay, or seed.

This is the completion of the deliberately added TikTok platform support. It
does not add another site, permission, API, or contribution entitlement.

## Correct the record

An automated logged-out headless session rendered Explore's category controls
without hydrating its video wall. That is evidence about that automation
session, not evidence that Explore lacks video cards. Lars's interactive page
showed the wall and is authoritative for product behavior. Implementation QA
must use the interactive rendered DOM in Brave with Shields on.

WO-076's proposed shortcut—classifying Explore, Following, and Live as
`HOME`—is rejected:

- it loses which recommender surface produced an observation;
- it treats a livestream wall as ordinary scroll history; and
- it opens the panel without making TikTok streams appear in Keel Live.

## 1. Exact surface classification

Keep the existing exact-host and exact-route model. Extend the daemon and
extension surface enums together:

| TikTok route | Surface | What it contains |
|---|---|---|
| `/`, `/foryou` | `HOME` | For You feed; unchanged |
| `/explore` | `EXPLORE` | ordered ordinary-video wall |
| `/following` | `FOLLOWING` | ordered ordinary-video feed |
| `/live` | `LIVE` | ordered active-livestream discovery wall |
| `/@creator/video/<id>` | `WATCH_NEXT` | ordinary video; unchanged |
| `/@creator/live` | `LIVE_ROOM` | one active livestream room |

Query strings and a trailing slash do not change the classification. No prefix
or wildcard route rule may accidentally arm observation on profiles, messages,
settings, upload, Shop, or arbitrary TikTok paths.

The panel gate, page proof, content observer, bridge validation, SQLite surface
validation, statistics, and tests must all recognize the same enum. Preserve
the actual surface in storage; do not translate it back to `HOME` at the bridge.

`/friends`, `/search`, `/shop`, `/stem`, and `/nearby` are not part of this
order. Some are authenticated, personalized, regional, or commerce surfaces,
but none has an approved DOM fixture and product decision yet.

## 2. Explore and Following are ordinary video feeds

Use the existing TikTok video-card parser and observation schema:

- derive the numeric TikTok video id from a rendered `/video/<id>` link or the
  already supported `xgwrapper-<slot>-<id>` player host;
- extract creator, caption/title, hashtags, sound, visible counts, badges, and
  sponsored state through the selector configuration and compiled parsers;
- assign `slot_index` from the displayed card order before filtering failures;
- tolerate virtualized empty shells without counting them as malformed video
  cards; and
- rescan through the existing bounded MutationObserver/throttle path as the
  wall/feed loads more cards.

Deduplicate within one page generation by `(platform, video_id)` while
preserving gaps in displayed slot order. SPA navigation between TikTok feeds
starts a new page generation and emits the correct new `PAGE_CONTEXT`.

Following is personalized even when it contains only followed creators. It is
still a ranked feed and must retain the `FOLLOWING` surface rather than being
treated as an author page or unranked archive.

## 3. TikTok Live cards use a live locator, not a fake video id

The rendered `/live` cards reviewed on 2026-08-13 expose:

- `[data-e2e="discover_category-list-live-card"]` per displayed stream;
- a canonical `/@creator/live` link;
- creator/display name;
- stream title;
- a visible current viewer count that is deliberately out of scope here; and
- cover/avatar images.

They do **not** expose a numeric video or room id in the rendered card and do
not contain the FYP `xgwrapper` id. Do not hash the locator into a number, use a
cover CDN hash as identity, scrape an unrendered hydration script, call a
TikTok API, or intercept a request to manufacture one.

Extend the Live record with an optional canonical locator:

```text
platform:     tt
live_locator: @creator/live
channel_id:   @creator
video_id:     optional; only when the rendered card genuinely supplies one
```

Canonicalization lowercases the handle for identity, removes query/fragment
and duplicate slashes, and accepts only the exact `@handle/live` shape. Keep
the display spelling separately if the DOM provides it.

For TikTok, the Live index key is the canonical live locator when present;
the numeric video id remains an optional alias. All sightings carrying the
same locator merge into one current entry. A TikTok account can expose one
current stream at that canonical route, and the Live index is already
short-lived, so reusing the locator after the old entry expires correctly
describes a later stream without inventing a permanent session id.

Do not put the locator into `Impression.video_id` or the durable video
catalogue. `/live` discovery sightings are ephemeral Live records. If TikTok
later renders a real stable room id, support it as an additional alias under a
separate reviewed ticket; do not change identity based on hidden page state.

## 4. One explicit Live-sighting bridge path

Add a revisioned `LIVE_SIGHTINGS` message for rendered livestream cards rather
than forcing them through the ordinary-video `IMPRESSIONS` schema.

Each sighting contains only:

- page-load id and `LIVE` or `LIVE_ROOM` surface;
- observed time and displayed slot;
- platform `tt`;
- canonical live locator and creator id/name;
- title;
- the existing normalized public badges.

The extension keeps the batch in the same bounded in-memory reconnect queue as
observations and never writes it to browser storage. The daemon validates every
field and sends valid sightings directly to its ephemeral Live index. A bad
card is rejected individually and increments bounded extraction diagnostics;
it does not discard unrelated cards.

At Level 2+, the Live page can receive the local entry as soon as the daemon
accepts it, then publish it through the existing Live-gossip path. The content
observer must use the daemon's negotiated Live capability so Level 1 does not
produce `LIVE_SIGHTINGS`; the daemon must also reject them while Live is off.
Level 1 has no Live object and emits, serves, relays, or seeds nothing Live, as
required by WO-089. This order does not reopen contribution policy.

Do not add viewer count to the extension/daemon bridge, Live record, wire,
ranking, or UI in this order. Its rendered presence may help identify a card
in a fixture, but handling volatile popularity data is separate feature work.

## 5. Repair the existing TikTok Live path

There are two confirmed code defects independent of selectors:

1. `announceLive()` constructs `LiveRecord` without copying
   `Impression.Platform`. A TikTok numeric id is consequently validated as a
   YouTube id and rejected. Every impression-derived record must preserve
   `platform` before validation, local merge, keying, or gossip.
2. `surfaceFromUrl()` classifies `/@creator/live` as `WATCH_NEXT` with a null
   context video id. `buildCtx()` then rejects it, so the observer is idle.
   The exact route must use `LIVE_ROOM` and its canonical live locator as its
   context, never a fabricated video id.

Existing FYP cards that carry a real numeric TikTok video id and a LIVE badge
remain supported. When their creator link is present, derive the same
`@creator/live` locator so a stream seen on FYP and `/live` coalesces instead of
appearing twice. If only a numeric id is available, retain the legacy
platform-plus-video key rather than guessing a creator.

The Live wire shape must remain backward compatible with existing YouTube and
numeric-TikTok records. Validate identity per platform and identity kind;
unknown platforms, malformed locators, and records with neither a locator nor
a valid platform video id fail closed.

## 6. Live page rendering and navigation

Keel's existing Live list/search remains one cross-platform feed. Each entry
retains its platform and opens the correct public locator:

- YouTube: existing watch URL;
- TikTok numeric video: existing `/@creator/video/<id>` behavior when the
  creator is known; and
- TikTok live locator: `https://www.tiktok.com/@creator/live`.

Never build a TikTok live URL from a title or CDN cover. A locator entry with a
malformed/missing handle is not clickable and should have been rejected by the
daemon.

TikTok Live cards appear in the Live page even if they were discovered on the
local `/live` wall before any peer reports them. Deduplication must not erase
the local provenance or replace a fresher local title with stale peer data.

## 7. Selector and fixture discipline

Selectors remain daemon-served bounded CSS data. Parsing and locator
canonicalization remain compiled extension logic. No downloaded regex,
expression, branch, or executable behavior is permitted.

Before changing selectors, capture the post-hydration rendered DOM from the
actual interactive Brave QA surface with Shields on:

- logged-out Explore;
- logged-in Explore if its structure differs;
- logged-in Following;
- logged-out Live; and
- an opened `/@creator/live` room.

Sanitize fixtures before committing them: preserve element/attribute shape but
replace account-specific handles, titles, ids, images, and personalized rows.
Never commit cookies, tokens, hydration state, request headers, or a user's raw
personal feed. Tests use the sanitized DOM fixtures only.

## Privacy and store-review constraints

- Read rendered DOM only. No MAIN-world script, fetch/XHR interception,
  TikTok API, or hydration-script scraping.
- No observation data in browser storage; the existing bounded in-memory queue
  and native bridge remain the only path.
- No raw search query is introduced or retained.
- Do not widen `*://www.tiktok.com/*`, add a third host, or add permissions.
- Hashtags, sounds, and creator/title data follow the existing local TikTok
  rules. This order adds no new peer graph fields.
- Live publication remains Level 2+ under WO-089; Level 1 sends nothing.

## Implementation boundaries

| Area | Owner |
|---|---|
| Exact route/surface classification and card parsing | extension content extractor |
| CSS selector data | `daemon/selectors_tt.json` |
| Surface and Live-sighting validation | extension and daemon bridge protocols |
| Ordinary Explore/Following observations | existing impression/store path |
| Live locator identity, merge, expiry, gossip | daemon Live index |
| Live result links and surface-aware panel behavior | extension render/page modules |

`sw.js` remains composition only. Do not add feature state, DOM parsing, or a
new storage owner there.

## Acceptance

- [ ] Exact route tests classify FYP, Explore, Following, Live discovery, live
      room, and ordinary video distinctly; adjacent/unapproved paths stay idle.
- [ ] Interactive Brave fixtures prove Explore and Following emit ordinary
      TikTok impressions in displayed order through SPA navigation and scroll.
- [ ] A `/live` fixture emits one validated Live sighting per real displayed
      card, including locator, creator, title, and slot, while
      virtualized/structural shells are skipped.
- [ ] No `/live` card requires a numeric video/room id, hidden JSON, request
      interception, CDN-derived identity, or fabricated hash id.
- [ ] `/@creator/live` is active as `LIVE_ROOM`; it no longer dies on the
      `WATCH_NEXT needs context_video_id` guard.
- [ ] `announceLive()` preserves `platform: tt`; a regression test proves a
      valid TikTok sighting reaches the local Live index.
- [ ] FYP and `/live` sightings for the same creator locator merge into one
      current TikTok Live entry when the locator is known.
- [ ] At Level 2+, local TikTok streams appear immediately in Keel Live and
      open the exact `/@creator/live` URL.
- [ ] Two Level-2 machines exchange a TikTok locator Live record and render one
      deduplicated, clickable entry; malformed records fail closed.
- [ ] Level 1 sends, relays, and serves no Live record or snapshot.
- [ ] Level 1 neither emits nor accepts `LIVE_SIGHTINGS`, and has no Live index.
- [ ] Viewer count is not added to any record, wire message, ranking, or UI.
- [ ] Sanitized fixtures contain no real account feed, cookies, tokens, headers,
      or hydration state.
- [ ] Existing YouTube Live, TikTok FYP Mirror, contribution policy, minimum
      permissions, and off-surface-idle tests remain green.

## Do not

- Do not map Explore, Following, or Live back to `HOME`.
- Do not treat the TikTok Live wall as ordinary video history only.
- Do not call a creator handle or live locator a video id.
- Do not invent a numeric room id or use CDN/hydration/network data as one.
- Do not conclude that a real interactive wall is empty because an automated
  session failed to hydrate it.
- Do not add Friends, Search, Shop, STEM, Nearby, profiles, or messages here.
- Do not change contribution levels or Level-1 outbound behavior.

## Stop conditions for the implementer

Return to architecture review if implementation appears to require:

- any TikTok API, request interception, MAIN-world access, or unrendered
  hydration data;
- a fake or unstable livestream identity;
- persisting a live locator as an ordinary video id;
- committing an unsanitized authenticated feed fixture;
- widening host permissions or route scope; or
- changing Live contribution consent or gossip entitlement.
