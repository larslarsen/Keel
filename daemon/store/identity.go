// SPDX-License-Identifier: Apache-2.0
// Swarm identity (WO-052).
//
// This is deliberately NOT the signing key from sign.go, and the separation is
// load-bearing.
//
// The signing key attributes published content: it says "this block came from
// the holder of key K". The swarm key is a network address: it is visible to
// every peer that ever connects, and it is what a passive observer of the DHT
// sees. If they were the same key, anyone who watched the network could link a
// node's IP address to everything it has ever published — which would hand out,
// for free, exactly the correlation the contribution levels exist to prevent.
//
// Keeping them apart means observing the network tells you a node exists, and
// observing published blocks tells you what a key published, and neither tells
// you they are the same machine.
package store

import (
	"crypto/ed25519"
	"encoding/base64"
)

const metaSwarmKey = "swarm_private_key"

// SwarmIdentity returns this install's libp2p private key, generating one on
// first use. Ed25519 because libp2p supports it natively and the key is small
// enough to store beside everything else.
//
// Callers get raw key bytes; the swarm package turns them into a libp2p
// identity. The key never leaves the machine.
func (s *Store) SwarmIdentity() ([]byte, error) {
	var enc string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaSwarmKey).Scan(&enc)
	if err == nil && enc != "" {
		raw, decErr := base64.StdEncoding.DecodeString(enc)
		if decErr == nil && len(raw) == ed25519.PrivateKeySize {
			return raw, nil
		}
		// A corrupt key is replaced rather than fatal. Rotating a network
		// identity costs a node its reputation with peers, which is nothing
		// here — there is no reputation system — and refusing to start would
		// be worse.
	}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaSwarmKey, base64.StdEncoding.EncodeToString(priv)); err != nil {
		return nil, err
	}
	return priv, nil
}

// ResetSwarmIdentity discards the network identity so the next start appears as
// a new node. This is the network-level counterpart to Wipe: a user who has
// stopped contributing should not remain linkable to what they served before.
func (s *Store) ResetSwarmIdentity() error {
	_, err := s.db.Exec(`DELETE FROM meta WHERE key = ?`, metaSwarmKey)
	return err
}
