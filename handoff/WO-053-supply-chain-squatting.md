# WO-053 — Dependency-squatting / name-hijack exposure audit

| | |
|---|---|
| **Addressee** | Engineer (Sr Dev lane) |
| **Status** | **Open** |
| **Date** | 2026-08-06 |
| **Source** | Lars, 2026-08-06 — "worried about hallucination squatting attacks; this is meant to be used by other people" |

Lars's concern: because Keel is distributed to other people, a dependency-name
hijack (typo-squat / hallucinated module name) would be a serious problem.
This ticket records what the codebase actually depends on, where the real
exposure is, and the concrete mitigations to put in place. **No code change is
urgent today** — the surface is small and mostly already mitigated — but the
daemon path needs guardrails before more people build/run it.

## What we actually depend on (verified, not assumed)

### Extension — ships ZERO third-party dependencies.
- `package.json` declares exactly one `devDependency`: `linkedom` ^0.18.9, used
  only by `test/extract.test.js`. It is **not** imported anywhere under
  `extension/` (grep for external `import` in shipped code returns nothing).
- The extension is plain ES modules per `BUILD_P0.md` §2 (no framework, no
  bundler, no runtime deps). There is therefore **no dependency to squat** in
  the shipped browser extension. This part of the worry is already resolved by
  design.
