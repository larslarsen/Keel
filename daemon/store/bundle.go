// SPDX-License-Identifier: Apache-2.0
// Bundle serialisation (WO-028): writing measurements to a file.
//
// A bundle is the aggregate layer on disk — nothing more. The same bytes are
// what a STAR submission aggregates over and what a published dataset
// contains, so this is a transport detail rather than a separate data path.
//
// Person-to-person bundle exchange is rejected (WO-047): importing a bundle
// means trusting the sender, and a signature proves who wrote the bytes, never
// that the observations happened (DESIGN_v2 §6.4). There is therefore no path
// that reads another person's bundle. Export remains — producing the aggregate
// is how a node contributes — and the digest and signature machinery below is
// the verification seam a published release (DESIGN_v2 §7.3) will be consumed
// through.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// BundleSchemaVersion is the on-disk bundle format.
const BundleSchemaVersion = 1

// Bundle is one node's aggregated corpus.
//
// Deliberately absent: page_load_id, observed_at, anything per-session. Those
// are what turn a set of counts back into a browsing timeline.
type Bundle struct {
	SchemaVersion int    `json:"schema_version"`
	NodeID        string `json:"node_id"`
	CreatedDay    string `json:"created_day"` // UTC date, not a timestamp
	Cohort        string `json:"cohort"`
	// ContentSHA256 is over the canonical payload — catalogue and edges only.
	// A bundle is identified by what it contains, not by where it was fetched
	// from, which is what makes the transport interchangeable: HTTPS, IPFS and
	// BitTorrent all become ways to obtain the same verifiable bytes
	// (DESIGN_v2 §7.3 uses the same posture for release manifests).
	//
	// Header fields are excluded so the digest is stable across re-exports of
	// an unchanged corpus — only the data is hashed, not when it was written.
	ContentSHA256 string `json:"content_sha256"`
	// Signature and PublicKey establish authorship over the same canonical
	// bytes the digest covers (WO-037). Optional: an unsigned bundle still
	// verifies, because refusing one would break every bundle written before
	// signing existed.
	Signature string                   `json:"signature,omitempty"`
	PublicKey string                   `json:"public_key,omitempty"`
	Algorithm string                   `json:"signature_alg,omitempty"`
	Catalogue []bridge.CatalogueEntry  `json:"catalogue"`
	Edges     []bridge.EdgeObservation `json:"edges"`
}

// canonicalPayload is the exact byte sequence the digest covers.
//
// EdgeObservations and CatalogueEntries are already emitted in a deterministic
// order, so the same corpus produces the same bytes on any machine — which is
// the property a hash is worthless without.
func canonicalPayload(cat []bridge.CatalogueEntry, edges []bridge.EdgeObservation) ([]byte, error) {
	return json.Marshal(struct {
		Catalogue []bridge.CatalogueEntry  `json:"catalogue"`
		Edges     []bridge.EdgeObservation `json:"edges"`
	}{cat, edges})
}

// contentDigest returns the hex sha256 of the canonical payload.
func contentDigest(cat []bridge.CatalogueEntry, edges []bridge.EdgeObservation) (string, error) {
	b, err := canonicalPayload(cat, edges)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// NodeID returns this install's identifier, creating one on first use.
//
// Random and local. It identifies an exported aggregate for replacement and
// verification, not a person — nothing derives it from the machine or user.
func (s *Store) NodeID() (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'node_id'`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id = "node-" + hex.EncodeToString(b)
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES('node_id', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, id); err != nil {
		return "", err
	}
	return id, nil
}

// ExportBundle writes the aggregate layer to path.
func (s *Store) ExportBundle(path, cohort string) (*bridge.BundleResultPayload, error) {
	nodeID, err := s.NodeID()
	if err != nil {
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
	payload, err := canonicalPayload(cat, edges)
	if err != nil {
		return nil, err
	}
	digest, err := contentDigest(cat, edges)
	if err != nil {
		return nil, err
	}
	sig, pub, err := s.signPayload(payload)
	if err != nil {
		return nil, err
	}
	b := Bundle{
		SchemaVersion: BundleSchemaVersion,
		NodeID:        nodeID,
		CreatedDay:    time.Now().UTC().Format("2006-01-02"),
		Cohort:        cohort,
		ContentSHA256: digest,
		Signature:     sig,
		PublicKey:     pub,
		Algorithm:     signAlgorithm,
		Catalogue:     cat,
		Edges:         edges,
	}
	// Indented: a bundle is something a person may reasonably want to read
	// before handing it to someone else.
	data, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	return &bridge.BundleResultPayload{
		Path:      path,
		NodeID:    nodeID,
		Edges:     int64(len(edges)),
		Catalogue: int64(len(cat)),
		Bytes:     int64(len(data) + 1),
	}, nil
}

// verifyBundle validates a bundle's structure, digest and signature.
//
// Person-to-person import is gone (WO-047); this is the verification seam a
// published release (DESIGN_v2 §7.3) will be consumed through — the same
// checks an import used to run, before anything is merged under its node ID.
// It writes nothing.
func (s *Store) verifyBundle(data []byte) (*Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("not a Keel bundle: %w", err)
	}
	if b.SchemaVersion != BundleSchemaVersion {
		return nil, fmt.Errorf("bundle schema %d, this daemon reads %d", b.SchemaVersion, BundleSchemaVersion)
	}
	if b.NodeID == "" {
		return nil, fmt.Errorf("bundle has no node_id")
	}
	mine, err := s.NodeID()
	if err != nil {
		return nil, err
	}
	if b.NodeID == mine {
		return nil, fmt.Errorf("that bundle came from this install")
	}
	// A digest mismatch means the bytes are not the ones their author produced
	// — the first line of integrity defence for a release that crossed
	// untrusted ground.
	if b.ContentSHA256 != "" {
		got, err := contentDigest(b.Catalogue, b.Edges)
		if err != nil {
			return nil, err
		}
		if got != b.ContentSHA256 {
			return nil, fmt.Errorf("bundle content does not match its digest (corrupt or altered)")
		}
	}
	// Signature is checked when present. A bundle that carries one and fails is
	// refused; one that carries none is accepted, since bundles predate signing.
	if b.Signature != "" || b.PublicKey != "" {
		payload, err := canonicalPayload(b.Catalogue, b.Edges)
		if err != nil {
			return nil, err
		}
		if err := verifyPayload(payload, b.Signature, b.PublicKey); err != nil {
			return nil, fmt.Errorf("bundle signature: %w", err)
		}
	}
	return &b, nil
}
