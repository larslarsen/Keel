// SPDX-License-Identifier: Apache-2.0
package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// seedManyTitles inserts n videos whose titles all contain the same
// meaningful word, so every one of them lands in that word's token shards.
// That is the only practical way to build a single shard with more rows than
// the old 4,096 cap.
func seedManyTitles(t testing.TB, st *Store, n int, word string) []string {
	t.Helper()
	ids := make([]string, 0, n)
	batch := make([]bridge.Impression, 0, 500)
	now := time.Now().UnixMilli()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if _, err := st.PutImpressions(batch); err != nil {
			t.Fatal(err)
		}
		batch = batch[:0]
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("vid%08d", i)
		ids = append(ids, id)
		batch = append(batch, bridge.Impression{
			PageLoadID: nextPageLoad(),
			ObservedAt: now, Surface: "HOME", SlotIndex: 0,
			VideoID: id, Title: fmt.Sprintf("%s episode %d", word, i),
		})
		if len(batch) == cap(batch) {
			flush()
		}
	}
	flush()
	return ids
}

// TestShardBeyondTheOldCapIsCompletelyReachable is WO-097 §6's headline
// acceptance: a shard with more than 4,096 rows must be fully reachable.
//
// The old code returned exactly 4,096 rows and reported success, so rows past
// the cap did not exist as far as any peer was concerned. Worse, it selected
// them while iterating an unordered map and sorted the survivors afterwards,
// so the subset was arbitrary and looked deliberate.
func TestShardBeyondTheOldCapIsCompletelyReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds several thousand rows")
	}
	st := openStore(t, "shard-big.sqlite")
	const rows = 4200 // deliberately above the removed maxShardEntries = 4096
	seedManyTitles(t, st, rows, "recommendation")

	shard := ShardOf(TokenizeQuery("recommendation")[0])
	all, err := st.ShardSlice(shard, AllSources)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != rows {
		t.Fatalf("ShardSlice returned %d of %d rows — the cap is back", len(all), rows)
	}

	// Every row is reachable through the paged traversal, exactly once,
	// whatever offset the nonce picks.
	for _, nonce := range []uint64{0, 1, 7919, 4199, 1 << 62} {
		got, offset, err := st.ShardRows(shard, AllSources, nonce)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != rows {
			t.Errorf("nonce %d: traversal covered %d of %d rows", nonce, len(got), rows)
		}
		if offset < 0 || offset >= rows {
			t.Errorf("nonce %d: offset %d out of range [0,%d)", nonce, offset, rows)
		}
		seen := map[string]bool{}
		for _, e := range got {
			if seen[e.VideoID] {
				t.Fatalf("nonce %d: %s appeared twice in one traversal", nonce, e.VideoID)
			}
			seen[e.VideoID] = true
		}
		if len(seen) != len(all) {
			t.Errorf("nonce %d: traversal saw %d distinct rows, the shard holds %d",
				nonce, len(seen), len(all))
		}
	}
}

// TestNonceVariesWhichRowsAPartialTraversalSees is the other half of §6: a
// partial-budget traversal must not always privilege the same first rows.
func TestNonceVariesWhichRowsAPartialTraversalSees(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds several thousand rows")
	}
	st := openStore(t, "shard-nonce.sqlite")
	seedManyTitles(t, st, 4200, "recommendation")
	shard := ShardOf(TokenizeQuery("recommendation")[0])

	firstPage := func(nonce uint64) []string {
		rows, _, err := st.ShardRows(shard, AllSources, nonce)
		if err != nil {
			t.Fatal(err)
		}
		n := MaxPageEntries
		if n > len(rows) {
			n = len(rows)
		}
		out := make([]string, 0, n)
		for _, e := range rows[:n] {
			out = append(out, e.VideoID)
		}
		return out
	}

	a, b := firstPage(0), firstPage(2731)
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("no rows to compare")
	}
	same := 0
	inA := map[string]bool{}
	for _, id := range a {
		inA[id] = true
	}
	for _, id := range b {
		if inA[id] {
			same++
		}
	}
	if same == len(a) {
		t.Error("two different nonces returned identical first pages — repeated " +
			"partial-budget searches would privilege the same rows forever")
	}

	// A full traversal is the same *set* regardless of order, or the rotation
	// would be a filter.
	setOf := func(nonce uint64) map[string]bool {
		rows, _, err := st.ShardRows(shard, AllSources, nonce)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, e := range rows {
			out[e.VideoID] = true
		}
		return out
	}
	s0, s1 := setOf(0), setOf(2731)
	if len(s0) != len(s1) {
		t.Fatalf("full traversals differ in size: %d vs %d", len(s0), len(s1))
	}
	for id := range s0 {
		if !s1[id] {
			t.Fatalf("full traversal under one nonce is missing %s", id)
		}
	}
}

