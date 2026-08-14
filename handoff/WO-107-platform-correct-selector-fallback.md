# WO-107 — TikTok must never fall back to YouTube selectors

| | |
|---|---|
| **Addressee** | Sr Dev (Grok 4.6 High / GPT-5.6 Terra High) |
| **Status** | **Code accepted** 2026-08-14 — daemon-unavailable live QA pending |
| **Date** | 2026-08-14 |
| **Source** | WO-106 live QA exposed the stale-owner path after the router itself was corrected |
| **Depends on** | WO-106 accepted routing correction |

## Confirmed defect

WO-106 now asks the daemon for the platform derived from the browser-attributed
sender. That fixes the healthy current-daemon path. It does not fix the
observer's fallback path.

`extension/content/observer.js` initializes every page with
`DEFAULT_SELECTORS`. That object is explicitly tagged `platform: "yt"`. If the
selector RPC fails, or a stale daemon returns a configuration the current
validator rejects, a TikTok page keeps that YouTube configuration. The warning
added during WO-106 live QA makes the mistake visible, but TikTok still searches
for YouTube containers and stops collecting.

This violates the platform boundary established by WO-057. A failed update may
leave a page on its platform's bundled selector data; it may never silently
cross into another platform's data.

## Required implementation

### 1. Make the bundled fallback platform-indexed

The extension must ship a validated bundled selector configuration for each
supported platform and choose it from `platformFromUrl(location.href)` before
it starts extraction or sends `GET_SELECTORS`.

The existing `DEFAULT_SELECTORS` export may remain as the YouTube compatibility
alias for pure extractor callers, but the observer must use an explicit
platform lookup. An unknown platform must not observe anything.

The TikTok bundled value must be semantically identical to the current shipped
`daemon/selectors_tt.json`. Because this repository deliberately has no build
step, add a closure test that fails if the two checked-in copies drift. Do not
introduce a generator, bundler, runtime dependency, browser-storage cache or
network fetch.

### 2. Treat platform identity as part of validation

A syntactically valid selector object is not sufficient for the observer. Its
inner `platform` must equal the platform derived from the current page. A
TikTok page must reject a valid YouTube configuration and keep its bundled
TikTok configuration; YouTube must do the converse.

On a missing, rejected or mismatched daemon response, retain the already chosen
platform-correct bundle. Keep the useful schema diagnostic from the WO-106 live
QA, but make its fallback wording name the platform actually retained.

Do not persist daemon selector responses. Observation-derived state remains
in-memory only under `DESIGN_v2.md` §2.1, and a selector override remains owned
by the daemon as WO-056 designed.

### 3. Prove failure paths, not only the healthy reply

Add focused tests proving:

- before any daemon response, a TikTok URL uses the bundled `tt` configuration
  and a YouTube URL uses the bundled `yt` configuration;
- a rejected, malformed or mismatched daemon configuration never changes that
  platform-correct fallback;
- a valid matching daemon configuration still replaces the bundle;
- the extension's bundled TikTok data and `daemon/selectors_tt.json` are
  semantically equal; and
- the current TikTok Explore fixture extracts with the bundled fallback alone.

Tests must exercise the observer's selection boundary, not merely call
`validateSelectorConfig()` on two independent objects.

## Live acceptance

With recording consent already granted, open TikTok Explore in a fresh page
lifetime while the current daemon is unavailable. The observer must use the
bundled TikTok set, find the Explore container, and must never log or use a
YouTube selector set. Starting the current daemon and reloading the page must
still log `selectors v1 for tt from daemon` and store `tt`/`EXPLORE` records.

Run and report:

```text
npm test
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

## Do not

- Do not weaken validation or accept part of a selector schema.
- Do not trust `message.payload.platform`; WO-106's sender-derived routing stays
  unchanged.
- Do not store selector data or observations in browser storage.
- Do not add MAIN-world code, page-network interception, runtime dependencies,
  a build step or permissions.
- Do not redesign the selector wire protocol or remove the daemon's legacy
  empty-platform compatibility fallback in this order.

## Challenge

If a single extension-bundled TikTok configuration cannot remain semantically
closed against the daemon embed without a build step, show the concrete drift
path and propose an equally testable single-source layout before changing this
boundary. Do not leave TikTok with YouTube as its failure mode.

## Implementation and review record — 2026-08-14

`selector_source.js` now owns the observer boundary. It derives the page
platform before the selector RPC, chooses that platform's bundled data, accepts
only a valid daemon configuration with the same inner platform, and otherwise
retains the bundle. Unsupported pages receive no selector set and do not arm.

The extension now ships `SELECTORS_TT`; a semantic-closure test compares it to
`daemon/selectors_tt.json`. The same test suite exercises both platform
baselines, missing/malformed and cross-platform replies, a valid matching
override, and the real Explore extractor using only the bundled TikTok data.
Both new modules are included in the transitive content-module closure and in
the host-scoped web-accessible-resource lists for Chromium and Firefox. The
ignored generated manifest exactly matches the Chromium template.

Reviewer verification: 23 extension suites pass; daemon Go tests, race and vet
pass; `git diff --check` is clean. No browser storage, permission, wire-protocol
or runtime-dependency change was introduced.

Code is accepted. The manual acceptance case that starts a fresh TikTok Explore
page while the owner is unavailable remains pending; the reviewer did not stop
the user's running owner merely to manufacture that state.
