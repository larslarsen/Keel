// SPDX-License-Identifier: Apache-2.0
// Level-2 contribution impact (WO-086).
//
// A Level-2 user's copy serves other people; this is the feedback that makes
// that visible. Two different kinds of number, computed two different ways:
//
//   - Current corpus state (claims eligible to serve, local-vs-peer-cached
//     split, buckets/shards announced) is recomputed fresh on every call from
//     the same store queries the serve handlers and the announce loop already
//     use. Nothing here is persisted, so there is nothing to bound and nothing
//     to reset.
//   - Cumulative serving activity (requests answered, bytes served) cannot be
//     a live query — it is history, not state — so it is the one thing this
//     file persists, in contribution_activity: one row, two counters and a
//     day-precision "since" marker, never a peer id, query, prefix/bucket
//     identifier or per-request timestamp. See ResetContributionActivity for
//     the explicit local reset the privacy boundary requires.
//
// Never derive rewards, rankings or service credits from any of this. It is
// local, unaudited feedback and may reset at any time.
package store

import "time"

// ImpactSnapshot is the live-computed half of WO-086's panel: current corpus
// state, not history, so none of it is persisted.
type ImpactSnapshot struct {
	GraphClaimsLocal, GraphClaimsPeerCached int
	CatalogueLocal, CataloguePeerCached     int
	BucketsAnnounced, ShardsAnnounced       int
}

// ContributionImpactSnapshot reports what this node currently holds and would
// announce, split by origin without exposing any identifier.
//
// prefixBits and graphSources/catalogueSources must be the same values the
// caller's node actually serves and announces under (Node.prefixBits,
// Policy.GraphSources/CatalogueSources) — this mirrors LocalPrefixes' own
// rule so the counts shown are provably the corpus WO-084's serve handlers
// answer from, not a coincidentally matching one. A source whose flag is
// false is not queried at all, so an unselected corpus never contributes an
// invented zero versus a genuine one.
func (s *Store) ContributionImpactSnapshot(prefixBits int, graphSources, catalogueSources SourceSet) (ImpactSnapshot, error) {
	var snap ImpactSnapshot

	if graphSources.Local {
		keys, err := s.LocalGraphKeys()
		if err != nil {
			return ImpactSnapshot{}, err
		}
		snap.GraphClaimsLocal = len(keys)
	}
	if graphSources.Peers {
		keys, err := s.PeerGraphKeys()
		if err != nil {
			return ImpactSnapshot{}, err
		}
		snap.GraphClaimsPeerCached = len(keys)
	}

	if catalogueSources.Local {
		local, err := s.heldCatalogue(SourceSet{Local: true})
		if err != nil {
			return ImpactSnapshot{}, err
		}
		snap.CatalogueLocal = len(local)
	}
	if catalogueSources.Peers {
		peers, err := s.heldCatalogue(SourceSet{Peers: true})
		if err != nil {
			return ImpactSnapshot{}, err
		}
		snap.CataloguePeerCached = len(peers)
	}

	if !graphSources.Empty() {
		prefixes, err := s.LocalPrefixes(prefixBits, graphSources)
		if err != nil {
			return ImpactSnapshot{}, err
		}
		snap.BucketsAnnounced = len(prefixes)
	}
	if !catalogueSources.Empty() {
		shards, err := s.LocalShards(catalogueSources)
		if err != nil {
			return ImpactSnapshot{}, err
		}
		snap.ShardsAnnounced = len(shards)
	}

	return snap, nil
}

// RecordContributionServe accounts for one successfully answered request.
//
// Called only after a reply has actually been written to the wire — never
// from a refusal (mayServeX/serveLimiter.admit failing) and never when
// serveLimiter.chargeBytes drops an already-built reply, so a request refused
// by policy or by the serving budget is never counted as answered.
func (s *Store) RecordContributionServe(bytesWritten int) error {
	res, err := s.db.Exec(
		`UPDATE contribution_activity
		 SET requests_answered = requests_answered + 1, bytes_served = bytes_served + ?`,
		bytesWritten)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	// Empty table: the one-time seed row.
	_, err = s.db.Exec(
		`INSERT INTO contribution_activity(requests_answered, bytes_served, since_day) VALUES(1, ?, ?)`,
		bytesWritten, dayBucket(time.Now().UnixMilli()))
	return err
}

// ContributionActivity reads the persisted counters.
//
// No row yet — nothing has ever been served — reports zeroes rather than an
// error; a fresh Level-2 node with no traffic is not a failure state.
func (s *Store) ContributionActivity() (answered, bytesServed int64, sinceDay string, err error) {
	err = s.db.QueryRow(
		`SELECT requests_answered, bytes_served, since_day FROM contribution_activity`,
	).Scan(&answered, &bytesServed, &sinceDay)
	if err != nil {
		// sql.ErrNoRows means nothing has ever been served yet, not a failure.
		return 0, 0, "", nil
	}
	return answered, bytesServed, sinceDay, nil
}

// ResetContributionActivity zeroes the persisted counters, the explicit local
// reset the privacy boundary requires for anything it persists.
//
// Deliberately does not touch impressions, peer_blocks or any other corpus
// table — same rule Wipe follows in reverse: a data-scoped action and a
// counter-scoped action must not be conflated into a single button.
func (s *Store) ResetContributionActivity() error {
	res, err := s.db.Exec(
		`UPDATE contribution_activity SET requests_answered = 0, bytes_served = 0, since_day = ?`,
		dayBucket(time.Now().UnixMilli()))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = s.db.Exec(
		`INSERT INTO contribution_activity(requests_answered, bytes_served, since_day) VALUES(0, 0, ?)`,
		dayBucket(time.Now().UnixMilli()))
	return err
}
