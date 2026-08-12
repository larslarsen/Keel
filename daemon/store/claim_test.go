// SPDX-License-Identifier: Apache-2.0
package store

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestLevelTwoGraphPayloadCarriesOnlyAggregatedEdges is the allow-list WO-084
// puts on what a locally derived block may contain.
//
// It is written against the encoded bytes rather than the struct on purpose.
// The struct's fields are reviewable; what actually leaves the machine is JSON,
// and a field added to bridge.EdgeObservation for some local purpose would ride
// along silently. This is the same posture WO-078 took for the live record.
func TestLevelTwoGraphPayloadCarriesOnlyAggregatedEdges(t *testing.T) {
	st := openStore(t, "payload.sqlite")
	seedEdge(t, st, "watchedvid1", "targetaaaa1", 0)
	seedEdge(t, st, "watchedvid1", "targetaaaa2", 4)

	blk, err := st.BuildBlock("watchedvid1", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	allowedBlock := map[string]bool{
		"schema_version": true, "key": true, "cohort": true, "revision": true,
		"edges": true, "content_sha256": true,
		"signature": true, "public_key": true, "signature_alg": true,
	}
	for f := range envelope {
		if !allowedBlock[f] {
			t.Errorf("block carries unexpected field %q: %s", f, raw)
		}
	}

	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["edges"], &edges); err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 {
		t.Fatalf("block has %d edges, want 2", len(edges))
	}
	// The aggregated edge shape, and nothing else. Every name deliberately
	// absent from this set is a thing WO-084 says must not leave: a page-load
	// id, a raw observation timestamp, a title, a query, a slot index that has
	// not been bucketed, or any field that would give the edges an order.
	allowedEdge := map[string]bool{
		"from": true, "to": true, "surface": true,
		"slot_bucket": true, "day_bucket": true, "cohort": true, "count": true,
	}
	for _, e := range edges {
		for f := range e {
			if !allowedEdge[f] {
				t.Errorf("edge carries unexpected field %q: %s", f, raw)
			}
		}
	}

	// Belt and braces against a rename: none of these substrings may appear
	// anywhere in the payload, whatever a future field is called.
	for _, banned := range []string{
		"page_load", "pageLoad", "observed_at", "observedAt",
		"title", "Title ", "query", "trail", "history", "watched_at",
	} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("block payload contains %q: %s", banned, raw)
		}
	}
}

