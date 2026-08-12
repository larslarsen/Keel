// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keel-app/keel/daemon/store"
)

// importReply must be resilient to a hostile bucket: one bad block must not
// poison the whole batch, and malformed JSON must not panic. These are the
// Category-4 fault-injection cases for the code path a peer can attack.

func validBlockJSON(t *testing.T, st *store.Store, key string) []byte {
	t.Helper()
	blk, err := st.BuildBlock(key, "GB-en")
	if err != nil {
		t.Fatalf("build block %s: %v", key, err)
	}
	raw, err := blk.Encode()
	if err != nil {
		t.Fatalf("encode block %s: %v", key, err)
	}
	return raw
}

// bucketJSON wraps blocks in the BlockProtocol 3.0.0 reply envelope. A bare
// array is not a bucket any more: truncation has to be visible to whoever
// receives it, so Held and Truncated travel with the blocks (WO-084).
func bucketJSON(t *testing.T, blocks ...json.RawMessage) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"schema_version": 3,
		"prefix":         "12:0000",
		"held":           len(blocks),
		"truncated":      false,
		"blocks":         blocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestImportReplyValidBucket(t *testing.T) {
	// The origin has to be a different store: a node re-importing its own claim
	// is the relay cycle, and ImportBlock refuses it on purpose.
	origin := newStore(t, "origin.sqlite")
	seed(t, origin, "seedaaaaaaa", "targetaaaa1", 0)
	seed(t, origin, "seedaaaaaaa", "targetaaaa2", 2)

	st := newStore(t, "imp.sqlite")
	raw := bucketJSON(t, validBlockJSON(t, origin, "seedaaaaaaa"))

	n := &Node{st: st}
	imported, blocks, edges := n.importReply(raw)
	if blocks != 1 {
		t.Fatalf("expected 1 block, got %d", blocks)
	}
	if edges <= 0 {
		t.Fatalf("expected >0 edges from a seeded bucket, got %d", edges)
	}
	if len(imported) != 1 {
		t.Fatalf("expected 1 imported block, got %d", len(imported))
	}
}

func TestImportReplyMalformedJSON(t *testing.T) {
	st := newStore(t, "imp.sqlite")
	n := &Node{st: st}
	// Truncated / invalid JSON must yield zero blocks, no panic.
	cases := [][]byte{
		[]byte(`not json at all`),
		[]byte(`{`),
		[]byte(`[`),
		[]byte(`{"x":1}`), // object, not array
		[]byte(`null`),
		[]byte(`123`),
		[]byte(``),
	}
	for _, c := range cases {
		imported, blocks, edges := n.importReply(c)
		if blocks != 0 || edges != 0 || imported != nil {
			t.Errorf("malformed input %q: got blocks=%d edges=%d imported=%v, want all zero", c, blocks, edges, imported)
		}
	}
}

func TestImportReplySkipsUnverifiableBlock(t *testing.T) {
	origin := newStore(t, "origin.sqlite")
	seed(t, origin, "seedaaaaaaa", "targetaaaa1", 0)
	st := newStore(t, "imp.sqlite")
	n := &Node{st: st}

	good := validBlockJSON(t, origin, "seedaaaaaaa")
	// A block whose signature/content cannot verify must be skipped, not fail
	// the batch. Build a validly-shaped but unverifiable block: a JSON object
	// missing the key that ImportBlock's verification requires.
	bad := []byte(`{"schema_version":1,"edges":[{"surface":"WATCH_NEXT","to":"xaaaaaaaaaa","slot":0,"observed_at":1}]}`)

	imported, blocks, _ := n.importReply(bucketJSON(t, json.RawMessage(bad), json.RawMessage(good)))
	if blocks != 1 {
		t.Fatalf("expected the bad block to be skipped and the good one kept (blocks=1), got %d", blocks)
	}
	if len(imported) != 1 {
		t.Fatalf("expected imported len 1, got %d", len(imported))
	}
}

func TestImportReplyHugeListDoesNotPanic(t *testing.T) {
	st := newStore(t, "imp.sqlite")
	n := &Node{st: st}
	// 10k entries — importReply must not allocate unbounded or hang. It should
	// import each (most will be empty/invalid and skipped) and return.
	huge := make([]json.RawMessage, 10000)
	for i := range huge {
		huge[i] = json.RawMessage(`{"schema_version":1}`)
	}
	imported, blocks, edges := n.importReply(bucketJSON(t, huge...))
	if blocks != 0 || edges != 0 || imported != nil {
		t.Errorf("huge empty list: got blocks=%d edges=%d imported=%v", blocks, edges, imported)
	}
}

// trimLine strips only TRAILING \n / \r / space. It deliberately does not touch
// leading characters: the prefix arrives via bufio.ReadString('\n'), so a
// well-formed line has no leading junk, and a malformed one simply won't match
// a bucket (a clean miss) rather than being silently mutated. Assert that
// contract: trailing whitespace gone, everything else preserved.
func TestTrimLineBoundaries(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a3f", "a3f"},
		{"a3f\n", "a3f"},
		{"a3f\r\n", "a3f"},
		{"a3f ", "a3f"},
		{"  a3f  ", "  a3f"}, // leading spaces kept, trailing stripped
		{"\r\n", ""},         // pure CRLF -> empty
		{"   ", ""},          // pure spaces -> empty
		{"", ""},
		{"a3f	", "a3f	"},     // tab is NOT in the trim set; preserved
		{"\na3f\n", "\na3f"}, // leading newline kept
		{"a3f\r", "a3f"},     // trailing \r stripped
	}
	for _, c := range cases {
		if got := trimLine(c.in); got != c.want {
			t.Errorf("trimLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// prefixCID must be deterministic and distinct: the same bucket always maps to
// the same DHT key (so provider records and lookups agree), and different
// buckets must not collide.
func TestPrefixCIDDeterministic(t *testing.T) {
	a, err := prefixCID("a3f")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := prefixCID("a3f")
	if err != nil {
		t.Fatal(err)
	}
	if a.String() != a2.String() {
		t.Fatal("prefixCID not deterministic for same input")
	}
	b, err := prefixCID("b9z")
	if err != nil {
		t.Fatal("prefixCID failed on valid input")
	}
	if a.String() == b.String() {
		t.Fatal("different prefixes collided to the same CID")
	}
	// Empty and pathological prefixes must still produce a stable key.
	e, err := prefixCID("")
	if err != nil {
		t.Fatal("prefixCID failed on empty prefix")
	}
	if strings.Contains(e.String(), "undefined") {
		t.Fatal("prefixCID produced an undefined CID")
	}
}
