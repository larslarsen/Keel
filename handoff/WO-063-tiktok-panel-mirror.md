# WO-063 — TikTok panel: the Mirror (no re-rank rails exist)

| | |
|---|---|
| **Addressee** | Engineer (Sr Dev lane) / reviewer |
| **Status** | **Draft — not started** |
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

- [ ] TikTok capture from a logged-out session exists; `selectors_tt.json`
      fields (creator, caption, dwell, hashtags, sound_id, engagement,
      not-interested) verified against it; fixture + test pass.
- [ ] New TikTok fields captured on `impressions` (or sibling), bounded/in-memory,
      never browser storage. Search query hashed, never raw.
- [ ] Panel renders (scoped to `tt`, per WO-057): scroll history, hashtag/sound
      clustering, engagement mirror, not-interested provenance, search→feed
      trace, self-analytics, mute nudge.
- [ ] Peer sharing, if shipped for `tt`, uses video-ID-only edges; hashtag/sound
      clusters are local-only and never exported.
- [ ] No re-rank rail feature exists or is promised; panel is a mirror.
- [ ] `go test ./daemon/...` and `npm run test` pass.

---

## Engineer notes (fill on build)

_Not started._
