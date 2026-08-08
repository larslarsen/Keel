# WO-056 — Implement Option B (data-driven selectors) for YouTube — minimal first bite

| | |
|---|---|
| **Addressee** | Engineer (Sr Dev lane) / reviewer |
| **Status** | **Done 2026-08-07** |
| **Date** | 2026-08-07 |
| **Source** | Lars, 2026-08-07 — wants the extension to stay stable; daemon ships extraction config so YouTube changes don't require an extension republish. |

Implement `DESIGN_BOOTSTRAP.md` §"Option B — data-driven selectors (recommended)"
(lines 235-294) for **YouTube only**, as a small first bite. This is the
foundation TikTok (WO-057) and any future platform build on. Option B is
*already designed* — this ticket implements what the doc specifies; it does not
re-derive the mechanism.

## What Option B is (from DESIGN_BOOTSTRAP.md, verbatim in spirit)

- The extension keeps its parsing engine but holds **no selectors**. It receives
  a selector config from the daemon: which elements are cards, which child yields
  the title, which yields the href. "YouTube changes, the daemon ships new
  config, the extension binary never changes." (DESIGN_BOOTSTRAP.md:237-239)
- "The rot moves to the daemon, which is exactly where Lars wants it."
  (DESIGN_BOOTSTRAP.md:243)
- `extract.js` stays a pure function testable against fixtures (AGENTS.md), with
  the selector table as an *input* rather than a constant.
- **Web Store hard line:** "The extension may download data. It may never
  download logic." Config = CSS selector strings + optional attribute names
  only. No branching, no regex, no expression evaluation, no remote code.
  (DESIGN_BOOTSTRAP.md:249-294)

## Confirmed not implemented (code check 2026-08-07)

`extension/content/extract.js` and `extract_yt.js` hold `RENDERER_KEYS` /
`CARD_SEL` as constants. The bridge protocol (`extension/lib/protocol.js`,
`daemon/bridge/protocol.go`) has no selector-config message. So Option B is
design-only; this ticket builds it.

## Scope of THIS ticket (YouTube only)

1. **Selector-config interpreter in the extension.** A small, fixed-behaviour
   engine: given a config of `{ field: cssSelector [, attr] }`, find card
   elements and read the declared fields. All behaviours (take text, read
   attribute, walk to parent) stay compiled in; config only *selects where*.
2. **Daemon hosts the YouTube selector config** and ships it to the extension
   (new bridge message type — design it; validate against a schema at load,
   refuse the whole config on any violation per DESIGN_BOOTSTRAP.md:285-294).
   Unknown keys rejected, no daemon-supplied regexes.
3. **`extract.js` becomes the pure-function engine** taking the selector table as
   an argument (keeps fixture-testability — AGENTS.md). Existing YouTube fixtures
   (`watch_next_mixed.html`, `watch_next_compact.html`) must still pass.
4. **Honest limit, stated explicitly:** Option B covers *selector-level* YouTube
   changes. If YouTube changes *structurally* (Shadow DOM, cards no longer渲染
   as elements the compiled behaviours can walk), no config change fixes it — an
   extension update is still required. The promise is "most YouTube breaks are
   config-only," not "the extension binary never changes." Do NOT set the
   expectation that the extension can never be republished; state the selector-
   level limit in the implementation notes and any user-facing claim.

## Left to later tickets (explicitly out of scope here)

- TikTok surface + platform dimension (WO-057).
- Whether badge / duration / view-count *logic* can be expressed as data-only
  config — engineering call within the web-store line; if attempted, it is a
  follow-on bite, not this one.
- Config versioning scheme, bad-config fallback behaviour — engineer decides
  within DESIGN_BOOTSTRAP constraints (unknown keys rejected; fall back to
  last-known-good or hardcoded default as the engineer sees fit).

## Acceptance

- [ ] Extension ships with **no YouTube selectors baked in as constants**; it
      receives them from the daemon as validated data config (Option B).
- [ ] A YouTube selector change on the daemon propagates **without an extension
      republish**; existing YouTube extraction still passes its fixtures.
- [ ] Config is data only (selectors + optional attr); no logic/regex/branching
      from the daemon. Web-store compliance preserved.
- [ ] Implementation notes state the selector-level limit honestly (no false
      "never republish" promise).
- [ ] `go test ./daemon/...` and `npm run test` pass.

---

## Engineer response — 2026-08-07

Implemented as specified, YouTube only.

`extension/lib/selectors.js` holds the config as data and the validator. Every
behaviour stays compiled in — read this attribute, fall back to text, prefer the
longest candidate, parse a duration — and the config only says *where* to look.
`extract.js` readers now take the config as an argument and default to the
bundled one, so they remain pure functions testable against fixtures.

The daemon serves it (`GET_SELECTORS`, embedded `selectors_yt.json`) and honours
an override file at `<config>/keel/selectors_yt.json`, so fixing a YouTube change
needs neither a compiler nor a republish. A malformed override is ignored rather
than served.

Validation refuses the whole config on any violation: unknown keys, malformed
selectors, anything containing a URL, an arrow function, braces or `javascript:`,
and any config missing a shape the engine reads. Partial acceptance would mean
extracting under a mixture of two schemas.

Two tests carry the weight. One renames the card element in a fixture, asserts
the shipped config now extracts *nothing* — otherwise the test proves nothing —
and then recovers every impression by changing selectors alone. The other
redirects a field selector. Six more cover the validator's refusals.

**The bundled default is a fallback, not a redundancy.** Extraction has to work
before the daemon answers, and has to survive a daemon offering something
unusable. It ships as data rather than as constants in the engine.

**Honest limit, as the ticket required.** This covers selector-level change,
which is most breakage. It does not cover structural change: if cards move into
shadow roots or stop being elements the compiled behaviours can walk, no config
fixes it and the extension must be republished. Recorded in the module header
and the daemon file rather than only here. No user-facing "never republish"
claim has been made anywhere.

Also added `lib/selectors.js` to `web_accessible_resources` in both manifests.
A content-script import missing from that list fails silently and takes the whole
observer with it — the WO-009 failure, which cost ninety minutes of dead
collection.

`npm test`: 35 pass. `go test ./daemon/...`: pass.

### Not done here
Container selectors (`#secondary`, the home grid root) are still compiled in.
They break far less often than card selectors and widening the config to cover
them is a second bite, per the ticket's "minimal first" framing.
