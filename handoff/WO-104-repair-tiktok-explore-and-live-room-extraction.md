# WO-104 — Repair TikTok Explore and Live-room extraction from interactive evidence

| | |
|---|---|
| **Addressee** | Sr Dev (Grok 4.6) |
| **Status** | **Accepted 2026-08-13 — awaiting commit** |
| **Date** | 2026-08-13 |
| **Depends on** | WO-098 accepted implementation; WO-103 interactive evidence |

## Outcome

Make TikTok Explore and individual Live rooms work against the DOM interactive
Brave actually rendered. Preserve the current zero-impression behavior of the
creator-card `/following` page. This is a selector/extraction repair, not a new
surface or privacy-policy change.

Do not infer DOM. Use the sanitized captures Lars produced:

- `~/Downloads/keel-tiktok-explore-sanitized.html` — 15 hydrated Explore cards;
- `~/Downloads/keel-tiktok-live-room-active-sanitized.html` — active room; and
- `~/Downloads/keel-tiktok-live-room-active-sanitized (1).html` — inactive
  automatic-replacement countdown despite its inherited filename.

Review each file for sanitization before copying it into `test/fixtures/`. The
privacy review in WO-103 passed, but the implementer remains responsible for
the committed artifact.

## 1. Repair Explore's observation root

The live page root is:

```css
[data-e2e="explore-item-list"]
```

and each ordinary card has:

```css
[data-e2e="explore-item"]
```

Put those public hooks before compiled-class fallbacks in
`daemon/selectors_tt.json`. Today neither configured Explore root matches, so
`observer.js` never arms and the otherwise working parser sees no cards.

With the accepted capture as the root, extraction must return 15 candidates,
15 impressions, zero failures and slots 0 through 14.

## 2. Stop treating Explore's like count as its title

The rendered `/video/` anchor contains the like-count overlay. Its text is not
the caption. `readLockupFields()` currently accepts that anchor text before it
consults the configured title selectors, so valid cards enter the catalogue
with counts as titles.

For TikTok lockups, prefer the configured semantic title element before link
text. On this Explore shape the image alternative-text field is the available
title source. Use the existing compiled attribute/text reader; do not encode
TikTok text, counts or class names as parsing logic. Keep existing YouTube title
behavior covered and unchanged.

The fixture deliberately has distinct synthetic count placeholders and a
synthetic image alternative. Assert that all 15 extracted titles come from the
title field and none comes from the count overlay.

## 3. Preserve `/following` as a zero-impression creator wall

The interactively observed `/following` page remained a grid/list of creator
cards with red Follow buttons after following one creator and reloading. Hover
plays a preview, but the cards are account recommendations, not ordinary video
recommendations.

Keep exact `FOLLOWING` route classification and panel access. Do not broaden
the video-card selectors to capture these creator cards or their hover previews.
If TikTok later renders a genuine video feed for another account state, that
requires a captured fixture and a new correction; WO-098's earlier prose is not
evidence that such cards exist today.

## 4. Add one evidence-gated `LIVE_ROOM` sighting

The active and inactive captures share all of these:

- `main#tiktok-live-main-container-id`;
- `[data-e2e="live-content-container"]`;
- `[data-e2e="live-room-content"]`;
- `[data-e2e="live-header-container"]`;
- `[data-e2e="room-header-anchor-name"]`; and
- a rendered room-header `/@creator` profile link.

Those hooks alone do not establish that the room is active. The inactive state
keeps them for roughly six to eight seconds while displaying an automatic-next-
LIVE countdown and 42 replacement stream cards.

The distinguishing active evidence is:

```css
[data-e2e="live-room-content"] .xgplayer-playing video
```

The active capture has exactly one matching player video; the inactive capture
has no video and no `xgplayer-playing` element. The active player also carries
`xgplayer-inactive`; that class describes hidden/inactive controls and is not an
offline-stream signal.

