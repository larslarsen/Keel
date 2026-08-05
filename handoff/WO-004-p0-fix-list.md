# WO-004 — P0 fix list (single prioritised queue)

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Implemented, **live QA failed** — see WO-005. Do not treat as closed. |
| **Date** | 2026-08-02 |
| **Supersedes** | WO-002 and WO-003 — **read this one only**, everything is folded in below |
| **Source** | Live QA on Brave + code review @ d7e00e9 |
| **Amended** | 2026-08-02 by reviewer @ cf44ba3 — §1 diagnosis narrowed, §2b promoted, §6.4 and §10 added. Amended in place rather than issued as WO-005 so this stays the single document to read. |

Work top to bottom. Items 1–2 make the product non-functional; everything below is correctness.

---

## 1. BLOCKER — nothing is extracted on live YouTube

QA on a hard-loaded `/watch?v=…`: `PAGE_CONTEXT` is sent, **no `IMPRESSIONS` ever is**. No
`[Keel SW] invalid` lines, so the validator never saw a record. Both extract paths return
`{impressions: [], failures: 0}` — the empty-with-no-failures signature means the candidate list was
empty, not that parsing failed.

Meanwhile the page has the data: `document.querySelectorAll('ytd-compact-video-renderer,
yt-lockup-view-model').length` → **20**, and the `ytInitialData` blob is present.

### Diagnosis (amended 2026-08-02 — read this instead of the original three-candidate list)

The original list named three candidates and asked for instrumentation to pick one. Static review
eliminated one, confirmed one, and replaced a third. **The two extract paths failed for different
reasons**, which is the important correction: fixing one leaves the other silent.

**Candidate 3 — `buildCtx()` returned null — is eliminated by direct observation. Do not spend a
cycle here.** The original WO noted QA had not recorded `PAGE_CONTEXT.surface`. It has now been
captured on a live hard-loaded watch page:

```
KEEL-DIAG PAGE_CONTEXT tab 192289913
{"surface":"WATCH_NEXT","pageLoadId":"df0f43b4-65fe-4af2-a964-3434137ed341",
 "href":"https://www.youtube.com/watch?v=zovSXVJq88I"}
```

`surface` is `"WATCH_NEXT"`, so `buildCtx()` returned a context and `surfaceFromUrl` parsed the
11-char `v` correctly. Both `observeInitial()` and `observeDom()` therefore ran to completion and
`armMo()` armed. This independently corroborates the inference from §2: had `ctx` been null,
`onNavigate` would have returned at `observer.js:109` and neither freeze source could have fired.

**ytInitialData path — candidate 2 confirmed.** `extract_yt.js:127` whitelists
`["compactVideoRenderer", "videoRenderer"]`. Current watch-next serves **`lockupViewModel`**, so
`collect()` walks the whole blob and matches nothing. `CARD_SEL` (`extract.js:204`) already includes
`yt-lockup-view-model`; the DOM path knows about the new component and this one does not. They have
drifted apart.

**DOM path — candidate 1 as written does not fit the evidence.** `CARD_SEL` *does* match
`yt-lockup-view-model`. Had `querySelectorAll` returned the 20 live cards, `readCardFields` — written
against `ytd-compact-video-renderer` internals — would have failed on each, giving `failures: 20`.
QA reported `failures: 0`. So the candidate list was genuinely empty **at the moment of the call**,
which is a timing fact, not a selector fact.

**The DOM path's actual cause is the starving debounce now filed under §2b — measured, not inferred.**
A bare `MutationObserver` on `#related` (`childList: true, subtree: true`, no extension involved) on
a live hard-loaded watch page recorded:

| Run | Mutations in 10 s | Rate | Mean gap |
|---|---|---|---|
| Idle | 13,491 | ~1,349/s | 0.74 ms |
| Scrolling | 14,470 | ~1,447/s | 0.69 ms |

The debounce at `observer.js:76` is 500 ms and resets on every mutation. It would need a 500 ms hole
in a stream arriving every ~0.7 ms. There is no such hole, idle or scrolling. **`observeDom()` runs
exactly once per navigation — the premature call at `observer.js:118` — and never again.**

Why that one run finds nothing: the content script is injected at `document_idle`
(`manifest.chrome.json:11`), at which point `#related` exists in the server-rendered shell but its
cards are not yet populated. So `observeDom()` correctly reports 0 cards and 0 failures — the
reported signature — and the scheduler that should have re-run it after the cards arrived never
fires. "No `IMPRESSIONS` ever" follows exactly.

