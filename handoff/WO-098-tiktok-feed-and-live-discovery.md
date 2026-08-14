# WO-098 — Capture TikTok Explore, Following, and Live discovery correctly

| | |
|---|---|
| **Addressee** | Sr Dev (GPT-5.6 Terra, High) |
| **Status** | **Architecture-reviewed — ready to code on merged `main`** |
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

`platformFromUrl()` and the panel gate must enforce the hosts actually named in
the manifests: `www.youtube.com` and `www.tiktok.com`. Do not treat an arbitrary
subdomain as approved merely because it ends with one of those domain names.

Do **not** implement one permissive surface enum everywhere. There are three
deliberately different validation domains:

| Record/domain | Allowed new surfaces | Persistence |
|---|---|---|
| Page context/proof | `EXPLORE`, `FOLLOWING`, `LIVE`, `LIVE_ROOM` | bounded extension memory only |
| Ordinary `Impression` | `EXPLORE`, `FOLLOWING` | daemon SQLite |
| `LiveSighting` | `LIVE`, `LIVE_ROOM` | daemon Live index memory only |

The panel gate and page proof recognize all four new page surfaces. The ordinary
impression validators, SQLite surface validation and durable statistics add only
`EXPLORE` and `FOLLOWING`. They must reject `LIVE` and `LIVE_ROOM`; accepting
those into `Impression` would silently persist the ephemeral sightings this
order explicitly keeps out of the catalogue. The Live-sighting validators do
the inverse and accept only `LIVE` and `LIVE_ROOM`.

Platform/surface combinations are validated too: `EXPLORE`, `FOLLOWING`,
`LIVE`, and `LIVE_ROOM` are TikTok-only. The service worker currently derives
surface from `sender.tab.url` but copies the payload's claimed platform despite
a comment saying otherwise; fix that mismatch and derive both from the sender
URL.

Preserve `EXPLORE` and `FOLLOWING` in SQLite; do not translate them back to
`HOME` at the bridge. On all six approved TikTok surfaces the toolbar may open
the existing TikTok Mirror panel. A Live page proof carries its context and an
empty ordinary-impression list; it does not copy Live-sighting payloads into the
proof or turn them into suggestion seeds.

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

Do not send these feeds through the current non-`HOME` extractor branch, which
forces every record back to `WATCH_NEXT`. Add a feed path that preserves the
context surface. Its `slot_index` is the card's position in the rendered scan
before invalid cards and virtualized shells are removed. A TikTok feed scroll
is not YouTube rail replacement: it must not bump the rail generation or clear
the page-level emitted-id set merely because virtualization changed which cards
occupy the DOM. Keep the first valid sighting of an id in that page generation;
do not claim a global rank that the rendered DOM did not expose.

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

The compiled canonicalizer—not selector data—owns that rule. It accepts a
relative or `https://www.tiktok.com` rendered link, rejects every other host and
path, and returns exactly lowercase `@handle/live`. A handle is 1–64 characters
from the same conservative `[A-Za-z0-9_.-]` set already accepted for TikTok
creator links. When `channel_id` is also present, its canonical handle must
equal the locator's handle case-insensitively; conflicting identity fields fail
closed.

Use one canonical identity function everywhere in the Live index—not fresh
string concatenation at each call site:

```text
YouTube: valid platform + video_id
TikTok:  canonical live_locator when present, otherwise valid numeric video_id
```

The locator is primary when a TikTok record carries both fields; the numeric id
is optional supporting data. The canonical key must drive merge, local-seen
tracking, publish suppression, retirement, snapshot deduplication and stable
sort ties. All sightings carrying the same locator merge into one current
entry. A fresh local observation may clear a retired locator and start a new
entry after the prior sighting has aged out; stale peer gossip alone may not
resurrect it. This is what permits a creator to use the same public live route
for a later stream without inventing a permanent session id.

Locator-only records are not backward-compatible with the current
`keel/live/1` validator, which requires `video_id`. Therefore this order bumps
the Live topic to `keel/live/2` and the snapshot protocol to
`/keel/live-snapshot/2.0.0`. The v2 validator continues to accept existing
YouTube and numeric-TikTok record shapes, but v1 and v2 are not dual-published.
Keel is still pre-release; keeping two meshes and two publication paths would
add permanent complexity to preserve a development-only wire. Two-machine QA
must put both machines on the v2 build.

