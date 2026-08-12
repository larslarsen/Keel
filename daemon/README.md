# Keel desktop owner and native host

One per-user desktop owner holds the SQLite corpus and peer network. Each browser
native-messaging launch is a thin authenticated local proxy to that owner. The
browser extension and proxy never persist observations or open the database.

**Target browsers:** Chrome, Chromium, **Brave** (primary QA), Edge, Firefox. Brave is Chromium —
use `manifest.chrome.json` / `prepare:chrome`. Only the native-messaging host path differs.

## Build

```bash
cd daemon
go build -o keel-host .
```

Binary defaults to `$XDG_CONFIG_HOME/keel/keel.sqlite` (or OS config dir `/keel/`). Override with
`KEEL_DB=/path/to.sqlite` or `KEEL_DATA_DIR=/dir`.

There is **no time-based retention sweep**. History is kept indefinitely until a P1 user setting
says otherwise.

## Register the native host

### Automatic (recommended)

```sh
cd daemon && go build -o keel-host .
./keel-host install -extension-id <your-extension-id>
```

Get the extension ID from `chrome://extensions` (or `brave://extensions`) with developer mode on.

The daemon registers itself — it knows its own absolute path, so nothing is edited by hand. It writes
a host manifest for **every browser it detects**, skipping ones that are not installed, and prints
what it wrote. Per-user only: no admin rights, nothing outside your own config directories.

| Flag | Effect |
|---|---|
| `-extension-id a,b` | One or more Chromium extension IDs. Required for Chromium browsers — `allowed_origins` takes no wildcards, so an empty list matches nothing. |
| `-firefox-id` | Firefox gecko ID. Defaults to `keel@local`, matching `manifest.firefox.json`. |
| `-all` | Write for every supported browser, not only detected ones. |
| `-dry-run` | Print what would be written; write nothing. |

To remove: `./keel-host uninstall` (add `-dry-run` to preview).

### Windows

Windows registers through the registry rather than a directory, so the installer writes the
manifests and then executes the `reg add` calls itself — nothing is printed for the user to run.
Specifically (WO-091):

- **Double-clicking `keel-host.exe` with no arguments installs.** A native-messaging launch always
  carries the caller's origin (Chromium) or manifest path (Firefox) in `argv`, so an invocation
  with arguments still becomes a proxy and never reinstalls underneath a running browser.
- **Two manifests, one per schema**, because a Chromium manifest has `allowed_origins` and a
  Firefox manifest has `allowed_extensions`:
  - `%LOCALAPPDATA%\Keel\chromium\com.keel.host.json` — Chrome, Chromium, Brave, Edge
  - `%LOCALAPPDATA%\Keel\firefox\com.keel.host.json` — Firefox

  The base comes from `LOCALAPPDATA`; if it is unset the install fails rather than guessing.
- **All five browsers are always registered.** An HKCU key only that browser reads is harmless, and
  there is no directory to detect them by.
- **Every write is read back.** Each manifest is decoded and checked (host name, current executable
  path, `stdio`, the right extension list, no field from the other schema), and each registry key
  is re-queried and compared with the exact intended path. A missing key, wrong value, invalid
  manifest or absent executable fails the install with a non-zero exit — never a warning.
- **`install-report.txt` is written beside the executable** on every real install, progressively, so
  a failure that happens before the console appears is still readable. It holds the version, the
  executable path, both manifests and their validation, every registry key with expected and
  observed value, the extension-folder result, `SUCCESS`/`FAILED`, and the first actionable error.
  It contains paths and registry keys only — no corpus, observation, peer, query or credential data.

`uninstall` removes both manifests and every registry key the installer owns. It does not delete
`keel.sqlite`.

Then reload the extension. The SidePanel should show **Desktop app connected**.

The first browser connection starts the owner if necessary. Closing the browser
ends only its proxy session; the owner remains alive for live gossip, pre-walk
and peer-provider work. All supported browsers and profiles for the OS user
share that owner.

Useful lifecycle commands:

```sh
./keel-host owner status
./keel-host owner stop
```

`uninstall` stops the owner and removes its local authentication credential in
addition to the browser host registrations. It does not delete `keel.sqlite` or
your recorded corpus.

### Local owner transport

- Linux/macOS: a mode-`0600` Unix socket in a mode-`0700` Keel runtime
  directory. A kernel-backed election guard protects stale-socket recovery.
- Windows: `\\.\pipe\keel-owner-<user SID>-<install id>`, created as the first
  named-pipe instance with a protected DACL granting only the current user.
- Both: the proxy must additionally prove a random 256-bit installation secret
  stored with current-user-only permissions. The first frame negotiates required
  `owner_ipc:1`; incompatible components fail closed and never start a second
  database/swarm owner.