Update the existing `live.active` selector data with the room-scoped predicate
and add `main#tiktok-live-main-container-id` to `containers.liveRoom`. Reuse the
existing `live.locator` and `live.creator` selector lists by adding room-header-
scoped candidates first:

- the locator candidate is the profile link under
  `[data-e2e="live-header-container"]`;
- the creator name is
  `[data-e2e="live-header-container"] [data-e2e="room-header-anchor-name"]`.

Within a `/live` discovery card those room selectors do not match, so the
existing wall candidates remain the fallback. Do not add a new selector-schema
field or put branching/expressions into selector data.

Replace the deliberate `LIVE_ROOM` empty-return guard in
`extractLiveSightings()` with a separate one-room branch:

1. require the room-scoped active-player selector;
2. derive `channel_id` from the scoped rendered profile link;
3. require the scoped rendered creator name;
4. derive canonical `@handle/live` through one compiled helper from that
   validated handle;
5. emit exactly one sighting at slot 0; and
6. ignore every recommended `/@creator/live` card elsewhere on the room page.

The page URL classifies `LIVE_ROOM` but is not active-state or identity
evidence. Never combine an old header with a newly loaded player. The existing
SPA navigation path must reset the page generation when TikTok advances to the
replacement room, then evaluate that room's own header and player together.

## 5. Room titles are truthfully absent

Interactive visual verification found only the current creator name and
follower count in the room header. Stream titles shown below the room belong to
other recommended streams and must not be copied.

Allow an empty `title` only for `LIVE_ROOM`. Keep a non-empty title mandatory
for `/live` discovery-wall sightings. Do not synthesize a title and do not copy
`channel_name` into the title field. The full-page Live renderer already uses
`title || channel_id || locator`; the daemon `LiveRecord` and swarm validator
already accept an omitted title.

Revision 1 of the extension bridge validator requires a non-empty title. Raise
the optional bridge capability to `live_sightings:2` in the extension and
daemon, and make revision 2 accept a titleless `LIVE_ROOM` while retaining the
revision-1 rule for `LIVE`. A revision-2 extension must not send the relaxed
shape to a daemon that negotiated revision 1.

This is a bridge compatibility revision only. Do not bump the Live gossip or
snapshot protocols: their current record already permits `t` to be absent.

## Tests

Commit sanitized fixture tests proving:

- Explore's real root yields 15/15 impressions, zero failures and stable slots;
- Explore titles come from the configured title field, never like counts;
- YouTube title extraction is unchanged;
- active room yields exactly one `LIVE_ROOM` sighting with the current header's
  locator/creator, slot 0 and empty title;
- removing either the room-scoped video or `xgplayer-playing` state yields none;
- missing/malformed/mismatched header identity yields none;
- the inactive countdown fixture yields none despite its 42 replacement cards;
- active-room replacement cards do not produce sibling sightings;
- `LIVE` wall cards still require and retain their real titles;
- bridge revision 1 rejects titleless sightings and revision 2 accepts them only
  for `LIVE_ROOM`;
- SPA replacement cannot pair one creator header with another room's player;
  and
- selector validation remains bounded CSS data with no new executable field.

Run and report:

