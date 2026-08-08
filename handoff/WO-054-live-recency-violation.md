# WO-054 — Live promotion ignores the 1-hour rule (keys off gossip freshness, not observation time)

| | |
|---|---|
| **Addressee** | Engineer (Sr Dev lane) / reviewer |
| **Status** | **Fixed 2026-08-06** |
| **Date** | 2026-08-06 |
| **Source** | Lars, 2026-08-06 — "panel showing a livestream from 6 hours ago; limit is supposed to be 1 hour" + "live streams page super outdated, way more/newer before rebuild" |

`LiveRecency = time.Hour` (suggest.go:38) is the published contract: a stream is
promoted to the top of the panel only if seen live within the last hour. That
contract is violated — streams hours old get promoted, and the live page shows
stale entries. Root cause is a single design error: **promotion and the live
page rank/filter on `LastSeen` (gossip freshness), not on the actual
observation time (`SeenAt` / `observed_at`).**

## The two live paths

1. **Local (correct).** `Store.currentlyLive()` (sqlite.go:63) queries
   `impressions WHERE observed_at >= now - LiveRecency AND badges_json LIKE '%LIVE%'`.
   This uses the *real observation time* and correctly respects the 1h window.
   Verified against the live corpus: rows older than 1h are excluded by this
   query, so a 6h-old stream cannot enter the panel through this path.
2. **Swarm (buggy).** `handleSuggest` (main.go:468-481) calls
   `swarmNode.Live().Search("", 5000)`, filters by `e.LastSeen >= cutoff`
   (cutoff = `now - LiveRecency`), and feeds the result to `SetLiveVideos(ids)`.
   `currentlyLive()` then UNIONS this swarm set with the local set. The swarm
   set is the leak.

## Why the swarm set defeats the 1h window

In `swarm/live.go`:
- `merge()` sets `e.lastSeen = now` on **every** gossip receive
  (live.go:371). Any stream still being announced by *any* node has its
  `lastSeen` continuously refreshed to ~now.
- `liveRefreshAfter = liveTTL/4` (live.go:119) makes nodes **re-announce**
  ageing records so suppression does not let them expire. This keeps a
  long-finished stream's `lastSeen` hot indefinitely, as long as anyone still
  gossips it.
- `Search` (live.go:438) filters on `lastSeen >= liveTTL-ago` (12h, the index
  TTL) and sorts by `LastSeen` (live.go:476). It never consults `SeenAt`.

So a stream that ended 6 hours ago but is still being re-gossiped has
`LastSeen ≈ now`, passes `main.go:473`'s `LastSeen >= cutoff` check, enters
`SetLiveVideos`, and is promoted to the panel — directly contradicting the 1h
rule. The field that *means* "when was this stream actually observed live"
(`LiveRecord.SeenAt`, live.go:128) is never used for the promotion decision.

### Evidence
- Corpus check: LIVE-badged `impressions` older than 1h exist (e.g. several at
  ~2h, and user reports a 6h-old one in the panel). The local query excludes
  them, so the promoted stale stream MUST originate from the swarm
  `SetLiveVideos` path — i.e. from `LastSeen`, not `observed_at`.
- The live page (LIVE_SEARCH → `LiveIndex.Search`) shows the same defect:
  sorted by `LastSeen`, 12h TTL (liveTTL=live.go:94), so dead streams linger
  at the top and recent ones are not guaranteed first.

## Separate but related: in-memory index loses everything on restart

