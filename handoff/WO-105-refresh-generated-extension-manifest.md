# WO-105 — A stale generated manifest must not disable the content observer

| | |
|---|---|
| **Addressee** | Sr Dev |
| **Status** | **Accepted 2026-08-14 — live TikTok observer loaded; selector follow-up is WO-106** |
| **Date** | 2026-08-13 |
| **Source** | Brave console on TikTok Explore after WO-098/104 |
| **Depends on** | None |

## Confirmed defect

The currently loaded Chromium extension reports both:

```text
Denying load of chrome-extension://.../content/live_policy.js.
Resources must be listed in the web_accessible_resources manifest key

[Keel] observer load failed: Failed to fetch dynamically imported module:
chrome-extension://.../content/observer.js
```

This is one failure, not two independent missing resources.
`content/bootstrap.js` is the classic isolated-world content-script entry and
dynamically imports `content/observer.js`. The observer then statically imports
`content/live_policy.js`. Chromium denies that transitive module because the
generated `extension/manifest.json` does not list it, so the top-level observer
import rejects as a consequence.

Both authoritative templates already list `content/live_policy.js` and have the
correct YouTube and TikTok match patterns:

- `extension/manifest.chrome.json`; and
- `extension/manifest.firefox.json`.

The ignored, generated `extension/manifest.json` predates that change. The
installer's `prepareExtensionFolder()` returns immediately whenever the
destination merely exists, calling it "already prepared" without comparing it
to the Chromium template. The source tests walk and validate both authoritative
templates but never inspect the generated file that Brave actually loads.

The release workflow already copies the Chromium template immediately before
building `keel-extension.zip`, so a fresh release package from the current
source does not reproduce this particular drift. The defect is in an existing
source/install folder whose generated manifest survives a template update.

## Impact

This is fatal to the entire observer module graph on the affected installation,
not just to TikTok Live policy. `bootstrap.js` runs, `observer.js` never starts,
and no rendered YouTube or TikTok observation reaches the daemon. Treat the
second console message as the propagated consequence; do not add separate
fallback behavior for it.

## Required implementation

### 1. Refresh instead of trusting existence

Keep `manifest.chrome.json` and `manifest.firefox.json` as the authoritative
browser-specific templates and keep `manifest.json` disposable/generated.

When `prepareExtensionFolder()` finds `manifest.chrome.json`, read it and the
destination and compare their bytes. If `manifest.json` is absent or differs,
replace it with the Chromium template. Write through a temporary file in the
same directory and rename it so the browser never observes a partial manifest.
Report whether the destination was created, refreshed, or already current.

If a packaged folder contains only a valid `manifest.json` and no Chromium
template, preserve the existing supported behavior; absence of an optional
template is not permission to erase a packaged manifest. A read or write error
for a template that is present must be returned, not silently converted into
"already prepared."

This path prepares the Chromium-family unpacked folder used by Brave, Chrome,
Chromium and Edge. It must not overwrite a deliberately Firefox-prepared source
folder during a Firefox-specific packaging command.

### 2. Make the loaded artifact part of the structural proof

Retain the existing transitive-import closure test for both authoritative
templates. Extract its manifest validation into a helper and apply the same
proof to a generated Chromium manifest fixture/artifact. The proof must start
at declared content scripts and literal `runtime.getURL()` module entries, walk
all static imports, and require every browser-fetched module in the closure to
match a `web_accessible_resources` entry.

Also assert that every web-accessible rule used for this module graph is scoped
to all and only the corresponding named content-script hosts. The current hosts
are YouTube and TikTok. Do not solve this with a new host wildcard.

The release packaging job must regenerate `manifest.json` and run that
validation before zipping. A developer test must fail clearly when a present
local `extension/manifest.json` is stale; it must name the regeneration command
rather than failing later as an unrelated observer test.

### 3. Cover the installer regression

Add deterministic Go tests proving:

- an absent destination is created from the Chromium template;
- a stale destination is atomically refreshed from the template;
- an identical destination is left current and reported truthfully;
- a destination without an optional template is preserved;
- a present but unreadable template or unwritable destination fails visibly;
  and
- the refreshed manifest contains the complete observer import closure,
  including `content/live_policy.js`.

The test should compare content, not filesystem timestamps.

## Immediate local recovery

For the currently loaded source tree, regenerate the ignored Chromium artifact
and then press **Reload** for Keel in `brave://extensions`:

```text
npm run prepare:chrome
```

This is an operational unblock, not acceptance of the work order. Acceptance
requires preventing the next template change from leaving a silently stale
loaded artifact.

## Acceptance

1. Start with an old `extension/manifest.json` that omits
   `content/live_policy.js`, run the normal installer/preparation path, and
   prove it is refreshed.
2. Reload the unpacked extension in Brave with Shields on.
3. Open one supported YouTube page and TikTok Explore.
4. Confirm neither resource-denial nor `observer load failed` appears.
5. Confirm the observer sends page context and at least one fixture-supported
   observation on each applicable surface.
6. Run and report:

