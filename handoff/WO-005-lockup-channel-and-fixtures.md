# WO-005 — Live QA failed: lockup `channel_id`, and fixtures that were not captured

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Closed — §1, §2, §3 all done and live-verified. |
| **Date** | 2026-08-02 |
| **Follows** | WO-004 (implementation landed; **live QA fails**) |
| **Source** | Live QA on Brave against the WO-004 working tree |
| **QA result** | §1 closed live. §2 JSON capture done; walk + browseId fixed against it; DOM fixtures de-fabricated (real public IDs; full outerHTML still preferred). §3 closed. **Needs live Brave re-QA for channel_id enrichment.** |

## QA result — 2026-08-02, after the §1 fix

Live Brave, two hard-loaded watch pages, corpus queried directly:

| | |
|---|---|
| Page loads | 2 |
| Rows per load | 20 |
| Slot range | 0–19, contiguous |
| Distinct videos per load | 20 |
| Duplicate rows | 0 |

WO-004 §4 and §5 are confirmed on live data rather than fixtures. Daemon suite passes with
`-count=1`. **§1 is closed.**

**Was: `channel_id` NULL on 100% of rows.** Root cause confirmed against the real capture: live
`browseId` lives on the lockup **avatar** tap path
(`metadata.lockupMetadataViewModel.image.decoratedAvatarViewModel…browseEndpoint.browseId`), not
metadataRows; a 200-node last-resort walk died inside `contentImage` first. Fixed: avatar path
first, metadata-first fallback walk, budget 2000. Observer builds `video_id → channel_id` from
ytInitialData once per nav and enriches DOM impressions (DOM still owns `slot_index`; JSON rows
are not emitted). **Live re-QA required** to confirm enrichment on Brave.

### Live re-QA — 2026-08-02, PASSED

Corpus queried directly after a fresh watch-page load with the enrichment in place:

| | |
|---|---|
| Rows in latest page load | 20, `slot_index` 0–19 contiguous |
| `channel_id` populated | **20 / 20** |
| Malformed (not `UC` + 22 chars) | **0** |
| Distinct channels across the rail | 14 |

Sample: `UCXN7rPhZK6Rp8lMhvpSri_Q` at slots 3 and 7 — two videos from one channel in a single rail,
which is the signal this corpus exists to record.

**§1 and §2 are closed.** The avatar-path `browseId` fix works against live YouTube, not just the
fixture.

**Corpus note for analysis:** 807 rows predate this fix and have `channel_id` NULL /
`channel_unknown = 1` permanently — they cannot be backfilled without re-visiting those exact page
loads. Any channel-level analysis must filter on `channel_unknown = 0` or it will silently mix two
eras of data collection.

### Open after this round — reviewer notes, 2026-08-02

**A. Browser froze once on tab-switch; not reproducible.** Reported after the armMo/emit/enrichment
round, on switching away from the watch tab rather than while on it. Could not be reproduced and no
profile was captured, so there is nothing to act on. Recorded only so a second sighting is not
treated as new. It does **not** match the WO-004 §2 profile, which was continuous jank while
scrolling the watch page itself — that remains fixed.

If it recurs, the thing to capture is a DevTools Performance recording spanning the tab switch, and
the widest self-time bar in Bottom-Up. Do not go looking for it otherwise.

**B. DOM fixture is now a real capture — CLOSED 2026-08-02, and it paid for itself immediately.**
`watch_next_lockup.html` is a captured `#related` subtree from a live watch page: 4 cards kept of 20
(3 with duration badges, 1 live), scripts/styles/`data-*`/signed media params stripped, no session
identifiers. 62 KB.

**It found a defect the hand-authored fixture could not, on the first run.** `view_count` was null on
every non-live card. The lockup path scanned `span, yt-formatted-string` for text matching `/view/`,
but real metadata rows are **divs**, and the rail **omits the word "views" entirely** — rows read
`"578 watching"`, `"21K 1h ago"`. The synthetic fixture had written `"1.2K views"` into a span, so
the selector matched the fixture and nothing else. Fixed: scan metadata rows, take the leading count,
keep the remainder as `published_at`.

One thing that was *not* broken, recorded so nobody re-fixes it: the duration selector
`badge-shape .yt-badge-shape__text` never matches real markup — the real class is `ytBadgeShapeText`
— but the `span, badge-shape` fallback catches it. Dead selector, working extraction.

The lockup test now asserts properties, not literals: `impressions === candidates`, contiguous
`slot_index` from zero, and at least one duration, one view count and one LIVE badge. Recapturing the
fixture no longer forces a test edit.

Residual: `watch_next_mixed.html` and `watch_next_compact.html` are still hand-authored. Both cover
`ytd-compact-video-renderer`, which the live capture shows is **extinct on watch-next** (0
occurrences), so they are historical regression cover and cannot be recaptured from a current page.
Leave them.

Prior context follows.

WO-004's code is largely correct — the scheduler, the bounded walk, retention removal, the PK change
and `slot_index` all look right. But on a live watch page the extension still emits **zero
impressions**, and the reason is a defect that the new tests are structurally unable to catch.

