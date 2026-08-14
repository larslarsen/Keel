// SPDX-License-Identifier: Apache-2.0
// The retained word-telemetry snapshot and the search targets it supplies
// (WO-097 §7 and §8).
//
// # Why a snapshot rather than a running merge
//
// WO-068's telemetry is right and its accumulation was not. A Count-Min sketch
// counts additions, and merging is element-wise saturating sum, so folding the
// *same* peer's pack in twice doubles every counter it touches. Nothing about
// re-fetching an unchanged pack says "you already have this" — the registers
// are identical, and identical is exactly what a max-merge would collapse and a
// sum-merge will not. A long-lived node that refreshed hourly would therefore
// drift upward without bound while looking perfectly healthy.
//
// So a refresh is a *round*, not an increment: build a fresh local pack, fetch
// one pack per responding peer, aggregate those and only those from zero, and
// atomically replace what was retained. Re-fetching unchanged packs reproduces
// the same aggregate instead of doubling it. The retained snapshot is persisted
// with its age so a restart, and every search, can read it immediately.
//
// # Why there is no word list
//
// Nothing here stores a word. The HLLs estimate how many distinct words and how
// many videos exist; the CMS answers "roughly how many videos contain *this*
// word" for a word the caller already has. That is enough to give a search a
// target and not enough to enumerate a vocabulary — a CMS cannot be read out,
// only queried, and a top-words list would need a candidate vocabulary this
// order deliberately does not build.
//
// # Why the raw sum is not the target
//
// Peer catalogues overlap. Summing per-peer CMS counters counts a video once
// per peer that holds it, so on a swarm of mirrors the raw estimate can exceed
// anything a search could ever find — an unreachable denominator that would
// leave a word bar permanently short. The correction is an average duplication
// factor measured on the packs already in hand:
//
//	duplication factor = sum(source graph-HLL estimates) / merged graph-HLL estimate
//	adjusted word count = summed CMS estimate / duplication factor
//
// Disjoint corpora give a factor near 1 and leave the estimate alone; N
// identical mirrors give a factor near N and divide it back down. It is an
// average, not a claim that every word overlaps equally, and CMS collisions
// still bias upward — which is why a target is a target and not a ceiling, and
// why WO-095 lets the actual count exceed 100%.
package store

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

// WordSnapshot is one accepted refresh round, retained whole.
type WordSnapshot struct {
	// Telemetry is the aggregate of this round's packs, built from zero.
	Telemetry *WordTelemetry
	// Sources is how many packs went into it: the fresh local pack plus one
	// per responding peer. Peers is the peer half alone.
	Sources int
	Peers   int
	// DuplicationFactor is the average corpus overlap correction, always >= 1.
	// HaveFactor is false when it could not be measured — an empty corpus, or
	// a merged graph estimate of zero — and the raw estimate stands unadjusted
	// with the uncertainty marked.
	DuplicationFactor float64
	HaveFactor        bool
	// RefreshedAt is when the round completed, in unix milliseconds.
	RefreshedAt int64
}

// WordTarget is one word's frozen search target, as a search reads it.
//
// Frozen is the caller's job, not this type's: a search snapshots these once at
// start and never re-reads them, so a refresh landing mid-search cannot move a
// denominator under a bar that is already filling (WO-097 §8).
type WordTarget struct {
	Word string
	// Raw is the summed CMS estimate across the round, kept for diagnostics
	// and corpus-stat presentation. It is deliberately NOT the search target:
	// on mirrored corpora it can be impossible to reach.
	Raw uint64
	// Adjusted is the overlap-corrected estimate, rounded up — the target.
	Adjusted uint64
	// Known is false when there is no retained snapshot at all, or no corpus
	// to measure against. The interface shows a count and "target unknown"
	// rather than inventing a denominator.
	Known bool
	// Uncertain marks an estimate whose duplication factor could not be
	// measured, so Adjusted is the unadjusted raw number wearing a target's
	// clothes.
	Uncertain bool
	// SnapshotAgeMS is how old the retained round is. A stale target is still
	// a usable target; hiding its age is what would not be.
	SnapshotAgeMS int64
}

// BuildWordSnapshot aggregates one refresh round from zero.
//
// `local` is this node's fresh pack, included in the aggregate and — at every
// level — never sent anywhere: it is part of the statistic the person reading
// it belongs to. `peers` are the packs accepted this round, already filtered.
//
// Building from a fresh NewWordTelemetry rather than from the previous
// snapshot is the whole mechanism. See the file comment.
func BuildWordSnapshot(local *WordTelemetry, peers []*WordTelemetry, now time.Time) (*WordSnapshot, error) {
	merged := NewWordTelemetry()
	sources := 0
	sumGraphs := uint64(0)

	fold := func(w *WordTelemetry) error {
		if w == nil {
			return nil
		}
		if err := merged.Merge(w); err != nil {
			return err
		}
		sumGraphs += w.DistinctGraphs()
		sources++
		return nil
	}
	if err := fold(local); err != nil {
		return nil, err
	}
	for _, p := range peers {
		if err := fold(p); err != nil {
			return nil, err
		}
	}
	merged.PrepareWire()

	snap := &WordSnapshot{
		Telemetry:         merged,
		Sources:           sources,
		Peers:             len(peers),
		DuplicationFactor: 1,
		RefreshedAt:       now.UnixMilli(),
	}
	if mergedGraphs := merged.DistinctGraphs(); mergedGraphs > 0 && sumGraphs > 0 {
		f := float64(sumGraphs) / float64(mergedGraphs)
		if f < 1 {
			// Below 1 means the union estimated larger than the sum of its
			// parts, which only HLL noise produces. Clamping keeps the
			// correction from *inflating* a target it exists to deflate.
			f = 1
		}
		snap.DuplicationFactor = f
		snap.HaveFactor = true
	}
	return snap, nil
}