`LiveIndex` is explicitly in-memory only (live.go:145-147, "Nothing here is
written to disk"). On daemon restart it is empty and refills only via gossip
trickle + one snapshot backfill (`backfillLive`, live.go:255). This matches
Lars's "way more/newer before I rebuilt" — the rebuild wiped the index and it
has not yet repopulated. This is *by design* (ephemeral, privacy-preserving),
but the repopulation is slow and the 12h TTL means what does survive is stale.
The promotion violation above is the priority; the repopulation slowness is a
known tradeoff to revisit, not a regression to chase here.

## Fix (minimal, targets the promotion violation)

Promotion must key off **observation time**, not gossip freshness:

- In `handleSuggest` (main.go:468-481), when filtering swarm entries, use
  `e.SeenAt` (the `LiveRecord.SeenAt` carried in `LiveEntry`) instead of
  `e.LastSeen`:
  `if e.SeenAt >= cutoff { ids = append(ids, e.VideoID) }`.
- `LiveEntry` already exposes `LiveRecord.SeenAt` (live.go:124, 466-470) — no
  new field needed; just use it at the filter site.
- For the live page, `Search` should likewise prefer `SeenAt` for both the
  cutoff and the sort, so the feed ranks by when a stream was *observed* rather
  than when this node *last heard gossip* about it. (Keep `LastSeen` only as a
  tiebreaker / index-TTL eviction signal.)

This keeps the design intent (ephemeral, gossip-fed, unsigned) while making the
1h promotion rule actually hold: a stream is promoted only if someone observed
it live within the last hour, not merely if gossip about it is still warm.

## Acceptance
- [ ] A stream whose `SeenAt` is > 1h ago is NOT promoted to the panel, even if
      it is still being gossiped (i.e. even with a fresh `LastSeen`).
- [ ] A regression test seeds the swarm live index with one record
      `SeenAt = now - 6h, LastSeen = now`, runs `handleSuggest`, and asserts the
      video is absent from the promoted/live set.
- [ ] The live page ranks by `SeenAt`; dead (old-`SeenAt`) streams do not sit
      above recently-observed ones.
- [ ] `go test ./daemon/...` passes.

---

# Part 2 — displayed "last seen" is not refreshed by your own observation

**Symptom (Lars, 2026-08-06):** scrolled youtube.com, saw two livestreams that
were already on the panel list — but one showed "last seen 1 hr ago" even though
he had just seen it live. It should say "just now".

**Root cause.** The panel's live list renders `fmtAgo(s.last_seen)`
(extension/page/index.js:230), reading `LastSeen` from the swarm index
(`LiveIndex.Search` → `e.lastSeen`, live.go:509). `LastSeen` is only advanced
inside `merge()` (live.go:371), and `merge` runs when a record is published or
received via gossip.

A local live observation flows through `announceLive` →
`swarmNode.PublishLive` (swarm_runtime.go:111-135). But `PublishLive`
(live.go:547-557) returns early when `!shouldPublish(videoID)`
(live.go:551). `shouldPublish` (live.go:402) returns **false** whenever the
record already exists and `time.Since(e.lastSeen) <= liveRefreshAfter` (3h). So
a stream already in the index with `lastSeen` ~1h ago — exactly the backfilled
entry Lars saw — fails `shouldPublish`, `PublishLive` bails, `merge` never runs,
and `lastSeen` is never refreshed to now. The displayed "1 hr ago" is the
*peer's* sighting time from backfill, not Lars's just-now observation.

**Why this is a distinct defect from Part 1.** Part 1 was about *promotion*
keying off `LastSeen`. This is about the *displayed timestamp* not reflecting a
local observation. The suppression's intent is to save network traffic (don't
re-gossip a stream that is already hot) — but it also suppresses the **local**
`merge`, which refreshes the on-device timestamp and costs nothing locally. The
fix decouples "update my own index" from "gossip to peers".

**Fix.** In `PublishLive` (live.go:547), always call `merge(r)` locally, and
gate only the `topic.Publish` (gossip) on `shouldPublish`:

```go
func (n *Node) PublishLive(ctx context.Context, r LiveRecord) {
    if n.live == nil {
        return
    }
    n.live.merge(r) // always refresh local index timestamp from our own sighting
    if !n.live.shouldPublish(r.VideoID) {
        return // but only gossip when due
    }
    if err := n.live.Publish(ctx, r); err != nil {
        n.logf("live publish %s: %v", r.VideoID, err)
    }
}
```

`merge` is idempotent (Publish also calls it at live.go:429), so the extra call
is harmless. `merge(r)` uses the unrounded `SeenAt` from the caller
(`imp.ObservedAt`, swarm_runtime.go:129), so the local index gets the accurate
observation time, and `lastSeen`/display update to "just now" immediately. No
privacy change: at Level 1 nothing is *published* (gossip still gated), but the
node's own view of what it just saw is now correct — which `currentlyLive()`
(local `impressions`) already provided; this just makes the swarm-index display
consistent with it.

**Acceptance (Part 2).**
- [ ] A node at any level that locally observes a LIVE badge refreshes that
      stream's `lastSeen` in its own index, so the panel shows "just now"
      (within 90s, per `fmtAgo`), even when `shouldPublish` would suppress
      gossip.
- [ ] Regression test: seed index with `lastSeen = now - 1h`, call
      `PublishLive` with a fresh `SeenAt = now` for the same video, assert
      `LastSeen` advanced to ~now (not still 1h ago), but assert `topic.Publish`
      was NOT called (gossip still suppressed).
- [ ] `go test ./daemon/...` passes.


---

## Engineer response — 2026-08-06

Diagnosis confirmed in full, and the bug was mine — I wrote both the
re-announcement logic and the `LastSeen` filter. `LastSeen` means "when gossip
about this last arrived", which stays warm for as long as anyone is still
passing a record around, so it can never express "seen live within the hour".

Fixed as specified: promotion filters on `SeenAt`, and the live page filters and
ranks on it too, with `LastSeen` kept only as a tiebreaker and as the eviction
signal for index size. Regression test added — a record with `SeenAt` six hours
ago and a warm `LastSeen` is ranked below a ten-minute-old stream and is not
promoted.

Two things beyond the ticket:

**Records with no observation time are now rejected**, by the topic validator
and by `merge`. One cannot be ranked or filtered honestly, so forwarding it puts
an unplaceable entry in every node's index. This surfaced as a test failure the
moment ranking moved to `SeenAt`, which is the test doing its job.

**The repopulation problem is fixed too**, rather than deferred. You were right
that it is by design and separate — but the fix costs nothing, because this
node's own sightings are already on disk in `impressions`. The index is now
seeded from them at startup, keeping their true observation times. On the live
corpus that took the Live tab from near-empty after a rebuild to 22 entries,
ranked correctly. Nothing new is persisted and the index stays in-memory.

Good catch, and the analysis was precise enough to act on directly — the two
paths, the exact fields, and the reason the local path was innocent.
