# WO-103 — Verify TikTok Explore, Following, and Live-room DOM interactively

| | |
|---|---|
| **Addressee** | Lars + reviewer |
| **Status** | **Review complete — implementation split to WO-104** |
| **Date** | 2026-08-13 |
| **Depends on** | WO-098 accepted implementation |

## Outcome

Prove the remaining TikTok surfaces against the DOM an actual Brave user sees,
then make selector-data corrections without changing WO-098's architecture.
Automated logged-out sessions did not hydrate Explore or Following, and no real
active/inactive room capture was available. A model must not replace that
missing evidence with plausible HTML.

## Capture protocol

Use Brave with Shields on and the same extension build on:

1. interactive Explore after its video wall has hydrated;
2. logged-in Following after ordinary cards have hydrated;
3. one currently active `/@creator/live` room; and
4. one offline, ended, loading, unavailable or error room.

Capture only rendered DOM. Do not export cookies, storage, request headers,
network logs, hydration scripts or account tokens. Sanitize before committing:
preserve relevant tag/attribute/ancestor/card structure while replacing handles,
titles, numeric ids, images and personalized rows.

## Verification

- Explore emits ordinary TikTok impressions with surface `EXPLORE`, displayed
  slot order and page-generation deduplication.
- Following emits ordinary TikTok impressions with surface `FOLLOWING` and the
  same virtualization behavior.
- SPA navigation among FYP, Explore and Following starts the correct new proof
  without YouTube rail-replacement semantics.
- An active room emits exactly one `LIVE_ROOM` sighting only when rendered
  active-player/badge evidence and required identity/title fields are present.
- Offline, ended, loading and error rooms emit nothing; their URL is never
  treated as evidence.
- Selector updates remain bounded CSS data in `daemon/selectors_tt.json`; no
  MAIN-world access, hydration parsing, API call or request interception.
- Sanitized fixture tests and interactive observation agree on counts and key
  fields. Record the Brave version, login state, route and result without
  retaining personal feed contents.

## Explore checkpoint — 2026-08-13

Lars captured the hydrated `https://www.tiktok.com/explore` wall in interactive
Brave with the agreed sanitizer. The local review copy is
`~/Downloads/keel-tiktok-explore-sanitized.html`; it is evidence, not yet a
repository fixture.

Privacy review passed:

- no script, style, link, meta, iframe, template or noscript elements remain;
- no image source, source set, inline style, form value or event-handler
  attributes remain;
- every text node, image alternative, accessible label, creator handle and
  video id is synthetic; and
- the retained attributes are structural selector evidence only. The capture
  contains no cookies, storage, requests or hydration state.

The rendered wall contains 15 ordered cards under:

```css
[data-e2e="explore-item-list"]
```

Each card has a stable public hook:

```css
[data-e2e="explore-item"]
```

and a rendered `/@creator/video/<id>` link. With the captured wall supplied as
the extraction root, the accepted WO-098 parser returns 15 candidates, 15
impressions, zero failures and slot indices 0 through 14.

Two implementation findings remain:

1. `daemon/selectors_tt.json` does not name
   `[data-e2e="explore-item-list"]`. Neither configured Explore root matches the
   real page, so `observer.js` never arms its MutationObserver and emits no
   Explore impressions. Add the public `data-e2e` root first; compiled CSS
   class fragments are fallback evidence, not the preferred contract.
2. The ordinary TikTok lockup reader currently accepts the text content of the
   `/video/` link as the title before consulting its configured title fields.
   On the captured Explore card that link contains the visible like-count
   overlay, while the descriptive card row is empty and the media image carries
   the alternative-text field. The extraction proof therefore returned the
   synthetic like-count placeholder as `title`. Correct title precedence so a
   configured semantic title/alternative-text source wins and count overlays
   cannot become catalogue titles. This is compiled extractor behavior, not a
   selector-data-only correction, so it must be handed to an implementer after
   the remaining captures show whether Following has the same shape.

Do not commit a correction from this checkpoint alone. Following may reuse the
same card structure, and its interactive capture should determine whether one
shared fix is correct. The local browser reports Brave `151.1.93.134`; login
state was not embedded in the sanitized file and remains to be recorded with
the final QA result.

## Following checkpoint — 2026-08-13

Interactive `/following` did not render ordinary video-feed cards. It rendered
creator/channel recommendation cards with a prominent red Follow button; a
short preview plays on hover, but the card is not a full video recommendation.
Following one creator and reloading did not change the route into a video feed.
The bounded video-card capture correctly found no ordinary video cards and
wrote no file.

This surface must emit no `FOLLOWING` impressions in the observed state. A hover
preview is presentation inside an account-recommendation card, not evidence
that TikTok recommended the previewed video as a feed item. Do not broaden the
card selector to make this wall produce records.

WO-098's description of `/following` as an ordered ordinary-video feed is not
supported by the live web surface and must not drive implementation. Preserve
the exact route classification and panel access, because TikTok may expose a
real feed for a different account state later, but make extraction conditional
on actual ordinary-video card evidence. Until such a state is captured,
`/following` is a proven zero-impression surface rather than a proven feed.

