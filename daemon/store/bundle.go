// SPDX-License-Identifier: Apache-2.0
// Bundle serialisation (WO-028): writing measurements to a file and reading
// one back.
//
// A bundle is the aggregate layer on disk — nothing more. The same bytes are
// what a STAR submission aggregates over and what a published dataset
// contains, so this is a transport detail rather than a separate data path.
//
// It is **attributable**. A bundle says which videos YouTube recommended to
// the person who made it. That is DESIGN_v2 §6's L3 posture, and it is only
// acceptable as an informed choice — the caller must surface that, not bury it.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// MaxBundleBytes caps what will be read from a URL. Bundles are ~236 KB at
// 1,100 observations; 64 MiB is far past any honest one and stops a hostile
// URL from filling the disk.
const MaxBundleBytes = 64 << 20

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
	// imports, because refusing one would break every bundle written before
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
// Random and local. It exists so imports can be attributed and replaced, not
// to assert an identity — nothing derives it from the machine or the user.
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

// ImportBundle reads a bundle and merges it under its node ID.
//
// The file is untrusted: a bundle can be edited by anyone who handles it, and
// nothing signs it. Structure is validated; the contents are not verifiable and
// this does not pretend otherwise (DESIGN_v2 §6.4).
func (s *Store) ImportBundle(path string) (*bridge.BundleResultPayload, error) {
	data, err := readBundle(path)
	if err != nil {
		return nil, err
	}
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
	// Verify before merging. A bundle crosses untrusted ground — a web host, a
	// USB stick, a chat app — and a digest mismatch means the bytes are not the
	// ones its author produced. It does not prove who wrote it; that needs the
	// signature layer §7.3 specifies.
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
	edges, cat, err := s.ImportEdges(b.NodeID, b.Edges, b.Catalogue)
	if err != nil {
		return nil, err
	}
	return &bridge.BundleResultPayload{
		Path:      path,
		NodeID:    b.NodeID,
		Edges:     edges,
		Catalogue: cat,
		Bytes:     int64(len(data)),
	}, nil
}

// readBundle reads a bundle from a local path or an http(s) URL.
//
// URL support is what makes DESIGN_v2 §7.3's model work in practice: every
// channel it names — Zenodo, GitHub Releases, Internet Archive — is an ordinary
// HTTPS download, reachable from behind any firewall. No peer-to-peer
// connection is established, so none has to be negotiated.
//
// This is the daemon's only outbound request, and it happens only when a person
// explicitly asks to import something.
func readBundle(src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(src)
	if err != nil {
		return nil, fmt.Errorf("fetch bundle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch bundle: %s", resp.Status)
	}
	// LimitReader rather than trusting Content-Length, which a server controls.
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxBundleBytes {
		return nil, fmt.Errorf("bundle larger than %d bytes", MaxBundleBytes)
	}
	return data, nil
}
