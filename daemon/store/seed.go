// SPDX-License-Identifier: Apache-2.0
// Seed packs (WO-052, DESIGN_BOOTSTRAP §5d "Bootstrap").
//
// Fetch-on-demand leaks the query: the peer you ask sees your address and which
// video you asked about. A seed pack removes the query rather than obscuring it.
//
// The distinction matters, because obscuring does not work here. Decoy requests
// and batched region fetches both give ambiguity within a set, and both fall to
// the same intersection attack — watch enough sets from one address and the
// common element is the real one. That is the flaw that sank the v1 k-anonymity
// buffer, and it applies unchanged to anything that hides a real query among
// fake ones.
//
// A seed pack has a different shape. Every node downloads the *same* pack, so
// requesting it discloses nothing beyond "this address runs Keel" — there is no
// per-user variation to intersect. And once the pack is loaded, lookups it
// covers never touch the network at all, so the head of the watch distribution
// stops generating queries entirely.
//
// It is not a complete answer. The long tail still needs fetch-on-demand, and
// hiding *that* query needs private information retrieval, which is a much
// larger piece of machinery. The seed is what makes the common case free.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

const seedSchemaVersion = 1

// SeedPack is a signed collection of blocks covering popular videos.
type SeedPack struct {
	SchemaVersion int     `json:"schema_version"`
	CreatedDay    string  `json:"created_day"`
	Cohort        string  `json:"cohort"`
	Blocks        []Block `json:"blocks"`

	Signature string `json:"signature,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Algorithm string `json:"signature_alg,omitempty"`
}

// PopularBlockKeys returns the videos worth pre-answering: those many other
// videos lead to, restricted to ones this node can actually serve a
// neighbourhood for.
//
// Both halves are necessary, and the second was found by running this against a
// real corpus rather than a fixture. Ranking by inbound popularity alone returns
// almost entirely *leaves* — videos recommended to the user but never watched,
// which have no outgoing edges and therefore produce empty blocks. On a corpus
// with thousands of observed videos and a few dozen watched ones, that yielded a
// pack of nothing at all.
//
// Inbound is still the right ranking signal, because it predicts what users will
// land on. It just has to be filtered to what is answerable.
func (s *Store) PopularBlockKeys(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(`
SELECT k, SUM(n) AS total FROM (
  SELECT video_id AS k, COUNT(*) AS n FROM impressions GROUP BY video_id
  UNION ALL
  SELECT to_id AS k, SUM(count) AS n FROM peer_edges GROUP BY to_id
)
WHERE k != ''
  AND (k IN (SELECT context_video_id FROM impressions WHERE context_video_id IS NOT NULL)
    OR k IN (SELECT from_id FROM peer_edges))
GROUP BY k
ORDER BY total DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		var total int64
		if err := rows.Scan(&k, &total); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// BuildSeedPack assembles blocks for the most popular videos this node knows
// about, signed as a unit.
//
// `sources` follows the contribution level exactly as block serving does
// (Policy.GraphSources), and it is a union: a Level-2 node's pack carries its
// own neighbourhood claims alongside the claims it holds for peers, each
// preserved under its own identity. Seeding is publication in bulk, so it may
// not include a source the node would not serve on demand.
func (s *Store) BuildSeedPack(limit int, cohort string, sources SourceSet) (*SeedPack, error) {
	keys, err := s.PopularBlockKeys(limit)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys) // deterministic: the same corpus produces the same pack

	pack := &SeedPack{
		SchemaVersion: seedSchemaVersion,
		CreatedDay:    time.Now().UTC().Format("2006-01-02"),
		Cohort:        cohort,
	}
	if sources.Local {
		for _, k := range keys {
			blk, err := s.BuildBlock(k, cohort)
			if err != nil {
				return nil, err
			}
			if len(blk.Edges) == 0 {
				continue // an empty block helps nobody and costs bytes
			}
			pack.Blocks = append(pack.Blocks, *blk)
		}
	}
	if sources.Peers {
		claims, err := s.PeerClaimsForKeys(keys)
		if err != nil {
			return nil, err
		}
		for _, c := range claims {
			if len(c.Edges) == 0 {
				continue
			}
			pack.Blocks = append(pack.Blocks, c)
		}
	}
	sort.Slice(pack.Blocks, func(a, b int) bool {
		if pack.Blocks[a].Key != pack.Blocks[b].Key {
			return pack.Blocks[a].Key < pack.Blocks[b].Key
		}
		return pack.Blocks[a].ClaimID() < pack.Blocks[b].ClaimID()
	})
	if len(pack.Blocks) == 0 {
		return nil, fmt.Errorf("no blocks to seed — this node has no edges to share")
	}

	payload, err := json.Marshal(pack.Blocks)
	if err != nil {
		return nil, err
	}
	if pack.Signature, pack.PublicKey, err = s.signPayload(payload); err != nil {
		return nil, err
	}
	pack.Algorithm = signAlgorithm
	return pack, nil
}

// ImportSeedPack verifies and loads a pack.
//
// Every block inside is verified individually by the same rules a fetched block
// faces — a pack is a delivery mechanism, not a trust shortcut. A pack with one
// bad block loads the rest rather than failing wholesale, because a partial
// seed is still useful and refusing everything would hand any publisher a way
// to break every consumer with one malformed row.
func (s *Store) ImportSeedPack(raw []byte) (loaded int, edges int64, err error) {
	var pack SeedPack
	if err := json.Unmarshal(raw, &pack); err != nil {
		return 0, 0, fmt.Errorf("seed pack is not valid JSON: %w", err)
	}
	if pack.SchemaVersion > seedSchemaVersion {
		return 0, 0, fmt.Errorf("seed pack schema %d is newer than this build understands (%d)",
			pack.SchemaVersion, seedSchemaVersion)
	}
	if len(pack.Blocks) == 0 {
		return 0, 0, fmt.Errorf("seed pack is empty")
	}
	if pack.Signature != "" || pack.PublicKey != "" {
		payload, err := json.Marshal(pack.Blocks)
		if err != nil {
			return 0, 0, err
		}
		if err := verifyPayload(payload, pack.Signature, pack.PublicKey); err != nil {
			return 0, 0, fmt.Errorf("seed pack: %w", err)
		}
	}

	for i := range pack.Blocks {
		encoded, err := pack.Blocks[i].Encode()
		if err != nil {
			continue
		}
		_, n, err := s.ImportBlock(encoded)
		if err != nil {
			continue // a bad block is skipped; the rest of the pack still loads
		}
		loaded++
		edges += n
	}
	if loaded == 0 {
		return 0, 0, fmt.Errorf("no block in the pack verified")
	}
	return loaded, edges, nil
}

// WriteSeedPack saves a pack to disk.
func (p *SeedPack) WriteSeedPack(path string) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// HaveBlock reports whether this node already holds edges for a video.
//
// This is what turns the seed into a privacy measure rather than just a speed
// one: a lookup the seed already answers never becomes a network request, so
// there is no query for anyone to observe.
func (s *Store) HaveBlock(videoID string) (bool, error) {
	var found bool
	err := s.db.QueryRow(`
SELECT EXISTS(SELECT 1 FROM peer_edges WHERE from_id = ?)
    OR EXISTS(SELECT 1 FROM impressions WHERE context_video_id = ?)`,
		videoID, videoID).Scan(&found)
	if err != nil {
		return false, err
	}
	return found, nil
}