// TestCataloguePrefixBeyondTheOldCapIsCompletelyReachable is the same
// acceptance for the catalogue/string side, which had the identical defect
// under a different constant.
//
// A 12-bit prefix cannot be made to hold 4,096 rows without millions of
// videos, so the bucket is widened to 1 bit here: the prefix arithmetic is the
// same at every width, and the property under test is the absence of a cap.
func TestCataloguePrefixBeyondTheOldCapIsCompletelyReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds several thousand rows")
	}
	st := openStore(t, "catalogue-big.sqlite")
	const rows = 9000
	ids := seedManyTitles(t, st, rows, "recommendation")

	counts := map[string]int{}
	for _, id := range ids {
		counts[CataloguePrefix(id, 1)]++
	}
	var prefix string
	for p, n := range counts {
		if n > 4096 {
			prefix = p
			break
		}
	}
	if prefix == "" {
		t.Fatalf("no 1-bit bucket exceeded 4,096 rows: %v", counts)
	}

	got, _, err := st.CatalogueRows(prefix, AllSources, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != counts[prefix] {
		t.Errorf("CatalogueRows returned %d of %d rows in bucket %s — the cap is back",
			len(got), counts[prefix], prefix)
	}

	seen := map[string]bool{}
	for _, e := range got {
		if seen[e.VideoID] {
			t.Fatalf("%s appeared twice in one traversal", e.VideoID)
		}
		seen[e.VideoID] = true
	}
}

// TestTerminalDetectsGapsDuplicatesAndReordering is what makes a framed
// response safe: the signed terminal has to catch every way a frame sequence
// can be tampered with, or pagination would be a downgrade from one signed
// blob.
func TestTerminalDetectsGapsDuplicatesAndReordering(t *testing.T) {
	st := openStore(t, "terminal.sqlite")
	seedTitle(t, st, "vid00000001", "Recommendation systems explained")
	shard := ShardOf(TokenizeQuery("recommendation")[0])
	rows, offset, err := st.ShardRows(shard, AllSources, 0)
	if err != nil {
		t.Fatal(err)
	}

	pageA, err := st.SignShardPage(shard, 0, offset, rows)
	if err != nil {
		t.Fatal(err)
	}
	pageB, err := st.SignShardPage(shard, 1, offset, rows)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := st.SignTerminal(fmt.Sprintf("%d", shard), len(rows), 2, true,
		ReasonComplete, []string{pageA.ContentSHA256, pageB.ContentSHA256})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTerminal(terminal); err != nil {
		t.Fatalf("VerifyTerminal rejected a terminal this store just signed: %v", err)
	}

	// A page signed for index 0 cannot pass as index 1: the index is inside the
	// signed payload, so replaying a frame at another position fails.
	if pageA.ContentSHA256 == pageB.ContentSHA256 {
		t.Error("two pages with identical entries at different indices share a digest — " +
			"a frame could be replayed at another position")
	}

	// Truncation: fewer digests than the claimed page count.
	short := *terminal
	short.PageDigests = short.PageDigests[:1]
	if err := VerifyTerminal(&short); err == nil {
		t.Error("VerifyTerminal accepted a terminal claiming more pages than it lists")
	}

	// Tampered counts, signature unchanged.
	lying := *terminal
	lying.Total = 999999
	if err := VerifyTerminal(&lying); err == nil {
		t.Error("VerifyTerminal accepted a terminal whose totals were altered after signing")
	}

	// A missing terminal is an error, never an empty success.
	if err := VerifyTerminal(nil); err == nil {
		t.Error("VerifyTerminal accepted a response that ended with no terminal frame")
	}
}

// TestIncompleteTerminationIsExplicit: running out of budget must be a stated
// incomplete, never success with a silent prefix.
func TestIncompleteTerminationIsExplicit(t *testing.T) {
	st := openStore(t, "incomplete.sqlite")
	terminal, err := st.SignTerminal("7", 10000, 2, false, ReasonBudget, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTerminal(terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Complete {
		t.Error("a budget-terminated response reported itself complete")
	}
	if terminal.Reason != ReasonBudget {
		t.Errorf("reason = %q, want %q", terminal.Reason, ReasonBudget)
	}
	if terminal.Total <= terminal.Pages*MaxPageEntries {
		t.Error("the terminal does not record that more rows exist than were sent")
	}
}

// TestPageStartAndRotate are the arithmetic the whole traversal rests on.
func TestPageStartAndRotate(t *testing.T) {
	if got := PageStart(0, 99); got != 0 {
		t.Errorf("PageStart on an empty bucket = %d, want 0", got)
	}
	if got := PageStart(10, 23); got != 3 {
		t.Errorf("PageStart(10, 23) = %d, want 3", got)
	}
	for _, offset := range []int{0, 3, 9, -1, 100} {
		idx := rotate(10, offset)
		if len(idx) != 10 {
			t.Fatalf("rotate(10, %d) produced %d indices", offset, len(idx))
		}
		seen := map[int]bool{}
		for _, i := range idx {
			if seen[i] {
				t.Fatalf("rotate(10, %d) visited %d twice", offset, i)
			}
			seen[i] = true
		}
	}
}
