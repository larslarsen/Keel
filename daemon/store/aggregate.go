// SPDX-License-Identifier: Apache-2.0
// Measurement aggregation (WO-026).
//
// Turns raw impressions into the EdgeObservation tuple DESIGN_v2 §6.2 defines:
//
//	(from, to, surface, slot_bucket, day_bucket, cohort) → count
//
// This is the one representation every downstream path consumes — STAR
// submissions, published datasets, and any bundle shared with another person.
// Raw impressions never leave this machine in any of those paths; the tuple is
// the boundary.
//
// Bucketing is not cosmetic. Exact slots and exact timestamps are
// high-dimensional and compose into a fingerprint across repeated releases
// (DESIGN_v2 line 22). Coarsening here is what makes any later aggregation
// defensible.
package store

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// HomeFrom is the sentinel origin for HOME rows, which have no context video.
const HomeFrom = "__home__"

// slotBucket coarsens an exact slot to the buckets §6.2 specifies.
//
// The top three positions stay exact because that is where the interesting
// asymmetry lives; everything below is grouped, since "slot 14 vs 15" carries
// no signal but plenty of uniqueness.
func slotBucket(slot int) string {
	switch {
	case slot <= 0:
		return "0"
	case slot == 1:
		return "1"
	case slot == 2:
		return "2"
	case slot <= 5:
		return "3-5"
	case slot <= 10:
		return "6-10"
	default:
		return "11+"
	}
}

// dayBucket is the UTC calendar day. Coarser than a timestamp by design: an
// exact observation time is a browsing timeline.
func dayBucket(unixMilli int64) string {
	return time.UnixMilli(unixMilli).UTC().Format("2006-01-02")
}

// EdgeObservations aggregates the corpus into measurement tuples.
//
// cohort is supplied by the caller rather than derived here — §6.3 restricts it
// to country plus interface language, and neither is observation data the store
// holds. Callers that do not have one should pass "unknown" rather than
// inventing something more specific: a richer cohort is a behavioural
// fingerprint, which §6.3 rules out explicitly.
func (s *Store) EdgeObservations(cohort string) ([]bridge.EdgeObservation, error) {
	if cohort == "" {
		cohort = "unknown"
	}
	rows, err := s.db.Query(`
SELECT surface,
       COALESCE(NULLIF(context_video_id, ''), ?) AS from_id,
       video_id,
       slot_index,
       observed_at
FROM impressions`, HomeFrom)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		from, to, surface, slot, day string
	}
	counts := map[key]int64{}
	for rows.Next() {
		var surface, from, to string
		var slot int
		var observedAt int64
		if err := rows.Scan(&surface, &from, &to, &slot, &observedAt); err != nil {
			return nil, err
		}
		// A video recommending itself is noise, not an edge.
		if from == to {
			continue
		}
		counts[key{from, to, surface, slotBucket(slot), dayBucket(observedAt)}]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]bridge.EdgeObservation, 0, len(counts))
	for k, n := range counts {
		out = append(out, bridge.EdgeObservation{
			From:       k.from,
			To:         k.to,
			Surface:    k.surface,
			SlotBucket: k.slot,
			DayBucket:  k.day,
			Cohort:     cohort,
			Count:      n,
		})
	}
	// Deterministic order so a bundle of the same corpus is byte-identical.
	sort.Slice(out, func(a, b int) bool {
		if out[a].From != out[b].From {
			return out[a].From < out[b].From
		}
		if out[a].To != out[b].To {
			return out[a].To < out[b].To
		}
		if out[a].DayBucket != out[b].DayBucket {
			return out[a].DayBucket < out[b].DayBucket
		}
		return out[a].SlotBucket < out[b].SlotBucket
	})
	return out, nil
}

// CatalogueEntries is the deduplicated video-level view: what YouTube published
// about each video, with no observation of any person attached.
//
// DESIGN_BOOTSTRAP §1: merged and stripped of observation times, this carries no
// personal content — it is the same class of data as a library catalogue. That
// is why it is separated from the edges rather than shipped alongside them.
func (s *Store) CatalogueEntries() ([]bridge.CatalogueEntry, error) {
	rows, err := s.db.Query(`
SELECT video_id, MAX(title), MAX(channel_id), MAX(duration_s), MAX(view_count), MAX(published_at)
FROM impressions
GROUP BY video_id
ORDER BY video_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []bridge.CatalogueEntry{}
	for rows.Next() {
		var e bridge.CatalogueEntry
		var title, ch, pub sql.NullString
		var dur, views sql.NullFloat64
		if err := rows.Scan(&e.VideoID, &title, &ch, &dur, &views, &pub); err != nil {
			return nil, err
		}
		e.Title = title.String
		if ch.Valid {
			v := ch.String
			e.ChannelID = &v
		}
		if pub.Valid {
			v := pub.String
			e.PublishedAt = &v
		}
		if dur.Valid {
			v := dur.Float64
			e.DurationS = &v
		}
		if views.Valid {
			v := views.Float64
			e.ViewCount = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AggregateSummary reports what aggregation achieves, for the UI and for
// anyone checking the claim that bundles are smaller than the raw corpus.
func (s *Store) AggregateSummary(cohort string) (*bridge.AggregateSummaryPayload, error) {
	var raw int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM impressions`).Scan(&raw); err != nil {
		return nil, err
	}
	edges, err := s.EdgeObservations(cohort)
	if err != nil {
		return nil, err
	}
	cat, err := s.CatalogueEntries()
	if err != nil {
		return nil, err
	}
	var days int64
	if err := s.db.QueryRow(
		`SELECT COUNT(DISTINCT date(observed_at/1000, 'unixepoch')) FROM impressions`).Scan(&days); err != nil {
		return nil, err
	}
	return &bridge.AggregateSummaryPayload{
		RawImpressions:   raw,
		EdgeObservations: int64(len(edges)),
		CatalogueEntries: int64(len(cat)),
		DistinctDays:     days,
		Cohort:           cohort,
		Note: fmt.Sprintf(
			"%d observations reduce to %d bucketed edges over %d day(s); exact slots and timestamps are dropped.",
			raw, len(edges), days),
	}, nil
}