// Target reads one word's target out of the snapshot. No network, no locking
// beyond the snapshot already being in hand.
func (s *WordSnapshot) Target(word string, now time.Time) WordTarget {
	t := WordTarget{Word: word}
	if s == nil || s.Telemetry == nil {
		return t
	}
	t.SnapshotAgeMS = now.UnixMilli() - s.RefreshedAt
	if t.SnapshotAgeMS < 0 {
		t.SnapshotAgeMS = 0
	}
	if s.Telemetry.DistinctGraphs() == 0 {
		return t
	}
	t.Known = true
	t.Raw = s.Telemetry.WordGraphCount(word)
	t.Uncertain = !s.HaveFactor
	factor := s.DuplicationFactor
	if !s.HaveFactor || factor < 1 {
		factor = 1
	}
	t.Adjusted = uint64(math.Ceil(float64(t.Raw) / factor))
	return t
}

// SaveWordSnapshot atomically replaces the retained round.
//
// One row, replaced whole, in one statement: a partially-written snapshot
// would be an aggregate of two different rounds, which is the arithmetic this
// file exists to prevent.
func (s *Store) SaveWordSnapshot(snap *WordSnapshot) error {
	if snap == nil || snap.Telemetry == nil {
		return fmt.Errorf("nil word snapshot")
	}
	snap.Telemetry.PrepareWire()
	_, err := s.db.Exec(`
INSERT INTO word_snapshot(id, p, word_registers, graph_registers, freq, sources, peers, duplication, have_factor, refreshed_at)
VALUES(1,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  p = excluded.p,
  word_registers = excluded.word_registers,
  graph_registers = excluded.graph_registers,
  freq = excluded.freq,
  sources = excluded.sources,
  peers = excluded.peers,
  duplication = excluded.duplication,
  have_factor = excluded.have_factor,
  refreshed_at = excluded.refreshed_at`,
		snap.Telemetry.P,
		snap.Telemetry.WordRegisters,
		snap.Telemetry.GraphRegisters,
		snap.Telemetry.FreqCounters,
		snap.Sources, snap.Peers,
		snap.DuplicationFactor, snap.HaveFactor,
		snap.RefreshedAt)
	return err
}

// LoadWordSnapshot reads the retained round. ok is false when none exists yet —
// a fresh install, or a node that has never completed a valid round.
func (s *Store) LoadWordSnapshot() (*WordSnapshot, bool, error) {
	var (
		p                       int
		wordReg, graphReg, freq []byte
		sources, peers          int
		duplication             float64
		haveFactor              bool
		refreshedAt             int64
	)
	err := s.db.QueryRow(`
SELECT p, word_registers, graph_registers, freq, sources, peers, duplication, have_factor, refreshed_at
FROM word_snapshot WHERE id = 1`).
		Scan(&p, &wordReg, &graphReg, &freq, &sources, &peers, &duplication, &haveFactor, &refreshedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	w := &WordTelemetry{
		P:              uint8(p),
		WordRegisters:  wordReg,
		GraphRegisters: graphReg,
		FreqCounters:   freq,
	}
	if err := w.Hydrate(); err != nil {
		// A corrupt retained round is discarded rather than repaired: it is
		// refetchable by construction, and a half-understood aggregate is
		// worse than no target at all.
		return nil, false, nil
	}
	return &WordSnapshot{
		Telemetry:         w,
		Sources:           sources,
		Peers:             peers,
		DuplicationFactor: duplication,
		HaveFactor:        haveFactor,
		RefreshedAt:       refreshedAt,
	}, true, nil
}

// WordTargets answers a search's start snapshot: one target per word, read
// from the retained round with no network I/O and no wait for a refresh
// (WO-097 §7.4).
//
// An unknown target is a normal answer, not an error. A node that has never
// completed a refresh round still searches; it just cannot say how much of the
// world it has seen, and WO-095 renders that honestly rather than inventing a
// denominator.
func (s *Store) WordTargets(words []string) ([]WordTarget, error) {
	snap, ok, err := s.LoadWordSnapshot()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]WordTarget, 0, len(words))
	for _, w := range words {
		if !ok {
			out = append(out, WordTarget{Word: w})
			continue
		}
		out = append(out, snap.Target(w, now))
	}
	return out, nil
}
