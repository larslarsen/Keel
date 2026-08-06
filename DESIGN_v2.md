# Keel — Architecture v2

**Status:** Design proposal, supersedes `/home/lars/EEE/old-strategy/masterplan.md` and the v1 strategy documents.
**Date:** 2026-08-02
**Audience:** the implementing agent, and reviewers.

---

## 0. What changed from v1, and why

v1 was four pillars: (1) archive YouTube videos to IPFS, (2) audit the recommendation
algorithm, (3) a better local feed/search, (4) an antitrust legal strategy for when Google
bans the extension.

Research invalidated three load-bearing assumptions. The changes below are not stylistic.

| v1 claim | Finding | v2 response |
|---|---|---|
| The "Trojan Horse" split (extension reads DOM, separate binary downloads video) keeps us Web Store compliant | CWS policy prohibits **facilitating** download of YouTube videos. The policy targets facilitation, not the location of the write syscall. Downloaders were purged from the store in 2025. | **Video archiving cut.** Metadata-only preservation (§7). |
| The Betamax precedent protects us | Betamax is a *contributory copyright infringement* doctrine. The exposure here is **DMCA §1201** anti-circumvention, which has no fair-use defense and no Betamax safe harbor. In *Yout v. RIAA* the district court held Yout failed to show it doesn't circumvent YouTube's rolling cipher. | Legal strategy rebuilt around §3, not litigation-as-marketing. |
| Requiring a valid `ytInitialData` state hash means "an attacker must produce a valid blob, forcing Google to leak their internal API signatures" | **`ytInitialData` is unsigned JSON embedded in the page.** Nothing signs it. A schema-valid blob costs an attacker essentially nothing to generate. Hashing it proves nothing about provenance. | Sybil resistance rebuilt on statistics + rate-limiting, not fake cryptography (§6.4). We state plainly that this is a survey instrument, not a proof system. |
| K-anonymity (k≥50 delay buffer) makes funnel contributions safe | Recommendation trails are high-dimensional, like search logs. K-anonymity suffers the curse of dimensionality and **has no compositionality** — repeated releases compose into re-identification. | Threshold aggregation at the crypto layer (STAR) + differential privacy (§6). Raw trails never leave the device. |
| OrbitDB as a global write-heavy index | Merkle-CRDT eventual consistency, every peer replicates the full log, no access control, no spam resistance, Helia defaults to an in-memory blockstore. A global index of every video every user watches does not scale this way. | Aggregate via STAR at a single untrusted server; **decentralize the publication, not the write path** (§7.3). Publication uses free channels only. |
| Get banned, then sue; litigation is a multi-year user-acquisition funnel | Federal antitrust needs standing, counsel, and years of money. Meanwhile *hiQ v. LinkedIn* settled with a **$500k judgment against hiQ** on breach of contract — the CFAA claim was the one hiQ won. Contract, not crime, is the realistic threat. | Minimize ToS surface; pursue the DSA Art. 40 vetted-researcher channel (§3.3). |

Two pieces of prior art define the survivable shape:

- **Mozilla RegretsReporter** — 37,380 users, opt-in recommendation-data donation, peer-reviewed
  findings, no archiving, still operating. This is the model.
- **NYU Ad Observer** — Facebook disabled the researchers' accounts for "unauthorized scraping."
  The research survived and helped drive DSA Art. 40. Lesson: retaliation is real, so never make
  a platform account a dependency, and have an institutional home.

**Also note, this is live right now:** CWS policy updates announced 1 July 2026 begin enforcement
**1 August 2026** — yesterday. The Limited Use policy now requires that *any user data collected be
strictly necessary to the extension's disclosed single purpose*, and all collection be prominently
disclosed. §3.1 is written to that standard.

---

## 1. Goals and non-goals

### Goals

- **G1 — Measurement.** Produce a defensible, reproducible public dataset of how YouTube's
  recommendation surfaces steer attention, at cohort granularity.
- **G2 — Preservation & relocation.** Keep a durable record of *what existed and what was removed*:
  metadata and recommendation context, with tombstones when videos disappear — and enough of an
  identity fingerprint to **find the same content re-hosted elsewhere** (Rumble, Odysee, archive.org).
  The fingerprint is the point; the bitstream is not.
- **G3 — Utility.** Give the user a genuinely better YouTube — strict boolean search, real
  chronological sort, persistent channel blocking, feed sanitization — entirely locally.
- **G4 — Survivability.** Remain installable from the Chrome Web Store. Compliance is a
  design constraint, not an afterthought.

### Non-goals

- **NG1** — Downloading, storing, or redistributing video or audio bitstreams. Out of scope
  permanently, not "phase 2." See Appendix A for what changing this would cost.
- **NG2** — Circumventing any technical protection measure, rate limit, bot detection, or
  authentication.
- **NG3** — Automated crawling. We observe only what the user's own browser actually rendered.
- **NG4** — Proving data provenance cryptographically. Not achievable without platform cooperation;
  we will not claim it.
- **NG5** — A P2P database with a live global write path.

### Success criteria

The instrument works if an independent party can take a published release, re-run the analysis
scripts, and reproduce our stated findings — and if a video removed in month *N* still has a
complete metadata and recommendation-context record in month *N+12*.

---

## 2. Architecture overview

Two trust zones. The browser is assumed hostile; everything that accumulates lives outside it.

```
  BROWSER (untrusted — Google's runtime)
┌───────────────────────────────────────────────────────────────────┐
│ EXTENSION — thin client. No observation data persisted, ever.     │
│   content script: read rendered DOM → normalize → in-memory only  │
│   sidepanel: render what the daemon sends; apply, never decide    │
│   chrome.storage: blocklist + toggles + consent state ONLY        │
└───────────────────────────┬───────────────────────────────────────┘
                            │  Keel Bridge (native messaging, §8.1)
                            ▼
  LOCAL MACHINE (trusted)
┌───────────────────────────────────────────────────────────────────┐
│ DAEMON (Go) — the product. Installs the extension (§2.2).         │
│   SQLite corpus (kept indefinitely) · LIV (never leaves machine) │
│   utility engine: boolean search, chrono sort, scrubbing, funnel  │
│   preservation: records, fingerprints, tombstones (§7)            │
│   contribution client: STAR (Prio behind the same seam, §6.2)     │
└───────────────────────────┬───────────────────────────────────────┘
                            │  opt-in only, default off (§6.1)
                            ▼
┌───────────────────────────────────────────────────────────────────┐
│ L3 → signed public bundles → IPFS                                 │
│ L2 → STAR → 1 aggregation server + independent OPRF helper        │
│                    ↓                                              │
│      signed releases → Zenodo / GitHub / IPFS / BitTorrent        │
└───────────────────────────────────────────────────────────────────┘
```

### 2.1 The daemon is required. The extension is a thin client.

v1's "dumb extension / smart daemon" split is **retained and is correct.** An earlier v2 draft made
the daemon optional and moved local storage into the extension; that was wrong on the project's own
threat model and is reverted.

**Why the corpus must not live in the browser.** Extension IndexedDB is origin-isolated and is wiped
on uninstall, so on data-at-rest exposure alone it compares well to a daemon's SQLite file. That is
the wrong axis. **Chrome is Google's software and Google is the adversary here.** Months of
accumulated watch history stored inside the adversary's own runtime is an unnecessary concentration
of risk. Moving observation out of Chrome does not protect any single impression — the content
script runs in Google's process either way — but it protects the *accumulation*, and the
accumulation is the sensitive asset.

**It also lightens Web Store review.** CWS data disclosures include a "web history" category, and
Limited Use requires disclosed collection to be strictly necessary to the single purpose. An
extension retaining 90 days of viewing history triggers a heavy review. An extension retaining only
a channel blocklist and some toggles declares almost nothing.

**The data boundary:**

| Data | Lives in | Rationale |
|---|---|---|
| Channel blocklist, category toggles, UI settings, consent state | Extension (`chrome.storage`) | User-authored preferences. Small, not observations, plainly within single purpose. |
| Impressions, watch history, the corpus, the Local Interest Vector | **Daemon only** | Never persisted in the browser — not IndexedDB, not `chrome.storage`, not anywhere. |

The extension may hold impressions in memory transiently in order to batch them across the bridge.
It must never write them to any persistent browser storage.

### 2.2 Distribution: the installer is the front door

Users install the **desktop app**, and the app registers the extension with the browser. Verified
mechanism:

| Platform | Method | User prompt |
|---|---|---|
| Windows | Registry `HKLM\Software\Google\Chrome\Extensions\<id>` with `update_url` | External-extension warning; user must enable |
| macOS | JSON in `/Library/Application Support/Google/Chrome/External Extensions/` | External-extension warning; user must enable |
| Linux | JSON in `/opt/google/chrome/extensions/` | **None** — installs automatically |

**Consequences that must not be missed:**

- **The extension must still be listed on the Chrome Web Store.** Since Chrome 33 (Windows) and 44
  (macOS), external install requires `update_url` to point at the Web Store; local CRX paths are
  refused. Only Linux may self-host. CWS compliance (§3.1) is therefore a hard requirement of this
  model, not something the installer routes around.
- The extension ID must match the published listing, so the listing has to exist before the
  installer can reference it.
