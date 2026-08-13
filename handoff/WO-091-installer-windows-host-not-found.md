# WO-091 — Windows installer writes the wrong native-host manifest for Brave

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Implemented 2026-08-12 — live QA on the affected machine pending** |
| **Date** | 2026-08-12 |
| **Source** | User live QA on Windows; architecture review of the first draft |
| **Observed error** | `runtime.lastError: Specified native messaging host not found` |
| **User constraint** | The affected machine cannot be diagnosed through typed commands. Installation and reporting must work by placing and double-clicking files. |

## Reproduced source defect

The current Windows install plan cannot safely install Chromium-family and
Firefox native-host manifests together.

`targets()` assigns every Windows browser the same directory and `runInstall()`
always uses the same destination name, `com.keel.host.json`. With `install
-all`, the loop writes that path once per browser. Firefox is last, so its
manifest—`allowed_extensions`, no `allowed_origins`—overwrites the Chromium
manifest that the Brave, Chrome, Chromium and Edge registry keys point at.

The affected user ran `install -all`. Therefore the installed Brave registry
key can point at a present file which has the wrong browser schema. This is a
direct, deterministic installer defect and the primary explanation to fix
before speculating about a stale executable or HELLO/version behavior.

Two additional Windows defects make first installation unreliable:

1. Every Windows target uses `%USERPROFILE%\AppData\Local\Keel` as its
   *browser-detection* path. On a fresh install that output directory does not
   exist, so plain `install` can skip every browser. The code already states
   that unused per-user registry keys are harmless; Windows should register all
   supported browsers rather than pretend the output directory detects them.
2. `installWindowsRegistry()` returns no result to `runInstall()`. Failed
   `reg add` calls are printed, but the overall install still returns success
   and does not verify what each key points at.

The reported “extension manifest not found” message is a separate discovery
bug. The release zip already contains `manifest.json`, so that warning does not
explain native-host lookup failure. It should still be fixed because the
documented standalone layout puts the executable beside an extracted
`keel-extension` or `extension` directory.

Do not infer the cause of the brief “wrong version” → “not running” UI sequence
until the self-report proves the registry → host manifest → executable chain.
If it remains after registration is valid, file a separate runtime order.

## Required implementation

### 1. Produce separate Windows manifests by schema

- Write one Chromium-family manifest and one Firefox manifest to distinct
  paths. Prefer canonical filenames in distinct directories, for example:
  - `%LOCALAPPDATA%\Keel\chromium\com.keel.host.json`
  - `%LOCALAPPDATA%\Keel\firefox\com.keel.host.json`
- Brave, Chrome, Chromium and Edge registry keys must point at the Chromium
  file. Firefox must point at the Firefox file.
- Derive the base from `LOCALAPPDATA` on Windows, with an explicit error or
  documented safe fallback when it is unavailable; do not synthesize it from
  `UserHomeDir` and a fixed `AppData\Local` suffix.
- Verify the files after writing by reading and decoding them, not merely with
  `os.Stat`:
  - Chromium: correct `name`, current executable `path`, `type: stdio`, and the
    fixed extension origin in `allowed_origins`; no Firefox-only field.
  - Firefox: correct `name`, current executable `path`, `type: stdio`, and the
    Gecko id in `allowed_extensions`; no Chromium-only field.

### 2. Make Windows installation type-free

- On Windows only, an invocation with no arguments must perform the equivalent
  of installing for all supported browsers. This is the Explorer/double-click
  path.
- Preserve native-messaging launches: browsers pass their origin/extension
  argument, so an invocation with browser arguments must continue into
  `runProxy()` and must never reinstall.
- Explicit `install` must also register all supported Windows browsers. Do not
  use the output directory as a browser-presence test. Keep explicit
  `install`, `uninstall`, `owner`, and other subcommands unchanged.

### 3. Verify registration and propagate failure

- Make the Windows registry writer return structured per-browser results and
  an error to `runInstall()`.
- After each `reg add`, query the same key's default value and compare the
  normalized value with the exact intended manifest path.
- A missing key, wrong value, failed manifest validation, or nonexistent
  executable is an installation failure: record it, print it to stderr, and
  return a non-zero exit. Do not label an incomplete registration a warning
  while returning success.
- Put command execution behind a narrow injectable runner so success, missing
  key, localized/noisy output, and wrong-value cases can be tested without a
  Windows registry on the CI host. Do not add a runtime dependency.

### 4. Always leave a readable report

- Every real Windows install attempt writes `install-report.txt` next to the
  executable. It must include:
  - build version/time and executable path;
  - overall `SUCCESS` or `FAILED`;
  - both host-manifest paths and their decoded validation result;
  - every browser registry key, expected value, observed value, and result;
  - extension-folder preparation result; and
  - the first actionable error, without requiring a command prompt.
- Write the report progressively or defer-close it so early failures are also
  captured. If the report itself cannot be created, fail and print the intended
  path to stderr.

### 5. Fix extension-folder discovery without coupling it to host registration

- Treat an existing `manifest.json` in the extracted extension as already
  prepared.
- Otherwise probe both the executable directory and its parent for directory
  names `extension` and `keel-extension`, and copy `manifest.chrome.json` only
  when found.
- A standalone binary with no adjacent extension is not a native-host install
  failure. Record “extension folder not found; host registration completed” in
  the report instead of claiming a present host manifest is missing.

### 6. Uninstall and documentation

- Windows uninstall must remove both schema-specific manifest files and all
  registry keys the installer owns, while retaining the existing rule that it
  does not delete the user's corpus.
