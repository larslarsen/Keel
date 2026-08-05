# Design note — sharing between people: REJECTED

**Status: rejected by Lars, 2026-08-04. Do not propose this again.**

> "I specifically told you no file sharing between friends — we can't have
> anyone trusting people."

Person-to-person bundle exchange requires every recipient to trust the sender.
A signature proves who produced the bytes; **nothing proves the observations
happened**. `DESIGN_v2` §6.4 already concedes that nothing signs `ytInitialData`,
so a signer can honestly sign fabricated edges. That makes each import a trust
decision, which does not scale past a handful of people and is not what the
design specifies.

## What the design says instead

Daemons do not exchange corpora. They:

1. Submit **aggregate measurements** to a STAR aggregator (§6.2) — the threshold
   hides anyone whose measurement fewer than K others also reported.
2. The project publishes **signed release bundles** over §7.3's channels —
   Zenodo, GitHub Releases, IPFS, Academic Torrents, Internet Archive.
3. Other daemons consume the published release.

Nobody trusts a person at any step. The threshold and the release signature do
the work.

## What survives from the rejected work

The layers below the transport were right and serve the path above:

- **`EdgeObservations`** — the `(from, to, surface, slot_bucket, day_bucket,
  cohort)` tuple is exactly what STAR submits (§6.2).
- **`CatalogueEntries`** — the catalogue/edges split is what makes publication
  defensible (`DESIGN_BOOTSTRAP` §1).
- **Content digest and ed25519 signature** — what §7.3 requires of a release
  manifest.
- **`peer_edges` / `peer_catalogue` and the merged graph** — how a *published
  release* is consumed once one exists.

## What should be removed or gated

The person-to-person surface built on top: `bundle import` from a file or URL,
the peers list, the "via shared bundle" label. It is a trust model the project
has rejected, and leaving it in the UI invites exactly the behaviour Lars ruled
out.

Everything below it stays.
