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
	// LevelMirror lends storage and bandwidth: the node holds and re-serves
	// data other people published, and fetches in buckets for itself.
	//
	// Named for what it does. It was LevelCatalogue when the plan was to share
	// video metadata and no edges, which is neither what shipped nor what makes
	// suggestions reach past a user's own history. Nothing this node observed is
	// published at this level — that begins at LevelCohort.
	LevelMirror = 2
	// LevelCohort publishes this node's own edge counts under threshold
	// encryption. Not implemented — STAR does not exist yet.
	LevelCohort = 3
	// LevelTransparency publishes attributable funnel state, deliberately
	// identifiable. Not implemented.
	LevelTransparency = 4

	// metaContributionKey holds stored_level: the user's persisted choice.
	metaContributionKey = "contribution_level"
	// metaStartupLevelKey holds startup_level: the highest policy a fresh
	// process may construct (WO-077).
	//
	// It exists because an upgrade is two steps — persist the choice, then
	// make it effective — and a crash between them must not be resolved by
	// escalating. During an upgrade this stays at the last level that was
	// actually running, so a restart reconstructs that, reports the mismatch,
	// and waits for the user rather than silently completing a transition
	// they never saw succeed. On downgrade both move together, in one
	// transaction, before the higher-level node is torn down.
	metaStartupLevelKey = "contribution_startup_level"
)

// MaxImplementedLevel is the highest level that actually does anything.
//
// Raise this only when the corresponding pipeline exists. It is what the UI
// reads to decide which options are selectable, so a stale value here would let
// the interface promise something the daemon cannot do.
const MaxImplementedLevel = LevelMirror

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
	if err := ValidateContributionLevel(level); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaContributionKey, strconv.Itoa(level)); err != nil {
		return 0, err
	}
	return level, nil
}

// ValidateContributionLevel reports whether a level may be selected at all.
func ValidateContributionLevel(level int) error {
	if level < LevelPersonal || level > LevelTransparency {
		return fmt.Errorf("contribution level must be 1, 2, 3 or 4")
	}
	if level > MaxImplementedLevel {
		return fmt.Errorf(
			"level %d is not available yet: Keel cannot send anything, so selecting it would change nothing",
			level)
	}
	return nil
}

// StartupLevel returns the highest policy a fresh process may construct.
//
// Defaults to the stored level when absent, which is the migration path for a
// database written before WO-077 split the two: a node that has only ever had
// one value has, by definition, never been mid-transition.
//
// Clamped to the stored level on read. A startup level above the stored one
// can only come from corruption or a partly-applied downgrade, and in both
// cases the safe reading is the user's choice, not the leftover.
func (s *Store) StartupLevel() int {
	stored := s.ContributionLevel()
	var v string
	if err := s.db.QueryRow(
		`SELECT value FROM meta WHERE key = ?`, metaStartupLevelKey).Scan(&v); err != nil {
		return stored
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < LevelPersonal || n > LevelTransparency {
		return LevelPersonal
	}
	if n > stored {
		return stored
	}
	return n
}

// SetStartupLevel commits the highest reconstructable policy on its own.
//
// Used to raise the startup level *after* an upgrade is effective, which is
// the commit point that makes the new policy survive a restart.
func (s *Store) SetStartupLevel(level int) error {
	if err := ValidateContributionLevel(level); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaStartupLevelKey, strconv.Itoa(level))
	return err
}

// SetContributionAndStartupLevel moves both values together, atomically.
//
// This is the downgrade path and it must be one transaction: a crash that
// left stored=1 with startup=2 would reconstruct a Level-2 node for a user
// who had chosen Level 1 — the exact failure the two-value scheme exists to
// prevent. Going down, there is no window where the higher level is still
// reconstructable.
func (s *Store) SetContributionAndStartupLevel(level int) error {
	if err := ValidateContributionLevel(level); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range []string{metaContributionKey, metaStartupLevelKey} {
		if _, err := tx.Exec(
			`INSERT INTO meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			key, strconv.Itoa(level)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
