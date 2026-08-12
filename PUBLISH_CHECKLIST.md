# Pre-publication checklist

Keel is local-only until this is worked through. Nothing here is urgent while the repo has no
remote; all of it matters the moment one is added.

## 1. Squash the history

The commit history is working material — work orders, review findings, QA results, several
corrections. It is useful while building and it is not what you want to publish, because every
intermediate blob ships with it, including any file that was later deleted.

At publication, replace it with a single commit from the final tree:

```sh
git checkout --orphan publish
git add -A
git commit -m "Keel: initial public release"
git branch -M publish main
```

Do this **once, at the end**. Doing it early costs the working history for no benefit — nothing is
exposed until a remote exists.

Everything below then only has to be true of the final tree, not of every commit that ever existed.

## 2. Decide on the fixture provenance

`test/fixtures/yt_initial_watch.json` is a real capture from a live watch page. It is scrubbed of
tracking tokens (`clickTrackingParams`, `trackingParams`, `loggingDirectives`,
`serializedShareEntity`, `playerParams`, continuation tokens — verified zero residual). What remains
is public catalogue data: video IDs, titles, channel IDs, view counts, thumbnail URLs.

It is nevertheless **one person's recommendation feed**, and that says something about their watch
history. Lars has said he is fine with this.

If that changes, the fix is thirty seconds: recapture from a logged-out private window, where the
rail is generic rather than personalised, and re-run the scrub. Same for the DOM fixtures.

## 3. Machine-specific values

- `daemon/host/com.keel.host.json` is a **template**. It must ship with placeholder `path` and
  `allowed_origins`, never a real home directory or extension ID. Fixed 2026-08-02; check it again
  before publishing, because the install instructions previously told you to edit it in place and
  someone may still do that.
- Confirm no absolute home paths survive anywhere tracked:
  ```sh
  git grep -nI -E "/home/[a-z]+|/Users/[a-z]+" -- . ':!*.md'
  ```

## 4. Corpus must never be committed

`.gitignore` covers `*.sqlite`, `*.sqlite-wal`, `*.sqlite-shm`, `*.sqlite-journal`. The live corpus
lives at `~/.config/keel/keel.sqlite` — outside the repo — so this is belt and braces. Verify:

```sh
git ls-files | grep -iE "\.sqlite|\.db$"   # must be empty
```

## 4b. Store listing must justify each permission in writing

The Chrome Web Store requires a written justification per permission, and rejects vague ones. Have
these ready, and keep them true:

| Permission | Justification |
|---|---|
| `sidePanel` | The extension's entire UI is a side panel. |
| `storage` | User settings only. **No observation data is stored in the browser** (§2.1). |
| `nativeMessaging` | All observation data goes to a local desktop app; the only outbound traffic is thumbnail images, see §4b below. |
| `alarms` | Two uses: native-link reconnect backoff surviving service-worker eviction, and a watchdog that keeps the worker from dying silently (WO-004 §7, WO-008). |
| `scripting` + `host_permissions` | Re-injects the extension's own content script into already-open YouTube tabs after a reload or update, so observation does not silently stop. Scoped to `*://www.youtube.com/*` and never used to inject into MAIN world or to add surfaces. |

Two things reviewers react badly to. Be precise about both — neither claim is unqualified:

- **Modifying page content** — it does, under user control (WO-009 hides the recommendation rail).
- **Sending data to a remote server** — it sends **no data anywhere**. It does load video thumbnails
  from `i.ytimg.com`, a static CDN, using URLs derived from the video ID (WO-039). No API call, no
  payload, no credentials. The accurate claim is that Keel transmits no observation data off the
  device; its only outbound traffic is fetching images YouTube already serves to the same browser.

**"Does not modify page content" expires if WO-009 ships.** That work order injects a stylesheet to
hide YouTube's recommendation rail. It is user-controlled and does not alter the recommendations
themselves, but it does change what the page renders, and a listing claiming otherwise would be
inaccurate. If 009 lands before publication, rewrite this to say the extension optionally hides
recommendation containers under user control, and never alters page data.

**Scope widened 2026-08-03 (WO-010) and these justifications were rewritten to match.** Both
`content_scripts.matches` and `host_permissions` are now `*://www.youtube.com/*`, because the
homepage is a collection surface and a soft SPA navigation never injects a `/watch*`-only script. The
install prompt covers the whole site; the listing must describe that, not the old narrow path.

## 5. Licence and attribution

Apache-2.0, `LICENSE` and `NOTICE` present. New source files carry
`// SPDX-License-Identifier: Apache-2.0`. Check any file added since the last audit.

## 6. Known limitations should be documented, not hidden

Publishing with open defects is fine; publishing while implying they don't exist is not. As of
2026-08-03 the README should say:

- **`channel_id` is only recorded for first-paint cards.** Videos loaded by scrolling carry no
  channel anywhere in the page, and Keel does not intercept network traffic (WO-013). Roughly 30% of
  rows have no channel. The export header and the UI both report the ratio.
- **No engagement signal.** Keel records what was *shown*, never what was watched or valued, so
  "videos people found useful" is not computable.
- **Suggestions are limited by how much you have watched.** A video only becomes a graph root when
  you watch it, so early graphs are star-shaped rather than networks (WO-023).
- **Two DOM fixtures are hand-authored** — `watch_next_mixed.html` and `watch_next_compact.html`.
  They cover `ytd-compact-video-renderer`, extinct on watch-next, so they cannot be recaptured.
- **Firefox is untested beyond a headless load.** The sidebar has never been opened by a human
  (WO-011).

## Chrome Web Store — data policy (added 2026-08-06)

Chrome's updated policy is enforced from **1 August 2026**. Two hard
requirements, both verified against the policy text rather than assumed:

- [x] **In-product consent before any collection.** All data collection must be
      prominently disclosed and affirmatively consented to *inside the
      extension's own interface*, not only in a privacy policy. There is no
      carve-out for data that never leaves the device — "user data" is defined
      as information collected about a user or a user's use of the product.
      WO-089 put the Level-1 recording/download disclosure above the action,
      moved Live plus outbound word telemetry to Level 2+, and requires a
      current daemon acknowledgement before any swarm node exists. Hosted
      privacy-policy URL and listing declarations below remain.
- [ ] **A hosted privacy policy URL.** A file in the repository does not
      satisfy this; the listing needs a page. Enable GitHub Pages on this
      repository — Settings → Pages → deploy from `main`, folder `/ (root)` —
      which serves `PRIVACY.md` at
      <https://larslarsen.github.io/Keel/PRIVACY>. Serving from this repo keeps
      one source of truth; a copy elsewhere would drift, and a privacy policy
      that disagrees with itself is worse than none.
- [ ] Listing privacy declarations match `PRIVACY.md` exactly.
- [ ] Listing and in-product level copy match WO-089 exactly: Level 1 receives
      broad graph/seed/word data but has no Live capability and sends no local
      word pack; Level 2+ enables full Live, local word telemetry and broad
      local-plus-cached graph/search service.
- [ ] Single-purpose description, and a justification for every permission.

Sources: [policy updates](https://developer.chrome.com/blog/cws-policy-updates-2026),
[disclosure requirements](https://developer.chrome.com/docs/webstore/program-policies/disclosure-requirements).
