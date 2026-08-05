# WO-003 — P0 review findings

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Open |
| **Date** | 2026-08-02 |
| **Reviewed** | `extension/`, `daemon/` @ d7e00e9 |

## Verified good — do not change

- Manifest is exactly right: `["sidePanel","storage","nativeMessaging"]`, no `host_permissions`,
  no `optional_permissions`, no `scripting`, matches `*://www.youtube.com/watch*`. WO-001 §3 done.
- `PAGE_CONTEXT` → `sender.tab.id` SidePanel enable. `watchTabsForSidePanel()` gone. WO-001 §3.4 done.
- `frame.go` — `binary.NativeEndian`, correct **per-direction** limits (1 MiB out, 64 MiB in). This
  is the detail most implementations get wrong.
- `onDisconnect` clears the port, resets `helloOk`, rejects pending, schedules reconnect. The v1
  defect is genuinely fixed.
- Envelope validation on both sides; malformed input dropped with an `ERROR` reply, stream survives.
- No IndexedDB, no `localStorage`, no persistence in the browser. Trust boundary holds.
- `extract.js` is pure and has a purity test. 11 JS tests and both Go packages pass.

---

## 1. BLOCKER — `slot_index` does not reflect visual position

`extract.js:228` assigns `slot_index: impressions.length`. Any skipped card — extraction failure
(`:219`), duplicate video (`:222`), or the context video (`:223`) — shifts every subsequent index
down by one.

**Reproduction** (3 cards, middle one has no title so `readCardFields` returns null):

```
failures: 1
  video=AAAAAAAAAAA slot_index=0
  video=CCCCCCCCCCC slot_index=1     ← visually in position 2
```

**Why this is a blocker, not a nit.** Slot position *is* the measurement. The entire research output
is "video B appears under video A, usually in slot 3." A slot index that silently drifts whenever a
sidebar card fails to parse — which happens constantly with ads, playlist cards, and lazy-loaded
placeholders — produces a corpus that looks correct and is quietly wrong. There is no way to detect
or repair it after the fact.

**Fix:** derive `slot_index` from the card's position in the queried node list, before any filtering.
Enumerate `cards` with its real index and pass that through; skipped cards leave a gap in the
sequence, which is the truthful representation.

Note `ytd-compact-radio-renderer` is in `CARD_SEL` but playlist cards have no `watch?v=` thumbnail
href, so they fail `readCardFields` and inflate `failures` on every extraction. Decide whether to
drop them from the selector or extract them properly; either way they must still consume a slot.

## 2. BLOCKER — WO-002 was not applied; the daemon deletes data on every start

`store/sqlite.go:17` still defines `Retention = 90 * 24 * time.Hour`, `:192` still implements
`Sweep()`, and **`main.go:31` calls it on every startup.** Any impression older than 90 days is
deleted whenever the daemon launches.

WO-002 removed this. It defeats G2 — the success criterion is that a video removed in month *N*
still has a record in month *N+12* — and it degrades recommendation quality, which improves with
history depth.

**Fix:** delete `Retention`, `Sweep()`, its test, and the call in `main.go`. Keep everything
indefinitely. A user-settable limit arrives in P1, defaulting to off.

## 3. MAJOR — the reconnect chain dies with the service worker

`native.js:39` schedules reconnects with `setTimeout`, backing off to 30 s. In MV3 the service
worker is evicted after ~30 s idle. While the daemon is down there is **no native port to keep the
worker alive**, so the pending timer is destroyed before it fires — and the later backoff values are
at or beyond the eviction window.

Net effect: after the daemon dies, automatic reconnection stops. It only resumes incidentally, when
a content-script message wakes the worker and the top-level `bridge.connect()` at `sw.js:166` runs
again. So the acceptance criterion *"daemon killed mid-session → extension reconnects when it
returns"* passes while the user keeps browsing and fails on a parked tab.

**Fix:** use `chrome.alarms` for the backoff (minimum period 30 s) rather than `setTimeout`, or
accept the behaviour and document it explicitly — but do not leave an acceptance criterion that
only passes by accident.

## 4. MAJOR — duplicate rows on re-render

The SQLite primary key is `(page_load_id, surface, video_id, slot_index)`. Because `slot_index` is
unstable (§1), the *same video on the same page* gets different indices across re-extractions
whenever the number of skipped cards changes — lazy loading makes this routine. Different key,
`INSERT OR IGNORE` does not fire, duplicate row.

Fixing §1 mostly resolves this, but the deeper issue is that `slot_index` is doing double duty as
both a measurement and part of the identity key. Consider `(page_load_id, surface, video_id)` as the
PK with `slot_index` as a plain column, using `ON CONFLICT DO UPDATE` to keep the first-observed
slot. Worth a decision.

Related: `observer.js` re-extracts and re-sends the entire container on every debounce tick, with no
cross-emission dedup, relying wholly on the database. Cheap to fix in the observer and it cuts
bridge traffic substantially.

---

## 5. Moderate

**5.1 Buffer semantics contradict WO-001 §2.** `sw.js:27-30` drops the buffer at the moment of
disconnect, but `handle()` at `:133` re-buffers every subsequent batch up to 200 and flushes on
reconnect. The spec says drop it. The code's behaviour is arguably more useful and does not violate
the trust boundary (in-memory only), but the two must not silently disagree — either change the code
or raise it and I will amend the spec.

**5.2 Wrong direction's size limit.** `native.js:47` checks outbound messages against
`MAX_HOST_MSG` (1 MiB), which is the *host→browser* cap. Browser→host is 64 MiB. Harmless at ~20
impressions per page, but the constant is misapplied and will silently drop a large batch later.

**5.3 Timer leak.** `native.js:110` rejects pending requests on disconnect without
`clearTimeout(w.t)`. Timers survive and fire against already-settled promises. Harmless, untidy.

**5.4 `channel_id` fallback is unstable.** `extract.js:154` falls back to `name:${displayName}`.
Display names change and collide, so this pollutes the channel dimension with identifiers that
silently alias. Prefer returning `null` (counted as a failure) over inventing an unstable ID, or
mark it with a flag so analysis can exclude it.

---

## Acceptance

- [ ] `slot_index` equals the card's position in the unfiltered node list; repro in §1 yields 0 and 2.
- [ ] No time-based deletion anywhere in `daemon/`; `Retention`, `Sweep()`, and the `main.go` call are gone.
- [ ] Reconnect survives service-worker eviction, or the limitation is documented and the acceptance criterion reworded.
- [ ] Re-rendering a page with a varying number of failed cards produces no duplicate rows.
- [ ] §5.1 resolved in one direction or the other, not left divergent.