**Both causes are now established; no further diagnosis is needed before fixing.** The original
instruction to instrument three branch points is withdrawn — candidate 3 is dead by capture,
candidate 2 is plain in the source, and the DOM path is measured above. Fix both.

Both fixes are required for §1 to close. §2b is no longer optional polish; it is half of this
blocker. A fix that addresses only the whitelist will still emit nothing from the DOM path, and a
fix that addresses only the scheduler will still miss every `lockupViewModel` in `ytInitialData`.

**Third fix, latent behind the other two: `readCardFields` is written for the old component.**
It has not caused a failure yet only because no card has ever reached it. The moment the scheduler
is fixed and cards are found, every `yt-lockup-view-model` will fail parsing and `failures` will
jump to ~20. Expect that, and fix it in the same pass.
`yt-lockup-view-model` has different internals — `a#thumbnail[href]` and `#video-title` are
`ytd-compact-video-renderer` selectors. Support both component shapes behind one interface, and keep
the DOM path and the ytInitialData whitelist in sync — divergence between them is what produced this.

**Fixtures are stale and that is the real defect.** WO-001 §5.2 predicted this rot; the tests passed
throughout because the fixtures describe a YouTube that no longer exists. Required:

- Recapture fixtures from live YouTube (both component shapes, plus a `lockupViewModel`
  `ytInitialData` blob).
- Add a **documented refresh procedure** in `test/README.md`: how to capture, scrub, and commit a
  fixture. Without it this recurs every few months and the tests keep passing while the product is
  broken.
- Add one test that **fails loudly when zero cards are found** in a fixture known to contain cards.
  A silent empty result must never again be a passing state.

## 2. BLOCKER — the observer freezes the browser

Brave hard-froze repeatedly during QA, requiring a kill. Two costs, both on the main thread:

**2a. `extract_yt.js:57` `collect()` walks the whole parsed `ytInitialData` recursively** — multi-MB
— on every navigation. It also calls `Object.values(node)` on every object (allocating an array per
node), and recurses *into* a node it has already matched, so nested renderers are re-walked.

Fix: parse once per navigation, never per mutation. Walk iteratively with an explicit stack rather
than recursively. Do not re-descend into a matched renderer. Bound total nodes visited and bail with
a logged counter if exceeded. Consider targeting the known path
(`twoColumnWatchNextResults.secondaryResults`) with the full walk as fallback.

**2b. `observer.js:83` `armMo()`** attaches a `subtree: true` MutationObserver over the container on
a page that mutates near-continuously, driving a 500 ms-debounced `querySelectorAll` over the whole
container.

Note the debounce at `:76` is a **resetting** debounce — every mutation clears the pending timer. On
a continuously mutating page it can starve and never fire at all.

> **Amended 2026-08-02 — this is not second-order.** The original text filed starvation as a minor
> bug to confirm in passing. Review promotes it: it is the DOM path's half of blocker §1. The
> extension is not merely slow because of it, it emits nothing. Do not close §1 by fixing
> `container()` or the `lockupViewModel` whitelist alone — if `schedule()` still starves, the DOM
> path stays silent after its one premature run at `document_idle`.

Also note `armMo()` falls back to `document.documentElement` when `container()` returns `document`
(`observer.js:86`). With `subtree: true` that observes the entire page, which both maximises the
mutation rate driving the starvation above and is the worst case for main-thread cost.

Measured mutation rate on `#related` is **~1,400/s** (see §1). `schedule()` therefore runs ~1,400
times a second, each call doing a `clearTimeout` plus a `setTimeout` — roughly 2,800 timer operations
per second on the main thread, to service a callback that then never fires. That is pure overhead on
top of the §2a walk.

Fix: switch to a throttle with a guaranteed maximum interval (fire at least every N ms regardless of
mutation rate), scope the observer to the results container rather than `document.documentElement`,
filter mutation records to relevant node additions before scheduling, and cap the work per scan.
At ~1,400 mutations/s the record filter matters as much as the throttle — most of those records are
not card insertions and should not reach the scheduler at all.

**Acceptance: a watch page must sustain 60 s of normal scrolling with no visible jank**, and the
extension's main-thread time must stay small in a profile. Verify on Brave.

