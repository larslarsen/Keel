// SPDX-License-Identifier: Apache-2.0
// Graph blocks (WO-052, WO-084, DESIGN_BOOTSTRAP §5d).
//
// §5d's structural claim is that a random walk needs neighbourhoods, not the
// graph: each hop only needs the edges out of the video it is standing on. So
// the graph is cut into blocks keyed by `context_video_id`, and a node holds the
// blocks it has actually used rather than the whole dataset. At full scale the
// deduped graph is ~2–35 TB; one user touches tens of MB of it.
//
// A block is therefore the unit of everything downstream — fetch-on-demand, the
// LRU cache the disk slider sizes, and the background prewarm that fires when a
// watch page loads. This file builds, signs, verifies and preserves them. It
// does no networking: the transport is a separate decision (WO-052), and
// defining the block first is what keeps that decision cheap to change.
//
// # A block is a claim, and WO-084 made that literal
//
// Schema 2 treated a block as a *view*: a node aggregated whatever edges it had
// for a video — its own and every peer's, summed together — and signed the
// total with its install key. Two things followed, and both were wrong.
//
// Counts grew on relay. A→B→C→A returned to A as a sum of sums, indistinguishable
// from independent corroboration, so a claim making a loop came back larger than
// it left. And every block a node published carried the same public key, which
// is a durable join across the broad buckets that were meant to keep one
// publisher's neighbourhoods apart (claim.go).
//
// Schema 3 makes a block one publisher's signed statement about one
// neighbourhood, at one revision, under an identity derived for that
// neighbourhood alone. Holding a claim means holding those bytes; serving it
// means handing them on unchanged. Nothing is re-aggregated and nothing is
// re-signed, so a bucket honestly contains several independent claims about the
// same graph key rather than one node's opinion of their sum — and a cycle
// re-delivers a claim this node already has, which replaces itself.
//
// Blocks reuse the bundle layer's canonical-bytes discipline rather than
// inventing a second signing story, so the guarantees are the ones sign.go
// already documents — integrity and attribution, never proof that an
// observation is true (DESIGN_v2 §6.4).
package store

