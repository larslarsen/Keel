# WO-044 — Channel display name under panel thumbnails

| | |
|---|---|
| **Addressee** | Jr Dev (opencode) |
| **Status** | **Done** |
| **Date** | 2026-08-04 |
| **Source** | Lars, 2026-08-04 |

---

## Problem

The panel's sub-line showed a channel only when the `channel_id` happened to be an
`@handle` (`readableChannel`). Most cards carry a `UC…` channel key or no channel
link at all, so the channel was effectively invisible. Lars: "We used to show the
channel name in the panel items. There is a little room under the thumbnail, could
we put the short at symbol form of the channel name there in small text?"

## Behaviour decided

**Extension reads the display name off each card; the daemon stores it.** Chosen by
Lars over the alternative (daemon derives names from a server): the name is already
in the card's DOM/JSON, so sending it with each impression costs nothing and the
daemon keeps a durable `channels` table the catalogue can later release.

- Under each panel thumbnail, in small text: the channel display name.
- The daemon persists `channel_id → name` in a `channels` table (upsert per
  impression). Channel names are public facts about public videos, so they may be
  exported like the rest of the catalogue.
- The name is removed from the sub-line (it moved under the thumbnail).

## How the name is found

The two extraction paths differ:

- **Compact cards** (`ytd-compact-video-renderer`, `ytd-rich-grid-media`): the name
  is the `ytd-channel-name a` / `#channel-name a` anchor text.
- **Lockup cards** (`yt-lockup-view-model`, current watch-next DOM): there is **no
  channel anchor** — the name is the first `ytContentMetadataViewModelMetadataRow`
  with no leading icon (row 2 is "1.2K views · 3 days ago"). This was the gap that
  made the feature appear dead on the live page: the initial version only read
  anchors, `channel_name` was null on every lockup card, and nothing rendered.
- **ytInitialData**: `fieldsFromLockup` reads the name from `metadataRows`; the
  observer's `channelByVideo` map now stores `{ channel_id, channel_name }` and
  backfills a missing DOM name.

`channel_name` is nullable end to end: the DOM often omits it, and validation
(`protocol.js`) and the daemon both treat null as "unknown", not a failure.

## The change

- `extension/content/extract.js` — `channel_name` in `readCompactFields` and
  `readLockupFields` (incl. the metadata-row fallback above).
- `extension/content/extract_yt.js` — `channel_name` in `fieldsFromCompact` and
  `fieldsFromLockup`.
- `extension/content/observer.js` — `channelByVideo` stores name + id; `enrichChannels`
  backfills both.
- `extension/lib/protocol.js` — `channel_name` validated (nullable string), output as
  `r.channel_name ?? null`.
- `daemon/bridge/protocol.go` — `ChannelName *string` on `Impression`.
- `daemon/store/sqlite.go` — `channels` table; upsert on `PutImpressions` when both
  id and name are present. A null name never clobbers a stored one.
- `extension/sidepanel/index.js` + `style.css` — name under the thumbnail
  (`.thumb-wrap` / `.chan`), sub-line entry dropped.

## Testing

- 27 JS tests pass (new: compact name "Rick Astley", lockup metadata-row name
  "Tommy TV", ytInitialData name + non-empty/null invariant, validateImpression).
- Daemon suite passes (new: `TestChannelsTableUpsertsName`).
- Live QA on Brave: after a **hard reload of the YouTube tab** the lockup names
  appeared under the thumbnails. Content-script changes are not picked up by an
  extension reload alone (see WO-043 "Testing").

## Acceptance

- [x] Panel shows the channel display name in small text under each thumbnail.
- [x] Lockup cards (no channel anchor) still get their name from the metadata row.
- [x] Cards without any name render no label — never a blank line.
- [x] Daemon persists `channel_id → name`; null names never clobber stored ones.
- [x] `channel_name` nullable in protocol and bridge; no validation regressions.
- [x] 27 JS tests + daemon suite pass.

## Pushback invited

`channel_name` is the display text as read off the card (e.g. "Tommy TV"), not the
`@handle`. Lars's phrasing "short at symbol form" is satisfied only when the card
itself carries an `@`-form; deriving `@name` from a plain name would be fabricating
data. If the panel should prefer the `@handle` when both exist, that is a follow-up.