Owner diagnostics are appended to a mode-`0600` `owner-<install id>.log` beside
the database. `KEEL_RUNTIME_DIR` can relocate the Unix runtime directory for
packaging/tests; it does not relocate the corpus.

### Manual (reference)

The installer does all of this. Kept for troubleshooting and for packagers.


1. Load the unpacked extension from `extension/` (`npm run prepare:chrome` at repo root).
2. Copy the extension ID (Chrome/Brave: `chrome://extensions` with developer mode).
3. **Copy** `host/com.keel.host.json` to the browser directory below, then edit **that copy** — not
   the one in the repo. The repo file is a template with placeholders; editing it in place means
   committing your absolute paths and extension ID, which are machine-specific and useless to anyone
   else. In the copy:
   - set `path` to the absolute path of the `keel-host` binary
   - set `allowed_origins` to `chrome-extension://<id>/`  
     (`allowed_origins` accepts **no wildcards** — list each browser’s extension origin if IDs differ)
4. Install the host manifest to the correct directory for **each** browser you use:

### Linux

| Browser | Host manifest directory |
|---|---|
| Chrome | `~/.config/google-chrome/NativeMessagingHosts/` |
| Chromium | `~/.config/chromium/NativeMessagingHosts/` |
| **Brave** | `~/.config/BraveSoftware/Brave-Browser/NativeMessagingHosts/` |
| Edge | `~/.config/microsoft-edge/NativeMessagingHosts/` |
| Firefox | `~/.mozilla/native-messaging-hosts/` |

File name: `com.keel.host.json`.

### macOS

| Browser | Host manifest directory |
|---|---|
| Chrome | `~/Library/Application Support/Google/Chrome/NativeMessagingHosts/` |
| Chromium | `~/Library/Application Support/Chromium/NativeMessagingHosts/` |
| **Brave** | `~/Library/Application Support/BraveSoftware/Brave-Browser/NativeMessagingHosts/` |
| Edge | `~/Library/Application Support/Microsoft Edge/NativeMessagingHosts/` |
| Firefox | `~/Library/Application Support/Mozilla/NativeMessagingHosts/` |

### Windows

| Browser | Host registration |
|---|---|
| Chrome | Registry `HKCU\Software\Google\Chrome\NativeMessagingHosts\com.keel.host` = path to JSON manifest |
| Chromium | `HKCU\Software\Chromium\NativeMessagingHosts\com.keel.host` |
| **Brave** | `HKCU\Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\com.keel.host` |
| Edge | `HKCU\Software\Microsoft\Edge\NativeMessagingHosts\com.keel.host` |
| Firefox | `HKCU\Software\Mozilla\NativeMessagingHosts\com.keel.host` |

The registry value is the full path to a `com.keel.host.json` file. The **Firefox key must point at a
different file** from the four Chromium keys: the schemas are not interchangeable, and one file
serving both means whichever browser was written last is the only one that works.

5. Reload the extension. SidePanel should show **Desktop app connected**.

*(Implemented — `keel-host install` registers every supported Chromium variant, not only Chrome, and
on Windows verifies each key by reading it back.)*

## Brave notes

- Use the Brave host path above; reusing Chrome’s directory will not attach.
- **Brave Shields** may block YouTube endpoints such as `youtubei/v1/log_event`
  (`ERR_BLOCKED_BY_CLIENT`). That is expected and does not affect DOM observation.
- Shields must **not** block extension content scripts. If extraction fails only with Shields up,
  check `brave://settings/shields` for the site and confirm the extension still injects
  (DevTools → Sources / Content scripts).

## Protocol

Browser ↔ proxy remains framed stdio JSON envelopes (`v: 2`) — `DESIGN_v2.md`
§8.1 and `extension/lib/protocol.js`. After the authenticated owner handshake,
the proxy forwards those JSON frames byte-for-byte over the local socket/pipe.
Correlation IDs stay scoped to that one proxy connection.

The first browser application frame must be `HELLO`. API range plus required
and optional capability revisions are negotiated before any other RPC; a
missing `core:1` or non-overlapping API fails that session closed. Optional
controls are disabled with a reason when their capability is absent. Envelope
`v: 2` is stable bootstrap framing, not the revision number for every payload.

The owner may send unsolicited `CONTRIBUTION_STATUS` envelopes with reserved
`owner-event-*` ids after a runtime policy change. Every authenticated browser
session receives the terminal effective state; these ids never resolve RPCs.

P0 types: `HELLO` / `HELLO_ACK`, `IMPRESSIONS` / `IMPRESSIONS_ACK`, `STATS` / `STATS_RESULT`, `ERROR`.

The current graph stream is `/keel/block/3.0.0`. It serves signed schema-3
claim buckets with declared `held`/`truncated` state; Level 2 selects the union
of local and imported claims, while Level 1 registers no graph/catalogue/search
serve handler or provider announcement.
