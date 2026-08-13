# WO-095 — The word-level bar should track live search progress, not a static ratio

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Designed in conversation, not yet built** — the mechanism below is settled; the engineering (wire shape, streaming mechanism, state lifecycle) is not |
| **Date** | 2026-08-13 |
| **Source** | Lars, walking Claude Sonnet through the design after Sonnet repeatedly guessed wrong and over-built alternatives that weren't asked for |

## This amends WO-068

WO-068 built the word-level top-tier bar as a single, one-shot statistic:
`CMS(graphs containing word) / HLL(graphs total)`, computed once when the UI
opens and explicitly documented as **"Separate from `renderPeerProgress`"**
and **"NOT a search/fetch axis."** That boundary is superseded by this
ticket. Everything else WO-068 built — the global word/graph HLL, the
per-word percentage as a concept, the direct on-demand (not gossiped) fetch
of the global counts — stays exactly as built. It becomes the **denominator**
for what this ticket adds.

## The problem

The current bar answers "how common is this word in the corpus," a fact that
is already fully known the instant it's computed — there is nothing for the
bar to progress toward, and watching it fill is meaningless. What the bar
should show instead: **as a distributed search for this word actually runs,
how many of the word's real, global occurrences has this search found so
far.** That is a genuine, live, incrementing quantity, and today nothing
computes or displays it.

## The mechanism (settled)

1. **Denominator — unchanged from WO-068.** The global word count
   (`WordGraphCount`/`DistinctGraphs`, from `FetchWordStats`'s on-demand,
   non-gossiped sketch fetch) is fetched once, up front, and used as-is. This
   ticket does not touch how that number is produced.

2. **Numerator — new.** A word chops into a sequence of ShardK tokens (e.g.
   "world" → "wor", "ld "). As `PeerSearch` fetches the shard/bucket for each
   of a word's constituent tokens, a video counts as a confirmed match for
   the *word* when it is present in the intersection across **all** of that
   word's token-buckets — the exact same matching primitive
   `resolveShardEntries` (`daemon/swarm/shard.go`) already uses today for
   ordinary multi-token query intersection (`for _, t := range e.Tokens { if
   t == token {...} }`). **No title is ever looked up, fetched, or sent.**
   This is a pure extension of the token-intersection logic that already
   exists for search, using the word's own tokens as if it were itself a
   multi-token query, purely to count matches — it does not have to actually
   run as a user-visible search.

3. **The running match count only ever increases.** A confirmed match stays
   valid even if the same bucket is later re-queried against a different
   peer. Never reset the numerator.

4. **Progress = confirmed matches so far / global word count — never buckets
   fetched / buckets planned.** Match yield is unevenly distributed across
   buckets; a bucket-fraction metric would misrepresent progress (e.g.
   reading "80% done" after 8 of 10 planned bucket fetches, while the actual
   match count sits at 8% because most real matches were sitting in the one
   bucket not yet fetched). The bar is allowed — expected — to sit still and
   then jump when a high-yield bucket lands. That is correct behavior, not
   something to smooth over.

5. **Separate, orthogonal concern: the per-bucket live-fetch display.** A
   bucket can be queried more than once, from different peers (this already
   happens — see `FetchShard`'s existing multi-peer accumulation). Whatever
   currently shows a bucket/token's in-flight fetch state — believed to be
   `renderPeerProgress`'s existing bars, **not confirmed** — resets to show
   the newest peer's individual response each time that specific bucket is
   re-queried, as it loads. This is distinct from item 3 above: the
   cumulative match count backing the word bar never resets; only the
   transient "what's the latest activity on this one bucket" display does.

6. **Daemon owns all of it. The extension is a thin client.** All tracking,
   counting, and intersection state lives in the daemon. The extension only
   renders whatever the daemon sends — no computation, no local state beyond
   display.

## Confirmed vs. not confirmed

Everything in "The mechanism" above was explicitly walked through and
confirmed in conversation. Two things were **not** fully nailed down and need
resolving before or during design, not assumed:

- Whether "the bar graph for it" in item 5 really is `renderPeerProgress`'s
  existing bars, or a different/new UI element — Lars said "as it loads" in
  response to the question rather than a direct yes/no.
- What "the bars at the very top" (raised earlier, separately, as a thing
  Lars said he doesn't understand either) actually are — never resolved.
  Worth clarifying before assuming it's the same element as the above.

## The architectural gap this requires

`PEER_SEARCH` replies exactly once, at the end
(`handlePeerSearch` → single `reply(out, env.ID, "PEER_SEARCH_RESULT", ...)`,
`daemon/main.go`). There is no streaming or broadcast of progress during an
active search today. "Live update as it loads" cannot be built as a data-shape
tweak to the existing request/response — it needs a genuine new delivery
mechanism: something that pushes progress *during* a search, shaped more like
`CONTRIBUTION_STATUS`'s broadcast-to-every-connected-browser pattern than
like a normal correlated RPC reply, scoped to one in-flight search rather
than global daemon state. Designing that mechanism — and the daemon-side
state lifecycle for per-word match tracking during a search (when it starts,
when it's torn down, what happens if the browser closes mid-search, whether
concurrent words in one query share a search pass) — is the actual
engineering work this ticket hands off.

## Do not

- Do not fetch or send video titles for this. The token-intersection check
  is the whole mechanism; if it needs a title, the design has drifted from
  what was agreed.
- Do not use buckets-fetched/buckets-planned as a progress proxy, even
  temporarily or as a fallback. See item 4.
- Do not put tracking or counting state in the extension. It renders; it
  does not compute.
- Do not silently redefine WO-068's existing static percentage — that
  computation and its denominator stay as built. This ticket adds a second,
  live numerator on top, it does not replace the first.

## Acceptance

- [ ] Global word count (denominator) is unchanged from WO-068's existing
      fetch — no regression there.
- [ ] A running per-word match count exists daemon-side, built from
      token-intersection over a word's own constituent tokens, using the
      same primitive as ordinary multi-token search intersection.
- [ ] The match count never decreases and is never reset by a bucket being
      re-queried.
- [ ] The displayed word-progress bar's fill is driven by match-count /
      global-count, not by fetch attempts.
- [ ] Some live delivery mechanism gets progress to the browser during an
      active search, not only in a final result.
- [ ] The extension-side change is render-only — confirm no new
      computation or tracked state was added there.
- [ ] The "confirmed vs. not confirmed" open questions above are resolved
      (with Lars, not assumed) before the per-bucket reset behavior is built.
