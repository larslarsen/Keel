# WO-001 — P0 re-baseline

| | |
|---|---|
| **Addressee** | Sr Dev (Grok) |
| **Status** | Done |
| **Date** | 2026-08-02 |
| **Supersedes** | `RENAME_NOTE.md`, `HANDOFF.md` — both retired; use this document |
| **Standing refs** | `DESIGN_v2.md` (architecture), `BUILD_P0.md` (P0 spec) |

---

## 0. Why this exists

The P0 spec changed after you began. Three decisions were made that invalidate parts of the work in
`src/`. This is not a critique of the code — the constraints you were given were followed correctly,
including the ones that were easy to get wrong. The specification moved.

**Assessment of current `src/`** (verified 2026-08-02):

| Check | Result |
|---|---|
| MAIN-world scripts | None — `world: "ISOLATED"` set explicitly ✅ |
| `fetch` / XHR / WebSocket | None anywhere in `src/` ✅ |
| Hand-rolled crypto | None — `crypto.randomUUID()`, `crypto.subtle.digest()` ✅ |
| Runtime dependencies | None — `linkedom` is devDependencies-only for fixtures ✅ |
| v1 defect: invalid match pattern | Fixed ✅ |
| v1 defect: missing content script | Fixed ✅ |
| `content/bootstrap.js` classic→ESM loader | Correct solution to a real constraint ✅ |

Keep that. The changes below are scope and architecture, not quality.

---

## 1. Rename to Keel

The project is **Keel**. "Audit Bridge" is retired.

| Old | New |
|---|---|
| `Audit Bridge for YouTube` | `Keel` |
| `Audit Bridge` (log prefixes, comments, `package.json` name/description) | `Keel` |
| `com.auditbridge.youtube` | `com.keel.host` |
| `audit-bridge-pipe` | `keel-pipe` |
| `Command Bridge` (protocol) | `Keel Bridge` |

**Rationale.** Two reasons, both in `DESIGN_v2.md` §3.1. First, YouTube's branding guidelines state
you *"must never use the YouTube name or any abbreviation, acronym, or variant of the word YouTube,
such as YT or You-Tube in conjunction with the overall name of your application."* The old name was
a standing violation. Second, "audit" signals an adversarial research instrument to a Chrome Web
Store reviewer and signals to users that they are its subject. The product is feed control.

Native messaging host names accept lowercase alphanumerics, underscores, and dots only.

Avoid in user-facing strings: *audit, watchdog, surveillance, track, monitor, expose, investigate*.

---

## 2. The daemon is required; nothing is persisted in the browser

**Delete `src/lib/store.js` entirely** (381 lines of IndexedDB). Remove all IndexedDB usage from
`src/lib/browser.js`, `src/content/observer.js`, and `src/sidepanel/index.js`.

**Rationale** (`DESIGN_v2.md` §2.1). Chrome is Google's software and Google is the adversary in this
project's threat model. Accumulated watch history must not sit inside the adversary's runtime.
Moving observation out of the browser does not protect any single impression — the content script
runs in Google's process regardless — but it protects the *accumulation*, which is the sensitive
asset. It also materially lightens Web Store review: an extension retaining viewing history triggers
a "web history" data disclosure and a heavy Limited Use review; one holding only preferences
declares almost nothing.

**Replacement:**

- Impressions live in a **bounded in-memory buffer** in the content script / service worker, long
  enough to batch across the bridge.
- **Bounded in-memory buffering across a disconnect is allowed** (cap ~200, newest kept), flushed on
  reconnect. **Never spill to any browser storage**, never grow unbounded. Losing the buffer to
  worker eviction is fine. *(Amended 2026-08-02; original text said drop on disconnect.)*
- `chrome.storage` holds UI state, and later the channel blocklist, toggles, and consent state.
  **Never** video IDs, titles, or history.

**New in P0: the Go daemon.** `daemon/` — stdio native messaging host, SQLite store, per
`BUILD_P0.md` §6–7. P0 is now an end-to-end vertical slice through the bridge, because native
messaging is the riskiest integration in this system and deferring it is how it becomes expensive.

---

## 3. Permission minimisation

**Target manifest permissions:**

```jsonc
"permissions": ["sidePanel", "storage", "nativeMessaging"],
// no "optional_permissions"
// no "host_permissions"
"content_scripts": [{
  "matches": ["*://www.youtube.com/watch*"],   // P0: WATCH_NEXT only
  "js": ["content/bootstrap.js"],
  "run_at": "document_idle",
  "world": "ISOLATED"
}]
```

Four changes:

**3.1 Drop `scripting`.** `browser.js:126` wraps `scripting.executeScript` but nothing calls it.
Remove the wrapper and the permission.

**3.2 `nativeMessaging` moves to `permissions`.** The daemon is no longer optional (§2).

**3.3 Narrow `content_scripts.matches` to the surfaces actually in scope.** P0 is `WATCH_NEXT`
only, so `*://www.youtube.com/watch*`. P1 adds `*://www.youtube.com/results*` and
`*://www.youtube.com/` (exact root, for `HOME`). Do not match all of `youtube.com`.

**3.4 Drop `host_permissions` entirely — this requires a design change, described below.**

### Why `host_permissions` is currently load-bearing, and how to remove the need