- `package-lock.json` exists (so the one dev dep's tree is pinned), but it never
  reaches users.

### Daemon — Go, with a real dependency tree.
- `daemon/go.mod` (module `github.com/keel-app/keel/daemon`, go 1.25.7) declares
  **5 direct** modules:
  - `github.com/ipfs/go-cid` v0.6.2
  - `github.com/libp2p/go-libp2p` v0.49.0
  - `github.com/libp2p/go-libp2p-kad-dht` v0.42.1
  - `github.com/multiformats/go-multihash` v0.2.3
  - `modernc.org/sqlite` v1.34.5
- Plus **~110 indirect** modules (libp2p's transitive tree: pion, quic-go,
  prometheus, golang.org/x/*, etc.).
- `daemon/go.sum` **exists (343 lines)** — so module hashes ARE pinned.

## Where the actual squatting window is

With `go.sum` present, the default Go toolchain runs with
`GOFLAGS=-mod=readonly`-equivalent behaviour: any module whose hash is not in
`go.sum` fails the build. So a *known* dependency cannot be silently swapped.
The window is specifically **first-time fetch of a name that is not yet in
go.sum** — i.e. the attacker only wins if their package is fetched and hashed
*before* a human/CI notices. Concrete vectors:

1. **Typo / hallucinated `import` path.** A developer (or an LLM assistant
   generating code) writes `import "github.com/libp2p/go-libp2pp"` (extra `p`),
   `"github.com/libp2p/go-libp2p-kad-dhtx"` (typo), or any plausible-but-wrong
   module name. The first `go get` / `go build` of that name resolves it
   against the module proxy and pins whatever hash comes back. That is exactly
   the "hallucination squatting" Lars named.
2. **Malicious / compromised `GOPROXY`.** If `GOPROXY` points at a tampered
   mirror, hashes can be served. Default is `proxy.golang.org,direct`; this is
   fine as long as it is not overridden in build docs or CI.
3. **Hand-edited `require` line** adding a name that does not exist yet, then a
   build that auto-fetches it.

The extension has none of this exposure. The daemon is the only surface, and it
is mitigated by go.sum — the remaining risk is purely the *first fetch* of a
wrong name.

## Mitigations to implement (in priority order)

- [ ] **Pin `GOPROXY` explicitly** in `daemon/go.mod` via a `//go:linkname`-free
      approach — actually set `GOPROXY=https://proxy.golang.org,direct` in the
      build/CI docs and a `.github/workflows` (or equivalent) so no builder
      silently uses a mirror. Document it in `daemon/README.md`.
- [ ] **CI / pre-commit guard:** fail the build if `go.mod` references any module
      whose path is not on an allowlist of known roots
      (`github.com/libp2p/*`, `github.com/ipfs/*`, `github.com/multiformats/*`,
      `modernc.org/*`, `golang.org/x/*`, `google.golang.org/*`, `go.uber.org/*`).
      A simple `grep`/script over `go list -m all` output catches a hallucinated
      name the moment it is added, before it is fetched.
- [ ] **Keep `go.sum` committed and verified:** `go mod verify` in CI; treat a
      missing/changed hash as a hard failure. (Already mostly in place — make it
      explicit and required.)
- [ ] **Document the rule for contributors/LLMs:** "never add a `require` or
      `import` for a module name you have not seen in this repo's `go.mod`; if
      you need a new dep, name it exactly and flag it for review." This directly
      addresses the hallucination vector.
- [ ] **Reproducible fetch:** ensure `GOFLAGS=-mod=readonly` is set so an
      un-pinned name cannot be auto-added by `go build` (it errors instead of
      fetching). Confirmed Go default, but set explicitly so it cannot regress.

## Current status — scanned 2026-08-06 (informational, NOT yet a gate)

Toolchain present: Go go1.26.0 (installed; `GOTOOLCHAIN=auto` pulled it despite
the `go 1.25.7` directive in `go.mod` — note this mismatch for the engineer).
Scanners run: `govulncheck ./...` (Go official), SBOM via `go list -m all`
(267 modules incl. main; 5 direct + ~110 indirect).

### Finding
- **Baseline scan on go1.26.0: 14 vulnerabilities**, nearly all in the **Go
  standard library** (`crypto/tls`, `crypto/x509`, `net`, `net/http`,
  `net/textproto`, `net/url`, `os` at `go1.26`), plus one third-party transitive
  dep `github.com/dunglas/httpsfv@v1.1.0` reached but not called on a vulnerable
  path.
- **Root cause:** the build was on `go1.26.0`; the CVE cluster is patched across
  `go1.26.x` patch releases (highest "Fixed in" = `crypto/tls@go1.26.5`).
- **Re-scan on `GOTOOLCHAIN=go1.26.5`: 1 vulnerability remaining** —
  `GO-2024-3218` in `github.com/libp2p/go-libp2p-kad-dht@v0.42.1`, **Fixed in:
  N/A** (no published fix in that version), and govulncheck confirms "your code
  doesn't appear to call these vulnerabilities."

### Action taken / recommended
- **Bump the toolchain to go1.26.5** (set `go.mod` directive `go 1.26.5` and let
  `GOTOOLCHAIN=auto` fetch it). This is a one-line change and clears 13 of 14
  findings. Doing the scan *now* — before 1.0 — turned a would-be release-blocker
  into a 1-line fix. (This is informational; the actual edit is the engineer's
  per the ticket's gate scope — confirm before committing.)
- The lone residual (`GO-2024-3218`, kad-dht) has no fix yet; track it, do not
  block on it. It is not on a called path in current code.
- SBOM baseline written to `daemon/sbom-modules.txt` (267 lines). A proper
  CycloneDX artifact is a CI deliverable (see Related below), not hand-maintained.

### Why scanning early was right
The dependency tree is smaller now than at 1.0, and the dominant finding was a
toolchain-patch bump — cheapest to fix today. This validates "run scans now as a
report; gate at release" over "wait until finished."

## Related: SBOM + SCA (the other two legs of supply-chain hygiene)

Two standard acronyms this ticket sits alongside — recorded here so the
engineer has the canonical terms and they are not re-discovered later:

- **SBOM** = Software Bill of Materials. A machine-readable inventory of every
  dependency (name, version, hash). The verified inventory in the section above
  is the seed; the deliverable is a generated artifact in a standard format
  (CycloneDX or SPDX). For Go: `go list -m all` feeds a generator
  (`cyclonedx-gomod`, or `syft` which emits CycloneDX directly). The extension
  side produces an effectively empty SBOM (no runtime deps) — still emit one so
  the artifact is complete.
- **SCA** = Software Composition Analysis. The *security* scan that checks the
  SBOM's components against known vulnerability databases (CVEs). This is the
  "other S" — distinct from the name-hijack guard above, which stops a *bad
  name* entering; SCA stops a *known-vulnerable version* of a *legitimate* dep
  from shipping. Free/right tools:
  - `govulncheck` (Go's official vuln scanner, reads `go.mod`/`go.sum`) — first
    choice for the daemon.
  - `syft` (SBOM) + `grype` (SCA) or `trivy` (SBOM + SCA in one) — covers both
    in CI.
  - GitHub Dependabot / `govulncheck` alerting for continuous coverage.

These are scoped as follow-ons, not blocked by this ticket:

- [ ] **SBOM generation** in CI: emit a CycloneDX SBOM from `daemon/go.mod`
      (and a stub for the extension) on every build; archive as an artifact.
- [ ] **SCA scan** in CI: `govulncheck ./...` (daemon) and a `grype`/`trivy`
      pass over the SBOM; fail the build on any HIGH/CRITICAL with a fix
      available. Wire Dependabot or `govulncheck` alerts for ongoing coverage.
- [ ] Publish/attach the SBOM with releases so downstream users (the people
      this is "meant for") can verify what they run.

## Out of scope
- The npm extension path needs no change (no runtime deps), though an empty SBOM
  is still emitted for completeness.
- This ticket's *primary* deliverable is the name-hijack guard (above). SBOM/SCA
  are recorded here as the adjacent, must-do-next steps so they are not lost.

## Acceptance
- [ ] `GOPROXY` pinned and documented; `go mod verify` passes in CI.
- [ ] A build/CI step fails if any module path falls outside the known-root
      allowlist.
- [ ] `GOFLAGS=-mod=readonly` is set explicitly in build instructions.
- [ ] Contributing note added covering the "name a dep exactly, flag new deps"
      rule.
- [ ] SBOM (CycloneDX) generated in CI for the daemon (and a stub for the
      extension) and archived as a build artifact.
- [ ] SCA (`govulncheck` + `grype`/`trivy`) runs in CI; build fails on
      HIGH/CRITICAL with a fix available.
