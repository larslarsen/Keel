// SPDX-License-Identifier: Apache-2.0
// Per-neighbourhood claim identities (WO-084).
//
// # The leak this closes
//
// Broad prefix buckets make one *request* k-anonymous: a peer sees "something
// in bucket a3f", never which neighbourhood. WO-084 makes Level 2 serve its own
// locally derived neighbourhoods inside those buckets, and that reopens the
// question on the response side — because sign.go's install-wide key would put
// the same public key on every block this node produced.
//
// A recipient collecting buckets over time could then group by public key and
// reassemble one publisher's whole contributed graph out of the broad buckets
// that were supposed to keep it apart. The bucket would still hide *which*
// member was wanted; it would no longer hide which members belong together.
// Breadth is only worth what the weakest correlating field allows.
//
// So a locally produced neighbourhood is signed by a key derived for that
// neighbourhood alone. Two of this node's claims carry two unrelated ed25519
// public keys, and nothing on the wire ties either to the install signing key,
// the libp2p peer id, or each other.
//
// # Why derived rather than random, and what stays stable
//
// The identity has to be stable for exactly one thing: replacement. A node that
// re-derives a neighbourhood after seeing more edges must publish a claim that
// *supersedes* its previous one, network-wide, without a central registry —
// otherwise every refresh mints a new source and the same observations
// accumulate forever across the network. Deriving the key from
// (root secret, graph key) gives that for free: the same neighbourhood always
// re-signs under the same identity, so `(claim identity, graph key)`
// replacement works at every holder.
//
// The root secret is separate from `sign_private_key` and from the swarm
// identity, and never leaves the machine. Without it, two claim public keys are
// two independent ed25519 keys — there is no relation to test.
//
// # What this is not
//
// It is not anonymity against a peer that watches the connection. The recipient
// still sees an address and, below Level 4, an ephemeral peer id; deliveries can
// be linked by connection metadata. WO-084 is explicit that broadness does not
// mean zero disclosure. This removes one specific, avoidable join — a durable
// identifier stamped on every block — and claims nothing further.
package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
)

const (
	// metaClaimRoot holds the secret every per-neighbourhood key is derived
	// from. Deliberately not metaSignKey: the install signing key is a
	// pseudonym meant to be seen (bundles, catalogue packs, seed packs), and
	// reusing it here would defeat the whole point of this file.
	metaClaimRoot = "claim_root_secret"

	// claimKeyDomain separates claim-key derivation from every other use of a
	// video id in this package.
	//
	// Not in keyscheme.go, and that is deliberate: the constants there exist so
	// two nodes derive the *same* key from the same input and are pinned by
	// golden vectors for that reason. This derivation is per-install and
	// unlinkable by construction — no peer can or should reproduce it — so it
	// has no cross-node agreement to protect and does not gate on
	// KeySchemeVersion.
	claimKeyDomain = "keel/claim/1/"
)

// claimRoot returns this install's claim-derivation secret, generating one on
// first use.
//
// A corrupt or short value is replaced rather than treated as fatal. The cost
// of rotating is that previously published claims stop being replaceable by
// this node and age out at their holders as ordinary stale claims; the cost of
// refusing to start would be worse.
func (s *Store) claimRoot() ([]byte, error) {
	var enc string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaClaimRoot).Scan(&enc)
	if err == nil && enc != "" {
		if raw, decErr := base64.StdEncoding.DecodeString(enc); decErr == nil && len(raw) == 32 {
			return raw, nil
		}
	}
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaClaimRoot, base64.StdEncoding.EncodeToString(root)); err != nil {
		return nil, err
	}
	return root, nil
}

// claimKey derives the signing key for one neighbourhood.
//
// SHA-512 over (domain, root, graph key), truncated to an ed25519 seed. The
// root is secret, so the mapping is a PRF from a peer's point of view: knowing
// a graph key and a claim public key reveals nothing about any other claim.
func (s *Store) claimKey(graphKey string) (ed25519.PrivateKey, string, error) {
	if graphKey == "" {
		return nil, "", fmt.Errorf("claim key requires a graph key")
	}
	root, err := s.claimRoot()
	if err != nil {
		return nil, "", err
	}
	h := sha512.New()
	h.Write([]byte(claimKeyDomain))
	h.Write(root)
	h.Write([]byte(graphKey))
	priv := ed25519.NewKeyFromSeed(h.Sum(nil)[:ed25519.SeedSize])
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	return priv, pub, nil
}

// claimRevision returns the revision to publish a neighbourhood under, given
// the digest of its current contents.
//
// Unchanged contents keep their revision, which is what makes a rebuilt block
// byte-identical to the one already circulating: identical claims collapse at
// every holder instead of each rebuild looking like news. Changed contents get
// the next revision, which is what makes the new claim win the
// `(claim identity, graph key)` replacement at holders that still have the old
// one — including holders that receive the two out of order.
func (s *Store) claimRevision(graphKey, publicKey, digest string) (int64, error) {
	var rev int64
	var stored string
	err := s.db.QueryRow(
		`SELECT revision, content_sha256 FROM local_claims WHERE graph_key = ?`,
		graphKey).Scan(&rev, &stored)
	if err == nil && stored == digest {
		return rev, nil
	}
	if err == nil {
		rev++
	} else {
		rev = 1
	}
	if _, err := s.db.Exec(`
INSERT INTO local_claims(graph_key, public_key, revision, content_sha256)
VALUES(?,?,?,?)
ON CONFLICT(graph_key) DO UPDATE SET
  public_key = excluded.public_key,
  revision = excluded.revision,
  content_sha256 = excluded.content_sha256`,
		graphKey, publicKey, rev, digest); err != nil {
		return 0, err
	}
	return rev, nil
}

// isOwnClaim reports whether a public key names a claim this node published.
//
// The cycle guard. Claims are re-served unchanged, so a node's own claim comes
// back to it around any relay loop — A→B→A, A→B→C→A — and importing it would
// merge this user's own observations into the peer corpus, double-counting them
// locally and making the node look like two independent sources agreeing.
// Recognising the key is enough: the identity is stable across revisions by
// construction, so this holds for every later version of the same claim too.
func (s *Store) isOwnClaim(publicKey string) (bool, error) {
	if publicKey == "" {
		return false, nil
	}
	var found bool
	err := s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM local_claims WHERE public_key = ?)`, publicKey).Scan(&found)
	return found, err
}
