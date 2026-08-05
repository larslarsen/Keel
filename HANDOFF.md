> **SUPERSEDED by `handoff/WO-001-p0-rebaseline.md`.** Retained for history only.

# Handoff to the implementing engineer

Paste the block below to start. Everything after it is context for whoever wants it.

---

## The prompt

> You're picking up **Keel** — a browser extension plus a local Go daemon that gives people control
> over the video recommendations they see, and (opt-in, much later) measures how those
> recommendations steer attention.
>
> **Read these three files, in this order, before writing any code:**
> 1. `DESIGN_v2.md` — the architecture. Long, but §0, §1, §2, §3 and §6.0 are load-bearing.
> 2. `RENAME_NOTE.md` — what changed recently and why.
> 3. `BUILD_P0.md` — your actual scope.
>
> **The design has changed since any earlier brief you may have seen.** Two reversals in particular:
> the daemon is now **required** and the extension is a thin client that persists **no observation
> data in the browser**; and there is **no framework, no bundler, no build step, and no runtime
> dependencies**. If you have code built on the old assumptions, `extract.js` carries over — the
> storage layer does not.
>
> **Your scope is P0 only:** one surface (`WATCH_NEXT` on `/watch`), end to end — content script →
> in-memory batch → native messaging bridge → Go daemon → SQLite → count back to the SidePanel.
> Nothing else. No home feed, no search, no filtering, no preservation, no crypto, no network calls.
> The acceptance criteria in `BUILD_P0.md` §9 are the definition of done; treat them as tests, not
> aspirations.
>
> **Constraints that look arbitrary but are not.** Each traces to a legal or threat-model
> requirement documented in the design doc, and breaking one silently breaks the project rather than
> just the code:
> - No observation data persisted in browser storage — not IndexedDB, `chrome.storage`, or
>   `localStorage`. In-memory only; drop the buffer if the daemon is down.
> - No MAIN-world scripts, no `fetch`/XHR interception. Read the rendered DOM.
> - Never call the YouTube Data API.
> - Never store a raw search query.
> - No runtime dependencies. Use `crypto.randomUUID()`, `crypto.subtle.digest()`, `MutationObserver`.
> - Nothing named "YouTube"/"YT" in the extension name.
>
> **Two things to get right because they are where this will break:**
> - `extract.js` must be a **pure function** from DOM subtree to record, with no browser APIs, so it
>   is testable against saved fixtures. YouTube's DOM changes constantly; this is the code that
>   rots, and fixture-driven tests are what make that a five-minute fix instead of an afternoon.
> - **Reconnect in `onDisconnect`.** A native messaging port keeps the service worker alive, but if
>   the host dies the worker dies with it. The old prototype set the port to null and stopped, so a
>   single daemon restart ended the session permanently. Also note failures arrive asynchronously
>   via `onDisconnect` + `chrome.runtime.lastError`, never as a synchronous throw — a `try/catch`
>   around `connectNative` is dead code.
>
> `v1_prototype/` is **not a reference.** Ten documented defects in `DESIGN_v2.md` §5.1.
>
> Licence is Apache-2.0; the project will be public.
>
> **Push back if something looks wrong.** The design doc has been revised several times and may
> still contain stale statements. If a constraint seems to make the code worse, say so and cite the
> section — some are genuinely load-bearing and some are just old. Don't route around one silently.
> If a requirement seems to need something from a later phase, that's a scope error; raise it.

---

## Context, if useful

**Why P0 is a vertical slice rather than "build the extension."** The riskiest integration is the
native messaging bridge — framing, the 1 MB host→browser cap, reconnect, daemon lifecycle — not the
DOM parsing. Building the extension fully and attaching the daemon afterwards would defer that risk
to the point where it's most expensive to discover.

**Why the daemon is required.** Chrome is Google's software and Google is the adversary here.
Accumulated watch history must not sit inside the adversary's runtime. It also lightens Chrome Web
Store review substantially: an extension retaining viewing history triggers a "web history" data
disclosure and a heavy Limited Use review; an extension holding only preferences declares almost
nothing. Full reasoning in `DESIGN_v2.md` §2.1.

**Why no framework.** Three reasons, in §2 of `BUILD_P0.md`: Web Store review is easier on plain
readable source; the project's central claim ("nothing leaves your machine") must be verifiable by a
stranger reading the source in an afternoon; and every dependency is a party that could later
exfiltrate data from every user.

**Review process.** Work will be reviewed against `BUILD_P0.md` §9. Expect the first pass to focus
on: slot-index ordering under lazy loading, deduplication on re-render, the reconnect path, schema
validation on both sides of the bridge, and whether anything observation-shaped ended up in browser
storage.
