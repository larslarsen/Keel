// SPDX-License-Identifier: Apache-2.0
// Graph blocks (WO-052, DESIGN_BOOTSTRAP §5d).
//
// §5d's structural claim is that a random walk needs neighbourhoods, not the
// graph: each hop only needs the edges out of the video it is standing on. So
// the graph is cut into blocks keyed by `context_video_id`, and a node holds the
// blocks it has actually used rather than the whole dataset. At full scale the
// deduped graph is ~2–35 TB; one user touches tens of MB of it.
//
// A block is therefore the unit of everything downstream — fetch-on-demand, the
// LRU cache the disk slider sizes, and the background prewarm that fires when a
// watch page loads. This file builds, signs, verifies and merges them. It does
// no networking: the transport is a separate decision (WO-052), and defining the
// block first is what keeps that decision cheap to change.
//
// Blocks reuse the bundle layer's canonical bytes, digest and signature rather
// than inventing a second format, so the guarantees are the same ones sign.go
// already documents — integrity and attribution, never proof that an
// observation is true (DESIGN_v2 §6.4).
package store

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/keel-app/keel/daemon/bridge"
)

// blockSchemaVersion is bumped when the wire shape changes incompatibly.
// Version 2 removed the embedded catalogue.
const blockSchemaVersion = 2

