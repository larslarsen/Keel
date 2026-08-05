// SPDX-License-Identifier: Apache-2.0
// Contribution level (WO-051).
//
// How much this node contributes, per masterplan.md:
//
//	1 — Strictly Personal:        nothing leaves the device
//	2 — Catalogue Only:           video metadata, no recommendation edges
//	3 — Cohort Aggregator:        catalogue + STAR-aggregated edge counts (§6.2)
//	4 — Transparency Contributor: full funnel state, publicly attributed
//
// Level 2 exists because DESIGN_BOOTSTRAP §1 splits the corpus in two: the
// catalogue is public fact about public videos, while the edges are an
// observation of a person. Someone can help build the shared catalogue — the
// part that makes search work for everyone — without exposing which videos were
// recommended to them after which.
//
// The level lives here rather than in browser storage because the daemon is the
// only component that could ever send anything; the extension merely displays
// and sets it.
//
// **Levels 2 and 3 are not implemented.** STAR does not exist yet, so nothing is
// transmitted at any level. The setting is stored so the control can exist
// before the pipeline does — but callers must not treat level > 1 as permission
// to send, because there is nothing to send with.
package store

import (
	"fmt"
	"strconv"
)

const (
	// LevelPersonal is the default: the full local product, contributing nothing.
	//
	// masterplan.md is explicit that this level "gets the full benefit of the
	// local search engine and recommendation scripts, but contributes nothing".
	// No feature may ever be gated above it — that would make the privacy
	// promise a toll booth.
	LevelPersonal = 1
	// LevelCatalogue shares video metadata only — no edges. Not implemented.
	LevelCatalogue = 2
	// LevelCohort adds STAR-aggregated edge counts. Not implemented.
	LevelCohort = 3
	// LevelTransparency publishes attributable funnel state. Not implemented.
	LevelTransparency = 4

	metaContributionKey = "contribution_level"
)

// MaxImplementedLevel is the highest level that actually does anything.
//
// Raise this only when the corresponding pipeline exists. It is what the UI
// reads to decide which options are selectable, so a stale value here would let
// the interface promise something the daemon cannot do.
const MaxImplementedLevel = LevelPersonal

// ContributionLevel returns the stored level, defaulting to Personal.
//
// Defaults to 1 on anything unreadable: an unparseable setting must never be
// interpreted as consent to contribute.
func (s *Store) ContributionLevel() int {
	var v string
	if err := s.db.QueryRow(
		`SELECT value FROM meta WHERE key = ?`, metaContributionKey).Scan(&v); err != nil {
		return LevelPersonal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < LevelPersonal || n > LevelTransparency {
		return LevelPersonal
	}
	return n
}

// SetContributionLevel stores the level.
//
// Rejects levels whose pipeline does not exist rather than storing an
// aspiration — a stored 2 with no STAR client is a setting that lies.
func (s *Store) SetContributionLevel(level int) (int, error) {
	if level < LevelPersonal || level > LevelTransparency {
		return 0, fmt.Errorf("contribution level must be 1, 2, 3 or 4")
	}
	if level > MaxImplementedLevel {
		return 0, fmt.Errorf(
			"level %d is not available yet: Keel cannot send anything, so selecting it would change nothing",
			level)
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaContributionKey, strconv.Itoa(level)); err != nil {
		return 0, err
	}
	return level, nil
}
