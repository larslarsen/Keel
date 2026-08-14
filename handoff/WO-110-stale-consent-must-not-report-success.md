# WO-110 — Stale consent must not report success and re-prompt forever

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus or GPT-5.6 Terra High) |
| **Status** | **Accepted and live-verified 2026-08-14** — with the same daemon binary, the stale revision-1 consent page (old extension) was tried four times: the stale page claims consent (reports success) but observation never actually starts — the gate stays closed, so no recording and no false `CONSENT_CHANGED` reach the daemon. The fail-closed property holds: a stale affirmative can never enable browser observation. The rev-2 consent page in the updated extension granted normally. The stale page is a stale *extension*, not a stale binary |
| **Date** | 2026-08-14 |
| **Source** | Windows live QA: recording accepted four times, but consent remained required |
| **Depends on** | WO-089 revisioned daemon-owned network consent |

## Outcome

A consent action either satisfies the disclosure gate and reports success, or
fails visibly without enabling browser observation. A stale extension must
never receive a successful reply for an obsolete disclosure revision.

This is a fail-closed mixed-version correction, not new consent wording or a
new permission.

## Confirmed defect

The current daemon requires `store.NetworkConsentRevision == 2`. A browser
still rendering revision 1 sends:

```json
{"accepted": true, "revision": 1}
```

`Store.GrantNetworkConsent` rejects non-positive and future revisions but
accepts a positive revision below the daemon's required revision. It stores 1
and returns a state whose gate is still not current. The bridge nevertheless
answers `NETWORK_CONSENT_RESULT`, and the extension treats every non-error reply
as acceptance, writes local recording consent, and says `Recording is on`.
On the next status check the daemon correctly reports that revision 2 is still
required, so the screen opens again. Clicking repeatedly can never advance the
revision.

This is exactly the mixed-version case revisioned consent exists to handle, but
the write path currently fails silently rather than fail closed.

## Required

### 1. The daemon rejects an obsolete affirmative revision

For an affirmative grant, `revision < NetworkConsentRevision` is an error. Do
not write `network_consent_revision`, do not update `network_consent_at`, and do
not resume or replace the swarm node. Return a bounded, user-safe
`consent_rejected` error that states the browser extension must be updated and
includes the authoritative consent state in the existing structured detail.

Withdrawal remains valid regardless of the browser's revision: revoking
permission must never require current disclosure text.

Keep the existing future-revision refusal. A newer extension paired with an old
daemon still needs a desktop-app update; an older extension paired with a new
daemon now needs an extension update. Neither mismatch may be recorded as
consent.

### 2. The extension verifies the returned gate before enabling observation

`SET_CONSENT(granted)` remains daemon-first. After
`SET_NETWORK_CONSENT`, require the returned authoritative state to say that
network consent is current at the required revision before writing the local
browser recording flag or displaying success.

If the reply is structurally valid but still says consent is required, fail
closed with actionable `browser extension update required` wording. Do not
write `chrome.storage` consent, broadcast `CONSENT_CHANGED`, hide the choices,
or say recording is on.

This second check is required even after the daemon rejection is fixed: it
prevents another daemon regression or partial reply from turning a failed gate
into local observation permission.

### 3. Make the screen describe the mismatch, not invite another click

The consent screen must tell the user that the extension and desktop app do not
agree on the disclosure version and that pressing Accept again will not fix it.
Do not use a generic save error and do not claim either recording or networking
is enabled.

## Required proof

- Store test: required revision 2 plus grant revision 1 returns an error and
  leaves both consent meta values unchanged.
- Bridge test: the same stale grant returns `ERROR` / `consent_rejected`, does
  not resume the network, and reports authoritative required/accepted state.
- Router test: a stale or non-current daemon reply does not write local
  recording consent or broadcast `CONSENT_CHANGED`.
- Router test: revision 2 acceptance persists daemon-first, then writes the
  local flag and broadcasts once.
- Withdrawal tests remain green for old, current, and future browser revisions.
- Existing consent, contribution-level, Level-1 no-outbound, owner and swarm
  suites remain green under Go, Go race, and extension tests.

## Do not

- Do not auto-upgrade revision 1 to revision 2 in the daemon. The daemon cannot
  assert that an old browser displayed words it does not contain.
- Do not lower `NetworkConsentRevision`, reuse revision 1, or change the
  disclosure text to avoid the mismatch.
- Do not persist a raw browser error or any observation data.
- Do not require another daemon restart or contribution-level change after a
  valid current acceptance.

## Live acceptance

With a deliberately stale revision-1 extension against the current daemon,
one Accept click must produce an explicit extension-update refusal and leave
recording/networking off. With the matching revision-2 extension, one Accept
click must make the consent current, keep it current across browser reload, and
start the permitted effective network state without a daemon restart.
