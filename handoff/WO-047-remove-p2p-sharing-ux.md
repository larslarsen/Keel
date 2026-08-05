# WO-047 — Remove the person-to-person sharing UX

| | |
|---|---|
| **Addressee** | Anyone |
| **Status** | **Done** |
| **Date** | 2026-08-04 |
| **Source** | `DESIGN_SHARING.md` — rejected by Lars, 2026-08-04 |

Person-to-person bundle exchange is rejected: importing a bundle means trusting
the sender, and a signature proves who wrote the bytes, never that the
observations happened (`DESIGN_v2` §6.4). Leaving the UI in invites exactly the
behaviour that was ruled out.

## Remove

- `bundle import` from a file path or URL — CLI and the daemon RPC.
- `keel-host bundle sync`, `peers`, `forget` — the peer-management surface.
- The full page's Share section: export/import controls, peer list, Forget
  buttons.
- The `via shared bundle` / `from a shared bundle` labels in suggestions and
  search.
- `readBundle`'s http(s) fetch path.

## Keep — all of it serves the published-release path

- `EdgeObservations` — the `(from, to, surface, slot_bucket, day_bucket, cohort)`
  tuple is exactly what STAR submits (§6.2).
- `CatalogueEntries` and the catalogue/edges split.
- The content digest and ed25519 signature — what §7.3 requires of a release
  manifest.
- `peer_edges` / `peer_catalogue` tables and the merged graph — this is how a
  **published release** is consumed once one exists. Only the person-to-person
  ingest goes; the merge machinery stays.
- `keel-host bundle export` and `summary` — producing an aggregate is still how
  a node contributes.

## Note

Do not delete the tables or the merge logic to "clean up". They are the
consumption path for §7.3 releases, and rebuilding them later would be pure
waste.

## Acceptance

- [x] No UI or CLI path imports another person's bundle.
- [x] `EdgeObservations`, catalogue, digest, signature, peer tables and merged
      graph all still present and tested.
- [x] Tests still pass.
