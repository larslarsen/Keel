// SPDX-License-Identifier: Apache-2.0
package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
)

// ============================================================================
// Regression tests — one per daemon-testable bug from the WO-003 / WO-004
// review findings and WO-055. Each locks in a fix so the bug cannot silently
// return. (TESTING.md §6: no bug fix without a failing-then-passing test.)
//
// Source bugs:
//   - WO-002 / WO-004 §3: daemon deleted data on every start via Retention/Sweep.
//   - WO-004 §5: re-render with varying skip count produced duplicate rows
//     (slot_index was part of the PK and unstable).
//   - WO-055: status showed raw DHT peer count as "connected peers" (misleading).
// ============================================================================

// Bug WO-002 / WO-004 §3 — no time-based deletion.
//
// An impression older than any retention window must survive. The corpus is kept
// indefinitely; deletion is a P1 user setting defaulting to off. A regression
// that re-adds a startup sweep would make this fail.
func TestRegressionNoTimeBasedDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retro.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	old := time.Now().AddDate(-3, 0, 0) // three years old
	vid := "ancientvid001"
	ctx := "ctx-ancient"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "pl-retro", ObservedAt: old.UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &ctx, VideoID: vid, Title: "Old video", SlotIndex: 0,
	}}); err != nil {
		t.Fatal(err)
	}

	// Simulate a daemon "restart" by re-opening the store on the same file.
	st.Close()
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })

	if rows := countImpressions(st2, vid); rows != 1 {
		t.Fatalf("3-year-old impression: %d rows after reopen, want 1 (regression: time-based deletion returned)", rows)
	}
}

// Bug WO-004 §5 — re-render must not create duplicate rows.
func TestRegressionNoDuplicateRowsOnRerender(t *testing.T) {
	st := openStore(t, "dup.sqlite")
	const (
		pl   = "pl-dup"
		surf = "WATCH_NEXT"
		vid  = "dupvid001"
	)
	ctx := "ctx-dup"

	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: pl, ObservedAt: time.Now().UnixMilli(), Surface: surf,
		ContextVideoID: &ctx, VideoID: vid, Title: "Dup", SlotIndex: 0,
	}}); err != nil {
		t.Fatal(err)
	}
	// Re-render: slot drifts to 5 because a card was skipped this time.
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: pl, ObservedAt: time.Now().UnixMilli(), Surface: surf,
		ContextVideoID: &ctx, VideoID: vid, Title: "Dup", SlotIndex: 5,
	}}); err != nil {
		t.Fatal(err)
	}

	if n := countImpressions(st, vid); n != 1 {
		t.Fatalf("re-render produced %d rows, want 1 (regression: duplicate rows on slot drift)", n)
	}
	if got := firstSlot(st, vid); got != 0 {
		t.Errorf("slot_index = %d after re-render, want 0 (first-observed slot must be preserved)", got)
	}
}

// Bug WO-055 — see daemon/swarm/regressions_test.go (needs a live Node, which
// lives in the swarm package). Kept here only as a pointer.

// ============================================================================
// Property tests — invariants that must ALWAYS hold (TESTING.md §2). These
// encode intent as a machine-checkable law, catching regressions nobody wrote a
// specific test for.
// ============================================================================

// Property: stringless. No watched title ever appears in a block key or an
// advertised prefix (DESIGN_v2: blocks are keyed by video id, titles travel in
// the separate catalogue). A regression that keys a block by title, or lets a
// title leak into an advertised prefix, would expose a user's watch history.
// This test seeds a distinctive title and asserts it never appears in any key
// or prefix the node emits.
func TestPropertyStringless(t *testing.T) {
	st := openStore(t, "stringless.sqlite")
	// A title no substring of which can appear in a video id or prefix.
	title := "SECRETwatchedTITLE-9f3a"
	seedEdge(t, st, "watchedvidX1", "targetaaaa1", 0)
	// Inject the title as the watched video's title via a direct impression.
	vid := "watchedvidX1"
	if _, err := st.PutImpressions([]bridge.Impression{{
		PageLoadID: "pl-sl", ObservedAt: time.Now().UnixMilli(), Surface: "WATCH_NEXT",
		ContextVideoID: &vid, VideoID: vid, Title: title, SlotIndex: 0,
	}}); err != nil {
		t.Fatal(err)
	}

	// Block key for the watched video must be the video id, never the title.
	blk, err := st.buildBlock(vid, "GB-en", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(blk.Key, title) || strings.Contains(blk.Key, "SECRET") {
		t.Errorf("block key leaks title: %q", blk.Key)
	}

	// Advertised prefixes must never contain the title.
	prefixes, err := st.LocalPrefixes(DefaultPrefixBits, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prefixes {
		if strings.Contains(p, title) || strings.Contains(p, "SECRET") {
			t.Fatalf("advertised prefix leaks watched title: %q", p)
		}
	}

	// The key must equal the video id exactly — the canonical invariant.
	if blk.Key != vid {
		t.Errorf("block key = %q, want video id %q", blk.Key, vid)
	}
}

// ---- test helpers ----

func countImpressions(st *Store, videoID string) int {
	var n int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM impressions WHERE video_id = ?`, videoID).Scan(&n); err != nil {
		return 0
	}
	return n
}

func firstSlot(st *Store, videoID string) int {
	var s sql.NullInt64
	if err := st.db.QueryRow(
		`SELECT slot_index FROM impressions WHERE video_id = ? LIMIT 1`, videoID).Scan(&s); err != nil {
		return -1
	}
	if !s.Valid {
		return -1
	}
	return int(s.Int64)
}
