// SPDX-License-Identifier: Apache-2.0
// Persistence and drift-based scheduling for gossiped token sketches
// (WO-067). See sketch.go for the HyperLogLog itself and
// daemon/swarm/sketch.go for the gossip transport that calls into this file.
//
// "Empirical feedback retargets the periodic sketch" (the original WO-059
// design) means concretely: every time this node runs a real search for a
// token, it compares what it actually found against what the merged sketch
// estimated beforehand. A big gap (drift) means the shared knowledge is
// stale and worth re-broadcasting soon; a small gap means it can wait. A
// token nobody has searched locally never gets an urgent schedule from this
// at all — only real search activity drives urgency, never mere receipt of
// gossip (see MergeTokenSketch).
package store

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	// baseGossipInterval is the default re-gossip spacing for a token with
	// no drift signal either way — a brand-new row just learned about via
	// gossip, or one whose last local search matched the estimate closely.
	baseGossipInterval = 30 * time.Minute
	// minGossipInterval/maxGossipInterval clamp the drift-scaled interval so
	// neither a wildly-off estimate nor a perfectly stable one can push the
	// schedule to an extreme that starves the gossip network's bandwidth
	// budget or lets a token go silent for weeks.
	minGossipInterval = 1 * time.Minute
	maxGossipInterval = 24 * time.Hour
)

// TokenSketchRow is one due-for-gossip entry: which dictionary slot, and the
// sketch to publish for it.
type TokenSketchRow struct {
	TokenIndex int
	Sketch     *Sketch
}

// loadTokenSketch reads the current merged sketch for a dictionary index, or
// a fresh one at TokenSketchP if this node has never held or heard anything
// for it.
func (s *Store) loadTokenSketch(idx int) (*Sketch, error) {
	var p int
	var registers []byte
	err := s.db.QueryRow(`SELECT p, registers FROM token_sketches WHERE token_index = ?`, idx).
		Scan(&p, &registers)
	if err == sql.ErrNoRows {
		return NewSketchP(KindToken, TokenSketchP), nil
	}
	if err != nil {
		return nil, err
	}
	return &Sketch{Kind: KindToken, P: uint8(p), Registers: registers}, nil
}

// saveTokenSketch upserts a row, touches last_used_at for LRU, and runs
// eviction — every write is a potential trigger for going over budget.
func (s *Store) saveTokenSketch(idx int, sk *Sketch, dueAt int64, lastObserved *int64, lastObservedAt *int64) error {
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`
INSERT INTO token_sketches(token_index, p, registers, size, last_observed, last_observed_at, due_at, last_used_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(token_index) DO UPDATE SET
  p = excluded.p, registers = excluded.registers, size = excluded.size,
  last_observed = COALESCE(excluded.last_observed, token_sketches.last_observed),
  last_observed_at = COALESCE(excluded.last_observed_at, token_sketches.last_observed_at),
  due_at = excluded.due_at, last_used_at = excluded.last_used_at`,
		idx, sk.P, sk.Registers, len(sk.Registers), lastObserved, lastObservedAt, dueAt, now,
	); err != nil {
		return err
	}
	return s.evictCache(s.DiskBudget())
}

// TokenEstimate is FetchShard's target lookup: this node's current best
// estimate of the network-wide distinct-video count for token, and whether
// it has any data at all (false means "never searched, never gossiped" —
// the caller falls back to pure-saturation behavior with no known target).
func (s *Store) TokenEstimate(token string) (uint64, bool) {
	idx, ok := TokenDictIndex(token)
	if !ok {
		return 0, false
	}
	var p int
	var registers []byte
	err := s.db.QueryRow(`SELECT p, registers FROM token_sketches WHERE token_index = ?`, idx).
		Scan(&p, &registers)
	if err != nil {
		return 0, false
	}
	sk := &Sketch{Kind: KindToken, P: uint8(p), Registers: registers}
	return sk.Count(), true
}

// MergeTokenSketch folds a sketch received via gossip into this node's
// running estimate for one dictionary slot.
//
// Deliberately does NOT touch the row's due_at schedule when it already
// exists: re-gossip timing is driven only by this node's own search
// activity (RecordTokenSearch), never by the mere act of receiving data —
// letting incoming gossip reset our own outgoing schedule would let a
// chatty (or malicious) peer control how often we broadcast, an
// amplification path. A brand-new row (never held or heard before this
// message) gets a starting schedule at baseGossipInterval so it eventually
// enters this node's own rotation rather than sitting there forever unless
// searched.
func (s *Store) MergeTokenSketch(idx int, incoming *Sketch) error {
	if idx < 0 || idx >= TokenDictSize {
		return fmt.Errorf("token index %d out of range [0,%d)", idx, TokenDictSize)
	}
	existing, err := s.loadTokenSketch(idx)
	if err != nil {
		return err
	}
	if err := existing.Merge(incoming); err != nil {
		return err
	}

	var dueAt int64
	err = s.db.QueryRow(`SELECT due_at FROM token_sketches WHERE token_index = ?`, idx).Scan(&dueAt)
	switch {
	case err == sql.ErrNoRows:
		dueAt = time.Now().Add(baseGossipInterval).UnixMilli()
	case err != nil:
		return err
	}
	return s.saveTokenSketch(idx, existing, dueAt, nil, nil)
}

