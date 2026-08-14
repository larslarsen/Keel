// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBudgetReaderCannotOvershootItsBalance is WO-100 §1's headline.
//
// The previous meter read and decoded a whole response and charged afterwards.
// At the 8 MiB floor one response could read up to the 64 MiB transport cap
// before anything noticed, and cancelling after the fact cannot un-download it.
// A reservation taken BEFORE the read is what makes the ceiling a ceiling.
func TestBudgetReaderCannotOvershootItsBalance(t *testing.T) {
	const balance = 1024
	m := newBudgetMeter(balance, nil)
	src := bytes.NewReader(make([]byte, 1<<20))
	r := &budgetReader{ctx: context.Background(), r: src, m: m}

	read, err := io.Copy(io.Discard, r)
	if !errors.Is(err, ErrSearchBudget) {
		t.Fatalf("read ended with %v, want ErrSearchBudget", err)
	}
	if read != balance {
		t.Errorf("read %d bytes against a balance of %d — a reader consumed more "+
			"than it was granted", read, balance)
	}
	if !m.isExhausted() {
		t.Error("the meter did not latch exhausted")
	}
}

// TestConcurrentReadersShareOneBalance is the four-reader case the reserve is
// for: each consulting a remaining balance independently, they could otherwise
// each see room and each spend it.
func TestConcurrentReadersShareOneBalance(t *testing.T) {
	const balance = 64 << 10
	m := newBudgetMeter(balance, nil)

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := int64(0)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &budgetReader{ctx: context.Background(), r: bytes.NewReader(make([]byte, 1<<20)), m: m}
			n, _ := io.Copy(io.Discard, r)
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()

	if total > balance {
		t.Errorf("four concurrent readers consumed %d bytes against a shared "+
			"balance of %d", total, balance)
	}
	if got := m.used(); got > balance {
		t.Errorf("meter recorded %d spent against a limit of %d", got, balance)
	}
}

// TestBudgetExhaustionCancelsTheJob: refusing further reads is not enough on its
// own — the streams already open have to stop rather than drain to their
// deadline.
func TestBudgetExhaustionCancelsTheJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newBudgetMeter(16, cancel)
	r := &budgetReader{ctx: context.Background(), r: bytes.NewReader(make([]byte, 1024)), m: m}
	_, _ = io.Copy(io.Discard, r)

	select {
	case <-ctx.Done():
	default:
		t.Error("exhausting the budget did not cancel the job, so outstanding " +
			"reads would sit until their network deadline")
	}
}

// TestMalformedBytesAreChargedIncrementally: a rejected reply still cost what
// it read. A budget charging only accepted responses is one a hostile peer
// walks straight through by sending garbage.
func TestMalformedBytesAreChargedIncrementally(t *testing.T) {
	m := newBudgetMeter(1<<20, nil)
	junk := strings.Repeat("not json at all ", 1000)
	r := &budgetReader{ctx: context.Background(), r: strings.NewReader(junk), m: m}

	if _, err := readPagedResponse(r); err == nil {
		t.Fatal("garbage parsed as a valid response")
	}
	if m.used() == 0 {
		t.Error("a rejected response cost nothing against the budget")
	}
}

// TestUnmeteredContextIsUnaffected: prewalk, seed and ordinary catalogue sync
// have no search meter and must keep working when a search has spent its own.
func TestUnmeteredContextIsUnaffected(t *testing.T) {
	if got := meterFrom(context.Background()); got != nil {
		t.Fatal("a plain context carried a search budget")
	}
	// reserve on a nil meter grants whatever was asked.
	var nilMeter *budgetMeter
	got, err := nilMeter.reserve(context.Background(), 4096)
	if err != nil || got != 4096 {
		t.Errorf("an unmetered reader was granted %d of 4096 (err=%v)", got, err)
	}
}

