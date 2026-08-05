// SPDX-License-Identifier: Apache-2.0
// Bundle signing (WO-037).
//
// The digest in bundle.go proves the bytes were not altered. It says nothing
// about who produced them — anyone can recompute a digest over content they
// wrote themselves. Authorship needs a signature, which is what DESIGN_v2 §7.3
// specifies for release manifests (`manifest.sig`, ed25519 over the manifest).
//
// This applies the same posture to exported release bundles, over the same
// canonical bytes bundle.go already hashes, so the two layers compose rather
// than duplicate.
//
// What a signature does and does not establish:
//
//   - It DOES establish that the holder of a private key produced these exact
//     bytes, and that nobody altered them afterwards.
//   - It does NOT establish that the observations are true. Nothing signs
//     `ytInitialData`, so a signer can honestly sign fabricated edges
//     (DESIGN_v2 §6.4). Signing is an integrity and attribution mechanism, not
//     a proof of provenance, and the code must not imply otherwise.
package store

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

const (
	metaSignKey   = "sign_private_key"
	metaSignPub   = "sign_public_key"
	signAlgorithm = "ed25519"
)

// signingKey returns this install's private key, generating one on first use.
//
// The key never leaves the machine and is not derived from anything — it is a
// pseudonym for a node, not an identity for a person. Losing it means future
// bundles are signed by a new key; it is not an account.
func (s *Store) signingKey() (ed25519.PrivateKey, error) {
	var enc string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaSignKey).Scan(&enc)
	if err == nil && enc != "" {
		raw, decErr := base64.StdEncoding.DecodeString(enc)
		if decErr == nil && len(raw) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(raw), nil
		}
		// A corrupt key is replaced rather than fatal: signing is additive, and
		// refusing to start because of it would be worse than rotating.
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaSignKey, base64.StdEncoding.EncodeToString(priv)); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaSignPub, base64.StdEncoding.EncodeToString(pub)); err != nil {
		return nil, err
	}
	return priv, nil
}

// PublicKey returns this install's base64 verifying key.
func (s *Store) PublicKey() (string, error) {
	if _, err := s.signingKey(); err != nil {
		return "", err
	}
	var pub string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaSignPub).Scan(&pub); err != nil {
		return "", err
	}
	return pub, nil
}

// signPayload signs the canonical bundle bytes.
func (s *Store) signPayload(payload []byte) (sig string, pub string, err error) {
	priv, err := s.signingKey()
	if err != nil {
		return "", "", err
	}
	pub, err = s.PublicKey()
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)), pub, nil
}

// verifyPayload checks a signature against the public key carried in a bundle.
//
// The key travels with the bundle, so this proves self-consistency — these
// bytes were signed by whoever holds that key — not that the key belongs to
// anyone in particular. Binding a key to a person is a trust decision the user
// makes out of band, exactly as with an SSH fingerprint.
func verifyPayload(payload []byte, sigB64, pubB64 string) error {
	if sigB64 == "" || pubB64 == "" {
		return fmt.Errorf("bundle is unsigned")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return fmt.Errorf("public key is not valid base64: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		return fmt.Errorf("signature does not match the bundle contents")
	}
	return nil
}