## 3. Retention sweep still deletes data (was WO-002, not applied)

`store/sqlite.go:17` `Retention`, `:192` `Sweep()`, and **`main.go:31` calls it on every startup** —
deleting anything older than 90 days.

Delete all three plus the sweep test. Keep the corpus indefinitely; a user-settable limit lands in P1
defaulting to off. Rationale: G2 requires a video removed in month *N* to still have a record in
month *N+12*, and recommendation quality improves with history depth.

## 4. `slot_index` does not reflect visual position (was WO-003 §1)

`extract.js:228` and `extract_yt.js:146` both assign `slot_index: impressions.length`. Any skipped
card — parse failure, duplicate, or the context video — shifts every later index down one.

Reproduced (3 cards, middle one has no title):

```
failures: 1
  video=AAAAAAAAAAA slot_index=0
  video=CCCCCCCCCCC slot_index=1     ← visually in position 2
```

Slot position *is* the measurement — the research output is "B appears under A, usually in slot 3."
A silently drifting index yields a corpus that looks correct and is wrong, undetectably, after the
fact.

Fix: derive `slot_index` from position in the unfiltered candidate list, before any filtering.
Skipped cards leave a gap; that is the truthful representation. Applies to **both** extract paths.

Related: `ytd-compact-radio-renderer` is in `CARD_SEL` but playlist cards have no `watch?v=`
thumbnail href, so they fail every extraction and inflate `failures`. Either extract them or drop
them from the selector — but they must still consume a slot.

## 5. Duplicate rows on re-render (was WO-003 §4)

PK is `(page_load_id, surface, video_id, slot_index)`. Because `slot_index` is unstable, the same
video on the same page gets different indices across re-extractions as the skip count changes, so
`INSERT OR IGNORE` does not fire and a duplicate row lands.

Fixing §4 mostly resolves it, but `slot_index` should not be doing double duty as measurement *and*
identity. Prefer PK `(page_load_id, surface, video_id)` with `slot_index` a plain column and
`ON CONFLICT DO UPDATE` keeping the first-observed slot.

Also: `observer.js` re-extracts and re-sends the whole container every tick with no cross-emission
dedup, relying entirely on the database. Cheap to fix in the observer; cuts bridge traffic.

## 6. Moderate (was WO-003 §5)

- **6.1** `native.js:47` checks outbound size against `MAX_HOST_MSG` (1 MiB) — that is the
  *host→browser* cap. Browser→host is 64 MiB. Wrong direction; will silently drop a large batch later.
- **6.2** `native.js:110` rejects pending requests on disconnect without `clearTimeout(w.t)`. Timers
  fire against settled promises. Harmless, untidy.
- **6.3** `extract.js:154` / `extract_yt.js:86` fall back to `name:${displayName}` for `channel_id`.
  Display names change and collide, silently aliasing distinct channels in the corpus. Prefer
  returning null (counted as a failure), or flag the record so analysis can exclude it.
- **6.4** *(added by review 2026-08-02)* `extractFromContainer` parses every card twice:
  `readCardFields(card)` at `extract.js:214`, then `extractFromElement(card, …)` at `:224` calls
  `readCardFields` again on the same node. Doubles the per-scan DOM cost — relevant to §2, since this
  runs on every observer tick — and lets one malformed card increment `failures` twice, inflating the
  metric §1 relies on to distinguish "no candidates" from "parsing failed". Parse once and pass the
  fields through.

## 7. Reconnect does not survive service-worker eviction (was WO-003 §3)

`native.js:39` backs off with `setTimeout`, up to 30 s. In MV3 the worker is evicted after ~30 s
idle, and while the daemon is down **there is no native port keeping it alive** — the timer dies with
the worker, and the later backoff values are at or past the eviction window.

Reconnection therefore only resumes incidentally, when a content-script message wakes the worker and
top-level `bridge.connect()` re-runs. The acceptance criterion passes for the wrong reason.

Fix with `chrome.alarms` (minimum period 30 s), or accept it and reword the criterion — but do not
leave a criterion that only passes by accident.

## 8. Buffer semantics — RESOLVED, no action

The spec said drop the in-memory buffer on disconnect; the code buffers up to 200 and flushes on
reconnect. **The spec was wrong and is being amended.** Current behaviour is correct — leave it.
It is memory-only and bounded, so the trust boundary is intact.

---

## 9. Brave is a target browser — record it

