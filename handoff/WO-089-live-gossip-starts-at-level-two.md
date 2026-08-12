# WO-089 — Live and outbound word statistics start at Level 2

| | |
|---|---|
| **Addressee** | Sr Dev (Claude Opus) |
| **Status** | **Implemented — two-machine wire inspection pending** |
| **Date** | 2026-08-12 |
| **Source** | Lars, during the WO-082 compliance review |

## Final decision

### Level 1 — Strictly Personal (default)

Keel records locally. It downloads:

- the starter recommendation dataset;
- shared recommendation data used for suggestions and graph exploration; and
- global word statistics.

It uses that downloaded data locally for suggestions, search and analysis.

Level 1 does not participate in Live. It also does not send or serve locally
derived word statistics. Live and outbound word statistics begin at Level 2.

Level 1 continues to send ordinary requests for the groups of shared data it
downloads. Those requests identify a large group, not one selected video.

### Level 2 — Broad Sharing

Level 2 has everything in Level 1, plus:

- the complete shared Live feed;
- publication of locally observed livestream sightings;
- relay and snapshot service for Live;
- contribution and service of aggregate word statistics;
- distributed search across other people's recommendation records; and
- broad graph, catalogue and search data containing both locally derived and
  cached peer claims.

Level 2 does not send raw observation rows or an ordered watch history.

### Levels 3 and 4

- Level 3 adds STAR-protected cohort and funnel comparisons.
- Level 4 adds deliberately public, attributed records.

## Consent

The first consent screen must explain, above its buttons, that Keel records
locally and downloads groups of shared recommendation data and global word
statistics. The daemon must acknowledge the current consent before starting the
Level-1 network. Decline starts neither recording nor networking.

Choosing Level 2 is the separate sharing decision. Its explanation must appear
before confirmation and name Live sightings, aggregate word statistics, broad
recommendation data and service to other users.

## Required implementation

1. Set Live off at Level 1 and on at Level 2+.
   - No Level-1 Live topic or subscription.
   - No Level-1 Live snapshot fetch or service.
   - No Level-1 Live relay.
   - No Level-1 seeding or publication from local observations.
2. Split global word statistics into download and upload/service capabilities.
   - Level 1 downloads statistics only.
   - Level 2+ downloads, contributes its local aggregate, and serves statistics.
3. Keep the existing Level-1 starter-data download, shared graph download and
   graph pre-walk unchanged.
4. Keep the existing Level-1 refusal of distributed peer search unchanged.
5. Keep Level 2's existing broad local-plus-cached graph sharing unchanged.
6. Persist a revisioned initial consent in the daemon. Do not start Level-1
   networking until the daemon has acknowledged the current consent.
7. Detect older daemons through capability negotiation and require an update.
8. At Level 1, keep the Live interface visible but disabled with a short message
   that Live starts at Level 2.
9. Update the privacy policy, README, consent screen, contribution descriptions,
   store listing and store data declarations in the same change as the runtime.

## Migration

Existing installations require the corrected initial consent once. Preserve
their local corpus. Do not treat an old browser preference or contribution
level as acceptance of the corrected disclosure.

## Tests

- Level 1 still downloads starter/shared graph data and global word statistics.
- Level 1 has no Live object, topic, snapshot, relay, local seed or publish path.
- Level 1 never contributes or serves a word-statistics pack.
- Level 2 enables the complete existing Live system and bidirectional word
  statistics.
- A 2→1 downgrade closes outbound work before replacing the node.
- Restart cannot restore Level-2 behavior at Level 1.
- No Level-1 network starts before current daemon-acknowledged consent.
- Two-machine inspection confirms the same boundary on the wire.

## Superseded decisions

WO-077 and WO-078 remain the history of the existing implementation, but their
Level-1 Live and outbound-word decisions are superseded by this order. WO-084's
Level-2 broad sharing and WO-085's distributed-search boundary remain unchanged.

## Do not

- Do not create a receive-only or relay-only Live mode at Level 1. Live is
  completely off there.
- Do not remove Level-1 starter/shared graph downloads or graph pre-walk.
- Do not remove Level-1 download of global word statistics.
- Do not weaken Level-2 broad local-plus-cached sharing.

## Implementation review — 2026-08-12

The daemon boundary, revisioned consent gate, extension/daemon negotiation and
primary disclosures are implemented. Automated Go, race, vet and extension
tests pass. Review found two stale full-page strings that implied Live worked
at Level 1; WO-090 corrected both and added DOM regression coverage.
Two-machine wire inspection remains operational QA.