// TestPrefixGroupMemoizesCompleteAndRetriesIncomplete is WO-100 §3.
//
// The first version dropped every entry the moment its call returned, so
// coalescing only covered exact overlap in time: a worker whose plan was
// computed slightly earlier could arrive just after the entry vanished and
// traverse the same bucket again.
func TestPrefixGroupMemoizesCompleteAndRetriesIncomplete(t *testing.T) {
	g := newPrefixGroup()
	ctx := context.Background()

	calls := 0
	complete := func() (catalogueResult, error) {
		calls++
		return catalogueResult{Outcome: catalogueComplete, Rows: 3}, nil
	}
	for i := 0; i < 5; i++ {
		res, err := g.resolve(ctx, "12:aaa0", complete)
		if err != nil || res.Outcome != catalogueComplete {
			t.Fatalf("resolve %d: %+v %v", i, res, err)
		}
	}
	if calls != 1 {
		t.Errorf("a verified complete traversal ran %d times, want 1 — it must be "+
			"memoized for the life of the job", calls)
	}

	// A complete-EMPTY traversal is completion too, and is memoized.
	emptyCalls := 0
	for i := 0; i < 3; i++ {
		_, _ = g.resolve(ctx, "12:bbb0", func() (catalogueResult, error) {
			emptyCalls++
			return catalogueResult{Outcome: catalogueComplete, Rows: 0}, nil
		})
	}
	if emptyCalls != 1 {
		t.Errorf("a complete empty bucket ran %d times, want 1", emptyCalls)
	}

	// Incomplete, unavailable and invalid are NOT memoized: caching one would
	// turn a transient peer problem into a prefix this search can never
	// resolve.
	for _, outcome := range []catalogueOutcome{catalogueIncomplete, catalogueUnavailable, catalogueInvalid} {
		n := 0
		fn := func() (catalogueResult, error) {
			n++
			return catalogueResult{Outcome: outcome}, nil
		}
		_, _ = g.resolve(ctx, "12:ccc0", fn)
		_, _ = g.resolve(ctx, "12:ccc0", fn)
		if n != 2 {
			t.Errorf("outcome %v was memoized after %d calls; it must stay retryable", outcome, n)
		}
	}
}

// TestPrefixGroupWakesWaitersOnCancellation is WO-100 §3's deadlock clause. A
// waiter parked on a traversal that is cancelled mid-flight must wake, or the
// job leaks a goroutine and never terminates.
func TestPrefixGroupWakesWaitersOnCancellation(t *testing.T) {
	g := newPrefixGroup()
	ctx, cancel := context.WithCancel(context.Background())

	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _ = g.resolve(context.Background(), "12:ddd0", func() (catalogueResult, error) {
			close(started)
			<-release
			return catalogueResult{Outcome: catalogueIncomplete}, nil
		})
	}()
	<-started

	woke := make(chan struct{})
	go func() {
		_, _ = g.resolve(ctx, "12:ddd0", func() (catalogueResult, error) {
			t.Error("the waiter started its own traversal instead of joining")
			return catalogueResult{}, nil
		})
		close(woke)
	}()

	cancel()
	select {
	case <-woke:
	case <-t.Context().Done():
		t.Fatal("a waiter did not wake on cancellation")
	}
	close(release)
}