```text
npm test
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

## Do not

- Do not read hydration scripts, storage, network responses or TikTok APIs.
- Do not add MAIN-world code or fetch/XHR interception.
- Do not use the route alone as evidence that a room is live.
- Do not index `/following` creator cards or hover previews as videos.
- Do not emit the inactive page's automatic replacement recommendations as the
  current room.
- Do not change Level-1/Level-2 Live consent or gossip policy.
- Do not widen host permissions or add runtime dependencies.

## Stop conditions

Return to the reviewer if the sanitized fixtures do not reproduce the counts
above, if the active predicate also matches the inactive fixture, if identity
cannot be scoped to the room header, or if the compatibility layer would send a
titleless room sighting under negotiated revision 1.

## Implementation record — 2026-08-13

Sanitized captures added as `test/fixtures/tiktok_explore.html`,
`tiktok_live_room_active.html` and `tiktok_live_room_inactive.html`. Privacy
review of the three files was repeated before the copy: no scripts, sources,
handlers or original identifiers remain.

- Explore root is `[data-e2e="explore-item-list"]` first; cards prefer
  `[data-e2e="explore-item"]`. The accepted capture yields 15/15/0 with slots
  0–14. TikTok lockups read the configured title field (image alt) before
  `/video/` link text, so like-count overlays are not catalogue titles.
  YouTube title order is unchanged.
- `/following` route and panel access are unchanged. Creator-card markup is
  not a video selector and still extracts nothing.
- `LIVE_ROOM` is a one-room branch: room-scoped
  `[data-e2e="live-room-content"] .xgplayer-playing video`, header profile
  link, and `room-header-anchor-name`. Title is empty. Replacement cards are
  ignored. The inactive countdown fixture yields none. The page URL classifies
  the surface only; `liveLocatorFromHandle` derives `@handle/live` from the
  rendered handle. SPA navigation still starts a new page generation, then
  evaluates that room's own header and player.
- Bridge capability is `live_sightings:2`. Revision 1 still requires a title.
  A revision-2 extension will not send a titleless room to a revision-1
  daemon. Live gossip/snapshot protocols were not bumped.

```text
npm test                 252/252
go test ./...            ok
go test -race ./...      ok
go vet ./...             ok
git diff --check         ok
```

## Reviewer findings — 2026-08-13

The implementation is not accepted yet. The reported suites pass, and the
reviewer independently repeated `npm test`, `go test ./...`,
`go test -race ./...`, `go vet ./...` and `git diff --check`, but the following
fixture-backed correctness gaps remain.

### 1. Explore still records a like count as `channel_name`

The new `leafMatches()` keeps the innermost `[data-e2e="explore-item"]` and
drops its enclosing grid item. The inner element contains the `/video/` anchor
and like-count overlay but not the sibling
`[data-e2e="explore-card-user-unique-id"]` author row.

On the accepted fixture, slot 0 currently extracts:

```text
channel_id:   @creator001
channel_name: SANITIZED_TEXT_1   # like-count overlay
```

The rendered creator name is `SANITIZED_TEXT_2`. `readLockupFields()` derives
the handle from the `/video/` link and immediately accepts that link's text as
the channel name. The new tests assert title provenance but never assert
creator-name provenance.

Keep one full card root that includes both the public `explore-item` content and
its sibling description/author row. Do not make a global innermost-match rule
for every non-HOME platform surface. Prefer the explicit Explore author link
before a generic `a[href^="/@"]`, and never use a `/video/` anchor's overlay
text as `channel_name`. Add fixture assertions for all 15 creator ids/names and
prove none equals a like-count placeholder.

### 2. The SPA identity test explicitly permits the race it claims to prevent

`surfaceFromUrl()` already derives the route's canonical `live_locator`, but
`buildCtx()` discards it. `extractLiveRoomSighting()` then ignores route
identity entirely. The test context deliberately carries
`@stale.header/live` while the fixture header says `@creator001`, and the test
expects the mismatched sighting to succeed.

The URL must not establish activity or creator identity, but it is a necessary
consistency guard. Carry the route locator into the `LIVE_ROOM` context and
require the independently rendered header locator to equal it. Continue to
require the room-scoped playing video and header name; URL plus player is still
insufficient.

Test both transition orders:

- new route/player with the old header emits nothing; and
- old route with the new header/player emits nothing.

Only matching route, rendered header and active player may emit. This prevents
the six-to-eight-second automatic replacement from attributing the next stream
to the departing creator.

The canonical helper must also accept only a bare validated handle or an exact
rendered `/@creator` profile href. Its current test deliberately accepts
`/@creator/video/<id>`, which is broader than the captured room-header evidence
and should fail closed.

### 3. A player hydrating after the first scan may never trigger another scan

The observer watches `childList` only and `mutationsRelevant()` checks only the
added node with `matches()`; it deliberately never searches an added subtree.
The new mutation selector matches the final `<video>`, but not its
`.xgplayer-playing` parent, the parent wrapper, or
`[data-e2e="live-room-content"]`. If React inserts an already-built player
subtree, the added wrapper does not schedule extraction. If the video exists
first and the player later gains `xgplayer-playing`, the class change is not
observed at all.

Add an observer-level regression beginning from the inactive room, then hydrate
the active player through the actual bounded observation path. Prove one
sighting is scheduled whether the player subtree arrives together or the
playing state changes after insertion. Keep the mutation-storm protection; do
not restore arbitrary subtree searches in the MutationObserver callback.

### 4. Replacement-card badges contaminate the current room

`extractLiveRoomSighting()` calls `extractBadges(root, cfg)` on the entire
`main`. That root contains the current room plus 12 active-capture or 42
inactive-capture recommendation cards. A `VERIFIED`, `SPONSORED` or other badge
on any recommended stream can therefore be attached to the current room.

This is reproducible by placing `Verified` on the first fixture replacement
card: the one current-room sighting incorrectly returns
`badges: ["VERIFIED"]`. The accepted room capture exposes no current-room badge.
Emit an empty badge list for `LIVE_ROOM`, or scope extraction strictly to
captured current-room header evidence. Add a regression proving sibling
recommendation badges never cross onto the room sighting.

Do not mark this order implemented or accepted until these four findings and
their tests are closed.

## Correction record — 2026-08-13

1. Explore extraction uses the grid-item root (`DivItemContainer`) so the
   author row stays in the card. `leafMatches()` is gone. Lockup channel
   links prefer `explore-card-user-link` and never take `/video/` overlay
   text as `channel_name`. Tests assert all 15 ids/names and that none
   equals a like-count placeholder.
2. `buildCtx()` carries `surfaceFromUrl().live_locator`. The room branch
   requires that locator to equal the header-derived locator and still
   requires the playing video and creator name. `liveLocatorFromHandle`
   accepts only a bare `@handle` or an exact `/@handle` profile href.
   Tests cover both mismatched transition orders.
3. `observeOptions("LIVE_ROOM")` watches `class` as well as childList.
   `mutationRecordsRelevant()` is the bounded `matches()`-only path.
   Room-content and `.xgplayer-playing` hosts are in `live.active` after
   the required playing-video predicate. A fixture regression hydrates
   the inactive room both by inserting the playing host and by adding
   the playing class later.
4. `LIVE_ROOM` sightings emit `badges: []`. A replacement card marked
   Verified does not appear on the room record.

## Final reviewer acceptance — 2026-08-13

Accepted. The reviewer independently verified that all four findings are
closed against the sanitized captures:

- all 15 Explore records use the author row for creator identity and the image
  alternative for title, never the like-count overlay;
- a room emits only when route locator, rendered header identity and the
  room-scoped playing video agree;
- inserting the playing host or adding `xgplayer-playing` later both schedule
  the bounded rescan; and
- badges on sibling replacement cards cannot contaminate the current-room
  sighting.

The compatibility path also fails closed: revision 1 rejects a titleless room
at both the extension relay and daemon validator, while negotiated revision 2
accepts it only for `LIVE_ROOM`. No Live gossip/snapshot revision, contribution
policy, permission, storage rule or Following behavior changed.

Independent final gates:

```text
npm test                 21/21 test files pass
go test ./...            all packages pass
go test -race ./...      all packages pass
go vet ./...             pass
git diff --check         pass
```

The accepted implementation and fixtures remain uncommitted in the working
tree; acceptance does not claim that the commit/merge step is complete.
