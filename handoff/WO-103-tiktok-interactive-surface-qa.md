# WO-103 — Verify TikTok Explore, Following, and Live-room DOM interactively

| | |
|---|---|
| **Addressee** | Lars + reviewer |
| **Status** | **Ready after WO-098 merge** |
| **Date** | 2026-08-13 |
| **Depends on** | WO-098 accepted implementation |

## Outcome

Prove the remaining TikTok surfaces against the DOM an actual Brave user sees,
then make selector-data corrections without changing WO-098's architecture.
Automated logged-out sessions did not hydrate Explore or Following, and no real
active/inactive room capture was available. A model must not replace that
missing evidence with plausible HTML.

## Capture protocol

Use Brave with Shields on and the same extension build on:

1. interactive Explore after its video wall has hydrated;
2. logged-in Following after ordinary cards have hydrated;
3. one currently active `/@creator/live` room; and
4. one offline, ended, loading, unavailable or error room.

Capture only rendered DOM. Do not export cookies, storage, request headers,
network logs, hydration scripts or account tokens. Sanitize before committing:
preserve relevant tag/attribute/ancestor/card structure while replacing handles,
titles, numeric ids, images and personalized rows.

## Verification

- Explore emits ordinary TikTok impressions with surface `EXPLORE`, displayed
  slot order and page-generation deduplication.
- Following emits ordinary TikTok impressions with surface `FOLLOWING` and the
  same virtualization behavior.
- SPA navigation among FYP, Explore and Following starts the correct new proof
  without YouTube rail-replacement semantics.
- An active room emits exactly one `LIVE_ROOM` sighting only when rendered
  active-player/badge evidence and required identity/title fields are present.
- Offline, ended, loading and error rooms emit nothing; their URL is never
  treated as evidence.
- Selector updates remain bounded CSS data in `daemon/selectors_tt.json`; no
  MAIN-world access, hydration parsing, API call or request interception.
- Sanitized fixture tests and interactive observation agree on counts and key
  fields. Record the Brave version, login state, route and result without
  retaining personal feed contents.

## Stop conditions

Return to architecture only if the rendered pages lack a stable public identity
or require hidden/network state. Ordinary selector drift or an additional
bounded container selector is implementation/QA work, not a new feature.
