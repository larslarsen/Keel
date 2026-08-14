# WO-106 — The selector router must not turn TikTok into YouTube

| | |
|---|---|
| **Addressee** | Sr Dev (Grok 4.6 High) |
| **Status** | **Accepted** 2026-08-14 — live Brave Explore records `tt`/`EXPLORE`; unrelated live-QA edits moved to WO-107/108 |
| **Date** | 2026-08-14 |
| **Source** | TikTok live console: `[Keel] selectors v1 for yt from daemon` followed by missing Explore container |
| **Depends on** | WO-104 accepted TikTok selectors; independent of WO-105 manifest refresh |

## Confirmed defect

The TikTok observer correctly derives `tt` from its rendered page and sends:

```js
browser.runtime.sendMessage({
  type: "GET_SELECTORS",
  payload: { platform: "tt" },
});
```

`extension/background/rpc.js` discards that payload:

```js
case "GET_SELECTORS": {
  requireDaemon();
  requireCap("selectors", "selectors");
  return { selectors: await relay("GET_SELECTORS") };
}
```

The daemon receives no platform. Its legacy empty-platform fallback selects
YouTube, so TikTok logs `selectors v1 for yt from daemon`. The observer then
looks for TikTok Explore through YouTube container selectors, fails ten bounded
attempts, and reports `armMo: container not ready after 10 attempts`.

This is the direct cause chain. It is not a selector-data defect, TikTok SPA
race, manifest failure or unbounded retry loop.

## Required implementation

### 1. Derive the selector platform from the browser-attributed sender

In the `GET_SELECTORS` router case, derive the platform from
`sender.tab.url` through the existing compiled `surfaceFromUrl()` boundary.
Accept only the two named platforms, `yt` and `tt`, and relay exactly:

```text
GET_SELECTORS { platform: <sender-derived platform> }
```

Do not trust `message.payload.platform`. A TikTok content script claiming `yt`
must still receive TikTok selectors, and vice versa. A message without a
browser-attributed supported tab must fail before contacting the daemon; an
extension page or arbitrary site has no need to request page selectors.

Keep the daemon's empty-platform fallback for compatibility with older
extension builds in this correction. The current extension must not exercise
it. Changing capability revision or breaking an older YouTube observer is not
required to repair this routing omission.

### 2. Prove the complete routing boundary

Add router tests with a recording fake bridge that assert:

- a TikTok sender relays `{platform:"tt"}` and receives the TikTok config;
- a YouTube sender relays `{platform:"yt"}` and receives the YouTube config;
- payload attempts to claim the other platform are ignored;
- a missing, malformed or unsupported sender URL makes no daemon request; and
- the capability and connected-daemon gates remain in force.

The assertions must inspect the bridge request payload, not merely a mocked
final return value. Add or retain daemon tests proving explicit `tt` and `yt`
requests select the correspondingly tagged embedded configuration.

### 3. Keep the retry diagnosis honest

Do not implement a retry-storm fix. `armMo()` schedules at most ten retries at
750 ms and then returns without another timer. The console summary's claim of
an unbounded `armMo → setTimeout → armMo` loop is false.

Do not treat `observer armed` as proof that the surface container was found;
that line means the observer module and navigation listeners started. Live
acceptance below must prove the correct selector platform and actual records.

## Live acceptance

After reloading the extension, hard-reload TikTok Explore with the console
cleared and **Preserve log** off. Confirm in the new page lifetime:

1. `[Keel] selectors v1 for tt from daemon` appears—never `yt`;
2. `[Keel] observer armed` appears;
3. no `armMo: container not ready after 10 attempts` appears;
4. no current-page `live_policy.js`, observer import, or
   `chrome-extension://invalid/` error appears; and
5. Keel's TikTok observation count increases from the rendered Explore cards.

An `invalid` extension origin retained from the moment the extension itself was
reloaded belongs to the orphaned old content-script context. It is not a base
URL Keel constructed. Clear the console and reload the page before judging the
new context.

Run and report:

```text
npm test
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

## Do not

- Do not change selector JSON, TikTok extraction, host permissions or manifests.
- Do not trust a caller-supplied platform over `sender.tab.url`.
- Do not add MAIN-world code, network interception, a polling loop or a new
  capability revision.
- Do not "fix" `chrome-extension://invalid/`; page reload is the browser-defined
  end of an orphaned content-script lifetime.

## Challenge

If sender-derived platform cannot be retained through this router without a
protocol revision, show the incompatible wire shape before widening scope.
Otherwise this is a bounded missing-payload correction; keep it that way.

## Implementation record — 2026-08-14

`GET_SELECTORS` now derives `yt`/`tt` from `sender.tab.url` via
`surfaceFromUrl()` and relays `{ platform }` to the daemon. The message
payload is ignored. A missing, empty or unsupported sender URL fails before
`request()`. The daemon's empty-platform YouTube fallback is unchanged and
is not used by this extension build.

Router tests inspect the recorded bridge payload for TikTok and YouTube
senders, a lying payload, unsupported senders, and the existing daemon and
capability gates. Daemon tests prove explicit `tt` and `yt` requests return
the correspondingly tagged embedded selector bytes.

Reload Keel in `brave://extensions`, then hard-reload TikTok Explore, to
complete live acceptance.

## Live QA — 2026-08-14

Brave printed `daemon selectors rejected; using the bundled set`, then
`armMo: container not ready after 10 attempts`. That is not a Shields/CORS
issue and not a missing Explore root.

The owner process was `/home/lars/keel/daemon/keel-host` started 2026-08-13
14:52, embedding the WO-063 TikTok config (`containers` only `watch`/`home`,
no `live`). The current extension refuses that schema, falls back to the
bundled YouTube defaults, and therefore never finds
`[data-e2e="explore-item-list"]`.

The owner binary must be rebuilt from current source and restarted. The
observer now logs the schema miss (missing `explore`/`following`/`liveWall`/
`liveRoom`) so the next stale binary is obvious.

## Live acceptance — 2026-08-14

After the current owner was running and recording consent was granted, Brave
Explore logged `selectors v1 for tt from daemon` and `observer armed`. The
corpus grew `tt`/`EXPLORE` rows. The sender-derived routing correction is
accepted.

Review found additional selector-diagnostic, Counts and service-worker startup
changes in the implementation diff. They are not part of this order. WO-107
owns the platform-correct fallback exposed by the diagnostic; WO-108 owns the
scope cleanup and tests for the Counts correction.