Do not start by re-reading the extractor. Start at §2.

---

## 1. BLOCKER — every lockup card is discarded

On a live hard-loaded `/watch`, the SidePanel shows `0` / `0`. The container resolves correctly and
holds the cards, so this is not WO-004 §1 recurring.

Measured in the page console over `#related`, 20 cards sampled:

```
{ tag: "YT-LOCKUP-VIEW-MODEL", sampled: 20, ok: 0, noId: 0, noTitle: 0, noChan: 20 }
```

Every card yields a `video_id` and a `title`. **Not one yields a channel.** A scan of the same cards
for `UC[\w-]{22}` anywhere in their markup returns `null` — the rendered lockup carries no channel ID
at all, and no `/channel/…` or `/@…` anchor.

`readLockupFields` (`extract.js`) requires one and returns `null` without it:

```js
const channel_id = channelIdFromHref(chA?.getAttribute("href"));
if (!channel_id) return null; // no name: fallback — count as failure
```

So all 20 cards become failures and no impression survives.

**This is my fault, not yours.** WO-004 §6.3 said "prefer returning null (counted as a failure), **or**
flag the record so analysis can exclude it," and you took the first branch. On
`ytd-compact-video-renderer` that was safe — those cards do carry a channel anchor. On
`yt-lockup-view-model` it turns "exclude a bad channel ID" into "discard the entire corpus."
**§6.3 is amended: flagging is required, discarding is forbidden.** A record with a usable
`video_id`, `title` and `slot_index` is research-grade data even when the channel is unknown; slot
position is the measurement, and throwing the row away to protect a secondary field is the wrong
trade.

### Fix

1. **Get the real ID from the JSON, not the DOM.** `lockupViewModel` in `ytInitialData` carries the
   channel `browseId` even when the rendered card drops it. The ytInitialData path should supply
   `channel_id`; the DOM path should not be the primary source for it.
2. **The DOM path must flag, not discard.** Emit the record with `channel_id: null` and a marker the
   corpus can filter on. Never synthesise `name:${displayName}` — WO-004 §6.3's original reasoning
   stands, display names collide and mutate.
3. **Schema blocks this today.** `store/sqlite.go` declares `channel_id TEXT NOT NULL` in both the
   live table and `impressions_v2`. Making it nullable is a migration, and you have just written one,
   so fold it into the same version bump rather than adding a second.
4. Decide and write down whether a null-channel row is a `failure` for metric purposes. It should not
   be — `failures` must keep meaning "a card we could not parse," or §1-class diagnosis breaks again.

---

## 2. ROOT CAUSE — the fixtures were authored, not captured

This is the item that matters. The extractor bug above is ordinary; it survived because the tests
cannot see it.

`test/fixtures/watch_next_lockup.html` contains:

```
href="/channel/UClockup2222222222222222"
href="/@lockupchannel1"
href="/watch?v=lockup00001"
```

Across the new fixtures the channel IDs are `UCjson111111111111111111`, `UCjsonlock11111111111111`,
`UClockup2222222222222222`, `UCmixed33333333333333333`. These are hand-typed placeholders. **The
fixtures were not captured from YouTube.**

The consequence is specific and severe: the synthetic lockup fixture gives every card a channel
anchor, which real lockup cards do not have. `readLockupFields` was then written to satisfy that
fixture. All 15 tests pass. The product extracts nothing. This is the exact defect WO-004 §1
identified — "the tests passed throughout because the fixtures describe a YouTube that no longer
exists" — reproduced inside the fix for it, one commit later.

The `candidates > 0` guard does not help, because a fabricated fixture always has candidates.

### Measured against real `ytInitialData` — 2026-08-02

Live watch page, `contents.twoColumnWatchNextResults.secondaryResults`:

| | |
|---|---|
| bytes | 638,704 |
| `lockupViewModel` | 40 |
| `compactVideoRenderer` | **0** |
| `browseId` | 40 |
| sample UC ids | `UCdy1IW4I7DnkU_3v0zMWDpQ`, `UC7PmuDA-nOZrp6Hq0vITxqU`, … |

Three consequences:

1. **Channel identity is recoverable.** The JSON carries a real `browseId` per lockup. The walk's
   failure is a fixture artefact, not missing data.
2. **`compactVideoRenderer` is extinct on watch-next.** `watch_next_compact.html` is a historical
   fixture, not a current one. Keep it if you want regression cover for old pages; do not treat it
   as representative.
3. **40 JSON entries vs 20 DOM cards — resolved by measurement, and my earlier reading was wrong.**
   An earlier revision of this WO warned that the 40 entries might not map 1:1 to the 20 rail
   positions. Measured against the real capture, they do:

   - 40 `lockupViewModel` keys, **none nested inside another** — they are 40 sibling occurrences.
   - Only **20 unique `contentId`s**. Each video is listed twice.
   - The **first occurrence** of each unique video sits at walk indices **0–19, contiguous, in
     order**.

   So `extractFromYtInitialData`'s existing `seen` dedup already produces 20 impressions with
   `slot_index` 0–19. No special handling is needed for the duplicate half.

   **The DOM path should still own `slot_index`**, but on narrower grounds than previously stated:
   the DOM *is* visual position by definition, whereas JSON walk order is only very probably rail
   order — that correspondence has not been verified against a same-page-load DOM capture, and it is
   not worth assuming for the one field the whole corpus is built on. Use the JSON for
   `video_id → channel_id` enrichment. If someone later verifies the ordering matches, this
   constraint can be relaxed; until then it costs nothing.

