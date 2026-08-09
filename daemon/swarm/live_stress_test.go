// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// Concurrency / race / invariant stress suite for LiveIndex.
//
// This is the highest-value gap the quality sweep surfaced: LiveIndex holds
// shared mutable state behind a single RWMutex (live.go:170) and is mutated by
// merge (gossip + local observations), sweep (ticker goroutine), and read by
// Search / Snapshot / Size / shouldPublish. No -race stress test exercised
// that path before — even though merge's write path was edited (BUG 1) and is
// the one that min-accumulates StartedAt. These tests exist to BREAK it under
// load.
//
// Run with: go test -race -run 'LiveIndexStress|LiveIndexInvariant' ./swarm/
// ============================================================================

// ytID returns an 11-char YouTube-style id (validVideoID requires len==11 and
// only [a-zA-Z0-9_-]). A merge that fails validation silently no-ops, which
// would make the stress test meaningless, so every generated id is valid.
func ytID(seed int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	b := make([]byte, 11)
	for i := range b {
		b[i] = chars[(seed>>(i*2))%len(chars)]
	}
	return string(b)
}

// LiveIndexStressConcurrentMergeSearch drives 128 concurrent workers that
// interleave merges, searches, snapshots, size reads, shouldPublish and
// local-seen writes, while a sweep goroutine runs concurrently. Under -race
// this detects data races in the mu-guarded state; the invariant checker
// detects logical corruption of the min-StartedAt accumulation.
func TestLiveIndexStressConcurrentMergeSearch(t *testing.T) {
	li := &LiveIndex{
		entries:    map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		retired:    map[string]time.Time{},
		logf:       func(string, ...any) {},
	}
	now := time.Now()

	var mu sync.Mutex
	seenStart := map[string]int64{} // key -> min StartedAt we've sent
	seenSeenAt := map[string]int64{} // key -> max SeenAt we've sent
	var panics int64

	const workers = 128
	const itersPerWorker = 400

	// Concurrent sweep goroutine, as in production (live.go:605): a blocking
	// loop that only returns on ctx cancel.
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	defer sweepCancel()
	go li.sweep(sweepCtx)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(w)*7919 + 1))
			for i := 0; i < itersPerWorker; i++ {
				key := fmt.Sprintf("yt:%s", ytID(w*7+r.Intn(40)))
				platform, vid := "yt", key[3:]
				seenAt := now.Add(-time.Duration(r.Intn(600)) * time.Second).UnixMilli()
				if seenAt <= 0 {
					seenAt = 1
				}
				// Sometimes omit StartedAt (the BUG-1 regression path), sometimes
				// send a real start a few hours back.
				var startedAt int64
				switch r.Intn(3) {
				case 0: // omit
					startedAt = 0
				case 1: // real, 1..6h back (within maxLiveAge=12h so no freeze/retire)
					startedAt = now.Add(-time.Duration(r.Intn(6)+1) * time.Hour).UnixMilli()
				default: // explicit zero
					startedAt = 0
				}

				mu.Lock()
				if startedAt > 0 {
					if prev, ok := seenStart[key]; !ok || startedAt < prev {
						seenStart[key] = startedAt
					}
				}
				if prev, ok := seenSeenAt[key]; !ok || seenAt > prev {
					seenSeenAt[key] = seenAt
				}
				mu.Unlock()

				rec := LiveRecord{
					VideoID:   vid,
					Platform:  platform,
					Title:     fmt.Sprintf("Stream %d", w),
					SeenAt:    seenAt,
					StartedAt: startedAt,
					ChannelID: fmt.Sprintf("@chan%d", r.Intn(10)),
				}
				// merge must never panic under concurrent load.
				func() {
					defer func() {
						if recover() != nil {
							atomic.AddInt64(&panics, 1)
						}
					}()
					li.merge(rec)
				}()

				// Interleave reads — these run concurrently with merges/sweep.
				switch r.Intn(4) {
				case 0:
					_ = li.Search("", 50)
				case 1:
					_ = li.Snapshot()
				case 2:
					_ = li.Size()
				case 3:
					_ = li.shouldPublish(platform, vid)
				}
				li.setLocalSeenAt(platform, vid, seenAt)
			}
		}(w)
	}

	wg.Wait()
	sweepCancel()

	if atomic.LoadInt64(&panics) != 0 {
		t.Fatalf("merge panicked %d times under concurrent load", panics)
	}

	// ---- Invariant checks ----
	mu.Lock()
	defer mu.Unlock()
	li.mu.RLock()
	defer li.mu.RUnlock()
	for key, entry := range li.entries {
		if want, ok := seenStart[key]; ok && want > 0 {
			if entry.rec.StartedAt != 0 && entry.rec.StartedAt != want {
				t.Errorf("key %s: StartedAt=%d not equal to min-sent %d (min-accumulation broken)", key, entry.rec.StartedAt, want)
			}
		}
		if entry.rec.SeenAt <= 0 {
			t.Errorf("key %s: SeenAt=%d (must be positive)", key, entry.rec.SeenAt)
		}
	}
	if li.Size() < 0 {
		t.Errorf("negative Size()=%d", li.Size())
	}
}

