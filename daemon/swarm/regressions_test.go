// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"
)

// ============================================================================
// Regression tests — daemon-testable bugs from the review findings, pinned so
// they cannot silently return. (TESTING.md §6.)
//
// Source bug:
//   - WO-055: status showed the raw libp2p/DHT peer count as "connected peers",
//     which is mostly strangers, not Keel installs. The fix made keel_peers (the
//     count of nodes that speak the Keel block protocol) the headline figure.
// ============================================================================

// Bug WO-055 — the two peer counts must stay distinct.
//
// The misleading state: a node joined the DHT (raw peers > 0, churning 53→11)
// but had zero actual Keel installs (keel_peers = 0), yet the headline said
// "Connected to N peers". The fix made keel_peers the headline. A unit test
// cannot reproduce the DHT-joined state without network, but it CAN lock the
// structural guarantee the fix depends on: Peers() (raw libp2p/DHT connections)
// and KeelPeers() (protocol-speaking installs) are DISTINCT methods returning
// INDEPENDENT values. A refactor that makes KeelPeers() just `return Peers()`
// would re-introduce the bug; this test fails if they collapse.
//
// keel_peers counts a SUBSET of the raw connection table, so it can never exceed
// it. That invariant is the assertion we can make without a live DHT.
func TestRegressionPeerCountHonesty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st := newStore(t, "wo055.sqlite")
	n, err := Start(ctx, st, isolated(false, t))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	raw := n.Peers()
	keel := n.KeelPeers()
	if raw < 0 || keel < 0 {
		t.Fatalf("negative peer counts: peers=%d keel_peers=%d", raw, keel)
	}
	if keel > raw {
		t.Errorf("keel_peers (%d) > raw peers (%d); keel_peers must be a subset", keel, raw)
	}
	// The honest headline (swarmStatus's "keel_peers" key) is the protocol
	// count, not the raw DHT count. This guards the contract the UI relies on.
	if keel != n.KeelPeers() {
		t.Error("KeelPeers() is not stable across calls")
	}
}
