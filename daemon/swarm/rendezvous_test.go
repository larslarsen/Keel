// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"strings"
	"testing"
)

// TestRendezvousCIDIsAConstantOfTheSoftware.
//
// The key must be identical on every install — that is what makes it a meeting
// point — and must contain nothing about the person running it. A key derived
// from anything local would give every node its own rendezvous, which is the
// same as having none.
func TestRendezvousCIDIsAConstantOfTheSoftware(t *testing.T) {
	a, err := RendezvousCID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := RendezvousCID()
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equals(b) {
		t.Fatalf("not stable: %s then %s", a, b)
	}
	if a.String() == "" {
		t.Fatal("empty CID")
	}
	// It is derived from the protocol identity, so a node speaking a different
	// revision lands on a different key and the two never meet — which is
	// correct: they could not talk anyway (WO-060).
	if !strings.Contains(rendezvousDomain, "keel/rendezvous") {
		t.Errorf("unexpected domain %q", rendezvousDomain)
	}
}

// TestRendezvousDiffersFromContentKeys: it must not collide with any bucket, or
// looking for nodes would return whoever holds one specific slice of data.
func TestRendezvousDiffersFromContentKeys(t *testing.T) {
	r, err := RendezvousCID()
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "0", "0a3f", "ffff"} {
		c, err := prefixCID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if c.Equals(r) {
			t.Errorf("rendezvous key collides with the bucket key for %q", prefix)
		}
	}
	for _, shard := range []int{0, 1, 255} {
		c, err := shardCID(shard)
		if err != nil {
			t.Fatal(err)
		}
		if c.Equals(r) {
			t.Errorf("rendezvous key collides with shard %d", shard)
		}
	}
}