// LiveIndexInvariantRandomSequence performs hundreds of randomized valid
// operations in random order on a single index (no goroutines) and verifies
// the core invariants hold after EVERY operation, not just at the end. This is
// the state-machine / invariant category: the system must stay consistent
// regardless of operation ordering.
func TestLiveIndexInvariantRandomSequence(t *testing.T) {
	li := &LiveIndex{
		entries:     map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		retired:     map[string]time.Time{},
		logf:        func(string, ...any) {},
	}
	now := time.Now()
	r := rand.New(rand.NewSource(42))

	wantStart := map[string]int64{} // key -> min StartedAt we've sent
	wantSeen := map[string]int64{}  // key -> max SeenAt we've sent

	check := func(key string) {
		e, ok := li.entries[key]
		if !ok {
			return
		}
		if e.rec.SeenAt <= 0 {
			t.Fatalf("invariant violated: %s SeenAt=%d", key, e.rec.SeenAt)
		}
		if ws, ok := wantStart[key]; ok && ws > 0 {
			if e.rec.StartedAt != 0 && e.rec.StartedAt != ws {
				t.Fatalf("invariant violated: %s StartedAt=%d != min-sent %d (min-accumulation broken)", key, e.rec.StartedAt, ws)
			}
		}
		if wv, ok := wantSeen[key]; ok && e.rec.SeenAt > wv {
			t.Fatalf("invariant violated: %s SeenAt=%d above max-sent %d", key, e.rec.SeenAt, wv)
		}
	}

	const ops = 2000
	for i := 0; i < ops; i++ {
		vid := ytID(r.Intn(60))
		key := "yt:" + vid
		seenAt := now.Add(-time.Duration(r.Intn(300)) * time.Second).UnixMilli()
		if seenAt <= 0 {
			seenAt = 1
		}
		var startedAt int64
		if r.Intn(3) != 0 {
			startedAt = now.Add(-time.Duration(r.Intn(6)+1) * time.Hour).UnixMilli()
		}
		li.merge(LiveRecord{
			VideoID:   vid,
			Platform:  "yt",
			Title:     "S",
			SeenAt:    seenAt,
			StartedAt: startedAt,
			ChannelID: "@c",
		})
		if startedAt > 0 {
			if cur, ok := wantStart[key]; !ok || startedAt < cur {
				wantStart[key] = startedAt
			}
		}
		if cur, ok := wantSeen[key]; !ok || seenAt > cur {
			wantSeen[key] = seenAt
		}
		check(key)

		switch r.Intn(3) {
		case 0:
			_ = li.Search("", 10)
		case 1:
			_ = li.Size()
		case 2:
			li.setLocalSeenAt("yt", vid, seenAt)
		}
	}
}

// LiveIndexSweepRaceWithMerge ensures the sweep goroutine and merge do not race
// on the same entry (sweep deletes from entries under lock; merge inserts/
// updates under the same lock). Focused -race probe; a failure points clearly
// at the sweep path.
func TestLiveIndexSweepRaceWithMerge(t *testing.T) {
	li := &LiveIndex{
		entries:     map[string]*liveEntry{},
		localSeenAt: map[string]int64{},
		retired:     map[string]time.Time{},
		logf:        func(string, ...any) {},
	}
	now := time.Now()

	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	defer sweepCancel()
	go li.sweep(sweepCtx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := rand.New(rand.NewSource(7))
		for i := 0; i < 5000; i++ {
			li.merge(LiveRecord{
				VideoID:   ytID(1),
				Platform:  "yt",
				Title:     "S",
				SeenAt:    now.Add(-time.Duration(r.Intn(10)) * time.Second).UnixMilli(),
				StartedAt: now.Add(-3 * time.Hour).UnixMilli(),
			})
		}
	}()

	// Let the writer finish, then stop the sweeper.
	wg.Wait()
	sweepCancel()
}