// RecordTokenSearch is the drift feedback loop: called after every real
// FetchShard search for token, comparing what was actually found against
// what was estimated beforehand, and rescheduling this node's own re-gossip
// of it accordingly.
//
// foundVideoIDs also sharpens the sketch itself for free — the search that
// just ran is real evidence, folded in before the comparison, so the drift
// this computes is against the PRE-search estimate (what the network could
// have told a searcher a moment ago), not a self-fulfilling post-hoc one.
func (s *Store) RecordTokenSearch(token string, foundVideoIDs []string) error {
	idx, ok := TokenDictIndex(token)
	if !ok {
		return nil // not a dictionary-shaped token; nothing to record
	}
	existed, err := s.tokenSketchExists(idx)
	if err != nil {
		return err
	}
	sk, err := s.loadTokenSketch(idx)
	if err != nil {
		return err
	}
	before := sk.Count()
	for _, id := range foundVideoIDs {
		sk.Add(id)
	}
	observed := uint64(len(foundVideoIDs))

	now := time.Now()
	var dueAt int64
	if !existed {
		// A token this node had no data on at all, now backed by a real
		// search, is qualitatively different from an update to a known
		// estimate: it is new information nobody gossiped yet, worth
		// sharing at the next opportunity rather than filtered through the
		// same drift math an update gets.
		dueAt = now.UnixMilli()
	} else {
		dueAt = now.Add(gossipBackoff(tokenDrift(before, observed))).UnixMilli()
	}
	obs := int64(observed)
	obsAt := now.UnixMilli()
	return s.saveTokenSketch(idx, sk, dueAt, &obs, &obsAt)
}

// tokenSketchExists reports whether a row already exists for idx, so
// RecordTokenSearch can tell "brand new discovery" from "update to a known
// estimate" — loadTokenSketch alone can't distinguish them, since both
// return a usable (possibly all-zero) sketch either way.
func (s *Store) tokenSketchExists(idx int) (bool, error) {
	var x int
	err := s.db.QueryRow(`SELECT 1 FROM token_sketches WHERE token_index = ?`, idx).Scan(&x)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}

// tokenDrift is the relative gap between what was estimated and what a real
// search actually found. Both zero (an estimate of nothing, a search that
// found nothing) is agreement, not drift — there is no discrepancy to
// report, so it must not read as maximally urgent.
func tokenDrift(estimate, observed uint64) float64 {
	if estimate == 0 && observed == 0 {
		return 0
	}
	denom := estimate
	if observed > denom {
		denom = observed
	}
	var diff uint64
	if observed > estimate {
		diff = observed - estimate
	} else {
		diff = estimate - observed
	}
	return float64(diff) / float64(denom)
}

// gossipBackoff maps a drift ratio to a re-gossip delay by linear
// interpolation between the two clamps: drift=0 (the last search matched
// the estimate exactly) gives maxGossipInterval, drift=1 (the theoretical
// ceiling — tokenDrift's diff can never exceed its own denominator) gives
// minGossipInterval. A drift/(1+k*drift)-style curve was tried first and
// was wrong twice over: tokenDrift never actually exceeds 1, so a curve
// shaped to expect larger inputs left minGossipInterval unreachable by any
// real call, and the interval it returned at drift=0 was the middle of the
// range (baseGossipInterval) rather than the slow end — "no discrepancy at
// all" was getting a moderate schedule instead of the calmest one available.
func gossipBackoff(drift float64) time.Duration {
	if drift < 0 {
		drift = 0
	}
	if drift > 1 {
		drift = 1
	}
	span := float64(maxGossipInterval - minGossipInterval)
	return maxGossipInterval - time.Duration(drift*span)
}

// DueTokenSketches lists tokens whose scheduled re-gossip time has arrived,
// most-overdue first, capped at limit — the publish-side rate limit
// (WO-067's "rate limit the gossip network"): however many tokens this node
// is tracking, one tick only ever gossips up to limit of them.
func (s *Store) DueTokenSketches(limit int) ([]TokenSketchRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT token_index, p, registers FROM token_sketches
WHERE due_at <= ?
ORDER BY due_at ASC
LIMIT ?`, time.Now().UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TokenSketchRow{}
	for rows.Next() {
		var idx, p int
		var registers []byte
		if err := rows.Scan(&idx, &p, &registers); err != nil {
			return nil, err
		}
		out = append(out, TokenSketchRow{
			TokenIndex: idx,
			Sketch:     &Sketch{Kind: KindToken, P: uint8(p), Registers: registers},
		})
	}
	return out, rows.Err()
}

// MarkTokenGossiped pushes a row's schedule out by baseGossipInterval after
// this node has just published it — a flat reset, not a drift computation:
// gossiping is not new evidence about accuracy, so it does not change the
// urgency signal, only restarts the clock on when to do it again.
func (s *Store) MarkTokenGossiped(idx int) error {
	due := time.Now().Add(baseGossipInterval).UnixMilli()
	_, err := s.db.Exec(`UPDATE token_sketches SET due_at = ? WHERE token_index = ?`, due, idx)
	return err
}