### Fix

1. ~~**Recapture from a live page.**~~ **DONE 2026-08-02 for the JSON blob.**
   `test/fixtures/yt_initial_watch.json` is now a real capture of
   `contents.twoColumnWatchNextResults.secondaryResults` from a live watch page, wrapped back into
   real `ytInitialData` shape, 501 KB. Scrubbed of `clickTrackingParams` (1013),
   `trackingParams` (552), `loggingDirectives` (200), `serializedShareEntity` (40),
   `playerParams` (38), and continuation tokens; verified zero residual tracking keys. 40 lockups,
   40 `browseId`s, 19 unique channels, 20 unique videos survive.

   **`test/extract.test.js` "compact + lockupViewModel" now FAILS, and that is intentional** — it is
   the red test this WO asked for, proving the walk does not handle real data. Do not delete or skip
   it; make it pass. `npm test` is 14/15 until then.

   **Still outstanding: the DOM fixtures.** `watch_next_lockup.html` and `watch_next_mixed.html` are
   still hand-authored. Capture them the same way, per `test/README.md`.
2. **Add a fixture-authenticity guard test** that fails on fabricated data. Cheap, decisive checks:
   video IDs must match `[A-Za-z0-9_-]{11}` and must not be sequential or repeated-character;
   channel IDs must match `UC[A-Za-z0-9_-]{22}` and must not contain runs of a repeated character.
   `UClockup2222222222222222` and `lockup00001` both fail on sight.
3. **Add one assertion that encodes the live finding**: in the lockup fixture, cards have **no**
   channel anchor, and extraction still yields impressions with `channel_id` null-and-flagged. That
   test is the one that would have caught this, and it must fail against the current extractor.

---

## 3. Carried over from the WO-004 review — still open

Raised at review, not addressed in the implementation. None are blockers.

- **3.1 `armMo()`'s retry dead-ends.** ~~A single guarded retry; if the rail is slower than 750 ms
  nothing re-arms.~~ **DONE 2026-08-02, verified.** Bounded retry, 10 attempts at `THROTTLE_MS`
  (~7.5 s), `armAttempts` reset on success and in `onNavigate`, `armRetryTimer` cleared before the
  non-watch early return so a pending retry cannot leak onto another page. Warns on give-up.
- **3.2 `ytd-compact-radio-renderer`.** **CLOSED 2026-08-02.** Live secondaryResults capture has
  `compactVideoRenderer` = 0 and lockups only. Mix/playlist shelves are not P0 video impressions.
  If a distinct non-video component reappears in the rail it must consume a slot without a
  `video_id`; until then leaving it out of `CARD_SEL` does not drift video slot indices.
  Recorded in `test/README.md`.
- **3.3 `emit()` re-sends `failures` on every scan.** **DONE 2026-08-02.** `emit` reports failure
  *delta* only (`lastEmittedFailures`), reset on navigate.
- **3.4 First-observation-wins can lock in a partial slot.** **CLOSED — keep first-observed.**
  Unobserved insertions above a card are inherent; rewriting slots every scan thrash the corpus.
  Revisit if live QA shows systematic drift. Recorded in `test/README.md`.
- **3.5 `"alarms"` vs `AGENTS.md`.** **DONE 2026-08-02 by reviewer** — `AGENTS.md` now lists
  `alarms` and says why, so a future agent does not strip it as unused.
- **3.6** **DONE 2026-08-02** — `go test -count=1 ./...` run against the new store; `bridge` and
  `store` both pass.

---

## Acceptance

- [ ] Live `/watch` on Brave produces `IMPRESSIONS` with ~20 records **and non-null `channel_id`**
      on rows whose lockup has a browseId in ytInitialData; SidePanel shows a numbered list.
- [x] Cards with no channel anchor are recorded and flagged, never discarded.
- [x] `channel_id` is sourced from `lockupViewModel`'s avatar `browseId` (unit-tested on real capture).
- [x] Schema permits a null `channel_id`, in the same migration as the WO-004 PK change.
- [x] P0 fixtures have no placeholder IDs (authenticity guard). JSON is a real capture.
      DOM HTML still simplified structure with real public IDs — full outerHTML recapture optional.
- [x] A guard test fails on synthetic-looking video or channel IDs.
- [x] Lockup fixture has no channel anchors; extraction yields `channel_id` null + `channel_unknown`.
- [x] §3 items each either fixed or answered in writing.

---

## Pushback invited

§1's framing is a correction to my own §6.3 and I would rather be told if the flagging contract is
wrong. §2 is not negotiable — a fabricated fixture is worse than no fixture, because it converts a
red test into a green one. If capturing a real `ytInitialData` blob is impractical for some reason,
say so and we will find another way to pin the JSON path; do not hand-write one.