// TestReservationIsNotExhaustion is WO-101 §1.
//
// The previous meter subtracted a read's whole grant up front, so while one
// reader held the last reservation another saw nothing remaining, latched
// exhaustion and cancelled every stream — even though the first reader was
// about to short-read and refund most of it. The job then reported `budget`
// with usable allowance unspent.
func TestReservationIsNotExhaustion(t *testing.T) {
	cancelled := false
	m := newBudgetMeter(budgetReadChunk, func() { cancelled = true })

	// Reader A leases the entire remaining balance.
	grantA, err := m.reserve(context.Background(), budgetReadChunk)
	if err != nil || grantA != budgetReadChunk {
		t.Fatalf("first reserve = %d, %v", grantA, err)
	}

	// Reader B asks while nothing is free. It must WAIT, not latch exhaustion.
	type res struct {
		n   int
		err error
	}
	got := make(chan res, 1)
	go func() {
		n, err := m.reserve(context.Background(), 4096)
		got <- res{n, err}
	}()

	select {
	case r := <-got:
		t.Fatalf("a reader treated a live reservation as exhaustion: %d, %v", r.n, r.err)
	case <-time.After(50 * time.Millisecond):
	}
	if m.isExhausted() || cancelled {
		t.Fatal("holding a reservation latched exhaustion and cancelled the job")
	}

	// Reader A short-reads and refunds almost all of it.
	m.settle(grantA, 100)

	select {
	case r := <-got:
		if r.err != nil {
			t.Errorf("the waiter got %v instead of the refunded capacity", r.err)
		}
		if r.n <= 0 {
			t.Errorf("the waiter was granted %d bytes after a refund", r.n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a refund did not wake the waiting reader")
	}
	if m.isExhausted() || cancelled {
		t.Error("the job was cancelled although allowance remained")
	}
	if got := m.used(); got != 100 {
		t.Errorf("committed = %d, want the 100 bytes actually read", got)
	}
}

// TestFullyConsumedFinalReservationIsExhaustion: the other side of §1. When the
// last lease really is spent, the next read must fail and the job must stop.
func TestFullyConsumedFinalReservationIsExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newBudgetMeter(1024, cancel)

	grant, err := m.reserve(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	m.settle(grant, grant) // fully consumed

	if _, err := m.reserve(context.Background(), 1); !errors.Is(err, ErrSearchBudget) {
		t.Errorf("a read after the ceiling was fully committed got %v, want ErrSearchBudget", err)
	}
	if !m.isExhausted() {
		t.Error("the meter did not latch exhausted")
	}
	select {
	case <-ctx.Done():
	default:
		t.Error("exhaustion did not cancel outstanding search streams")
	}
	if got := m.used(); got != 1024 {
		t.Errorf("committed = %d, want exactly the limit", got)
	}
}

// TestMeterWaitersWakeOnCancellation: a reader parked waiting for leased
// capacity must wake when the job is cancelled, or the search leaks a goroutine
// and never terminates.
func TestMeterWaitersWakeOnCancellation(t *testing.T) {
	m := newBudgetMeter(budgetReadChunk, nil)
	held, err := m.reserve(context.Background(), budgetReadChunk)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	woke := make(chan error, 1)
	go func() {
		_, err := m.reserve(ctx, 1024)
		woke <- err
	}()

	cancel()
	select {
	case err := <-woke:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter woke with %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter did not wake on cancellation")
	}
	m.settle(held, 0)
}

// TestConcurrentReadersNeverExceedTheCommittedLimit re-states the ceiling under
// the reserve/settle split, which is where an off-by-one would hide.
func TestConcurrentReadersNeverExceedTheCommittedLimit(t *testing.T) {
	const limit = 64 << 10
	m := newBudgetMeter(limit, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &budgetReader{ctx: context.Background(), r: bytes.NewReader(make([]byte, 1<<20)), m: m}
			_, _ = io.Copy(io.Discard, r)
		}()
	}
	wg.Wait()

	if got := m.used(); got > limit {
		t.Errorf("committed %d bytes against a limit of %d", got, limit)
	}
}

// TestPagedErrorsAreClassified is WO-101 §3: invalid and unavailable are
// distinct facts about a prefix, and the transport boundary must not erase the
// distinction before catalogue code sees it.
func TestPagedErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want catalogueOutcome
	}{
		{"no response", fmt.Errorf("%w: dial failed", errNoResponse), catalogueUnavailable},
		{"budget", ErrSearchBudget, catalogueUnavailable},
		{"malformed framing", errors.New("malformed response frame: bad json"), catalogueInvalid},
		{"bad terminal", errors.New("terminal frame does not match its digest"), catalogueInvalid},
	} {
		if got := classifyPagedError(tc.err); got != tc.want {
			t.Errorf("%s classified as %v, want %v", tc.name, got, tc.want)
		}
	}
}