Do not put the locator into `Impression.video_id` or the durable video
catalogue. `/live` discovery sightings are ephemeral Live records. If TikTok
later renders a real stable room id, support it as an additional alias under a
separate reviewed ticket; do not change identity based on hidden page state.

## 4. One explicit Live-sighting bridge path

Add a revisioned `LIVE_SIGHTINGS` message for rendered livestream cards rather
than forcing them through the ordinary-video `IMPRESSIONS` schema. Advertise it
as optional bridge capability `live_sightings:1`; an older daemon must be
treated as unable to accept this RPC.

Each sighting contains only:

- page-load id and `LIVE` or `LIVE_ROOM` surface;
- observed time and displayed slot;
- platform `tt`;
- canonical live locator and creator id/name;
- title;
- the existing normalized public badges.

The extension keeps sightings and impressions in **one** tagged reconnect FIFO
with one total cap of 200 records, oldest dropped first. Two independent
200-record buffers would violate the existing browser-memory bound. A reconnect
flush groups adjacent tags into the correct RPC without reordering them and
never writes either type to browser storage. Before flushing a queued Live
sighting, re-check the daemon's current Live entitlement; discard it if the
effective policy is no longer Level 2+.

The service worker derives platform and surface from `sender.tab.url`, checks
the sighting's `page_load_id` against that tab's current proof, and never trusts
those ownership fields from the payload. Valid cards continue when a sibling
card is invalid. The daemon validates every field again, reports accepted and
rejected counts, and converts only accepted records into `LiveRecord`. It drops
page-load id, surface and slot at that boundary: none enters the Live gossip
record, SQLite, logs, snapshots or the durable catalogue.

At Level 2+, the Live page can receive the local entry as soon as the daemon
accepts it, then publish it through the Live-gossip path. Do not confuse the
static negotiated `live_sightings:1` protocol capability with the dynamic Live
entitlement. On startup/navigation the content observer asks for the daemon's
effective contribution state and treats unknown, disconnected, transitioning
or `live:false` as off. Existing `CONTRIBUTION_STATUS` changes must also reach
content scripts: an upgrade arms and rescans approved Live surfaces; a downgrade
immediately disarms them and purges queued Live sightings.

The daemon independently checks the current node policy and permission gate in
the `LIVE_SIGHTINGS` handler and again in `PublishLive`. Level 1 receives a typed
`contribution_required` refusal and never constructs or mutates a Live index.
It emits, accepts, serves, relays and seeds nothing Live, as required by WO-089.
This order does not reopen contribution policy.

That dynamic gate applies to the dedicated `LIVE_SIGHTINGS` observer. Ordinary
FYP, Explore and Following impressions remain local recording features at every
level after recording consent, including when an ordinary card carries a LIVE
badge. At Level 1 the daemon may store that ordinary impression, but the
existing `n.Live() == nil` boundary prevents its badge from becoming a Live
index entry or network notice.

Do not add viewer count to the extension/daemon bridge, Live record, wire,
ranking, or UI in this order. Its rendered presence may help identify a card
in a fixture, but handling volatile popularity data is separate feature work.

## 5. Repair the existing TikTok Live path

There are four confirmed code defects independent of selectors:

1. `announceLive()` constructs `LiveRecord` without copying
   `Impression.Platform`. A TikTok numeric id is consequently validated as a
   YouTube id and rejected. Every impression-derived record must preserve
   `platform` before validation, local merge, keying, or gossip.
2. `surfaceFromUrl()` classifies `/@creator/live` as `WATCH_NEXT` with a null
   context video id. `buildCtx()` then rejects it, so the observer is idle.
   The exact route must use `LIVE_ROOM` and its canonical live locator as its
   context, never a fabricated video id.
3. `fetchLiveSnapshot()` currently admits only `len(video_id) == 11`, silently
   discarding numeric TikTok records already supported by the topic. Replace
   that shortcut with the same complete record/identity validator used for
   gossip and local publication; v2 snapshots must accept both identity kinds.
4. `live.go` still opens with the superseded claim that Live publication is
   ungated and available at every level. Rewrite that header to match WO-089
   and `ARCHITECTURE_CURRENT.md`; contradictory security commentary beside the
   enforcement code is an implementation hazard.