// TestClaimIdentitiesAreUnlinkable is the response-side half of broadness.
//
// A bucket that hides which neighbourhood was wanted is worth nothing if every
// block in it carries one durable publisher key: a recipient just groups by
// that key and rebuilds the publisher's whole contributed graph out of the
// buckets meant to keep it apart. So two claims from one node must look like
// two unrelated publishers, and neither may be the install signing key or the
// libp2p identity.
func TestClaimIdentitiesAreUnlinkable(t *testing.T) {
	st := openStore(t, "claims.sqlite")
	seedEdge(t, st, "watchedvid1", "targetaaaa1", 0)
	seedEdge(t, st, "watchedvid2", "targetbbbb1", 0)

	one, err := st.BuildBlock("watchedvid1", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	two, err := st.BuildBlock("watchedvid2", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	if one.PublicKey == "" || two.PublicKey == "" {
		t.Fatal("a locally produced block carries no claim identity")
	}
	if one.PublicKey == two.PublicKey {
		t.Error("two neighbourhoods share a claim identity; broad buckets can be joined back into one publisher's graph")
	}

	installKey, err := st.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	rawSwarm, err := st.SwarmIdentity()
	if err != nil {
		t.Fatal(err)
	}
	swarmPub := base64.StdEncoding.EncodeToString(
		ed25519.PrivateKey(rawSwarm).Public().(ed25519.PublicKey))
	for _, blk := range []*Block{one, two} {
		if blk.PublicKey == installKey {
			t.Errorf("claim %q is the install signing key; every block would be linkable", blk.Key)
		}
		if blk.PublicKey == swarmPub {
			t.Errorf("claim %q is the libp2p identity; blocks would be linkable to the connection", blk.Key)
		}
	}

	// Stable across rebuilds, so an update replaces its own prior version
	// rather than minting a new source at every holder.
	seedEdge(t, st, "watchedvid1", "targetaaaa2", 1)
	updated, err := st.BuildBlock("watchedvid1", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	if updated.PublicKey != one.PublicKey {
		t.Error("an updated neighbourhood changed claim identity; it can no longer replace its predecessor")
	}
	if updated.Revision <= one.Revision {
		t.Errorf("revision went %d → %d after the contents changed; replacement needs a higher revision",
			one.Revision, updated.Revision)
	}

	// And an unchanged rebuild is byte-identical, so an idle node does not
	// publish an endless stream of "new" claims that are all the same data.
	again, err := st.BuildBlock("watchedvid1", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != updated.Revision || again.ContentSHA256 != updated.ContentSHA256 {
		t.Error("rebuilding an unchanged neighbourhood produced a new revision")
	}
}

// TestUpdatedClaimReplacesItsPredecessor covers both delivery orders.
//
// Out-of-order is the normal case on a mesh, not an edge case: a peer that
// cached revision 1 goes on serving it after revision 2 has reached someone
// else, so a holder must not be talked backwards by a stale copy.
func TestUpdatedClaimReplacesItsPredecessor(t *testing.T) {
	origin := openStore(t, "origin.sqlite")
	seedEdge(t, origin, "seedaaaaaaa", "targetaaaa1", 0)
	first, err := origin.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	seedEdge(t, origin, "seedaaaaaaa", "targetaaaa2", 1)
	second, err := origin.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}

	held := func(st *Store) []Block {
		t.Helper()
		got, err := st.PeerClaimsForKeys([]string{"seedaaaaaaa"})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	importBlock := func(st *Store, b *Block) {
		t.Helper()
		raw, err := b.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.ImportBlock(raw); err != nil {
			t.Fatal(err)
		}
	}

	inOrder := openStore(t, "inorder.sqlite")
	importBlock(inOrder, first)
	importBlock(inOrder, second)
	if got := held(inOrder); len(got) != 1 || len(got[0].Edges) != 2 {
		t.Fatalf("in-order delivery left %d claims; want one claim at the newer revision", len(got))
	}

	reversed := openStore(t, "reversed.sqlite")
	importBlock(reversed, second)
	importBlock(reversed, first)
	got := held(reversed)
	if len(got) != 1 {
		t.Fatalf("out-of-order delivery left %d claims, want 1", len(got))
	}
	if got[0].Revision != second.Revision || len(got[0].Edges) != 2 {
		t.Errorf("a stale copy rolled the holder back to revision %d", got[0].Revision)
	}
}

// TestRelayCycleDoesNotAmplify is the three-node loop WO-084 requires.
//
// The pre-WO-084 store re-aggregated everything it held for a key and re-signed
// the total under its own install key, so A→B→C→A came back to A as a sum of
// sums signed by a third party — one observation looking like several
// independent ones, growing with every lap. Preserved claims make a lap a
// no-op: the same claim identity at the same revision replaces itself.
func TestRelayCycleDoesNotAmplify(t *testing.T) {
	a := openStore(t, "a.sqlite")
	b := openStore(t, "b.sqlite")
	c := openStore(t, "c.sqlite")
	seedEdge(t, a, "seedaaaaaaa", "targetaaaa1", 0)
	seedEdge(t, a, "seedaaaaaaa", "targetaaaa1", 0) // twice: count 2

	origin, err := a.BuildBlock("seedaaaaaaa", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	if len(origin.Edges) != 1 || origin.Edges[0].Count != 2 {
		t.Fatalf("fixture: origin claim = %+v, want one edge with count 2", origin.Edges)
	}
	prefix := BlockPrefix("seedaaaaaaa", DefaultPrefixBits)

	// relay serves its whole bucket to the next node, exactly as the swarm
	// handler would, and reports what the next node ended up holding.
	relay := func(from, to *Store) {
		t.Helper()
		bucket, err := from.BlocksInPrefix(prefix, "GB-en", AllSources, 256)
		if err != nil {
			t.Fatal(err)
		}
		for i := range bucket.Blocks {
			raw, err := bucket.Blocks[i].Encode()
			if err != nil {
				t.Fatal(err)
			}
			// ErrOwnClaim is the cycle closing and is not a failure.
			if _, _, err := to.ImportBlock(raw); err != nil && err != ErrOwnClaim {
				t.Fatalf("import: %v", err)
			}
		}
	}

	for lap := 1; lap <= 3; lap++ {
		relay(a, b)
		relay(b, c)
		relay(c, a)

		for name, st := range map[string]*Store{"b": b, "c": c} {
			claims, err := st.PeerClaimsForKeys([]string{"seedaaaaaaa"})
			if err != nil {
				t.Fatal(err)
			}
			if len(claims) != 1 {
				t.Fatalf("lap %d: node %s holds %d claims for one neighbourhood, want 1 — the relay minted sources",
					lap, name, len(claims))
			}
			if claims[0].ClaimID() != origin.ClaimID() {
				t.Fatalf("lap %d: node %s re-signed the claim as %q, want the original publisher %q",
					lap, name, claims[0].ClaimID(), origin.ClaimID())
			}
			if len(claims[0].Edges) != 1 || claims[0].Edges[0].Count != 2 {
				t.Fatalf("lap %d: node %s holds %+v, want the unchanged count of 2 — the relay amplified",
					lap, name, claims[0].Edges)
			}
		}

		// A must not have absorbed its own claim back as a peer's.
		back, err := a.PeerClaimsForKeys([]string{"seedaaaaaaa"})
		if err != nil {
			t.Fatal(err)
		}
		if len(back) != 0 {
			t.Fatalf("lap %d: the origin imported its own claim back as a peer's (%d rows)", lap, len(back))
		}
		var peerRows int
		if err := a.db.QueryRow(
			`SELECT COUNT(*) FROM peer_edges WHERE from_id = ?`, "seedaaaaaaa").Scan(&peerRows); err != nil {
			t.Fatal(err)
		}
		if peerRows != 0 {
			t.Fatalf("lap %d: the origin double-counted its own observations as %d peer rows", lap, peerRows)
		}
	}
}

// TestServedCorpusMatchesWhatIsAnnounced is WO-084 requirement 4.
//
// A provider record is a promise. Announcing a bucket the stream then answers
// empty costs the requester a round trip, and — worse — a divergence between
// the two sets is itself observable: a peer that announces widely and serves
// narrowly is distinguishable from one that does not.
func TestServedCorpusMatchesWhatIsAnnounced(t *testing.T) {
	st := openStore(t, "corpus.sqlite")
	seedEdge(t, st, "localseed01", "localvid001", 0)

	origin := openStore(t, "origin.sqlite")
	seedEdge(t, origin, "peerseedaa1", "peervid0001", 0)
	blk, err := origin.BuildBlock("peerseedaa1", "GB-en")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ImportBlock(raw); err != nil {
		t.Fatal(err)
	}

	prefixes, err := st.LocalPrefixes(DefaultPrefixBits, AllSources)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) == 0 {
		t.Fatal("a node holding two neighbourhoods announced no graph buckets")
	}
	for _, p := range prefixes {
		bucket, err := st.BlocksInPrefix(p, "GB-en", AllSources, 256)
		if err != nil {
			t.Fatalf("announced bucket %s cannot be served: %v", p, err)
		}
		if len(bucket.Blocks) == 0 {
			t.Errorf("announced bucket %s returns nothing", p)
		}
	}

	// Both neighbourhoods must be reachable through the announced set, which is
	// the union half: a mirror-only implementation announces and serves only
	// peerseedaa1 and passes every per-bucket check above.
	found := map[string]bool{}
	for _, p := range prefixes {
		bucket, err := st.BlocksInPrefix(p, "GB-en", AllSources, 256)
		if err != nil {
			t.Fatal(err)
		}
		for _, blk := range bucket.Blocks {
			found[blk.Key] = true
		}
	}
	for _, want := range []string{"localseed01", "peerseedaa1"} {
		if !found[want] {
			t.Errorf("the announced buckets never serve %q", want)
		}
	}
}

// TestBucketRefusesToServeBelowTheAnonymityFloor is WO-084 requirement 5: a
// response is never quietly narrowed below the documented floor. Failing closed
// is indistinguishable from holding nothing; a small honest-looking answer is
// not.
func TestBucketRefusesToServeBelowTheAnonymityFloor(t *testing.T) {
	st := openStore(t, "floor.sqlite")
	seedEdge(t, st, "seedaaaaaaa", "targetaaaa1", 0)
	prefix := BlockPrefix("seedaaaaaaa", DefaultPrefixBits)

	if _, err := st.BlocksInPrefix(prefix, "GB-en", AllSources, BucketAnonymityFloor-1); err == nil {
		t.Error("a reply cap below the anonymity floor was accepted")
	}
	if _, err := st.BlocksInPrefix(prefix, "GB-en", AllSources, BucketAnonymityFloor); err != nil {
		t.Errorf("a reply cap exactly at the floor was refused: %v", err)
	}
}