// Block is neighbours(v): every edge observed leading out of one video.
//
// Blocks are **stringless**. Titles and channel names live in the catalogue
// dataset (§1) and travel separately, for two reasons. Measurement: strings were
// 45 KB of a 63 KB pack, because the same title ships again in every block that
// points at that video. And structure: the graph and the catalogue converge
// differently — the graph churns forever, the catalogue is written once per
// video — so binding them into one payload forces the wrong sync policy on both.
//
// The cost is that a walk reaching new territory renders ids until the catalogue
// catches up. That is deliberate.
type Block struct {
	SchemaVersion int    `json:"schema_version"`
	Key           string `json:"key"` // the context video these edges lead out of
	Cohort        string `json:"cohort"`

	Edges []bridge.EdgeObservation `json:"edges"`

	ContentSHA256 string `json:"content_sha256"`
	Signature     string `json:"signature,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	Algorithm     string `json:"signature_alg,omitempty"`
}

// blockEdges aggregates this node's observations leading out of one video.
//
// This mirrors EdgeObservations exactly — same bucketing, same self-edge drop,
// same deterministic order — so a block is a slice of what the node would
// publish, never a differently-shaped view of it.
func (s *Store) blockEdges(videoID, cohort string) ([]bridge.EdgeObservation, error) {
	if cohort == "" {
		cohort = "unknown"
	}
	rows, err := s.db.Query(`
SELECT surface, video_id, slot_index, observed_at
FROM impressions
WHERE COALESCE(NULLIF(context_video_id, ''), ?) = ?`, HomeFrom, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct{ to, surface, slot, day string }
	counts := map[key]int64{}
	for rows.Next() {
		var surface, to string
		var slot int
		var observedAt int64
		if err := rows.Scan(&surface, &to, &slot, &observedAt); err != nil {
			return nil, err
		}
		if to == videoID {
			continue // a video recommending itself is noise, not an edge
		}
		counts[key{to, surface, slotBucket(slot), dayBucket(observedAt)}]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]bridge.EdgeObservation, 0, len(counts))
	for k, n := range counts {
		out = append(out, bridge.EdgeObservation{
			From: videoID, To: k.to, Surface: k.surface,
			SlotBucket: k.slot, DayBucket: k.day, Cohort: cohort, Count: n,
		})
	}
	sort.Slice(out, func(a, b int) bool {
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

// mirrorEdges returns the edges for one video that came from *other people* —
// rows this node imported, never its own observations.
//
// This is the difference between contribution Level 2 and Level 3, and it has
// to be enforced in the query rather than by a caller remembering. Level 2 is
// "mirror and serve the public aggregate": the node donates storage and
// bandwidth, and nothing it personally observed leaves. Serving a block built
// from `impressions` at Level 2 would publish the user's own funnel, which is
// what Levels 3 and 4 exist to gate.
func (s *Store) mirrorEdges(videoID string) ([]bridge.EdgeObservation, error) {
	rows, err := s.db.Query(`
SELECT to_id, surface, slot_bucket, day_bucket, cohort, SUM(count)
FROM peer_edges
WHERE from_id = ? AND to_id != from_id
GROUP BY to_id, surface, slot_bucket, day_bucket, cohort`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []bridge.EdgeObservation{}
	for rows.Next() {
		var e bridge.EdgeObservation
		if err := rows.Scan(&e.To, &e.Surface, &e.SlotBucket, &e.DayBucket, &e.Cohort, &e.Count); err != nil {
			return nil, err
		}
		e.From = videoID
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool {
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

// BuildBlock assembles a block from this node's own observations. Serving one
// publishes what this user was recommended, so it belongs to contribution
// Level 3 and above.
//
// An empty block is returned rather than an error when the node has seen
// nothing from that video — "I have no edges here" is a valid answer to serve.
func (s *Store) BuildBlock(videoID, cohort string) (*Block, error) {
	return s.buildBlock(videoID, cohort, false)
}

// BuildMirrorBlock assembles a block from imported edges only — the Level 2
// answer. Nothing this node observed is included, so serving it donates
// bandwidth and storage without disclosing anything personal.
func (s *Store) BuildMirrorBlock(videoID, cohort string) (*Block, error) {
	return s.buildBlock(videoID, cohort, true)
}

func (s *Store) buildBlock(videoID, cohort string, mirrorOnly bool) (*Block, error) {
	if videoID == "" {
		return nil, fmt.Errorf("block key required")
	}
	var edges []bridge.EdgeObservation
	var err error
	if mirrorOnly {
		edges, err = s.mirrorEdges(videoID)
	} else {
		edges, err = s.blockEdges(videoID, cohort)
	}
	if err != nil {
		return nil, err
	}

	b := &Block{
		SchemaVersion: blockSchemaVersion,
		Key:           videoID,
		Cohort:        cohort,
		Edges:         edges,
	}
	if b.ContentSHA256, err = contentDigest(nil, edges); err != nil {
		return nil, err
	}
	payload, err := canonicalPayload(nil, edges)
	if err != nil {
		return nil, err
	}
	if b.Signature, b.PublicKey, err = s.signPayload(payload); err != nil {
		return nil, err
	}
	b.Algorithm = signAlgorithm
	return b, nil
}

// LocalBlockKeys lists what this node advertises to the network.
//
// mirrorOnly must follow the contribution level, and getting it wrong is a real
// disclosure rather than a cosmetic one: the set of context videos drawn from
// `impressions` is the list of videos this user watched. Advertising that is
// equivalent to publishing a viewing history, which no level below 3 permits.
//
// So a mirroring node advertises only what it holds on behalf of other people.
// It is genuinely donating storage and bandwidth, and a peer watching its
// announcements learns what it is hosting, not what its user did.
func (s *Store) LocalBlockKeys(mirrorOnly bool) ([]string, error) {
	q := `SELECT DISTINCT from_id FROM peer_edges`
	args := []any{}
	if !mirrorOnly {
		q = `SELECT DISTINCT COALESCE(NULLIF(context_video_id, ''), ?) FROM impressions
UNION
SELECT DISTINCT from_id FROM peer_edges`
		args = append(args, HomeFrom)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if k != "" {
			out = append(out, k)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// VerifyBlock parses and checks a block received from anywhere.
//
// Both checks matter and they answer different questions: the digest says the
// bytes were not altered, the signature says which key produced them. Neither
// says the edges are real — nothing signs ytInitialData (DESIGN_v2 §6.4) — so a
// caller must still treat block contents as a claim, not a fact.
func VerifyBlock(raw []byte) (*Block, error) {
	var b Block
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("block is not valid JSON: %w", err)
	}
	if b.SchemaVersion > blockSchemaVersion {
		return nil, fmt.Errorf("block schema %d is newer than this build understands (%d)",
			b.SchemaVersion, blockSchemaVersion)
	}
	if b.Key == "" {
		return nil, fmt.Errorf("block has no key")
	}
	for _, e := range b.Edges {
		if e.From != b.Key {
			return nil, fmt.Errorf("block %q contains an edge from %q — blocks hold one neighbourhood",
				b.Key, e.From)
		}
	}
	digest, err := contentDigest(nil, b.Edges)
	if err != nil {
		return nil, err
	}
	if digest != b.ContentSHA256 {
		return nil, fmt.Errorf("block contents do not match its digest")
	}
	// Signing postdates the format, so an unsigned block is accepted and simply
	// carries no attribution — the same allowance bundle.go makes.
	if b.Signature != "" || b.PublicKey != "" {
		payload, err := canonicalPayload(nil, b.Edges)
		if err != nil {
			return nil, err
		}
		if err := verifyPayload(payload, b.Signature, b.PublicKey); err != nil {
			return nil, err
		}
	}
	return &b, nil
}

// ImportBlock verifies a block and merges it into the peer tables.
//
// Scoped replacement is the point: a source publishes many blocks, so this
// replaces only the rows for (source, this block's key). ImportBundle's
// whole-source replacement would delete every other block from the same peer,
// which is correct for a bundle and wrong for a block.
func (s *Store) ImportBlock(raw []byte) (*Block, int64, error) {
	b, err := VerifyBlock(raw)
	if err != nil {
		return nil, 0, err
	}
	source := b.PublicKey
	if source == "" {
		source = "unsigned"
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM peer_edges WHERE source = ? AND from_id = ?`, source, b.Key); err != nil {
		return nil, 0, err
	}
	stmt, err := tx.Prepare(`
INSERT INTO peer_edges(source, from_id, to_id, surface, slot_bucket, day_bucket, cohort, count)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(source, from_id, to_id, surface, slot_bucket, day_bucket, cohort)
DO UPDATE SET count = excluded.count`)
	if err != nil {
		return nil, 0, err
	}
	defer stmt.Close()

	var n int64
	for _, e := range b.Edges {
		if e.To == "" || e.Count <= 0 {
			continue // a malformed row is dropped, not fatal — blocks are untrusted
		}
		if _, err := stmt.Exec(source, b.Key, e.To, e.Surface,
			e.SlotBucket, e.DayBucket, e.Cohort, e.Count); err != nil {
			return nil, 0, err
		}
		n++
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return b, n, nil
}

// Encode renders a block for transport.
func (b *Block) Encode() ([]byte, error) { return json.Marshal(b) }