Existing FYP cards that carry a real numeric TikTok video id and a LIVE badge
remain supported. When their creator link is present, derive the same
`@creator/live` locator so a stream seen on FYP and `/live` coalesces instead of
appearing twice. If only a numeric id is available, retain the legacy
platform-plus-video key rather than guessing a creator.

The `/@creator/live` URL only arms a possible `LIVE_ROOM` observation. It is not
evidence by itself that the creator is currently live. Emit a room sighting only
when approved rendered-room selectors prove an active live player or badge and
supply the required identity/title fields; an offline, ended, loading or error
room emits nothing.

All local, gossip and snapshot inputs use one record validator. It bounds text,
time and identity fields; rejects future/expired claims; requires at least one
valid identity; and rejects an optional id if it is malformed. `PublishLive`
must not rely on the peer validator to sanitize a locally constructed record.

For mutable display fields, keep the non-empty values from the newest valid
sighting. A stale peer record must not replace a fresher local title or creator;
on equal observation times local wins. This provenance is internal Live-index
state only and is never added to the wire.

The Live wire shape must remain backward compatible with existing YouTube and
numeric-TikTok records. Validate identity per platform and identity kind;
unknown platforms, malformed locators, and records with neither a locator nor
a valid platform video id fail closed.

## 6. Live page rendering and navigation

Keel's existing Live list/search remains one cross-platform feed. Rendering and
navigation take the complete entry, not only `(video_id, platform)`, because a
locator-only entry has no video id. Each entry retains its platform and opens
the correct public locator:

- YouTube: existing watch URL;
- TikTok numeric video: existing `/@creator/video/<id>` behavior when the
  creator is known; and
- TikTok live locator: `https://www.tiktok.com/@creator/live`.

Never build a TikTok live URL from a title or CDN cover. A locator entry with a
malformed/missing handle is not clickable and should have been rejected by the
daemon.

This order does not add cover/avatar URLs to the bridge or wire. A locator-only
entry renders the existing empty thumbnail placeholder and must not call the
video-thumbnail RPC with a locator or empty id. Its text fallback is title,
then creator, then the canonical locator—never a blank `video_id`.

TikTok Live cards appear in the Live page even if they were discovered on the
local `/live` wall before any peer reports them. Deduplication must not erase
the local provenance or replace a fresher local title with stale peer data.

## 7. Selector and fixture discipline

Selectors remain daemon-served bounded CSS data. Parsing and locator
canonicalization remain compiled extension logic. No downloaded regex,
expression, branch, or executable behavior is permitted.

Extend the selector schema with a dedicated bounded TikTok Live shape (Live
card/container, room container, locator link, creator, title and badge fields).
Do not force locator-only cards through `readCardFields`, whose contract
correctly requires a video id for durable impressions. The MutationObserver's
compiled selector union may include the configured Live-card selector, while
the callback retains its existing O(1) `matches()`/bounded-node rule.

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

The contribution policy does not change, but the existing disclosure is no
longer field-complete: `PRIVACY.md` currently describes a Live notice as a video
id and title, while this order adds a canonical TikTok creator/live locator and
observes a dedicated Live wall. Before the observer is enabled:

- update the privacy policy's recording and Live-network sections to distinguish
  durable Explore/Following clips from ephemeral Level-2 Live sightings and to
  name the public platform, video id or creator/live locator, creator/title and
  coarse sighting time that can enter a Live notice;
- update the in-product Broad-sharing disclosure to say that this includes
  streams found on TikTok's Live discovery wall and that page id, displayed
  slot and sender identity do not enter the notice; and
- increment the extension `CONSENT_REVISION` and daemon
  `NetworkConsentRevision` together. Existing acceptances predate this expanded
  surface/field disclosure, so the daemon remains network-off until the current
  revision is accepted through the existing WO-089 flow.

This is a disclosure revision, not a new contribution level or a change to the
Level-1/Level-2 capability table.

## Implementation boundaries