- **The extension must degrade gracefully with no daemon**, showing a clear "desktop app not
  running" state and never throwing. This is what satisfies CWS Minimum Functionality when a
  reviewer evaluates it without installing the binary, and it is also the normal state whenever the
  daemon is restarting. Malformed or absent daemon input must produce a clean error state, never a
  broken panel.

### 2.3 Code signing — mostly solvable at zero cost

Daemon-first puts a downloadable binary on the critical path, so installer friction matters. Nothing
here *blocks* unsigned software; it warns. Accurate current behaviour:

| Platform | Unsigned behaviour | Cost to remove it |
|---|---|---|
| **Linux** | No warning at all | **\$0** |
| **Windows** | SmartScreen "Windows protected your PC" → *More info* → *Run anyway*. Not blocked. Reputation attaches to the binary hash, so every release re-triggers it. | **\$0** via SignPath Foundation |
| **macOS** | Blocked on first launch; user goes to System Settings → Privacy & Security → *Open Anyway*. Sequoia removed the older Finder right-click bypass. | \$99/yr Apple Developer Program (notarization) |
| **macOS (Apple Silicon)** | Binaries need *a* signature to execute, but an **ad-hoc signature is free** (`codesign -s -`, no Apple account). | **\$0** for ad-hoc |

**Windows is free if Keel is open source.** The [SignPath
Foundation](https://signpath.io/solutions/open-source-community) issues free OV code-signing
certificates to open-source projects with a publicly available codebase and a recognized licence,
via a managed signing pipeline. Apply early — approval takes days to weeks.

**This makes open-sourcing a practical requirement, not just a philosophical one.** It is already
required by the project's central claim: "nothing leaves your machine" is only credible if a
stranger can read the source and confirm it. Free Windows signing is a second, concrete reason.

**Package managers avoid most of the remaining friction** — Homebrew, winget, Scoop, apt, AUR, Nix.
Early users are disproportionately the people who already use them. Prefer these over
direct-download for as long as possible.

**Net position:** Linux \$0, Windows \$0 (SignPath), macOS \$0 early via Homebrew plus ad-hoc
signing, deferring the \$99/yr notarization until there is grant funding (§11). The zero-budget
constraint (§7.3) holds.

---

## 3. The compliance envelope

This section is a constraint set. Every design decision below traces to it.

### 3.1 Chrome Web Store

- **Name.** The product is **Keel**. Do **not** put YouTube, YT, or any variant in the extension
  name: YouTube's branding guidelines state you *"must never use the YouTube name or any
  abbreviation, acronym, or variant of the word YouTube, such as YT or You-Tube in conjunction with
  the overall name of your application."* The previous working name ("Audit Bridge for YouTube")
  violated this and additionally signalled adversarial intent to a reviewer. Reference YouTube
  descriptively in the *description* only ("Works with YouTube") — nominative use in copy is normal
  and safe; name incorporation is not.
- **Single purpose:** *"Give people control over the video recommendations they see."*
  Phrase it as user-facing control, not as research. This is a well-understood category a reviewer
  has approved many times; "audit the algorithm" is one they have not, and it escalates. The
  measurement work is *in service of* that control — you cannot control what you cannot see — which
  is what keeps opt-in contribution inside the single purpose. Do not add features outside it: no
  downloader, no ad-blocker, no unrelated productivity tooling.
- **Store listing tone.** Lead with what the user gets: a quieter feed, real chronological order,
  search that obeys operators, channels that stay blocked. Contribution is a footnote the user opts
  into, not the pitch. Nobody installs an audit.
- **Limited Use (enforced from 2026-08-01):** collected data must be *strictly necessary* to that
  purpose. The utility plane collects nothing off-device, so it does not implicate this rule at all.
  The contribution plane collects recommendation edges, which are strictly necessary to the
  transparency purpose. Nothing else may be collected. No analytics SDKs.
- **Disclosure:** an in-extension consent screen, plus the store listing, plus a public privacy
  policy. Data handling changes require proactive re-notification.
- **Permissions — minimise aggressively.** `["sidePanel", "storage", "nativeMessaging", "alarms"]`
  plus `scripting` and a **path-scoped** `host_permissions: ["*://www.youtube.com/watch*"]`
  (*amended by WO-008, 2026-08-03*: the path-scoped host + `scripting` let the SW re-inject the
  observer into already-open `/watch` tabs after a reload/update — without them the tab stays dark
  until the user navigates). The host permission is scoped to exactly the surface already declared
  in `content_scripts.matches` and grants nothing the content script does not already read. No
  `tabs` permission, no optional permissions, no broader host patterns. Enable the SidePanel from a
  content-script `PAGE_CONTEXT` message using `sender.tab.id`, rather than reading `tab.url` in
  `tabs.onUpdated` — the latter is the thing that forces a broad host permission. Match
  `content_scripts` to the surfaces actually in scope. *Amended by WO-010, 2026-08-03*: that is now
  `*://www.youtube.com/*`, with `host_permissions` widened to match. Two reasons, both load-bearing:
  a soft SPA navigation never injects, so a `/watch*`-only match records nothing when the user
  reaches a video by clicking rather than by hard-loading; and the homepage is itself a collection
  surface. The script must stay inert on pages that are neither `/` nor `/watch` — no observer, no
  parse, no timers. **`/results*` is deliberately not in scope**: search results are what the user
  asked for, not what YouTube chose to show them, and only the latter is a recommendation (WO-010
  §5). Justify every permission in the listing, and re-justify when scope widens. See
  `handoff/WO-001` §3, `handoff/WO-008` and `handoff/WO-010`.
- **Code readability:** ship readable, unminified, un-obfuscated source. No remotely hosted code.
  (The v1 `manifest.json` is minified onto one line — fix.)

### 3.2 YouTube

- **Never use the YouTube Data API.** This is counterintuitive and load-bearing. The YouTube API
  Services Developer Policies require stored API data — explicitly including *video titles, creator
  names, and descriptions* — to be **deleted or refreshed within 30 calendar days**. Accepting that
  agreement contractually destroys the ability to keep a permanent archive. Data the user's own
  browser rendered is not "API Data" under an agreement we never entered. **The DOM is the
  legally cleaner source.**
- **No automated crawling.** Record only what the user actually viewed. The single exception is
  availability re-probing (§7.2), which is isolated, rate-limited, and independently disableable.
- **No logged-in-only or private data.** Nothing behind an account boundary, nothing about other users.

### 3.3 Legal posture

- The realistic threat is **breach of contract**, not CFAA. *hiQ* won on CFAA and still paid $500k
  on contract. Minimize ToS surface; assume the ToS is enforceable against us.
- The user is the one browsing. The extension acts as the user's agent over content YouTube already
  sent them. That is the data-portability framing, and it is only credible if NG1–NG3 hold.
- **DSA Article 40** (delegated act adopted 2 July 2025) creates a lawful data-access channel for
  vetted researchers. The strategic play is to be the independent instrument that vetted researchers
  and regulators use — which requires an academic partner (§11, open decision).
- **Venue: the EU claim is the strong one.** v1's "Essential Facility Doctrine" argument is weak in
  the United States — *Verizon v. Trinko* (2004) stated the Supreme Court has never recognized the
  essential-facilities doctrine and declined to adopt it. The EU has no equivalent limitation and has
  repeatedly fined Google (Shopping, Android, AdTech). A US-based project with a large EU user base
  should treat the EU as the primary venue for any competition claim, and the US as secondary.
- **Compliance is the offensive strategy, not a defensive one.** The stated goal is to be plaintiff,
  not defendant. That requires (a) **standing** — removal from the Web Store *while fully compliant*
  is a cognizable injury; removal for actually shipping a downloader is not, and (b) **clean hands** —
  any §1201 exposure invites a counterclaim that converts us from plaintiff to defendant. Every unit
  of legal risk shed is leverage gained. NG1–NG3 are therefore strategic assets, not concessions.
- Expect account-level retaliation against contributors, per NYU. Therefore: never require a
  YouTube login, never touch account state, and make the utility plane fully functional for a user
  who never contributes anything.

---

## 4. Observation plane

### 4.1 How we read the page

**Primary mechanism: `MutationObserver` over rendered DOM, ISOLATED world.**

This is deliberate and is both the technically robust and the legally coherent choice. Reading what
was rendered is precisely the "like a screen reader" claim the legal posture depends on. Intercepting
`fetch`/XHR would require `world: "MAIN"`, is materially closer to "scraping," and undercuts §3.3
for no analytical gain.

- Initial page load: `ytInitialData` may be read by parsing the text of the inline `<script>` from
  the DOM. Note this is still DOM reading — we never execute page-context code and never enter the
  MAIN world. Content scripts in the ISOLATED world cannot see `window.ytInitialData` directly.
- SPA navigation: YouTube does not reload. Listen for `yt-navigate-finish` and re-arm the observer.
  Treat every navigation as a new `page_load_id`.
- Normalize **in the content script**. Raw `ytInitialData` must never cross a process boundary
  (see §5.1 defect 8).

### 4.2 Impression record

Produced by the content script; the only thing the rest of the system sees.

```ts
type Surface = "WATCH_NEXT" | "HOME" | "SEARCH" | "CHANNEL" | "SHORTS";

interface Impression {
  page_load_id: string;      // uuidv4, groups one render
  observed_at: number;       // unix ms, full precision, LOCAL ONLY
  surface: Surface;
  context_video_id: string | null;  // video being watched; null for HOME
  context_query_hash: string | null; // sha256(normalized query)[0:16]; raw query never stored
  slot_index: number;        // 0-based position within the rail
  video_id: string;
  channel_id: string;
  title: string;
  duration_s: number | null;
  view_count: number | null;
  published_at: string | null;      // ISO8601 if exposed
  badges: string[];                 // "LIVE" | "VERIFIED" | "SPONSORED" | "AGE_GATED"
}
```

Note `context_query_hash`: search queries are among the most identifying data a person produces.
We never store the raw string, even locally.

**Channel attribution is first-paint only (WO-013).** On WATCH_NEXT, `channel_id` is filled reliably
for the cards present in `ytInitialData` at navigation (typically the first ~20 rail slots): that
JSON carries `browseId` on lockups. Cards that appear after scroll arrive from YouTube continuation
requests. We do not intercept `fetch`/XHR (§4.1) and do not call the Data API (§3.2). Live
`yt-lockup-view-model` markup exposes the channel *name* as text and an avatar control, but **no
`/channel/UC…` or `@handle` href** — established WO-005, reconfirmed on continuation-loaded cards.
Continuation payloads are not written into readable `<script>` / `ytInitialData` in the DOM. So
there is no legitimate ISOLATED-world source for `channel_id` past the initial rail. Rows still
record `video_id` and `slot_index` with `channel_unknown: true`. Exports carry
`channel_unknown_count` / `channel_known_count` in the header; the SidePanel surfaces the same
counts so channel-level analysis cannot silently ignore the gap.

**Channel hard block (WO-016; supersedes WO-015 page-level hide).** The blocklist of `UC…` ids lives
in the **daemon SQLite** (`channel_blocklist` table). The extension does not decide and does not
hide YouTube’s cards: the SidePanel omits blocked channels from *its* list only. The corpus remains
a faithful witness — blocked channels are still observed and stored. When Keel surfaces its own
suggestions later, the same list filters that view. Per-card CSS on youtube.com was rejected
(store-review risk, CPU cost, and incomplete under WO-013).

**Channel catalogue backfill (WO-016).** On insert, if `channel_id` is missing, the daemon fills it
from any prior impression of the same `video_id` that has a channel, and sets `channel_unknown = 0`.
A one-shot catalogue pass on open updates existing unknowns the same way. No display-name fallback.

### 4.3 Local store

- Impressions land in IndexedDB (extension) or SQLite (daemon, when present).
- **Kept indefinitely by default.** The corpus is the product: recommendation quality depends on
  history depth, and G2 (preservation) requires that a video removed in month *N* still has a record
  in month *N+12*. A 90-day sweep would destroy exactly what this exists to keep.
- Retention is **user-controlled**, not policy: a setting offering "keep everything" (default), or a
  user-chosen limit. Delete-all is always one click away.
- This is the user's own data, about themselves, on their own machine. We never receive it, so data
  minimisation obligations do not attach to us — and imposing deletion on them is a product defect,
  not a privacy feature.
- The user can export everything as JSON and wipe everything, from one screen, with no dark patterns.

---

## 5. Extension (MV3)

### 5.1 Defects in the v1 prototype to fix

The implementing agent should treat these as known-broken, not as reference:

1. **`host_permissions: ["://www.youtube.com/watch", "://www.youtube.com/results"]` is an invalid
   match pattern** — no scheme. The extension will fail to load. Use `"*://www.youtube.com/*"`.
2. **No `content_scripts` declared at all.** The entire architecture depends on observing the page
   and there is currently no mechanism to do so.
3. **`try/catch` around `connectNative` is dead code.** Connection failure surfaces asynchronously
   via `onDisconnect` + `chrome.runtime.lastError`, never as a synchronous throw.
4. **No reconnect.** `onDisconnect` sets `nativePort = null` and stops. Per Chrome's documentation,
   a native port keeps the service worker alive, but *if the host dies the SW dies with it* — you
   must re-`connectNative` inside `onDisconnect`. As written, one daemon hiccup permanently ends the
   session.
5. **`tabs.onUpdated` fires many times per navigation.** `setOptions` is called on each. Guard on
   `info.status === "complete"`.
6. **`new URL(tab.url)` will throw** on non-http schemes. The `if (!tab.url)` guard is insufficient.
7. **Only `/watch` is enabled.** The home feed and search results are the two highest-value audit
   surfaces and both `host_permissions` and the sidepanel logic exclude them.
8. **The protocol ships whole `ytInitialData`** (`DOM_REPORT: { yt_state: object }`). It is often
   multiple MB, and it hands the daemon raw page state for no reason. Normalize first (§4.2).
9. **No message validation**, despite the v1 doc claiming a "Schema Enforcement" rule.
10. **`manifest.json` is minified to a single line.** CWS code-readability expects readable source.

### 5.2 Target manifest

```json
{
  "manifest_version": 3,
  "name": "Keel",
  "version": "0.1.0",
  "minimum_chrome_version": "116",
  "description": "See and control how YouTube recommends videos to you.",
  "permissions": ["sidePanel", "storage", "nativeMessaging"],
  "content_scripts": [
    {
      "matches": ["*://www.youtube.com/watch*"],
      "js": ["content/observer.js"],
      "run_at": "document_idle",
      "world": "ISOLATED"
    }
  ],
  "side_panel": { "default_path": "sidepanel/index.html" },
  "background": { "service_worker": "background/sw.js", "type": "module" },
  "action": { "default_title": "Open Keel" },
  "content_security_policy": {
    "extension_pages": "script-src 'self'; object-src 'self'"
  }
}
```

`minimum_chrome_version: 116` — WebSocket-based SW keepalive landed in 116 and we want the
modern service-worker lifetime semantics.

### 5.3 Feed modification rules

Carried forward from v1 because this part was right:

- **Never mutate YouTube's DOM structurally.** Fighting their DOM is unbounded tech debt and looks
  like tampering.
- The SidePanel renders *our* feed in its own document.
- Hiding native elements, when the user enables the custom feed, is done with **CSS only**.
- The user can toggle between YouTube's feed and ours at any time.

**Display suppression (WO-009).** Under user control the extension injects a single
`<style id="keel-hide-recommendations">` on `document.documentElement` that applies
`display: none` to the watch-page secondary column (`ytd-watch-flexy #secondary`).
**Watch page only;** the home grid is never hidden (no player, no space contention). After
adding or removing the style, the content script dispatches a `window` `resize` event on the
next animation frame so `ytd-watch-flexy` recomputes player size — a synthetic DOM event, not
MAIN-world script (§4.1). Player width may still be imperfect; inconsistent-but-usually-wider
is accepted over a permanent gutter. This does **not** reorder, filter, substitute, or block
network delivery of recommendations — the card markup and `ytInitialData` still arrive; only
paint (and lazy thumbnail fetches inside a `display: none` subtree) are suppressed.
**Collection is unchanged:** extractors read attributes and `textContent` only, so a hidden
subtree remains fully measurable and the corpus must be identical with the toggle on or off.
Default mode is `on` (hide the rail); `off` leaves YouTube alone. Legacy
`never` / `with-panel` / `always` values are coerced on read (WO-017). The preference is a string
in `chrome.storage` — configuration, not observation data (§2.1). A small header icon toggles it.

Live updates to content scripts use `tabs.sendMessage` (host permission on youtube.com), not
`runtime.sendMessage` alone — the latter reaches extension pages only.

This is the first intentional page *interaction* beyond observation. Prior work orders kept Keel
strictly non-interacting; an injected stylesheet can be blamed for YouTube layout breakage, and
store listings must not claim "reads only." Support and diagnosis should assume that possibility.
Do **not** pile on flexy overrides or forced theater mode without a new work order.

---

## 6. Contribution plane (opt-in, default off)

### 6.0 Primer — what the privacy machinery is and why it exists

Read this before §6.1. The rest of the section is unintelligible without it.

**The problem.** We want to publish "in the US, video B is recommended under video A ~40,000 times a
day, usually in slot 3." The naive way is for everyone to send observations to a server that counts
them — but then that server holds everyone's viewing history, and we have built the surveillance
system we exist to criticise. One subpoena or one breach ends the project. The requirement is:
**learn the totals, never learn who contributed what.**

**STAR — "which things are common?"** Every contributor locks an observation in a box whose key is
derived *from the observation itself*. Identical observations produce identical keys. A server
holding 50 boxes with the same key can open all of them; with 49 it can open **none**, ever — not
"won't", *can't*. So observations 50+ people share become visible, and observations unique to one
person stay sealed permanently. The threshold is enforced by mathematics, not by our promise. This
is why STAR needs only **one** server: it isn't trusted, it's incapable.

**The OPRF randomness helper.** STAR's weakness: if the space of possible observations is guessable
— and video IDs are enumerable — the server can try every possible observation, compute the key each
would produce, forge the 49 boxes it lacks, and open your single real one. The fix is to derive the
key from the observation *plus a secret a second party holds*. Contributors ask that party to help
compute it, and the math ("oblivious") means **the helper never learns what the observation was**.
The aggregator can no longer precompute; it would have to query the helper in real time for every
guess, which is rate-limitable and visible.

What the helper actually holds: **a secret key, rotated per epoch**, plus whatever connection
metadata it fails to avoid logging, plus an uptime obligation. It never sees measurements, reports,
or identities. It is a small ask — not a zero ask. It must not be operated by whoever runs the
aggregation server; if one party holds both, it can brute-force its own users and the guarantee is
void.

**Prio / DAP — "what is the exact number?"** Secret sharing: split your value into two random pieces
that individually mean nothing, send one to each of two servers, each sums its pieces, and only the
two totals are combined at the end. Nobody ever saw an individual value; the sum is exact. This
requires **two non-colluding servers** — if they combine their shares they reconstruct your value.

**Why the helper does not lower the user count.** Common misreading: the helper does *not* reduce K.
Fifty is fifty either way. The threshold stops the server reading rare things; the helper stops the
server *faking* its way past the threshold. They defend different attacks and neither substitutes
for the other.

### 6.1 Consent — the slider

The v1 Anonymity Slider was right, and its three levels map cleanly onto the three contribution
paths. Original naming retained. **What v1 got wrong was narrow**: it sent full funnel state
(video IDs + full cohort ID) at Level 3 *and* claimed K-anonymity protection for those contributors.
Those cannot both be true. Full disclosure is a legitimate option; it just has to be labelled
honestly rather than dressed as anonymity.

| Slider setting | What leaves the device | Mechanism | Partner needed |
|---|---|---|---|
| **L1 — Strictly Personal** *(default)* | Nothing, ever | — | No |
| **L2 — Cohort Aggregator** | Aggregate edge counts only | STAR (§6.2) | **Yes** — OPRF helper |
| **L3 — Transparency Contributor** | Signed public observations, attributed | Direct publish to IPFS | No |

**L1 is and remains the default.** Not only because CWS Limited Use and GDPR both require
freely-given specific consent rather than a pre-ticked box, but because a transparency tool that
collects by default is precisely the behaviour we criticise.

**L3 must be labelled as public.** The consent screen must say, in plain words: *this is published,
it is attributable to you, anyone including YouTube can read it, and it cannot be retracted once
mirrored.* L3 contributors are exposed to the NYU Ad Observer scenario — platform retaliation
against identified researchers. That is their informed choice to make; it is not ours to obscure.

**There is no setting that uploads a raw watch trail under a promise of anonymity.** That capability
must not exist in the codebase.

### 6.2 Aggregation

Do not invent privacy machinery. Use deployed protocols.

**Decision: build STAR first. Prio follows in the same phase, not indefinitely deferred.**

*Amended 2026-08-03 by Lars.* The original decision was STAR only, on the grounds that it answers the
headline question and removes the two-aggregator requirement from the critical path. That reasoning
still holds for **sequencing** — STAR ships first and the system works without Prio.

It does not hold as a permanent exclusion. The table below states plainly that STAR cannot answer
*"how common are rare-but-harmful pathways"* — and that is the finding most likely to matter.
Suppression and harm live in the tail, and a system structurally blind below K will report that the
tail is empty. Building STAR alone and calling the question answered would bake a bias toward popular
content into the only evidence we produce.

**What Prio does and does not fix.** It removes the K-threshold, so rare events become countable, and
it yields denominators. It does **not** make small-N outputs safe to publish: with few contributors
an aggregate still leaks, and differential-privacy noise at that scale destroys the signal. Prio is
for *volume with sparse events*, not for the cold-start period. Nothing about it shortens the wait
for the first useful result.

STAR does not merely discover values, it counts them — once K clients report an identical
measurement, the aggregator learns the value *and* the number of reports. Because the measurement is
a tuple of `(from, to, surface, slot_bucket, day_bucket, cohort)`, STAR alone yields counts broken
out by slot and country. That answers the headline research question directly, and it removes the
two-non-colluding-aggregator requirement from the critical path entirely.

Where Prio would still earn its place, later:

| Research question | STAR alone? |
|---|---|
| Which edges dominate? Where are the gravity wells? | **Yes** |
| How often, in which slot, in which country? | **Yes** |
| What *fraction* of all recommendations do X? | No — needs a denominator including rare events |
| How common are rare-but-harmful pathways? | No — STAR is blind below K by construction |

STAR's blindness below the threshold is a **known, disclosable bias toward popular content**. It
must be stated in every published methodology, not buried.

#### Server requirements

| Layer | Servers required | Who | Phase |
|---|---|---|---|
| STAR aggregation | **One** untrusted server | Us | P4 |
| STAR randomness (OPRF) | One helper, independent of the above | Third party | P4 |
| Prio/DAP numeric aggregates | **Two** non-colluding aggregators | Us + Divvi Up/ISRG | Deferred |

STAR is explicit that it "allows a single untrusted server to perform the aggregation process, as
opposed to Poplar which requires two non-colluding servers."

If Prio is ever built: **Divvi Up's operating model is precisely this arrangement** — the project
hosts one aggregator, Divvi Up hosts the other. Operated by ISRG (the Let's Encrypt nonprofit); DAP
is co-developed by ISRG, Cloudflare, and Mozilla. Verify current intake status and cost early; lead
times are long.

#### Designing for Prio without building it

Prio is **deferred, not excluded**. Build the seam now so enabling it later is configuration plus a
consent flow, never a rewrite:

- **Backend interface.** All contribution goes through one boundary:
  `ContributionBackend.submit(measurement) -> Result`. `StarBackend` ships in P4. `PrioBackend`
  implements the same interface later. Nothing above this line knows which is active.
- **Runtime capability document.** The client fetches (or receives by update) a signed config
  declaring which backends are live, and their parameters: `K`, epoch length, aggregator endpoint,
  OPRF helper endpoint, DP parameters. Adding a Prio aggregator becomes a config change plus a
  client that already understands the field.
- **Measurement shape carries both.** Keep the categorical tuple (STAR) and any numeric quantities
  separable in the internal record, so a Prio backend can derive its inputs without re-instrumenting
  the observer.
- **No dead crypto.** Ship the interface and the config field; do **not** ship an unused,
  unreviewed Prio implementation. Untested cryptographic code in a privacy tool is a liability.

**The constraint that makes this not a silent toggle:** CWS Disclosure Requirements state that
developers must *proactively disclose to users if their data handling practices change at any point
after the initial installation*. Enabling Prio changes what leaves the device. Therefore:

> Switching on an additional backend **requires re-consent**, not a config flip. Users who consented
> to L2-under-STAR have not consented to L2-under-STAR-plus-Prio. The client must treat an unknown
> or newly-added backend as **disabled until the user affirmatively re-consents**, and must fail
> closed if the capability document offers a backend the installed version does not recognise.

Build that re-consent gate in P4 alongside the first backend, while there is only one to reason
about. Retrofitting consent state machines is where privacy tools historically break.

#### When to switch collection on — measure, do not guess

The threshold is not "50 users." It is **50 users who observed the same edge within one epoch**.
Because YouTube viewing is heavily concentrated, a modest user base surfaces the head of the
distribution — which is where the gravity wells are — while the long tail needs real scale.

Epoch length is the tunable knob: a one-month epoch is roughly four times likelier to clear K than a
one-week epoch, at the cost of permitting more linkage. Run long epochs early, shorten as you grow.

**This is measurable before any collection happens.** Phases 0–3 build a purely local corpus. Each
user can compute locally: *"of the edges I saw last month, how many were seen by ≥K of us?"* — or
simply estimate concentration from a handful of volunteers. Switch L2 on when the local data says it
will produce a dataset, not before. Collecting reports that mathematically never decrypt buys risk
and no research.

Each `EdgeObservation` is submitted **independently**, so trails cannot be reassembled server-side
even by a malicious aggregator:

```ts
interface EdgeObservation {
  from: string;   // video_id | "__home__" | query hash
  to: string;     // video_id
  surface: Surface;
  slot_bucket: "0" | "1" | "2" | "3-5" | "6-10" | "11+";
  day_bucket: string;  // "YYYY-MM-DD", UTC
  cohort: string;      // see 6.3
}
```

### 6.3 Cohorts

**Country + interface language only.** Coarse, stable, low-dimensional.

v1's "Region + Interest Drift" cohort must not be built. Interest drift is a behavioral fingerprint;
it is among the *most* identifying things you could attach to a report, and attaching it to funnel
edges would undo the entire privacy design. Country comes from the browser's own locale/timezone —
we do not IP-geolocate.

### 6.4 Abuse resistance (honest version)

We cannot prove an observation came from a real YouTube page. Nothing signs `ytInitialData`. So:

- **Rate limit per client** using anonymous credentials (Privacy Pass style) — bounds contribution
  without identifying anyone.
- **STAR's K-threshold** already forces an attacker to control ≥K clients to surface a fake edge.
- **Statistical outlier detection** on aggregate release candidates before publication.
- **Publish the methodology and the noise parameters**, so findings can be challenged on the merits.

State this limitation in the published methodology. Do not repeat v1's "cost of spoofing" claim.

### 6.5 Why not just pseudonymise? (the objection everyone raises)

Every reviewer, contributor, and future contributor-agent will propose the same simpler design:
*give each user a random ID, publish on a time delay, done — it's anonymous.* It is not, and this
is settled empirically rather than as a matter of opinion. Do not re-litigate it; cite this section.

**This is precisely what Netflix did in 2006** — random subscriber IDs, plus stated perturbation of
the data including dates. Narayanan and Shmatikov broke it (*Robust De-anonymization of Large Sparse
Datasets*, IEEE S&P 2008), using public IMDb ratings as background knowledge. Measured results, from
their Figure 4:

- **8 ratings, only 6 needing to be correct, dates known to ±14 days → probability of
  de-anonymization ≈ 1.0.**
- **2 ratings with dates to ±3 days → ~68%.**

On the perturbation defence specifically (p. 9): Netflix's *"claim that the data were perturbed does
not appear to be borne out… the level of noise is far too small to affect our de-anonymization
algorithm, which has been specifically designed to withstand this kind of imprecision."* And flatly:
*"the Netflix Prize dataset clearly has not been k-anonymized for any value of k > 1."* Their
abstract states the techniques are *"robust to perturbation in the data and tolerate some mistakes
in the adversary's background knowledge"* — timestamp fuzzing is the attack they designed against.

The same pattern had already appeared in the AOL search-log release of 2006, where user 4417749 was
identified from queries alone despite the random ID.

Point by point:

- **"It's only a number."** That is the definition of *pseudonymous*, not anonymous, and the law
  agrees. GDPR Recital 26: data that *"could be attributed to a natural person by the use of
  additional information should be considered to be information on an identifiable natural person."*
  Pseudonymised data remains personal data, fully in scope, with every obligation intact.
- **"It's on a time delay."** A delay defeats real-time correlation. It does nothing against
  content-based re-identification, which works from *what* was watched, not *when*. Netflix fuzzed
  dates; it did not save them.
- **The ID is the vulnerability, not the protection.** A stable pseudonym is what staples one
  person's observations into a *trail*, and the trail is what is unique. Removing it and submitting
  each observation independently (§6.2) leaves nothing to link. Pseudonymisation preserves exactly
  the structure that makes the data identifying and then names that structure "anonymised."

**Additional US exposure flagged, not opined on:** the same paper raises the **Video Privacy
Protection Act of 1988**, which restricts disclosure of identifiable records of "prerecorded video
cassette tapes or similar audio visual material." A published dataset linking people to videos
watched sits in that neighbourhood, and its application to web platforms has been actively
litigated. Route to counsel before any release that could be re-identified; it is a further reason a
re-identifiable corpus is worse than an awkward one.

---

## 7. Preservation plane — "archive what we still can"

### 7.1 What is archivable

Not the video. The *record* of the video: what it was, when it existed, what it was recommended
alongside, and when it vanished. That record is unavailable anywhere else once YouTube removes a
video, and it is the actual research contribution.

```ts
interface VideoRecord {
  video_id: string;
  first_seen_at: string;         // ISO8601 date
  last_seen_at: string;
  title_history: Array<{ value: string; first_seen: string; last_seen: string }>;
  channel_id: string;
  channel_name_history: Array<{ value: string; first_seen: string; last_seen: string }>;
  published_at: string | null;
  duration_s: number | null;
  thumbnail_phash: string;       // perceptual hash — NOT the image
  description_sha256: string;    // integrity anchor
  description_excerpt: string;   // <= 200 chars
  observed_view_counts: Array<{ at: string; count: number }>;
  availability: Array<{ checked_at: string; status: Availability }>;
  removal_first_observed_at: string | null;
}

type Availability = "AVAILABLE" | "PRIVATE" | "REMOVED" | "GEO_BLOCKED" | "AGE_GATED" | "UNKNOWN";
```

Copyright posture: titles are generally too short to carry copyright; descriptions are thin but
non-zero, so we publish a hash plus a short excerpt and gate full text behind researcher access;
thumbnails are unambiguously copyrighted, so we store a **perceptual hash** and never redistribute
the image. Transformative research use, factual metadata, no market substitution — a defensible
position, and materially different from redistributing the work itself.

### 7.1a Identity fingerprint — finding removed videos elsewhere

The reason to keep a record is not sentiment; it is **relocation**. When YouTube removes a video, we
want to identify the same content re-hosted on Rumble/Odysee/archive.org. That is near-duplicate
detection, and it does **not** require the video file.

**Tier 1 — composite key (build this).** `duration_s` (exact seconds) + `thumbnail_phash`
(Hamming ≤ 8) + fuzzy `title` + `channel_name`. Strong in practice because re-uploads are typically
wholesale: the uploader takes the video *and its thumbnail* and re-posts both. Duration alone is a
brutal discriminator; combined with a thumbnail hash it approaches a unique key.

**Tier 2 — on-screen frame hashing (only if Tier 1 under-performs).** A content script cannot read
video pixels: `drawImage` from a cross-origin `<video>` taints the canvas and `getImageData` throws
`SecurityError`. But `chrome.tabs.captureVisibleTab` is a browser-level screenshot API and is not
subject to canvas tainting. Sample N frames from video the user is **already watching**, hash them,
**discard the pixels immediately**. Store a 64-bit hash, never an image.

This is legally distinct from downloading and must stay that way: the browser already decoded those
frames in order to display them, so no protection measure is touched, and reading what is on screen
is the same act the "like a screen reader" posture already depends on (§3.3). Never persist frames;
never reconstruct a sequence that approximates the work.

**Robustness caveat.** pHash degrades sharply under cropping, rotation, and added letterboxing —
all common in re-uploads. If matching becomes a core feature, store a CNN embedding vector
alongside the hash; embeddings consistently outperform perceptual hashing on transformed content.

**Search index ≠ identity key.** For searching *within* the corpus, the index is **text**: title,
description, and especially **caption/ASR transcripts**, which YouTube exposes as caption tracks.
This is how large-scale video search actually works; keyframes are a dedup/matching technique, not
a retrieval technique. Two different jobs:

| Job | Mechanism |
|---|---|
| "Find me videos about X" | transcript + title + description full-text index |
| "Is this Rumble video the removed YouTube video?" | duration + thumbnail pHash + embedding |

**Thumbnails have direct precedent.** *Perfect 10 v. Amazon* (9th Cir. 2007) held a search engine's
use of thumbnail images "highly transformative" and fair use, because it converts an image into a
pointer to information. That is exactly this use. Storing and displaying thumbnails for a search
index sits on considerably firmer ground than the rest of the corpus — stronger than the
conservative `phash`-only posture in §7.1, which can be relaxed if desired.

### 7.2 Tombstoning

The tombstone is the product. Two sources:

1. **Organic** — a contributor's browser observes the video is gone. Zero ToS risk. Preferred.
2. **Central low-rate probing** — a project service checks availability for videos already in the
   corpus.

Probing is automated access and is therefore **the highest-ToS-risk component in the system**.
Accordingly: isolate it in its own service, rate-limit it hard, run it unauthenticated, use the
public oEmbed endpoint if it proves suitable (**verify this before building on it** — confirm it
returns a distinguishable error for removed vs. private and confirm which terms govern it), and
build a kill switch. The corpus must remain useful with probing entirely off.

### 7.3 Is IPFS the right choice?

**Partly. Use it for what it is actually good at, and do not rely on it for durability.**

IPFS gives you *content addressing* — a CID is a verifiable, tamper-evident name for an exact
byte-sequence, which is precisely what an audit corpus needs, and it lets anyone mirror a release
without asking permission. What IPFS does **not** give you is persistence: unpinned content is
garbage-collected, and unpopular content with few replicas simply disappears. "Publish to IPFS" is
not an archival strategy; it is a naming strategy.

So invert v1: **stop treating IPFS as the database, and use it as the release-naming layer.**

Publish immutable, signed **release bundles**:

```
release/2026-08-01-01/
  video_records.parquet
  edge_aggregates.parquet
  availability_events.parquet
  manifest.json     # {files:[{path,sha256,bytes}], row_counts, dp_params, produced_at, prev_release_hash}
  manifest.sig      # ed25519 over manifest.json
```

#### Distribution: zero-cost channels only

**Budget constraint: this project spends nothing.** An earlier draft of this section reached for
paid infrastructure (R2/S3 mirrors, paid IPFS pinning, Arweave). That was a mistake — it replaced
v1's free P2P model with a monthly bill, and it isn't necessary. The requirements are *durability*,
*verifiability*, and *citability*, and the academic-publishing infrastructure provides all three for
free and with better research credibility than crypto-native storage.

| Channel | Cost | Role |
|---|---|---|
| **Zenodo** | Free | Primary archive. CERN-operated, built for research datasets, ~50 GB/dataset, and it mints a **permanent DOI** — the corpus becomes formally citable, which directly serves the DSA Art. 40 story (§3.3). |
| **GitHub Releases** | Free | Fast primary retrieval, CDN-backed, versioned. Release bundles as assets. |
| **IPFS** | Free | Verifiable identity. The CID *is* the citation hash. Pinned opportunistically by contributors and volunteers — **do not depend on the swarm for durability**, Zenodo covers that. |
| **BitTorrent / Academic Torrents** | Free | Bulk distribution of large historical dumps; Academic Torrents hosts research datasets at no cost. |
| **Internet Archive** | Free | Additional mirror, mission-aligned. |

Nothing above requires a payment method, a company, or a recurring commitment.

**The honest trade against Arweave.** Zenodo, GitHub, and archive.org are all takedown-able;
Arweave is not. Three reasons that's acceptable: the corpus is metadata rather than infringing
content, so takedown risk is low; multi-channel publication means no single takedown removes it; and
contributors can mirror independently. If permanence later becomes worth buying, Arweave at
metadata scale is roughly a one-time \$35/TB — a decision for when there is money, **not now**.

Plus an **append-only transparency log**: each `manifest.json` carries `prev_release_hash`, and the
chain is signed. Nobody — including us — can silently revise history. This is what makes the corpus
citable, and it is a stronger censorship-resistance property than a live P2P database would have
given you.

**Drop OrbitDB entirely.** It was solving a problem (live global writes) that we no longer have.

#### Running-cost ledger

| Phase | Recurring cost | Why |
|---|---|---|
| P0–P2 | **\$0** | No network calls at all. Everything is local. |
| P3 | **\$0** | Optional local daemon; still no network. |
| P3.5 (L3) | **\$0** | Contributors publish signed bundles themselves; releases go to the free channels above. See open decision 8 — the collection mechanism still needs specifying. |
| P4 (L2/STAR) | Small | One modest VPS for the aggregation server. The OPRF helper is the partner's cost, not ours. |
| P5 | **\$0** | Publication uses the free channels above. |

No phase before P4 requires spending anything. If P4's hosting is ever a barrier, NLnet NGI Zero
and OTF (§11) fund exactly this class of infrastructure.

---

## 7.4 The swarm — how graph data actually moves

**Status: built and tested against loopback, 2026-08-05. Not yet exercised
between two machines on the real DHT.**

§7.3 chose zero-cost channels for the *published aggregate* — a periodic
artifact people download. That is still right, and it is a different problem from
the one solved here. Suggestions need a live graph that no user can hold: at full
scale the deduped graph is 2–35 TB, while one user touches tens of MB of it. So
the graph is served, not shipped.

### The shape

A Go daemon runs a libp2p host. Peer discovery rides the **public IPFS DHT**,
used strictly as a directory: a node announces "I can serve bucket X" as a
provider record and serves the bytes itself over its own stream protocol. No
content enters the DHT. This satisfies §5b of `DESIGN_BOOTSTRAP` ("a DHT does not
store anything") and keeps `PRIVACY.md`'s claim true — the daemon contacts no
Keel-operated server, because none exists.

NAT traversal is libp2p's: port mapping, circuit relay, hole punching, AutoNAT. A
home machine is reachable without the user configuring anything.

### Two datasets, two protocols

`DESIGN_BOOTSTRAP` §1 splits the corpus into the graph and the catalogue. That
split is now physical:

| | `/keel/block/2.0.0` | `/keel/catalogue/1.0.0` |
|---|---|---|
| Carries | Stringless edges, keyed by context video | Titles, channel, duration, views, upload date |
| Mutability | Churns forever | Written once per video |
| Sync | On-demand, TTL-refreshed, whole-block replacement | Derived from fetched graph buckets, monotonic |
| Storage | LRU, evictable | Durable, never evicted |

**Blocks are stringless.** Measured: strings were 45 KB of a 63 KB pack, because
the same title ships again in every block pointing at that video. Real packs from
a live corpus fell from 1,311 KB to 582 KB on removing them.

**No deltas for the graph.** A stripped block is about a kilobyte, so refetching
beats versioning, ordering, idempotence, and the double-counting hazard of a
repeated additive delta. Whole-block replacement keyed on `(source, from_id)` is
already idempotent. Deltas may still make sense for a large *baseline snapshot*,
which is a different object — do not conflate the two.

### Prefix bucketing — the query leak and what actually fixes it

Fetch-on-demand leaks the query: asking a peer for video V's neighbourhood tells
that peer you are interested in V.

Three families of answer, and only two survive:

- **Obscuring the query** — decoy requests, batched region fetches. Both fail to
  intersection attacks: repeated sets from one address converge on the common
  element. This is the flaw that sank the v1 k-anonymity buffer, and enlarging
  the sets does not fix it. **Rejected.**
- **Breaking the link** — relay routing. Sound, and complementary.
- **Removing or drowning the query** — what is built.

A node never asks for one video. It asks for every neighbourhood whose **hashed**
key falls in a prefix bucket and takes all of them. This is not decoy traffic,
and the difference is why it holds: there is no real-versus-fake structure for
repeated observation to separate, because the node genuinely takes the whole
bucket. Every member is equally consistent with what the user did, and repeating
the request returns the same complete bucket.

Prefixes are over a hash, not the video id: YouTube ids are not uniformly
distributed, so raw-id buckets would vary wildly in population and some would
hold a single video — k-anonymity with k=1. Bucket occupancy is tested across
4,096 buckets.

**The cover traffic is the contribution.** Blocks fetched to hide the target are
exactly the blocks that make a node a useful mirror. Level 2's privacy mechanism
and its donation are the same act, and the disk budget is the anonymity
parameter.

### Ephemeral identity

Prefix bucketing alone is not enough, and this is the part most likely to be
dropped by a later change that does not understand it.

Each request is k-anonymous on its own. But a serving peer sees the requester's
libp2p peer id, and a *sequence* of bucket requests under one stable identity is
a trajectory. Trajectories re-identify people even when every point is coarse —
the same result that defeats "anonymised" mobility data. A relay hides the
address; it does not hide the peer id.

So every level below 4 generates a fresh network identity per daemon start.
Level 4 keeps a stable one, because being attributable is what that level is for.
The network key is separate from the signing key throughout, so observing the
network never links a node to what it published.

**Honest residual:** requests remain linkable *within* a session. Unlinking them
fully means a new identity per request, which costs connection reuse and relay
reservations. Not solved.

### The whole-bucket catalogue rule

A graph bucket holds many neighbourhoods whose targets span many catalogue
buckets. If a node fetched catalogue only for the targets of the block it
actually wanted, the catalogue request pattern would identify that block and undo
the anonymity the graph fetch just bought.

**Catalogue requests are derived from the entire graph bucket, never from the
block of interest.** The request set stays a function of contents the node
already disclosed by asking for that bucket. Any "resolve titles on demand for
what I need" implementation breaks this and must be rejected however much cheaper
it looks; the code takes a whole bucket reply rather than a video id so the wrong
thing is awkward to write.

Catalogue uses a **separate hash namespace** from graph buckets. Sharing one
would land a node's two request streams on correlated buckets and hand an
observer a join between the datasets.

This is affordable only because the catalogue converges: titles never change, so
a node that holds a row never asks again and steady-state catalogue traffic tends
to zero. `view_count` drifts and is treated as expendable — it is a ranking
signal, and staleness changes ordering slightly and nothing else.

### What each level does, as implemented

| Level | Asks the network | Serves | Publishes its own observations |
|---|---|---|---|
| 1 Personal | **Nothing** | Nothing | Nothing |
| 2 Mirror | Prefix buckets | Mirrored rows only | Nothing |
| 3 Cohort | Prefix buckets | Own edges too | Aggregate (STAR — not built) |
| 4 Transparency | Prefix buckets | Own edges too | Attributed (not built) |

**Level 1 asks for nothing at all**, and that is a stronger promise than "we do
not upload anything": a request discloses which video was asked about, so the
only node that leaks nothing is one that never asks. Level 1 runs on its own
recording. Consumption above Level 1 is not gated on contributing — a Level 2
node fetches and benefits while publishing nothing it observed. The privacy
promise is not a toll booth.

**Below Level 3, a node serves only what it holds for other people.** Serving
blocks built from its own `impressions` would publish a funnel; serving catalogue
derived from them discloses viewing at *video* granularity, since a requester
sees exactly which bucket members the node holds. Both are enforced by which
query runs, not by a caller remembering, and both are tested.

### Consequences to keep in mind

**Titles lag the graph.** A walk reaching new territory renders ids until the
catalogue arrives. Suggestions are still surfaced unlabelled rather than dropped
— discarding them would make fetched graph data useless — *except* when the user
has blocked channels, since an unlabelled video's channel is unknown and cannot
be checked. Fail closed on an explicit instruction, fail open where none was
given.

**The disk budget splits.** Catalogue is durable; graph blocks are LRU. A node
that evicts catalogue re-fetches it forever, because the graph keeps pointing
back at the same popular videos.

### Not built

- **Seed packs.** Designed and measured (a million-video seed projects to ~760 MB
  stripped and gzipped). Deferred: they matter when the network is large, and a
  seed built from one corpus would ship one person's algorithmic bubble to every
  new user, so it waits for several corpora.
- **STAR / Levels 3 and 4.** Gated on the cross-user dedup measurement.
- **The global search slice and long-tail posting lists.** Tier 1 search — over
  what a node holds — works now. Tiers 2 and 3 wait for a corpus worth indexing;
  tier 3 reuses the prefix mechanism with a third namespace.

### Measuring the gate before STAR

`DESIGN_BOOTSTRAP` §5d names cross-user dedup as the question to resolve before
committing to STAR, and notes one machine cannot answer it. `keel-host sketch`
builds a HyperLogLog over a node's edge keys; merging two gives the union, and
against the sum that is the overlap. Sketches are a few KB and cannot be
enumerated or tested for membership, so nodes settle the question without
publishing an edge. **Exchange happens node-to-node over the transport; the
subcommands are diagnostics and no user moves a file.**

## 7.5 Livestreams — an ephemeral index *(designed, not built)*

Livestreams are the case the block/catalogue split handles badly. A stream is
interesting for hours and worthless afterwards, so persisting it into the
catalogue — which is durable and never evicted — accumulates dead rows forever.
They need a third dataset with the opposite lifetime policy: **memory, not disk;
TTL, not permanence.**

Measured against a live corpus 2026-08-05: **3.78% of distinct videos carried a
LIVE badge** (67 of 1,774). Live content is a real share of what gets
recommended, not a curiosity.

### Shape

Same prefix bucketing as everything else, a third hash namespace. A node that
observes an active stream holds a lightweight record — video id, title, first
seen — bucketed by prefix. Records carry a TTL and are refreshed while still
being observed; when nothing refreshes one it expires and the index stays lean.

Search works exactly as catalogue search does: **fetch the whole bucket, filter
locally.** The daemon never sends a keyword anywhere. It pulls every active
record in a prefix bucket, unpacks it in memory, and runs the text match, sort
and filter on the user's own machine. A serving node cannot tell which stream the
user was after, because the user took all of them.

### Sizing

The concurrent global set is what matters, not the share of the catalogue. At
~100 bytes a record and 4,096 buckets:

| Concurrent streams worldwide | Per bucket |
|---|---|
| 100,000 | ~2.5 KB |
| 1,000,000 | ~24 KB |
| 10,000,000 | ~240 KB |

This is comfortable across the whole plausible range, which is the point: the
design does not depend on getting the estimate right.

### Correction — the DHT cannot hold these records

The obvious formulation, "publish the record into the DHT to the nodes
responsible for that bucket", does not work on the network we use. The public
IPFS DHT accepts provider records and a small set of validated namespaces; it
will not store arbitrary application values for us. Storing them would mean
running a separate DHT with our own protocol prefix, which forfeits the free,
already-populated network that §7.4 depends on.

**Keep the DHT as a directory, exactly as blocks do.** A node holding live
records for a bucket announces itself as a *provider* of that bucket; requesters
find providers and fetch the records over a Keel protocol stream.

This also removes custodial assignment, which is a gain rather than a
compromise: no node is *responsible* for a bucket, so there is nobody to coerce,
nobody to DoS off the network, and no placement metadata. A requester assembles a
bucket by merging replies from several providers, which is more robust than
trusting one custodian and costs a few extra streams.

### Three honest problems

**1. Publishing discloses the publisher's viewing.** A record says "someone saw
this stream". For a popular stream that is meaningless; for a rare one the
publisher set approximates the viewer set, and ephemeral identity plus a relay
reduce but do not remove it. Publishing must therefore be **opt-in at Level 2 and
above, never at Level 1**, and described as what it is. This is weaker than the
fetch-side guarantee, which is unconditional — the asymmetry should be stated
rather than smoothed over.

**2. Records are unverifiable claims.** Nothing signs YouTube state (§6.4), so a
node can announce a stream that is not live, or attach a misleading title. The
cheap mitigation is to display only records corroborated by *k* distinct
publishers, which raises the cost of poisoning without pretending to solve it —
sybil resistance is an open problem project-wide, not one this feature can fix.

**3. Liveness means "a Keel user is watching", not "the stream is live".** A
record survives only while someone with Keel keeps observing it. A stream nobody
here is watching expires from the index while still broadcasting. That is a
different quantity from YouTube's own liveness and the interface must not claim
otherwise — though it may be the more interesting one, since it measures
attention rather than availability.

### Why this is worth building

It yields a global, real-time livestream search with no central index, no server,
and the same query privacy as the rest of the system. Nothing else in the design
produces a live view of the network, and it costs a third namespace on machinery
that already exists.

## 8. Native daemon (Go) — required

The daemon is the product; the extension is its browser-side sensor and display (§2.1). It ships
first and it installs the extension (§2.2).

Responsibilities — **everything that touches observation data**:
- The SQLite corpus. This is the only place impressions are ever persisted.
- Retention: keep indefinitely by default; enforce a user-chosen limit only if one is set (§4.3).
- The Local Interest Vector for ranking. **Never leaves the machine** — this v1 rule was correct and
  is retained verbatim.
- Channel scrubbing and feed sanitization decisions (the extension applies, it does not decide).
- Preservation records, fingerprints, tombstones (§7).
- STAR client cryptography; later, Prio behind the same seam (§6.2).
- Batching and scheduling of contributions.

Not responsibilities: downloading media, running an IPFS node, touching the YouTube API.

**The extension may hold impressions in memory only**, long enough to batch them across the bridge.
If the daemon is unreachable, the buffer is dropped — it is never spilled to browser storage. Losing
observations while the daemon is down is the correct trade; persisting history in Google's runtime
is not.

### 8.1 Keel Bridge protocol v2

Native messaging constraints, from Chrome's documentation:
- **Host → browser: 1 MB max per message.**
- **Browser → host: 64 MiB max.**
- Framing: 32-bit length prefix in **native byte order**, then UTF-8 JSON.
- `allowed_origins` does not support wildcards.

Envelope:

```ts
interface Envelope {
  v: 2;
  id: string;               // correlation id; responses echo it
  type: string;
  payload: unknown;
  chunk?: { index: number; total: number };  // host→ext messages near 1 MB
}
```

Extension → daemon: `HELLO` (version negotiation), `IMPRESSIONS` (batched, normalized — never raw
`ytInitialData`), `USER_ACTION`, `QUERY`, `CONSENT_SET`.
Daemon → extension: `HELLO_ACK`, `FEED_UPDATE`, `FUNNEL_INFO`, `SYSTEM_STATUS`, `ERROR`.

Rules: validate every message against its schema on both sides and drop non-conforming packets
(v1 asserted this but never implemented it); reconnect in `onDisconnect`; the daemon must treat all
extension input as untrusted.

---

## 9. Build phases

Each phase ships something usable. Phases 0–3 need no server, no partner, and no crypto.

| Phase | Deliverable | Blocked on |
|---|---|---|
| **P0** | **End-to-end vertical slice:** extension observes `WATCH_NEXT` → Keel Bridge → Go daemon → SQLite → count back to SidePanel. One surface only. | — |
| **P1** | Remaining surfaces (`HOME`, `SEARCH`), export/wipe, user-set retention limit (optional, default off), graceful no-daemon state. | — |
| **P2** | Utility plane: boolean search, chronological sort, channel hard-block, sanitization, funnel inspector. **Ship to CWS here**, plus installer. | SignPath application (§2.3) |
| **P3** | Local preservation corpus, fingerprints, organic tombstoning. | — |
| **P4** | **L3 — Transparency Contributor.** Signed public observation bundles → IPFS. | Open decision 8 |
| **P5** | **L2 — Cohort Aggregator.** STAR + backend seam + re-consent gate. | OPRF helper host |
| **P6** | Release pipeline, signing, transparency log, multi-channel publication. | — (free channels, §7.3) |
| **P7** *(deferred)* | Prio/DAP numeric aggregates behind the existing seam. | Second aggregator |

**P0 is a vertical slice on purpose.** The riskiest integration in this system is the native
messaging bridge, not the DOM parsing. Building the extension fully and bolting the daemon on later
would defer that risk to the worst possible moment. One surface, end to end, exercises the bridge,
the framing, the reconnect path, and the daemon lifecycle while the surface area is still small.

**P4 is the first contribution mode and needs nobody.** L3 is public attributed publication — no
aggregator, no OPRF helper, no threshold crypto, because there is no privacy claim to keep. It
publishes straight to IPFS, which is what content addressing is actually good at. Cost: a
self-selected, non-representative sample, which must be disclosed in every finding, and contributor
exposure to platform retaliation (§6.1). Ship it before P4; it produces a real public corpus while
the L2 dependency is still being negotiated.

**P4 is the first mode that needs an outside party**, and only one: the OPRF helper. It must not be
operated by whoever runs the aggregation server (§6.0).

**P6 is deferred, not excluded.** The backend seam, capability document, and re-consent gate all
ship in P4 (§6.2) so that adding Prio later is configuration plus a consent flow rather than a
rewrite. Do not ship an unused Prio implementation in the meantime.

The rule that must not bend: if one organization ends up controlling both Prio aggregators, the
privacy guarantee is void and **we must not claim it**. Ship without Prio rather than overclaim.

### 9.1 Aggregator selection — conflict of interest

**Do not accept a Google competitor as an aggregator.** Not on threat-model grounds and not on
credibility grounds:

- DAP's guarantee is non-collusion. A party with a *stake in the outcome* is the worst possible
  aggregator — it has incentive both to peek and to skew.
- The instrument's only real asset is credibility. A competitor-run aggregator lets Google dismiss
  every finding as a competitor-funded attack instrument without contesting the analysis, and hands
  them a coordinated-harm narrative in discovery. Regulators discount competitor-sponsored evidence.

Acceptable roles for a competitor: **arms-length funding** with published terms and no editorial
control, or **mirroring releases**. Not aggregation.

Preferred aggregator candidates, in order: ISRG/Divvi Up; a university lab (also serves the DSA
Art. 40 path, §3.3); a digital-rights NGO. Note one conflict to disclose rather than hide: Mozilla
co-develops DAP and ran RegretsReporter, but derives most of its revenue from Google's search
placement. That does not disqualify them — they published RegretsReporter's YouTube criticism
anyway — but it must be stated publicly if they participate.

---

## 10. Threat model

| Threat | Mitigation | Residual |
|---|---|---|
| Google removes the extension | Single-purpose compliance, no downloader, minimal permissions, prominent disclosure | Non-zero. Keep a Firefox build ready. |
| YouTube bans contributor accounts (the NYU scenario) | Never require login, never touch account state, utility plane works without contributing | Low but real |
| Breach-of-contract claim (the hiQ scenario) | No API ToS accepted, no crawling, no circumvention, user-agent framing | Real; needs counsel before P4 |
| Sybil poisoning of aggregates | STAR K-threshold, anonymous-credential rate limits, outlier detection, published methodology | Acknowledged, not eliminated (§6.4) |
| Aggregation server brute-forces its own users' rare reports | Independent OPRF helper (§6.0) — must not be us | Void if we operate both |
| OPRF helper is pressured into quitting by a legal threat letter | Prefer an institution that routinely receives them; make the role swappable in days; allow multiple helpers | Real — a letter needs no valid claim to scare off a volunteer |
| Re-identification from published releases | Coarse cohorts, day buckets, independent edge submission, no stable pseudonym, no trails (§6.5) | Composition risk across many releases — track a global privacy budget |
| A future contributor "simplifies" the design to pseudonyms + time delay | §6.5 exists to settle this with citations rather than argument | Recurring; expect it in every review |
| We become the surveillance system we're auditing | Level 0 default, LIV never leaves device, raw queries never stored, one-click export and wipe | Requires ongoing discipline |

---

## 11. Open decisions

These need answers before the phases they block:

1. **OPRF helper host** — blocks P4 only. Nothing before P4 needs it, and P3.5 (L3) delivers a
   public corpus without it. The ask: run one small stateless service, hold a rotating key, keep it
   up, don't log. Candidates: ISRG, EFF, AlgorithmWatch, Panoptykon, a university lab. Approach them
   *after* there are users — nobody partners with a design document. Grant routes for the
   infrastructure cost: NLnet NGI Zero (small, accessible, low-bureaucracy), Open Technology Fund
   (already funds Divvi Up). Competitors of Google are excluded on conflict grounds (§9.1).
   Fallback if nobody agrees: operate it under a separate legal entity with an independently-held
   key, and **state publicly that the guarantee is weaker** rather than claim one we don't have.
2. **Academic partner** for DSA Art. 40 vetting — shapes the legal posture, not the code.
3. ~~**Jurisdiction**~~ — **decided: US-based, EU-leading.** Operator is in the USA; the tool targets
   mass adoption including the EU. Competition claims lead with EU law (§3.3). GDPR obligations attach
   to us once EU users contribute: if any contribution is deemed personal data we need a lawful basis
   and a named controller. Coarse cohorts + DP + STAR is the argument that it is *not* personal data;
   that argument needs counsel review before P4, not before P0.
4. **Description full text** — publish openly, or gate behind researcher access? Recommend gating.
5. **oEmbed suitability** for availability probing (§7.2) — verify before building on it.
6. ~~**Firefox parity**~~ — **decided: build for it from P0** via a thin `browser.*` compatibility
   shim. Chrome's ephemeral service worker and Firefox's persistent background scripts diverge enough
   that retrofitting means rewriting the entire background layer. Rationale: Google controls both the
   Chrome Web Store and the adverse position; Firefox is the only distribution channel that survives
   the removal scenario the legal strategy is built around. Cheap at P0, expensive at P4.
7. **Tier-2 frame hashing** (§7.1a) — build only if Tier-1 composite-key match rates prove
   insufficient. Measure before building.
8. ~~**Open-source licence**~~ — **decided: Apache-2.0.** Chosen for the explicit patent grant
   (§3 of the licence), which matters when the adverse party holds a large patent portfolio, and for
   widest adoption. `LICENSE` and `NOTICE` are in the repo. This also satisfies SignPath eligibility
   for free Windows code signing (§2.3) — **apply before P2**, approval takes weeks.
9. **P3.5 collection mechanism** — L3 contributors publish signed bundles, but *how do the bundles
   reach a release?* Options, all zero-cost: contributor uploads to their own IPFS/GitHub and
   submits a CID/URL via a form (highest friction, truly \$0); a GitHub repo where contributions
   arrive as pull requests (free, auditable, rate-limited by GitHub, and the review trail is a
   feature); or a small collector service (lowest friction, first thing that costs money). Decide
   before P3.5. **Recommendation: pull requests** — free, public by construction which matches L3's
   semantics exactly, and spam-resistant without building anything.

---

## Appendix A — If you insist on archiving video

Recorded so the decision is explicit rather than forgotten.

To archive bitstreams you would need to accept, simultaneously:

- **Removal from the Chrome Web Store.** Not a risk — an expected outcome. Distribution becomes
  sideloading, enterprise policy, and Firefox. Realistically this costs you >95% of adoption.
- **DMCA §1201 exposure** with no fair-use defense, on the *Yout* fact pattern, which the district
  court decided against Yout.
- **Direct-infringement liability transferred to your users.** IPFS pinning makes each volunteer a
  distributor. This is the strongest objection: it is not your risk to take.
- **Loss of the entire legal narrative.** "We only read the DOM, like a screen reader" and "we
  download and redistribute video" cannot both be true. v1 asserted both.

If the goal behind archiving is *preventing loss of the record*, §7 achieves that for metadata at
near-zero legal risk. If the goal is *preserving the works themselves*, that is a different project
with a different risk appetite, and it should not share a binary, a brand, or a user base with this one.

**Unbundling is the correct outcome, not a compromise.** Once this project publishes an open corpus
of video records plus identity fingerprints (§7.1a), anyone can build an archiver against it without
our involvement, our binary, or our legal exposure. The corpus is the hard part and the part that
disappears if nobody captures it in time; the downloading is commodity work that an independent
project can do under its own risk appetite. Publishing the fingerprints *enables* archiving while
keeping this project clean enough to be a plaintiff (§3.3).

---

## Sources

- [Chrome Web Store policy updates 2026](https://developer.chrome.com/blog/cws-policy-updates-2026) · [Program policies](https://developer.chrome.com/docs/webstore/program-policies/policies) · [Quality guidelines FAQ](https://developer.chrome.com/docs/webstore/program-policies/quality-guidelines-faq)
- [Native messaging](https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging) · [Service worker lifecycle](https://developer.chrome.com/docs/extensions/develop/concepts/service-workers/lifecycle)
- [YouTube API Services Developer Policies](https://developers.google.com/youtube/terms/developer-policies)
- [Yout v. RIAA (TorrentFreak coverage)](https://torrentfreak.com/suno-udio-wade-into-youtube-ripper-circumvention-lawsuit-appeal-251008/) · [Stream-ripper circumvention ruling](https://completemusicupdate.com/stream-ripper-yout-circumvents-youtubes-technical-protection-measures-court-concludes/)
- [hiQ v. LinkedIn — CFAA](https://www.eff.org/deeplinks/2022/04/scraping-public-websites-still-isnt-crime-court-appeals-declares) · [breach of contract judgment](https://www.privacyworld.blog/2022/12/linkedins-data-scraping-battle-with-hiq-labs-ends-with-proposed-judgment/)
- [Mozilla RegretsReporter](https://www.mozillafoundation.org/en/youtube/regretsreporter/) · [findings](https://www.mozillafoundation.org/en/research/library/user-controls/report/meager-and-inadequate-a-quantitative-analysis-of-youtubes-user-controls/)
- [NYU Ad Observer accounts disabled](https://www.npr.org/2021/08/04/1024791053/facebook-boots-nyu-disinformation-researchers-off-its-platform-and-critics-cry-f)
- [DSA Article 40 delegated act](https://www.techpolicy.press/unpacking-the-eus-digital-services-act-delegated-act-on-data-access-/) · [Hogan Lovells analysis](https://www.hoganlovells.com/en/publications/who-gets-to-see-inside-the-eus-new-rules-on-data-access-under-article-40-of-the-dsa)
- [STAR (Brave, CCS 2022)](https://brave.com/research/files/star-ccs-2022.pdf) · [STAR IETF draft — trust model & OPRF](https://www.ietf.org/archive/id/draft-dss-star-00.html) · [Divvi Up / DAP](https://divviup.org/blog/dap-update/) · [Divvi Up: how it works](https://docs.divviup.org/how-it-works/) · [LWN on Divvi Up](https://lwn.net/Articles/983843/)
- [Curse of dimensionality in de-identification (FPF)](https://fpf.org/blog/the-curse-of-dimensionality-de-identification-challenges-in-the-sharing-of-highly-dimensional-datasets/)
- **Narayanan & Shmatikov, [Robust De-anonymization of Large Sparse Datasets](https://www.cs.cornell.edu/~shmat/shmat_oak08netflix.pdf) (IEEE S&P 2008)** — the Netflix result; see Fig. 4 and p. 9 (§6.5)
- [GDPR Recital 26 — pseudonymised data is personal data](https://www.privacy-regulation.eu/en/recital-26-GDPR.htm) · [ICO on pseudonymisation](https://ico.org.uk/for-organisations/uk-gdpr-guidance-and-resources/data-sharing/anonymisation/pseudonymisation/)
- [Perfect 10 v. Amazon, 508 F.3d 1146 (9th Cir. 2007) — Copyright Office summary](https://www.copyright.gov/fair-use/summaries/perfect10-amazon-9thcir2007.pdf)
- [Canvas cross-origin tainting (MDN)](https://developer.mozilla.org/en-US/docs/Web/HTML/How_to/CORS_enabled_image)
- [videohash — perceptual video hashing](https://github.com/akamhy/videohash) · [pHash vs CNN embeddings on transformed media](https://www.mdpi.com/2079-9292/15/7/1493)
- [IPFS persistence/pinning](https://docs.ipfs.tech/concepts/faq/) · [OrbitDB](https://github.com/orbitdb/orbitdb)
