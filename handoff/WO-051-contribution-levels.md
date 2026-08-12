# WO-051 — Contribution level control

> **Current-contract note (2026-08-11):** WO-078 supersedes the "Nothing." row
> below. Level 1 remains a full block consumer — it fetches whole graph/
> catalogue/search buckets and pre-walks the graph — and originates live
> notices plus WO-068's whole word-level HLL/CMS aggregate. It serves no
> blocks and joins no three-gram yield/token-sketch topic. See
> `ARCHITECTURE_CURRENT.md` §3.

| | |
|---|---|
| **Addressee** | Anyone |
| **Status** | **Levels 1–2 implemented and enforced** (`daemon/store/contribution.go`, `MaxImplementedLevel = LevelMirror`); Level 3 (Cohort) is defined but not yet enforced end-to-end. |
| **Date** | 2026-08-05 |
| **Source** | `masterplan.md` contribution levels; `DESIGN_v2` §6 |

The last extension change before the frozen-extension goal is reachable: a
control for how much this node contributes.

## The three levels

| Level | Name | What leaves the device |
|---|---|---|
| **1** | Strictly Personal | **Nothing.** Full local product — search, suggestions, blocks, analysis. |
| **2** | Catalogue Only | Video metadata — title, channel, length, view count. **No edges.** |
| **3** | Cohort Aggregator | Catalogue plus STAR-aggregated edge counts (`DESIGN_v2` §6.2). Threshold-protected. |
| **4** | Transparency Contributor | Full funnel state, publicly attributed and **not retractable once mirrored**. |

### Level 2 added 2026-08-05 — deviates from `masterplan.md`

`masterplan.md` defines three levels. This adds a fourth, and renumbers, because
`DESIGN_BOOTSTRAP` §1 splits the corpus into two things with very different
sensitivity:

- **Catalogue** — facts about public videos. Bounded (~0.1–0.5 TB/year), and the
  part that makes search work for everyone.
- **Edges** — which video was recommended after which. Unbounded
  (~80 TB/year per million users) and an observation of a person.

Sharing one without the other is coherent and useful, so it should be offerable.

**State the residual honestly:** a catalogue contribution from one person is
still the set of videos they encountered. Far weaker than the edges — no
structure, no positions, no ordering — but not nothing, and the UI says so.

`masterplan.md` should be updated to match, or this deviation recorded there.

## Non-negotiables

**Default is Level 1, and Level 1 is fully functional.** `masterplan` is explicit
that a Level 1 user "gets the full benefit of the local search engine and
recommendation scripts, but contributes nothing." If any feature is gated behind
contributing, the privacy promise becomes a toll booth. Nothing in the product
today is gated, and nothing may become gated by this ticket.

**Opt in, never opt out.** No pre-selection above Level 1, no "recommended"
badge on 2 or 3, no nagging.

**Level 3 needs its own confirmation**, separate from selecting it. `DESIGN_v2`
§6 is blunt: L3 output is attributable, anyone including YouTube can read it, and
it cannot be retracted once mirrored — the NYU Ad Observer scenario. The user
must see that sentence before it takes effect, in those terms.

## The honest problem: the pipeline does not exist

STAR is not built. Selecting Level 2 today would send nothing, and a control
that silently does nothing is worse than no control.

So: **ship the setting, show 2 and 3 as unavailable.** Store the chosen level,
render 2 and 3 disabled with "not yet available — no data is being sent by any
version of Keel today." When the pipeline lands, the control is already there and
the extension does not need to change again, which is the point.

Do not hide them entirely: a user deciding whether to trust this should be able
to see what is planned.

## Implementation

- Level lives in the **daemon** (`meta`), not `chrome.storage` — it governs what
  the daemon sends, and the daemon is the only thing that could send it. The
  extension reads and writes it over the bridge, as it does the blocklist
  (WO-016).
- One compact control on the full page, near "Your data". Not in the panel.
- The panel's data section may show current level as text; no control there.

## Acceptance

- [ ] Default is Level 1 on a fresh install.
- [ ] Every existing feature works at Level 1 — verified, not assumed.
- [ ] Levels 2 and 3 render as unavailable with an accurate explanation.
- [ ] Level stored in the daemon and survives a restart.
- [ ] No pre-selection, no dark patterns, equal visual weight.
- [ ] Tests pass.
