# WO-048 — Privacy policy

| | |
|---|---|
| **Addressee** | Anyone |
| **Status** | **Policy written (`PRIVACY.md`, commit 84a40ad) — not yet published at a stable URL or linked from a store listing. Blocks store submission until hosted.** |
| **Date** | 2026-08-04 |

The Chrome Web Store requires a hosted privacy-policy URL for any extension that
handles user data. `DESIGN_v2` §"Disclosure" also requires one alongside the
listing and the consent screen. **Neither exists.**

## What makes this unusual, and easy

Keel's honest policy is far stronger than most, and the strength comes from
architecture rather than promises. State the facts, not intentions:

- **Observation data never leaves the device.** It goes from the extension to a
  local daemon over native messaging and into SQLite on the user's own disk.
  There is no account, no server receiving it, no telemetry.
- **The extension stores no observation data at all** — not IndexedDB, not
  `chrome.storage`, not `localStorage`. Only one preference key
  (`hide_recommendations`) and, per WO-016, the blocklist lives in the daemon.
  This is verifiable: `DESIGN_v2` §2.1 and a LevelDB audit in WO-011 Part 2
  confirmed it on disk, including write history.
- **What is collected:** which videos YouTube recommended, in what position, on
  which surface, and their public metadata. Never watch history, never
  keystrokes, never search queries — search is out of scope by decision
  (WO-010 §5), and any future query would be hashed, never stored raw
  (§4.2).
- **Outbound network:** the extension makes none. The daemon fetches video
  thumbnails from `i.ytimg.com` (WO-040) and nothing else.
- **Deletion:** one button wipes everything, and it `VACUUM`s so the file
  actually shrinks. Export gives the user every row and column as JSON.

## Also cover

- **Third parties:** none. No analytics, no ad networks, no crash reporting.
- **Children:** the extension is not directed at children and collects nothing
  that identifies anyone.
- **Changes:** `DESIGN_v2` requires *proactive re-notification* if data handling
  changes — say so, and mean it.
- **Contact:** a working address for questions and complaints.

## Hosting

A page in the repo published via GitHub Pages is sufficient and free — no new
infrastructure, consistent with the zero-cost constraint (§7.3).

## Do not

- Do not claim the extension makes no network requests. It does not, but the
  daemon does; say precisely which and why (this exact claim has already been
  wrong twice — WO-030 and WO-039).
- Do not promise anonymity for anything. Nothing here is anonymised today, and
  STAR is not built.

## Acceptance

- [ ] Policy published at a stable URL and linked from the store listing.
- [ ] Every claim traceable to code, not intention.
- [ ] Reviewed against the final manifest — permissions listed match what ships.
