// SPDX-License-Identifier: Apache-2.0
// Contribution level (WO-051).
//
// How much this node contributes. ARCHITECTURE_CURRENT.md §3 is normative:
//
//	1 — Strictly Personal:        live notices + word aggregate; no served blocks/observations
//	2 — Broad Sharing:            complete prefix buckets, local and cached together
//	3 — Cohort Aggregator:        STAR-protected cohort measurement (§6.2)
//	4 — Transparency Contributor: full funnel state, publicly attributed
//
// Level 2 serves the complete eligible contents of each bucket it advertises:
// neighbourhoods derived from this node's own observations *and* the claims it
// holds on behalf of peers, indistinguishable in the response (WO-084). The
// privacy mechanism is the breadth of the bucket, not the exclusion of local
// data — a bucket holds many neighbourhoods, is requested and answered whole,
// and carries only aggregated edge counts. Level 3 is not "the first level that
// shares"; it is the first level that runs STAR.
//
// The level lives here rather than in browser storage because the daemon is the
// only component that could ever send anything; the extension merely displays
// and sets it.
//
// Level 2 is implemented. Levels 3 and 4 remain unavailable until their own
// privacy mechanisms exist; callers must not infer unavailable capabilities
// merely from an integer stored in the database.
package store

import (
	"fmt"
	"strconv"
)

const (
	// LevelPersonal is the default full-consumer policy. It serves no blocks or
	// observations; WO-078's narrow live and word-aggregate products remain on.
	//
	// It gets the full search/recommendation product without serving blocks or
	// observations. No consumer feature may be gated above it — that would make
	// the privacy promise a toll booth. Its live notices and fixed-shape word
	// aggregate are explicit narrow disclosures, not observation publication.
	LevelPersonal = 1
	// LevelBroad serves complete prefix buckets: aggregated neighbourhoods
	// this node derived from its own observations together with the ones it
	// holds for peers, plus the public catalogue and search material needed to
	// render them.
	//
	// It was LevelMirror, and before that LevelCatalogue. Both names described
	// a contract this level does not have. "Mirror" claimed nothing the user
	// observed is published, which WO-084 corrected: locally derived
	// aggregated blocks do leave the device here. What keeps that acceptable is
	// the unit — a whole hashed-prefix bucket of many neighbourhoods, answered
	// whole, carrying counts and no page-load id, timestamp, title, query or
	// ordered trail — not the absence of local data.
	LevelBroad = 2
	// LevelCohort publishes cohort measurements under STAR threshold
	// encryption. Not implemented — no STAR client exists.
	//
	// It does not "turn on" ordinary graph blocks; LevelBroad already serves
	// those. Its product is the comparison measurement, recoverable only once
	// enough independent nodes report the same value.
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
const MaxImplementedLevel = LevelBroad

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