`sw.js:41` registers `tabs.onUpdated` and reads `tab.url` to decide whether to enable the SidePanel.
**`tab.url` is only populated when the extension holds host permission for that tab** (or the `tabs`
permission). That is the sole reason the broad host pattern exists.

**Invert the flow.** The content script only runs on pages we match, so it already knows the answer.
Have it message the service worker on load and on `yt-navigate-finish`:

```js
// content -> sw
{ v: 2, type: "PAGE_CONTEXT", payload: { surface: "WATCH_NEXT", pageLoadId } }
```

The service worker calls `sidePanel.setOptions({ tabId: sender.tab.id, ... })` using `sender.tab.id`
from the message, which needs no permission. Delete `watchTabsForSidePanel()`.

Net effect: no `host_permissions`, no `tabs` permission, a smaller install-time permission prompt,
and a shorter list to justify in the Web Store listing. Permission minimisation is a review
requirement (`DESIGN_v2.md` §3.1), not tidiness.

### Known coverage gap — accepted, do not work around

Content scripts inject at document load. Once injected the script survives SPA navigation within the
same document, so a user who hard-loads `/watch` and then browses onward stays covered.

**The gap:** a user who hard-loads a *non-matching* page — `/@channel`, a playlist, `/shorts` — and
then navigates to a watch page via SPA gets **no injection and no observation**, because no document
load occurred on a matching URL.

This is accepted for P0/P1. The matched set (`/watch`, `/results`, `/`) covers the overwhelming
majority of entry points. Do not widen the match pattern to close it, and do not add programmatic
injection — both re-introduce the broad permission this change removes. If the gap later proves
material, it comes back as its own work order with data behind it.

> **Amended by WO-008 (2026-08-03).** The *reload/update* half of this gap is now closed: a standing
> `scripting` watchdog re-injects `content/bootstrap.js` into already-open `/watch` tabs (the
> overnight dark-tab incident was the materiality trigger). The *non-matching entry page* half
> remains accepted exactly as written here — the match pattern is still not widened, and `scripting`
> is used for re-injection only, never to add surfaces. `host_permissions` is path-scoped to
> `*://www.youtube.com/watch*`.

Note we cannot measure this gap from inside the extension; unobserved sessions are invisible by
construction. Say so in the methodology when the time comes rather than implying full coverage.

---

## 4. Scope reduction

P0 is now **`WATCH_NEXT` only**. `HOME` and `SEARCH` move to P1.

Keep the `home_*` and `search_*` fixtures — they are still valid and P1 will use them. Keep the
`Surface` enum complete. Just don't emit or extract the other two yet.

---

## 5. Housekeeping

**5.1** `src/manifest.json` is generated by the `manifest:chrome` / `manifest:firefox` scripts and is
currently tracked. Add it to `.gitignore` so it cannot drift against the two real manifests.

**5.2** `src/content/extract.js` is 718 lines. Once scoped to `WATCH_NEXT` it should shrink
substantially. Split per-surface as `HOME` and `SEARCH` return in P1 — one file per surface behind a
common interface. The project's central claim is that a stranger can read the source and verify
nothing leaves the machine; a 700-line extractor works against that, and it is also the file most
likely to rot when YouTube reshuffles their DOM.

**5.3** Licence is **Apache-2.0** (`LICENSE`, `NOTICE`). Add SPDX headers to new source files:
`// SPDX-License-Identifier: Apache-2.0`. The project will be public — this is required both for the
auditability claim and for free Windows code signing via SignPath (`DESIGN_v2.md` §2.3).

---

## 6. Acceptance criteria

`BUILD_P0.md` §9 is authoritative. The ones that change with this WO:

- [ ] `permissions` is exactly `["sidePanel", "storage", "nativeMessaging"]`; no `host_permissions`, no `optional_permissions`, no `scripting`.
- [ ] `content_scripts.matches` is exactly `["*://www.youtube.com/watch*"]`.
- [ ] `grep -rn "indexedDB\|localStorage" src/` returns nothing.
- [ ] After a full session, no video IDs, titles, or history exist in IndexedDB, `chrome.storage`, or `localStorage`.
- [ ] SidePanel enablement works with no host permission (driven by `PAGE_CONTEXT`, not `tab.url`).
- [ ] Daemon absent → clean "desktop app isn't running" state, no uncaught exceptions.
- [ ] Daemon killed mid-session → extension reconnects when it returns, no browser restart.
- [ ] Malformed / oversized / non-JSON bridge messages in either direction → dropped, logged, connection survives.
- [ ] Nothing in the extension name or `package.json` contains "YouTube"/"YT"/"Audit Bridge".

---

## 7. Pushback

Challenge anything here that makes the code worse, and cite the section. The design doc has been
revised several times: some constraints are load-bearing and some are stale, and you cannot tell
which from the outside. Ask rather than routing around one.

Specifically worth arguing with if you disagree:

- **§3.4** — if `PAGE_CONTEXT` proves unworkable for SidePanel enablement, say so; the fallback is
  the `tabs` permission, which is narrower than host permissions but not free.
- **§2** — dropping the buffer when the daemon is down loses data. That is intentional, but if the
  loss rate turns out to be severe in normal use, that is worth knowing.
- **§3.3** — if narrowing the match pattern breaks observation in a way §3's coverage-gap analysis
  did not anticipate, report it with a reproduction before widening anything.