- Update `INSTALL.md` for the no-terminal Windows path and the location of
  `install-report.txt`.
- Update `daemon/README.md`: current code executes `reg add`; it does not merely
  print commands for the user to run.

## Acceptance

- [x] A pure install-plan test proves Windows Chromium browsers and Firefox
      receive distinct manifest paths and correct decoded schemas.
      `TestWindowsPlanSeparatesSchemas`.
- [x] A regression test proves the `-all` plan cannot overwrite the Chromium
      manifest with the Firefox manifest. `TestWindowsPlanAllCannotOverwrite`
      checks both the plan and the end state after replaying every write;
      `buildInstallPlan` refuses a plan where two schemas share a path.
- [x] A fresh Windows plan registers every supported browser even when the Keel
      output directory did not previously exist.
      `TestWindowsPlanRegistersEveryBrowserOnAFreshMachine`; Windows targets
      carry no `detect` directory at all, so there is nothing left to mis-detect.
- [x] No-argument Windows dispatch selects install; a simulated browser launch
      with an origin argument selects proxy mode. `TestDispatch`, including the
      Chromium `--parent-window` and Firefox manifest-path launch forms.
- [x] Registry add/query success, missing-key, wrong-value, and command-failure
      cases are tested through the injected runner; every invalid result makes
      installation fail. `TestRegistryInstallFailsClosed`, plus localized
      `reg query` output in `TestRegQueryDefaultToleratesLocalizedOutput`.
- [x] `install-report.txt` covers both success and an early failure and contains
      no corpus, observation, peer, query, or credential data.
      `daemon/install_report_test.go`, including `TestInstallReportCarriesNothingPrivate`.
- [x] Standalone extension layouts named `extension` and `keel-extension` work;
      an already packaged `manifest.json` is accepted without a false warning.
      `TestPrepareExtensionFolder`; a missing extension folder is reported as
      "host registration completed", not as an install failure.
- [x] Windows uninstall removes both manifests and the owned registry keys.
      `TestUninstallRemovesEveryOwnedKey`, `TestUninstallReportsAKeyItCannotRemove`.
      The corpus is still never deleted.
- [x] `GOOS=windows GOARCH=amd64 go build` succeeds; `go test ./...`,
      `go test -race ./...`, `go vet ./...`, `npm test` (174/174), and
      `git diff --check` pass.
- [x] Live QA on the affected machine: the install completed and the panel
      reported **Desktop app connected**. Independently verified since by a
      `windows-latest` CI job (`.github/workflows/windows-e2e.yml`) that
      installs, reads every registry key back with PowerShell rather than
      trusting Keel's own verification, decodes each manifest, checks the
      executable exists, reinstalls, and runs the end-to-end suite on Windows.

## What this order got wrong

Every defect it named was real and is fixed. None of them was the reason the
user could not connect.

The actual blocker was `os.NewFile` on a synchronous named-pipe handle: Go's
runtime requires an overlapped handle, so the owner's first read failed with
"The handle is invalid" and it dropped every client. Removing os.NewFile
exposed a second fault underneath — Windows serializes I/O on a synchronous
handle, so the proxy's reader goroutine held the pipe and the writer queued
behind it forever, and the browser's first frame was never sent. Both handles
are overlapped now, each read and write carrying its own OVERLAPPED.

That is worth recording because of how it presented. A pipe that cannot talk
looks exactly like a missing registry key, an unreadable manifest, a locked
directory, a database that will not open, and a blocked download — and it was
diagnosed as each of those in turn, with a build shipped for every one. The
thing that found it was running the program on a Windows runner and reading the
owner's own log, which was available from the first hour and used in the last.

Two further defects were introduced by those speculative fixes and are also
fixed: `lib/errors.js` was not in `web_accessible_resources`, which killed every
content script; and a protected DACL applied to `%LOCALAPPDATA%\Keel` locked
browsers out of reading their own manifest.


## Implementation record

| Where | What |
|---|---|
| `daemon/install.go` | `targetsFor(goos, home, env)` builds any OS's plan from any host; Windows manifests split by schema under `%LOCALAPPDATA%\Keel\{chromium,firefox}`; `buildInstallPlan` decides everything before touching disk and rejects a shared-path plan; `verifyManifestFile` re-decodes each write; `prepareExtensionFolder` probes both directories under both names and returns `errNoExtensionFolder` for a standalone binary |
| `daemon/install_registry.go` | Injectable `cmdRunner`; `installWindowsRegistry` writes and reads back every key; `regQueryDefault` anchors on the `REG_SZ` type marker because the value name is localized; `normalizeWindowsPath` for comparison; `uninstallWindowsRegistry` confirms removal |
| `daemon/install_report.go` | `install-report.txt`, written progressively and closed by `defer`, so an early return still leaves `RESULT: FAILED` and the first actionable error |
| `daemon/main.go` | `dispatch(goos, args)` — no arguments on Windows means install; any browser launch carries an origin or manifest path and still becomes a proxy |

`install -all` is unchanged and still valid; on Windows it is now redundant,
since detection no longer gates registration.

## Do not

- Do not change HELLO negotiation, consent, contribution policy, browser
  permissions, host name, or native-message framing.
- Do not add Node, PowerShell, administrator rights, or a typed terminal step.
- Do not use one JSON file for both Chromium and Firefox schemas.
- Do not soften failed registration into a successful exit.

## Challenge

The install is complete only when the browser-specific registry key resolves to
a decoded manifest of the correct schema whose executable exists. Console text
alone is not proof, especially when the user cannot keep or type into a console.