## Active Live-room checkpoint — 2026-08-13

Lars captured a visibly active `/@creator/live` room in interactive Brave. The
local review copy is
`~/Downloads/keel-tiktok-live-room-active-sanitized.html`; it is evidence, not
yet a repository fixture.

Privacy review passed. The capture retains only `class`, `data-e2e`, `role`,
sanitized `id`, sanitized `href`, `alt` and `aria-label` attributes. It contains
no scripts, hydration state, media/image sources, styles, handlers or original
text/identifiers. Chat, comment, gift and viewer structures were removed before
download.

The active page exposes:

- room root `main#tiktok-live-main-container-id`;
- one `[data-e2e="live-content-container"]`;
- one `[data-e2e="live-room-content"]` containing a rendered `<video>`;
- a player ancestor with the public state class `xgplayer-playing`;
- one `[data-e2e="live-header-container"]`;
- one `[data-e2e="room-header-anchor-name"]`; and
- a room-header profile link whose rendered `/@creator` handle supplies the
  creator identity.

The accepted WO-098 implementation correctly emits no room sighting today:

1. `extractLiveSightings()` explicitly returns an empty list for `LIVE_ROOM`
   until active and inactive captures exist.
2. The configured room roots (`[data-e2e="live-room"]`) do not match the real
   page. `main#tiktok-live-main-container-id` is currently configured only as
   the `/live` discovery-wall root.
3. The configured active selectors (`[data-e2e="live-player"]` and
   `[data-e2e="live-badge"]`) do not match this active room.
4. The generic `/@creator/live` selector finds many recommended-stream links in
   the surrounding navigation, not the current room. Room identity must be
   scoped to the rendered room header and must not select a sidebar stream.

Do not implement the positive predicate from this capture alone. The inactive
or unavailable room capture must prove which of the video element,
`live-room-content` hook and `xgplayer-playing` state actually disappears or
changes. A room URL remains identity context only; it is never sufficient
evidence that the stream is active. The capture also proves a creator name but
does not identify which sanitized header text, if any, is a distinct stream
title; that field must be resolved from the visible page rather than guessed.

Lars's visual verification resolves the title question: the current room shows
only the creator name and follower count. Stream links below the room acquire
titles only after scrolling, but those titles belong to other streams. They
must never label the current room.

Therefore `LIVE_ROOM` sightings may omit `title`. Keep `title` required for
`LIVE` discovery-wall cards, where TikTok renders a real stream title. Do not
copy `channel_name` into `title` and do not synthesize "Creator is live"; the
Live page already renders `title || channel_id || locator`, so the creator is a
truthful display fallback without corrupting the field's meaning. The daemon
`LiveRecord`, gossip validator and page renderer already tolerate an empty
title. The extension bridge validator does not, so this compatibility change
must advertise `live_sightings:2`; a revision-2 extension must not send a
titleless room sighting to a revision-1 daemon.

Room identity comes from the rendered room-header profile link and
`[data-e2e="room-header-anchor-name"]`, scoped under
`[data-e2e="live-header-container"]`. A single shared canonical helper derives
`@handle/live` from that validated rendered handle. The page URL classifies the
surface but is not active-state or identity evidence. Related `/@creator/live`
links elsewhere in the page remain out of scope for the one `LIVE_ROOM`
sighting.

## Inactive/automatic-replacement checkpoint — 2026-08-13

Lars captured the unavailable-room state before TikTok automatically navigated
to another active stream. The state lasts roughly six to eight seconds; the
captured page was displaying "Next LIVE will automatically play in 1 second"
near the end of that countdown. The local review copy is
`~/Downloads/keel-tiktok-live-room-active-sanitized (1).html`; despite the
filename inherited from the capture script, this is the inactive fixture.

The inactive state retains the same main root, `live-content-container`,
`live-room-content`, room header and creator-name hooks as the active state. It
contains zero `<video>` elements and zero `xgplayer-playing` elements. It also
contains 42 `discover_category-list-live-card` replacement recommendations.
The active capture contains one room-scoped `<video>` under an
`xgplayer-playing` ancestor.

This proves the negative boundary:

- room shell, header, creator identity, countdown text and replacement cards do
  not establish that the requested room is active;
- require the room-scoped player path
  `[data-e2e="live-room-content"] .xgplayer-playing video` in addition to the
  scoped header identity before emitting the one current-room sighting;
- emit nothing throughout the automatic-replacement countdown; and
- when TikTok navigates to the next active room, let the existing SPA route
  change start a new page generation and evaluate that room's own header and
  player. Never emit one room's identity with another room's player or title.

The active player also carries an `xgplayer-inactive` class while playing; that
class describes player-control activity and must not be interpreted as an
offline-stream signal.

## Stop conditions

Return to architecture only if the rendered pages lack a stable public identity
or require hidden/network state. Ordinary selector drift or an additional
bounded container selector is implementation/QA work, not a new feature.