```text
npm test
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

## Do not

- Do not add a MAIN-world script, script tag, fetch/XHR interception, bundler,
  framework or runtime dependency.
- Do not broaden host permissions or web-accessible matches beyond the two
  named platforms.
- Do not swallow a module-load error or pretend the observer is healthy.
- Do not commit a one-line `live_policy.js` addition only to the ignored
  generated file; it would repair one checkout and leave the drift mechanism.
- Do not change TikTok extraction, Live sharing policy or contribution levels.

## Challenge

If Chromium can load this ES-module closure without web-accessible resources
while preserving the isolated-world and no-build constraints, demonstrate it
in Brave before changing the design. Otherwise implement the bounded refresh
and validation above; do not route around the manifest boundary.

## Implementation record — 2026-08-13

`prepareExtensionFolder` compares `manifest.json` to `manifest.chrome.json`
when the template is present. Absent or differing bytes are replaced through
a same-directory temp file and rename. Outcomes are prepared, refreshed, or
already current. A packaged folder with only `manifest.json` is preserved.
A present but unreadable template or unwritable destination fails.

The content-script WAR/import-closure check is shared (`test/manifest-closure.js`)
and applied to both templates and the generated Chromium artifact. A stale
local `extension/manifest.json` fails with `npm run prepare:chrome`. WAR
rules for the observer graph must list YouTube and TikTok only.

Release packaging and the extension CI job regenerate the Chromium manifest
and run the structural proof before zipping/testing. This checkout was
regenerated with `npm run prepare:chrome`. Reload Keel in `brave://extensions`
to complete live acceptance.

## Reviewer findings — 2026-08-14

The Chromium repair is sound: the ignored generated artifact now equals
`manifest.chrome.json`, includes `content/live_policy.js`, and the installer
refreshes differing bytes through a same-directory temporary file and rename.
The reviewer independently ran `npm test`, `go test ./...`,
`go test -race ./...`, `go vet ./...` and `git diff --check`; all passed.

One supported-browser regression blocks acceptance. The generic extension test
assumes that every present `extension/manifest.json` must equal
`manifest.chrome.json`. Therefore this legitimate sequence fails:

```text
npm run prepare:firefox
npm test
```

`manifest.json` is not stale in that state; it is the exact artifact produced
by the repository's supported Firefox preparation command. Calling it stale
and instructing the developer to replace it with Chromium contradicts the
browser-specific template design and makes the ordinary test suite unusable
for a Firefox-prepared checkout.

### Required correction

- The generic structural suite must recognize a generated manifest that is
  byte-identical to either authoritative template, then apply the closure and
  named-host proof to that selected artifact.
- A generated manifest that matches neither template must still fail with a
  clear message naming both valid preparation commands.
- Add regression proof for all three states: current Chromium passes, current
  Firefox passes, and the pre-WO-098 stale Chromium artifact fails.
- Keep the release and Chromium CI jobs explicitly Chromium: they already run
  `prepare:chrome` before validation and must continue to prove the artifact
  they package.

After correction, repeat the full suites and both preparation sequences. Live
Brave reload remains the final manual acceptance gate.

## Correction record — 2026-08-14

The generated-artifact check accepts a `manifest.json` that is byte-identical
to either `manifest.chrome.json` or `manifest.firefox.json`, then applies the
same closure and named-host proof. Drift that matches neither template fails
and names both `npm run prepare:chrome` and `npm run prepare:firefox`.

The suite proves three states independently of the working-tree file: current
Chromium passes, current Firefox passes, and the pre-WO-098 Chromium artifact
(missing `content/live_policy.js`) is rejected. Release packaging and the
extension CI job still run `prepare:chrome` before validation.

## Acceptance record — 2026-08-14

The reviewer independently verified both supported generated artifacts through
the complete extension suite:

```text
npm run prepare:firefox && npm test   pass, 21/21 files
npm run prepare:chrome  && npm test   pass, 21/21 files
go test ./...                         pass
go test -race ./...                   pass
go vet ./...                          pass
git diff --check                      pass
```

The checkout was restored to the Chromium artifact afterward, and
`manifest.json` exactly matches `manifest.chrome.json`. The stale fixture
matches neither template, omits `content/live_policy.js`, and the regression
test requires the diagnostic to name both preparation commands.

Code is accepted. The remaining live evidence is recorded below.

## Live-QA checkpoint — 2026-08-14

Brave's recorded unpacked-extension path is `/home/lars/keel/extension`, and
the manifest there exactly matches the corrected Chromium template. The
reported console summary combined events from both sides of the extension
reload: the old context produced resource denial/`chrome-extension://invalid/`,
while a newly injected observer later loaded and logged `observer armed`.

Clear the console and hard-reload the page before the final WO-105 judgment.
The same log exposed a separate functional defect—TikTok received YouTube
selectors because `GET_SELECTORS` dropped its platform—which is isolated in
WO-106. That selector defect can prevent observation counts from increasing
even when the WO-105 module graph is healthy.

The newly injected TikTok observer later logged `observer armed`. Because
`live_policy.js` is a static dependency evaluated before `observer.js` can run,
that line proves the corrected module graph loaded in the new context. Together
with the confirmed Brave path and byte-identical current manifest, WO-105 is
accepted. Observation-count acceptance belongs to WO-106 because the wrong
selector platform, not module loading, now blocks the Explore container.