QA ran on **Brave**, which is the intended target, but nothing documents that. Brave is Chromium, so
`manifest.chrome.json` applies unchanged; only the native-messaging host path differs.

`daemon/README.md` must carry the full per-browser table:

| Browser | Linux host manifest dir |
|---|---|
| Chrome | `~/.config/google-chrome/NativeMessagingHosts/` |
| Chromium | `~/.config/chromium/NativeMessagingHosts/` |
| **Brave** | `~/.config/BraveSoftware/Brave-Browser/NativeMessagingHosts/` |
| Edge | `~/.config/microsoft-edge/NativeMessagingHosts/` |
| Firefox | `~/.mozilla/native-messaging-hosts/` |

Add the macOS and Windows equivalents. The P2 installer must write to every Chromium variant present,
not just Chrome. Note `allowed_origins` takes no wildcards, so each browser's manifest lists the
exact extension ID.

**Also sanity-check Brave Shields against extraction.** QA saw Shields block YouTube's own
`youtubei/v1/log_event` (`ERR_BLOCKED_BY_CLIENT`) — expected and fine, but confirm Shields does not
also suppress any content the extractor depends on. Users will have it on.

---

## 10. Widen injection — APPROVED, absorbed into WO-010

*(Lars approved option 1 on 2026-08-02; the work now lives in `handoff/WO-010-home-surface.md`.
Nothing to do here.)*

### Original note

*(added 2026-08-02; not assigned, do not implement until Lars rules)*

`manifest.chrome.json:9` matches `*://www.youtube.com/watch*` only, so the content script is injected
on a **document load** of a watch URL and never otherwise. `listenSpa()` (`observer.js:123`) handles
`yt-navigate-finish` and wrapped `pushState`/`popstate`, but only once the script is already running
in that tab. The common real path — open `youtube.com`, click a video — is a soft navigation with no
document load, so nothing injects and nothing is recorded.

QA hard-loaded `/watch` directly, which is why this did not surface. It means P0 as built measures
only sessions that begin at a watch URL, which is a biased sample of exactly the behaviour the corpus
is meant to capture.

I am flagging rather than fixing because every remedy touches a hard rule or the permission set, and
that is Lars's call, not the implementer's:

- Widening `matches` to `*://www.youtube.com/*` injects on every YouTube page. Cheapest, no new
  permission, but the script then runs where it has no work to do.
- `chrome.tabs.onUpdated` + `scripting.executeScript` needs `tabs` and `scripting`, both forbidden by
  the minimum-permissions rule (WO-001 §3).

The first option looks compatible with the standing rules; I have not assumed it. **If it is in
scope, it needs its own work order and a `DESIGN_v2.md` note** — per handoff rule 1, and because
AGENTS.md forbids widening scope inside an existing WO.

---

## Acceptance

- [ ] Live `/watch` on Brave produces `IMPRESSIONS` with ~20 records; SidePanel shows them.
- [x] ~~Root cause of §1 identified by instrumentation and recorded in this WO, not guessed.~~
      **Done by review 2026-08-02 — see amended §1.** Candidate 3 eliminated by captured
      `PAGE_CONTEXT.surface`; candidate 2 confirmed by source; DOM path confirmed by measured
      mutation rate (~1,400/s vs a 500 ms resetting debounce). Two causes, one per path.
- [ ] Both §1 causes fixed, not one: `lockupViewModel` in the ytInitialData whitelist **and** a
      non-starving scheduler. Confirmed by the two counters described in §1.
- [ ] `readCardFields` handles both `ytd-compact-video-renderer` and `yt-lockup-view-model` behind
      one interface; DOM selector list and ytInitialData whitelist cover the same component set.
- [ ] Fixtures recaptured from current YouTube; `test/README.md` documents the refresh procedure.
- [ ] A test fails if zero cards are extracted from a fixture known to contain cards.
- [ ] 60 s of scrolling on a watch page with no visible jank on Brave; main-thread cost small in a profile.
- [ ] `ytInitialData` parsed at most once per navigation.
- [ ] No time-based deletion anywhere in `daemon/`.
- [ ] `slot_index` equals unfiltered position in both extract paths; §4 repro yields 0 and 2.
- [ ] Re-render with a varying failure count produces no duplicate rows.
- [ ] `daemon/README.md` lists host paths for Chrome, Chromium, Brave, Edge, Firefox across all three OSes.
