# WO-087 — STAR cohort funnel comparison visualizer

| | |
|---|---|
| **Addressee** | Product design + Sr Dev (Claude Opus) |
| **Status** | **Deferred — design against the real Level-3 STAR aggregate, not before** |
| **Date** | 2026-08-11 |
| **Source** | Lars contribution-level incentive discussion, 2026-08-11 |

## Product decision

Level 3 contributes recommendation-edge measurements through STAR and earns the
comparison product that protected cohort data makes possible: a visual funnel
showing how this user's recommendation paths compare with the cohort's paths.

This is the Level-3 incentive. Level 2 already contributes public broad graph
buckets and receives reciprocal distributed search; Level 4 is a separate,
deliberately attributed research/transparency choice with no gated reward.

## Dependency

Do not implement or freeze the interaction before the STAR measurement schema,
threshold, epoch, suppression behavior and returned aggregate shape exist. A
mock cohort would encourage UI assumptions that the privacy protocol may not be
able to answer.

When those inputs exist, write the implementation amendment around the actual
aggregate. It should specify at least:

- which funnel stages and branch comparisons STAR can answer;
- the cohort and time window the comparison represents;
- denominators, uncertainty and minimum report counts;
- how below-threshold or unavailable cells are suppressed; and
- how the user's local funnel is visually distinguished from protected cohort
  aggregates without implying that individual cohort trails are visible.

## Privacy and interpretation rules

- Render aggregates only. Never request, reconstruct or cache participant-level
  edges or ordered trails.
- Suppressed cells stay suppressed in the UI, exports, tooltips and error paths.
- Do not interpolate a hidden small cell from displayed totals.
- Explain that the cohort is participating Level-3 users, not “everyone.”
- Do not present correlation as proof that a platform caused a later action.
- Level 4 attribution must not be mixed into the Level-3 protected cohort path.

## Acceptance gate for the later implementation

- [ ] Every displayed value maps to a documented STAR aggregate and privacy
      threshold.
- [ ] A suppression test proves hidden cells cannot be recovered by subtraction
      from other displayed values.
- [ ] Empty, below-threshold and delayed epochs have honest distinct states.
- [ ] The visualization is unavailable below Level 3 and updates safely across
      a 3→2 downgrade.
- [ ] No raw cohort report or participant identifier enters extension storage.
- [ ] User testing shows non-technical readers can distinguish “my funnel” from
      “participating cohort” and understand the time window.

## Do not

Do not code this ticket merely because the visual concept is attractive. The
STAR output contract is the input to the design, not an implementation detail
to invent afterward.