import (
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// blockSchemaVersion is bumped when the wire shape changes incompatibly.
//
//	1 — original.
//	2 — removed the embedded catalogue.
//	3 — WO-084: preserved claims. A block carries a revision, is signed by a
//	    per-neighbourhood claim key rather than the install key, and covers its
//	    key, cohort and revision in the signature. Unsigned is no longer
//	    accepted at this version: a schema-3 block with no claim identity has
//	    nothing to replace or deduplicate against.
const blockSchemaVersion = 3

// UnsignedClaim is the claim identity a legacy unsigned block is filed under.
//
// Every unsigned block for one graph key shares it, which is the honest
// consequence of having no identity: they cannot be told apart, so they cannot
// accumulate. Schema 3 rejects unsigned blocks outright; this exists for
// schema ≤2 material already on disk.
const UnsignedClaim = "unsigned"

// ErrOwnClaim is returned by ImportBlock for a claim this node published.
//
// Not a failure — it is the relay cycle working correctly. Callers skip the
// block and carry on; treating it as an error to log would make a healthy
// three-node loop look broken.
var ErrOwnClaim = errors.New("block is this node's own claim")

// Block is neighbours(v): every edge one publisher observed leading out of one
// video, aggregated.
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
//
// What a block may contain is also a privacy boundary, not just a schema
// (WO-084). Edges carry (from, to, surface, slot_bucket, day_bucket, cohort,
// count) and nothing else: no page-load id, no raw observation timestamp, no
// title, no query, no per-impression row, no ordered watch trail. That list is
// what makes a locally derived block servable at Level 2 at all, and
// TestLevelTwoGraphPayloadCarriesOnlyAggregatedEdges holds it.
type Block struct {
	SchemaVersion int    `json:"schema_version"`
	Key           string `json:"key"` // the context video these edges lead out of
	Cohort        string `json:"cohort"`

	// Revision orders successive versions of one claim.
	//
	// It exists so replacement survives out-of-order delivery: a holder that
	// already has revision 4 must not be talked back to revision 3 by a peer
	// re-serving a copy it cached earlier. Zero on schema ≤2 blocks, which
	// therefore replace each other freely — the old scoped-replacement rule.
	Revision int64 `json:"revision,omitempty"`

	Edges []bridge.EdgeObservation `json:"edges"`

	ContentSHA256 string `json:"content_sha256"`
	Signature     string `json:"signature,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	Algorithm     string `json:"signature_alg,omitempty"`
}

// ClaimID is the identity a block is filed and replaced under.
//
// At schema 3 this is a per-neighbourhood claim key (claim.go) and reveals
// nothing about its publisher's other blocks. At schema ≤2 it is whatever
// install key signed the block, or UnsignedClaim.
func (b *Block) ClaimID() string {
	if b.PublicKey == "" {
		return UnsignedClaim
	}
	return b.PublicKey
}

// claimBody is the exact structure a schema-3 block's digest and signature are
// computed over.
//
// Key and Cohort are inside it, unlike schema 2, where only the edges were
// covered: a relay could re-label a block's cohort without breaking anything.
// Revision is inside the signature but not the digest — see claimDigest.
type claimBody struct {
	Key      string                   `json:"key"`
	Cohort   string                   `json:"cohort"`
	Revision int64                    `json:"revision"`
	Edges    []bridge.EdgeObservation `json:"edges"`
}

// claimDigest identifies a neighbourhood's *contents*, ignoring which revision
// they were published as.
//
// That omission is deliberate and load-bearing: claimRevision compares this
// digest against the last published one to decide whether anything actually
// changed. Including the revision would make every rebuild differ from itself
// and mint a new revision on every announce, so an idle node would publish an
// endless stream of "new" claims that are all the same data.
func claimDigest(key, cohort string, edges []bridge.EdgeObservation) (string, error) {
	raw, err := json.Marshal(claimBody{Key: key, Cohort: cohort, Edges: edges})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// claimPayload is what the signature covers: contents *and* revision, so a
// revision cannot be edited in flight to win a replacement it did not earn.
func claimPayload(key, cohort string, revision int64, edges []bridge.EdgeObservation) ([]byte, error) {
	return json.Marshal(claimBody{Key: key, Cohort: cohort, Revision: revision, Edges: edges})
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
	sortEdges(out)
	return out, nil
}

func sortEdges(out []bridge.EdgeObservation) {
	sort.Slice(out, func(a, b int) bool {
		if out[a].To != out[b].To {
			return out[a].To < out[b].To
		}
		if out[a].DayBucket != out[b].DayBucket {
			return out[a].DayBucket < out[b].DayBucket
		}
		return out[a].SlotBucket < out[b].SlotBucket
	})
}

// BuildBlock assembles this node's own claim for one neighbourhood.
//
// The result is the *only* thing this node ever signs about a graph key. It is
// built from `impressions` alone: claims held for peers are re-served as they
// arrived (PeerClaimsForKeys) and are never folded in here, because folding
// them in is exactly what made counts grow around a relay loop.
//
// Serving one is Level 2's contribution (Policy.IncludeLocalGraph). What leaves
// is aggregated edge counts inside a bucket of many neighbourhoods — not a
// funnel, not an ordered history, and not a selected video among decoys.
//
// An empty block is returned rather than an error when the node has seen
// nothing from that video — "I have no edges here" is a valid answer to serve,
// and BlocksInPrefix drops it rather than spending bytes on it.
func (s *Store) BuildBlock(videoID, cohort string) (*Block, error) {
	if videoID == "" {
		return nil, fmt.Errorf("block key required")
	}
	edges, err := s.blockEdges(videoID, cohort)
	if err != nil {
		return nil, err
	}

	b := &Block{
		SchemaVersion: blockSchemaVersion,
		Key:           videoID,
		Cohort:        cohort,
		Edges:         edges,
	}
	if b.ContentSHA256, err = claimDigest(videoID, cohort, edges); err != nil {
		return nil, err
	}
	priv, pub, err := s.claimKey(videoID)
	if err != nil {
		return nil, err
	}
	if b.Revision, err = s.claimRevision(videoID, pub, b.ContentSHA256); err != nil {
		return nil, err
	}
	payload, err := claimPayload(videoID, cohort, b.Revision, edges)
	if err != nil {
		return nil, err
	}
	b.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	b.PublicKey = pub
	b.Algorithm = signAlgorithm
	return b, nil
}

// LocalGraphKeys lists the neighbourhoods this node can build a claim for: the
// context videos in its own `impressions`.
//
// Advertising this set is a real disclosure and WO-084 accepts it knowingly: at
// bucket granularity. LocalPrefixes hashes each key into a 12-bit bucket shared
// by thousands of videos, so what reaches the DHT is "this node holds something
// in a3f", never a video id. The keys themselves must never be announced —
// that would be a viewing history — which is why nothing outside this package
// takes this list to the network.
func (s *Store) LocalGraphKeys() ([]string, error) {
	return s.queryKeys(`
SELECT DISTINCT COALESCE(NULLIF(context_video_id, ''), ?) FROM impressions`, HomeFrom)
}

// PeerGraphKeys lists the neighbourhoods this node holds claims for on behalf
// of other people.
//
// Read from peer_blocks, not peer_edges, and that difference is the point
// (WO-084 requirement 4): peer_blocks is what the serving path can actually
// return. Rows imported before this migration exist only in peer_edges, where
// they still feed the local graph walk, and are deliberately not advertised —
// the claims that would have to be re-served were never kept, so announcing
// them would advertise material the stream must refuse.
func (s *Store) PeerGraphKeys() ([]string, error) {
	return s.queryKeys(`SELECT DISTINCT graph_key FROM peer_blocks`)
}

func (s *Store) queryKeys(q string, args ...any) ([]string, error) {
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

// LocalBlockKeys is the union of the neighbourhoods `sources` selects.
//
// It is what LocalPrefixes advertises and what BlocksInPrefix serves, and both
// must be computed from the same call so a provider record can never name a
// bucket the stream would answer empty.
func (s *Store) LocalBlockKeys(sources SourceSet) ([]string, error) {
	seen := map[string]bool{}
	if sources.Local {
		keys, err := s.LocalGraphKeys()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			seen[k] = true
		}
	}
	if sources.Peers {
		keys, err := s.PeerGraphKeys()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// PeerClaimsForKeys returns the claims this node holds for a set of graph keys,
// exactly as they were received.
//
// "Exactly" is the contract. The rows are re-decoded from the bytes that
// verified and handed on untouched, so a recipient checks the original
// publisher's signature over the original publisher's counts. This node's
// opinion never enters, which is what makes repeated relay a no-op instead of
// an amplifier.
func (s *Store) PeerClaimsForKeys(keys []string) ([]Block, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := []Block{}
	const chunk = 400
	for start := 0; start < len(keys); start += chunk {
		end := start + chunk
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[start:end]
		args := make([]any, len(batch))
		ph := make([]byte, 0, len(batch)*2)
		for i, k := range batch {
			args[i] = k
			if i > 0 {
				ph = append(ph, ',')
			}
			ph = append(ph, '?')
		}
		rows, err := s.db.Query(
			`SELECT block_json FROM peer_blocks WHERE graph_key IN (`+string(ph)+`)
			 ORDER BY graph_key, claim_id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			var b Block
			if err := json.Unmarshal(raw, &b); err != nil {
				continue // a row that no longer parses is dropped, never repaired
			}
			out = append(out, b)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// VerifyBlock parses and checks a block received from anywhere.
//
// Both checks matter and they answer different questions: the digest says the
// bytes were not altered, the signature says which claim produced them. Neither
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

	if b.SchemaVersion < blockSchemaVersion {
		return verifyLegacyBlock(&b)
	}

	// Unsigned is refused from here on. A claim with no identity cannot be
	// replaced by its own later revision and cannot be told apart from anyone
	// else's claim about the same key, so accepting one would reintroduce the
	// accumulation schema 3 exists to stop.
	if b.Signature == "" || b.PublicKey == "" {
		return nil, fmt.Errorf("block %q carries no claim identity", b.Key)
	}
	digest, err := claimDigest(b.Key, b.Cohort, b.Edges)
	if err != nil {
		return nil, err
	}
	if digest != b.ContentSHA256 {
		return nil, fmt.Errorf("block contents do not match its digest")
	}
	payload, err := claimPayload(b.Key, b.Cohort, b.Revision, b.Edges)
	if err != nil {
		return nil, err
	}
	if err := verifyPayload(payload, b.Signature, b.PublicKey); err != nil {
		return nil, err
	}
	return &b, nil
}

// verifyLegacyBlock checks a schema ≤2 block against the rules it was written
// under: digest and signature over the edges alone, and unsigned allowed.
//
// Kept because such blocks exist on disk and inside already-distributed seed
// packs. They do not arrive over the network any more — BlockProtocol moved to
// 3.0.0, so a schema-2 peer never opens a stream to this build.
func verifyLegacyBlock(b *Block) (*Block, error) {
	digest, err := contentDigest(nil, b.Edges)
	if err != nil {
		return nil, err
	}
	if digest != b.ContentSHA256 {
		return nil, fmt.Errorf("block contents do not match its digest")
	}
	if b.Signature != "" || b.PublicKey != "" {
		payload, err := canonicalPayload(nil, b.Edges)
		if err != nil {
			return nil, err
		}
		if err := verifyPayload(payload, b.Signature, b.PublicKey); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// ImportBlock verifies a claim and stores it whole.
//
// Two tables, two jobs. peer_blocks keeps the claim as it arrived so it can be
// re-served unchanged. peer_edges keeps the flattened rows the local graph walk
// reads, scoped to (claim, key) so a replacement supersedes its own prior
// version and nobody else's.
//
// Returns ErrOwnClaim, having written nothing, for a claim this node published.
// That is the relay cycle closing: without the check, A→B→C→A would merge this
// user's own observations back in as a peer's, double-counting them locally and
// making one publisher look like two sources agreeing.
func (s *Store) ImportBlock(raw []byte) (*Block, int64, error) {
	b, err := VerifyBlock(raw)
	if err != nil {
		return nil, 0, err
	}
	claim := b.ClaimID()
	if own, err := s.isOwnClaim(b.PublicKey); err != nil {
		return nil, 0, err
	} else if own {
		return b, 0, ErrOwnClaim
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// An older revision of a claim we already hold is discarded outright. It is
	// the normal shape of a cycle: a peer re-serving what it cached before our
	// update reached it must not roll us back.
	var held int64
	err = tx.QueryRow(
		`SELECT revision FROM peer_blocks WHERE claim_id = ? AND graph_key = ?`,
		claim, b.Key).Scan(&held)
	switch {
	case err == nil && b.Revision < held:
		return b, 0, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return nil, 0, err
	}

	encoded, err := b.Encode()
	if err != nil {
		return nil, 0, err
	}
	if _, err := tx.Exec(`
INSERT INTO peer_blocks(claim_id, graph_key, revision, block_json, updated_at)
VALUES(?,?,?,?,?)
ON CONFLICT(claim_id, graph_key) DO UPDATE SET
  revision = excluded.revision,
  block_json = excluded.block_json,
  updated_at = excluded.updated_at`,
		claim, b.Key, b.Revision, encoded, time.Now().UnixMilli()); err != nil {
		return nil, 0, err
	}

	if _, err := tx.Exec(
		`DELETE FROM peer_edges WHERE source = ? AND from_id = ?`, claim, b.Key); err != nil {
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
		if _, err := stmt.Exec(claim, b.Key, e.To, e.Surface,
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