| Area | Owner |
|---|---|
| Exact route/surface classification and card parsing | extension content extractor |
| CSS selector data | `daemon/selectors_tt.json` |
| Surface and Live-sighting validation | extension and daemon bridge protocols |
| Ordinary Explore/Following observations | existing impression/store path |
| Live locator identity, merge, expiry, gossip | daemon Live index |
| Live result links and surface-aware panel behavior | extension render/page modules |
| Dynamic Live entitlement and one bounded reconnect FIFO | background RPC owner + daemon contribution state |
| Normative surface/Live identity record | `ARCHITECTURE_CURRENT.md` §2–3 and `DESIGN_v2.md` §7.5 |
| Exact user-facing disclosure + revision | `PRIVACY.md`, contribution/consent copy, extension + daemon revision constants |

`sw.js` remains composition only. Do not add feature state, DOM parsing, or a
new storage owner there. The implementation commit updates both standing design
documents named above; the work order must not land with the code and normative
architecture disagreeing.

## Acceptance

- [ ] Exact route tests classify FYP, Explore, Following, Live discovery, live
      room, and ordinary video distinctly; adjacent/unapproved paths stay idle.
- [ ] Only exact manifest hosts classify as supported; sender-derived platform
      and surface override contradictory message payload claims.
- [ ] Page-context, ordinary-impression and Live-sighting validators enforce
      their three distinct surface sets; no `LIVE`/`LIVE_ROOM` row can enter
      SQLite, exports, graph blocks or durable statistics.
- [ ] Interactive Brave fixtures prove Explore and Following emit ordinary
      TikTok impressions in displayed order through SPA navigation and scroll.
- [ ] TikTok feed virtualization does not trigger YouTube rail-replacement
      semantics or re-emit an id already emitted in that page generation.
- [ ] A `/live` fixture emits one validated Live sighting per real displayed
      card, including locator, creator, title, and slot, while
      virtualized/structural shells are skipped.
- [ ] No `/live` card requires a numeric video/room id, hidden JSON, request
      interception, CDN-derived identity, or fabricated hash id.
- [ ] `/@creator/live` is active as `LIVE_ROOM`; it no longer dies on the
      `WATCH_NEXT needs context_video_id` guard, and its URL alone cannot emit a
      sighting while the rendered room is offline or unproven.
- [ ] `announceLive()` preserves `platform: tt`; a regression test proves a
      valid TikTok sighting reaches the local Live index.
- [ ] FYP and `/live` sightings for the same creator locator merge into one
      current TikTok Live entry when the locator is known.
- [ ] One canonical identity/validation path is used by local publish, gossip,
      snapshot, merge, publish suppression, retirement and local-seen state.
- [ ] `keel/live/2` and `/keel/live-snapshot/2.0.0` accept YouTube ids, numeric
      TikTok ids and canonical TikTok locators; the old 11-character snapshot
      shortcut is gone and locator-only records never enter the v1 topic.
- [ ] At Level 2+, local TikTok streams appear immediately in Keel Live and
      open the exact `/@creator/live` URL.
- [ ] Two Level-2 machines exchange a TikTok locator Live record and render one
      deduplicated, clickable entry; malformed records fail closed.
- [ ] The extension and daemon negotiate `live_sightings:1`; a missing static
      capability is distinct from an effective Level-1 refusal.
- [ ] Upgrade to Level 2 arms/rescans an already-open TikTok Live surface;
      downgrade disarms it and drops queued sightings before any reconnect
      flush. Impressions plus sightings never exceed 200 queued records total.
- [ ] Privacy and in-product Broad-sharing copy name the TikTok Live wall and
      locator notice fields; both consent-revision constants advance together,
      and a pre-WO-098 acceptance leaves the daemon network-off until renewed.
- [ ] Level 1 sends, relays, and serves no Live record or snapshot.
- [ ] Level 1 neither emits nor accepts `LIVE_SIGHTINGS`, and has no Live index.
- [ ] A stale peer title/creator cannot replace fresher local display data, and
      a new local sighting can reuse a creator locator after the prior entry
      ages out without accepting peer-only resurrection.
- [ ] Locator-only results render useful text and a blank thumbnail placeholder
      without calling `THUMBNAIL` or constructing a URL from title/cover data.
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
- Do not admit `LIVE` or `LIVE_ROOM` through the ordinary-impression validator
  or persist a `LIVE_SIGHTINGS` field in SQLite.
- Do not keep the v1 Live topic while changing its required identity contract.
- Do not create a second reconnect queue or let a queued sighting survive a
  downgrade.
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
