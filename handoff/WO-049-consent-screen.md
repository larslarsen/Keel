# WO-049 — In-extension consent screen

| | |
|---|---|
| **Addressee** | Anyone |
| **Status** | **Done** — `extension/consent/` first-run gate (commit 3be856b), restored after a regression (commit 3459a78). |
| **Date** | 2026-08-04 |

> **Correction required by WO-089 (2026-08-12):** the current page lost its
> explicit Decline button and places network details below the affirmative
> action. WO-089 restores Decline, moves the Level-1 recording/download
> disclosure above the action, and requires daemon acknowledgement before the
> observer or Level-1 network starts.

`DESIGN_v2` §"Disclosure" requires **an in-extension consent screen**, plus the
store listing, plus the privacy policy (WO-048). Only the extension part is
missing, and a store listing alone does not satisfy it — most users never read
one.

## What it must do

Shown once on first run, before any observation is recorded.

- **Say what is collected in one screen**, in plain language: which videos
  YouTube recommends to you, where they appeared, and their public details.
- **Say where it goes:** a program on this computer, into a file on this disk.
  Not to us — there is no server.
- **Say what it is for:** so you can see and control your recommendations, and
  so the pattern can be studied.
- **Offer a real choice.** Declining must leave a working extension that records
  nothing, not a dead one. If declining breaks the product, it is not consent.
- **Link the privacy policy and the export/wipe controls** so the exits are
  visible at the moment of the ask, not buried afterwards.

## What it must not do

- No pre-ticked boxes, no "by continuing you agree", no styling that makes
  Decline harder to find than Accept. `DESIGN_v2` requires no dark patterns and
  WO-012 already applies that standard to the wipe confirmation — match it.
- Do not ask again on every update. Re-ask only when data handling actually
  changes, which the design requires be a proactive re-notification.

## Implementation notes

- A dedicated extension page shown on install, or the SidePanel's first view
  before observation starts. Either is fine; it must not be a `window.confirm`.
- Store the decision in `chrome.storage` — configuration, not observation data,
  so it does not touch the §2.1 rule.
- **The observer must not record before consent.** Gate `startHide`/`observer`
  arming on the stored decision, and verify with a live corpus count that
  nothing lands while consent is pending.

## Acceptance

- [ ] First run shows the screen before any impression is written.
- [ ] Declining yields a working extension that records nothing; verified by row
      count staying flat through a watch session.
- [ ] Accept and Decline have equal visual weight; no pre-ticked anything.
- [ ] Privacy policy and export/wipe reachable from the screen.
- [ ] Decision persists; not re-asked on ordinary updates.
