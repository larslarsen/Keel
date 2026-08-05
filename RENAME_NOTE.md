> **SUPERSEDED by `handoff/WO-001-p0-rebaseline.md`.** Retained for history only.

# Rename note for the implementing agent

**The project is now called Keel.** The old working name "Audit Bridge" is retired. If you have
already written code using it, this is a find-and-replace, not a rewrite — do it now rather than
after more files exist.

## Why

Two reasons, both load-bearing:

1. **"for YouTube" is a branding violation.** YouTube's guidelines: *"You must never use the YouTube
   name or any abbreviation, acronym, or variant of the word YouTube, such as YT or You-Tube in
   conjunction with the overall name of your application."* The old name handed Google a free
   pretext, which is precisely what this project's strategy exists to avoid.
2. **"Audit" is wrong for both audiences.** It tells a Chrome Web Store reviewer this is an
   adversarial research instrument, and it tells users they are the subject of one. Users install
   things that give them features. Nobody installs an audit.

## Replacements

| Old | New |
|---|---|
| `Audit Bridge for YouTube` | `Keel` |
| `Audit Bridge` | `Keel` |
| `Open Audit Dashboard` / `Open Audit Bridge` | `Open Keel` |
| `com.auditbridge.youtube` | `com.keel.host` |
| `audit-bridge-pipe` | `keel-pipe` |
| `Command Bridge` (protocol name) | `Keel Bridge` |
| any `audit*` identifier prefix | `keel*` |

Native messaging host names are restricted to lowercase alphanumerics, underscores, and dots — and
must not contain "youtube" either.

## Positioning — this affects copy, not just identifiers

The extension name, description, and all user-facing strings lead with **user benefit**, not
research. See §3.1 of `DESIGN_v2.md`.

- **Single purpose:** *"Give people control over the video recommendations they see."*
- **Description:** may reference YouTube descriptively — "Works with YouTube" is fine. Nominative
  use in copy is safe; name incorporation is not.
- **Tone:** a quieter feed, real chronological order, search that obeys operators, channels that
  stay blocked. Contribution is an opt-in footnote, never the pitch.

Avoid in user-facing strings: *audit, watchdog, surveillance, track, monitor, expose, investigate*.
They are accurate about the research and wrong for the product.

## ARCHITECTURE CHANGE — read before writing more code

**The daemon is required, not optional. The extension is a thin client.** An earlier draft had the
extension storing impressions in IndexedDB; that is reverted. See `DESIGN_v2.md` §2.1.

- **No observation data is ever persisted in the browser** — not IndexedDB, not `chrome.storage`,
  not `localStorage`. In-memory buffer only, flushed across the bridge. If the daemon is down, drop
  the buffer.
- `chrome.storage` holds UI state, the channel blocklist, toggles, and consent state. Nothing else.
- `nativeMessaging` moves from `optional_permissions` to `permissions`.
- **Distribution is daemon-first**: the desktop app installs the extension (§2.2). The extension
  must still be listed on the Web Store — Windows/macOS external install requires `update_url` to
  point there — so compliance work is unchanged.
- The extension **must degrade gracefully with no daemon**: a clean "desktop app isn't running"
  state, never a thrown exception or blank panel. A Web Store reviewer will see exactly this state.

**`BUILD_P0.md` has been rewritten.** P0 is now an end-to-end vertical slice — one surface
(`WATCH_NEXT`) through extension, bridge, daemon, SQLite, and back — rather than a complete
extension. If you have built toward the old brief, the `extract.js` work carries over; the storage
layer does not.

## Also changed since the brief was written

Two design updates landed at the same time; both are already reflected in `DESIGN_v2.md` and
`BUILD_P0.md`:

- **§2.1 of `BUILD_P0.md` — no framework, no bundler, no runtime dependencies, no build step.**
  Plain ES modules. Target under ~600 lines of JavaScript for all of P0. This is now an acceptance
  criterion, not a preference. If you have pulled in React, a bundler, or a UUID/hashing library,
  remove them — the platform provides `crypto.randomUUID()`, `crypto.subtle.digest()`, and
  `indexedDB` directly.
- **§7.3 of `DESIGN_v2.md` — publication is zero-cost.** Paid infrastructure (S3/R2, paid IPFS
  pinning, Arweave) is out. Zenodo, GitHub Releases, IPFS, BitTorrent, Internet Archive. Nothing in
  P0 touches this, but do not build toward the paid model.
