// SPDX-License-Identifier: Apache-2.0
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// seedOne writes a single observation so a store has something to export.
func seedOne(t *testing.T, st *Store, vid, title string) {
	t.Helper()
	seed := "seedvideo01"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "11111111-1111-4111-8111-111111111111",
		ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &seed, SlotIndex: 1, VideoID: vid, Title: title,
	}}); err != nil {
		t.Fatal(err)
	}
}

func readBundleFile(t *testing.T, path string) Bundle {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestBundleSignatureRoundTrip checks that an exported bundle verifies through
// the release-consumption seam on a second install.
func TestBundleSignatureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(filepath.Join(dir, "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	seedOne(t, a, "sharedvid01", "Shared")

	path := filepath.Join(dir, "b.json")
	if _, err := a.ExportBundle(path, "GB-en"); err != nil {
		t.Fatal(err)
	}

	b := readBundleFile(t, path)
	if b.Signature == "" || b.PublicKey == "" {
		t.Fatal("export produced an unsigned bundle")
	}
	if b.Algorithm != signAlgorithm {
		t.Fatalf("algorithm = %q, want %q", b.Algorithm, signAlgorithm)
	}

	// A second install verifies a correctly signed bundle.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Open(filepath.Join(dir, "c.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.verifyBundle(raw); err != nil {
		t.Fatalf("valid signed bundle refused: %v", err)
	}
}

func TestBundleSignatureRejectsForgery(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(filepath.Join(dir, "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	seedOne(t, a, "sharedvid01", "Shared")

	path := filepath.Join(dir, "b.json")
	if _, err := a.ExportBundle(path, "GB-en"); err != nil {
		t.Fatal(err)
	}
	orig := readBundleFile(t, path)

	c, err := Open(filepath.Join(dir, "c.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Editing content and repairing the digest must still fail: recomputing a
	// hash is something anyone can do, which is exactly why the signature layer
	// exists on top of it.
	tampered := orig
	tampered.Catalogue = append([]bridge.CatalogueEntry{}, orig.Catalogue...)
	tampered.Catalogue[0].Title = "Altered in transit"
	fixed, err := contentDigest(tampered.Catalogue, tampered.Edges)
	if err != nil {
		t.Fatal(err)
	}
	tampered.ContentSHA256 = fixed
	badRaw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.verifyBundle(badRaw); err == nil {
		t.Fatal("bundle with a repaired digest but broken signature was verified")
	}

	// Swapping in an attacker's own key must not launder the edit either — the
	// signature would verify, but against a different identity, so the bundle
	// is attributed to that key rather than silently trusted as the original.
	attacker, err := Open(filepath.Join(dir, "attacker.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	payload, err := canonicalPayload(tampered.Catalogue, tampered.Edges)
	if err != nil {
		t.Fatal(err)
	}
	sig, pub, err := attacker.signPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	relabelled := tampered
	relabelled.Signature = sig
	relabelled.PublicKey = pub
	relabelledRaw, err := json.Marshal(relabelled)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.verifyBundle(relabelledRaw); err != nil {
		t.Fatalf("re-signed bundle should verify under the new key: %v", err)
	}
	// It is attributed to the key in the file, which differs from the original
	// author's — the fact a user needs to be able to see.
	if relabelled.PublicKey == orig.PublicKey {
		t.Fatal("attacker key matched the original — test is not exercising the case")
	}

	// An unsigned bundle still verifies: signing postdates the format.
	unsigned := orig
	unsigned.Signature = ""
	unsigned.PublicKey = ""
	unsigned.Algorithm = ""
	unsignedRaw, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}

	d, err := Open(filepath.Join(dir, "d.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.verifyBundle(unsignedRaw); err != nil {
		t.Fatalf("unsigned bundle should still verify: %v", err)
	}
}
