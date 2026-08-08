# WO-057 — TikTok surface + platform-scoped panel (depends on WO-056)

| | |
|---|---|
| **Addressee** | Engineer (Sr Dev lane) / reviewer |
| **Status** | **Built 2026-08-08 — selectors unverified against live TikTok** |
| **Date** | 2026-08-07 |
| **Source** | Lars, 2026-08-07 — wants TikTok support; panel must be platform-scoped (show YouTube suggestions on youtube.com, TikTok suggestions on tiktok.com, never a blended feed). Live fullpage shows all platforms in one table. |

TikTok support builds on WO-056 (data-driven selectors). WO-056 moves selector
config to the daemon for YouTube; this ticket adds a TikTok config + the
`platform` dimension + platform-scoped UI. TikTok is a phase, not a toggle.

## Depends on WO-056

WO-056 implements `DESIGN_BOOTSTRAP.md` Option B (daemon-hosted selector config,
extension holds no selectors, web-store-compliant data only). This ticket assumes
that engine exists and adds a second platform's config to it. Do not start WO-057
until WO-056 is merged.

## Scope

### Work item 1 — TikTok selector config (reuses WO-056 engine)
- Author a TikTok config in the same data-only format WO-056 defines: which
  elements are cards, which child yields title / author / href / LIVE badge.
- TikTok's DOM is SPA-heavy and changes often — this is the expected rot, now
  living in the daemon config (editable without an extension republish), exactly
  the WO-056 payoff.
- Hand-authored TikTok fixtures + a test, mirroring the YouTube fixture
  convention (test/README.md:107 — commit fixtures + selector changes together).

### Work item 2 — platform dimension
- Add `platform` to core records: `impressions.platform` (`yt` | `tt`),
  `LiveRecord.Platform`, and the live index entry.
- TikTok IDs are not 11-char YouTube IDs, so `validateLiveMessage` / `merge`
  (daemon/swarm/live.go:344) must stop assuming `len(VideoID) == 11` — gate on a
  platform-aware ID check instead.
- TikTok livestreams map onto the existing live feature (`/live/{id}`, LIVE
  badge); reuse `LiveRecord` with a `Platform` field.

### Work item 3 — platform-scoped panel (the separation Lars requires)
- Active-tab URL → platform (`youtube.com` → yt, `tiktok.com` → tt). Replace the
  single `YT_URL` guard (sw.js:24, WO-040/WO-042) with a small platform map.
- **SidePanel** reads (suggestions, blocks) are scoped to the active platform.
  The sidepanel is **per-site, never blended** — no cross-platform suggestions in
  one view.
- **Live fullpage shows ALL platforms in one table.** The live page
  (`extension/page/`) is a standalone fullscreen tab opened via
  `browser.tabs.create` (page/index.js:125) with no associated source tab, so no
  active-URL platform can be derived. Render one **table** of live streams with a
  **platform column** (yt / tt / …) so both platforms appear together but stay
  distinguishable and sortable/filterable by platform. Rows are narrow, so merging
  is fine; the platform column preserves separation without subtab click-cost.
  `LIVE_SEARCH` already returns the whole index; the fullpage adds the platform
  cell from each entry's `Platform` field (work item 2).
- SidePanel auto-open keys off "any supported platform", not youtube.com only.
- Blocks: platform-scoped or cross-platform is a reviewer call; default proposal
  = platform-scoped (different creator ecosystems), with a possible "block across
  all platforms" toggle later.

### Work item 4 — permissions widen (AGENTS.md hard-rule exception)
- `host_permissions` + `content_scripts.matches` add `*://www.tiktok.com/*`.
- Deliberate, documented exception to AGENTS.md §hard-rules ("no patterns outside
  youtube.com"). Update AGENTS.md to record the exception + rationale
  (multi-platform support); Brave/Chromium/Firefox unaffected.
- No `tabs` permission, no optional permissions, no MAIN-world script — unchanged.

## Rejected approach — "skip the extractor, send raw data to the daemon"
DESIGN_v2.md §5.1 item 8 already rejected shipping raw `ytInitialData` (multi-MB
per navigation, larger blast radius if the daemon is compromised);
DESIGN_BOOTSTRAP.md:226-233 notes the objection is negotiable only with a reason,
and WO-056/Option B (normalize first, ship small records) is that reason. Sending
raw TikTok pages to the daemon also moves rot into Go with no fixture-test net and
still requires the extension to touch the page. Option B keeps the extension
stable AND payloads small AND web-store compliant.

## Acceptance
- [ ] TikTok config exists in the WO-056 data-only format; TikTok fixtures + tests
      pass.
- [ ] `platform` present on impressions + live records; `validateLiveMessage` no
      longer assumes 11-char IDs.
- [ ] SidePanel shows only the active platform's suggestions/blocks; no blended
      feed. Live fullpage = one table, all platforms, platform column.
- [ ] `host_permissions` includes tiktok.com; AGENTS.md records the exception.
- [ ] `go test ./daemon/...` and `npm run test` pass.

---

## Engineer response — 2026-08-08

All four work items are built, and one carries a caveat worth reading before
anyone trusts it.

**Platform dimension.** `impressions.platform`, `LiveRecord.Platform`, and the
suggestion walk are all scoped. Defaulted rather than nullable: every row
predating this is a YouTube row, and a NULL would drop silently out of every
scoped query. `validateLiveMessage` no longer assumes eleven characters — ids are
checked against the platform claiming them, and an unknown platform is refused
outright rather than defaulted, since a record naming a platform this build
cannot display would only put unusable entries in everyone's index.

Live entries and local sightings are keyed by platform *and* id. Nothing
guarantees two platforms never mint the same string, and one collision would
merge unrelated streams.

**Selector config.** `daemon/selectors_tt.json`, served per platform. Asking for
an unknown platform is an error rather than a YouTube fallback — silently
handing back the wrong selectors would produce extraction failures that look
like bugs in the engine.

**Panel scoping.** The side panel asks for its own platform's graph and links
with that platform's URL shape. The live page is one table across platforms with
a Where column, as specified: it opens with no site behind it, and "what is live
now" is a question that spans platforms.

**Permissions.** `*://www.tiktok.com/*` added to both manifests; AGENTS.md
records the exception and why the old absolute rule was wrong — the danger is
broad or growing permissions, not a second named site.

### The caveat

**The TikTok selectors are unverified.** The fixture is hand-authored from the
config, not captured from tiktok.com, so the tests prove the *engine* extracts
correctly when driven by a non-YouTube config — shapes, slot indexing, field
readers, badges, and that YouTube's config finds nothing in a TikTok page. They
prove nothing about whether those selectors match real TikTok markup.

TikTok hashes and rotates its class names, so the config leans on `data-e2e`
attributes, which are more stable. Whether they are the *right* ones needs a
capture from a logged-out session, exactly as `test/README.md` requires for
YouTube.

Until then: the platform machinery is real and tested; the TikTok config is a
hypothesis.

Also made id parsing platform-aware, which the ticket did not mention and
without which nothing worked — `videoIdFromHref` looked for an eleven-character
`v=` parameter, so no TikTok link resolved at all. Kept compiled rather than
configured: an id's shape is a fact about a platform, and a daemon able to
redefine it could have the extension record arbitrary strings as video ids.

`npm test`: 48 pass. `go test ./daemon/...`: pass.
