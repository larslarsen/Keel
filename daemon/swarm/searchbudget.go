// SPDX-License-Identifier: Apache-2.0
// The aggregate resource meter and the prefix-coalescing resolver for one
// streaming search (WO-099 §4–§6, WO-100 §1–§3).
package swarm

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ErrSearchBudget ends a read because the job's aggregate ceiling is spent.
//
// A sentinel because the terminal frame depends on telling this apart from a
// verification failure and from a cancellation: budget exhaustion is a visibly
// incomplete *success*, and the other two are not.
var ErrSearchBudget = errors.New("search byte budget exhausted")

// budgetReadChunk bounds one reservation.
//
// A reservation is granted before the bytes are read and refunded afterwards,
// so an oversized reservation would idle balance that another reader could have
// used. 32 KiB is large enough that the reserve/refund bookkeeping is noise
// against the read itself, and small enough that four readers interleave
// smoothly at the 8 MiB floor.
const budgetReadChunk = 32 << 10

// budgetMeter is one search's hard aggregate ceiling on network payload.
//
// # Why the meter lives inside the reader
//
// The first implementation read and decoded a whole response and then charged
// what it had cost. That is accounting, not a ceiling: at the 8 MiB floor a
// single response could read up to the 64 MiB transport cap before anything
// noticed, and cancelling afterwards cannot un-download it. Worse, four
// concurrent readers each consulting the remaining balance could each see room
// and each spend it in full.
//
// So the balance is reserved *before* each read and refunded after: a reader
// can never consume more than it was granted, and the grant comes out of one
// job-wide balance under one lock. Four readers therefore share a real
// ceiling rather than four optimistic copies of it.
//
// # Why exhaustion cancels as well as refusing
//
// Refusing further reads leaves the streams that are already open sitting until
// their ordinary network deadline. Cancelling the job's context lets
// requestPaged reset them promptly, so "the budget is spent" means the work
// stops rather than merely stops growing.
type budgetMeter struct {
	mu        sync.Mutex
	remaining int64
	spent     int64
	// exhausted latches, so the reason survives the cancellation that follows
	// it. Without the latch, a budget stop and a page cancel are
	// indistinguishable at the terminal, and one is a visibly incomplete
	// success while the other is not a completion at all.
	exhausted bool
	cancel    context.CancelFunc
}

func newBudgetMeter(limit int64, cancel context.CancelFunc) *budgetMeter {
	return &budgetMeter{remaining: limit, cancel: cancel}
}

type budgetKey struct{}

// withBudget attaches a meter to every request made under ctx.
func withBudget(ctx context.Context, m *budgetMeter) context.Context {
	return context.WithValue(ctx, budgetKey{}, m)
}

// meterFrom returns the meter for this request, or nil when the caller is not a
// search — prewalk, seed and ordinary catalogue sync are not metered by a
// search budget and must keep working when a search has spent its own.
func meterFrom(ctx context.Context) *budgetMeter {
	m, _ := ctx.Value(budgetKey{}).(*budgetMeter)
	return m
}

// reserve grants up to want bytes from the shared balance, atomically.
//
// Returns 0 when nothing is left, which is the signal to stop reading. The
// grant is a spend: whatever is not actually read must be refunded, or a
// reader that asked for 32 KiB and got 900 bytes would quietly burn the rest.
func (m *budgetMeter) reserve(want int) int {
	if m == nil {
		return want
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.remaining <= 0 {
		if !m.exhausted {
			m.exhausted = true
			if m.cancel != nil {
				defer m.cancel()
			}
		}
		return 0
	}
	grant := int64(want)
	if grant > m.remaining {
		grant = m.remaining
	}
	m.remaining -= grant
	m.spent += grant
	return int(grant)
}

// refund returns unused reservation to the balance.
func (m *budgetMeter) refund(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	m.remaining += int64(n)
	m.spent -= int64(n)
	m.mu.Unlock()
}

func (m *budgetMeter) isExhausted() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exhausted
}

func (m *budgetMeter) used() int64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent
}

// budgetReader is the reader every search-caused response is read through.
//
// Every byte that crosses the wire for a search passes here exactly once,
// whatever the caller later decides about the response — so malformed,
// rejected and explicitly incomplete replies all cost what they actually read.
// A budget charging only for *accepted* responses is one a hostile peer walks
// straight through by sending garbage.
type budgetReader struct {
	r io.Reader
	m *budgetMeter
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if len(p) > budgetReadChunk {
		p = p[:budgetReadChunk]
	}
	grant := b.m.reserve(len(p))
	if grant == 0 {
		return 0, ErrSearchBudget
	}
	n, err := b.r.Read(p[:grant])
	if n < 0 {
		n = 0
	}
	b.m.refund(grant - n)
	return n, err
}

// catalogueOutcome is what a broad prefix traversal actually established
// (WO-100 §2).
//
// The distinction is load-bearing rather than descriptive. A row count alone
// cannot tell "this bucket genuinely holds nothing" from "the peer stopped
// early" from "nobody answered" — and treating the last two as the first
// declares a candidate absent that was never looked for, which then counts as a
// gainless response and can saturate a word that had more to find.
type catalogueOutcome int

const (
	// catalogueUnavailable: no provider answered at all.
	catalogueUnavailable catalogueOutcome = iota
	// catalogueInvalid: a response arrived and failed verification.
	catalogueInvalid
	// catalogueIncomplete: verified pages, but the authenticated terminal says
	// the provider did not finish the bucket. The rows are real and may be
	// cached as public cover data; the prefix is NOT resolved.
	catalogueIncomplete
	// catalogueComplete: a verified complete traversal. Rows may be zero —
	// a complete empty bucket is an answer, not an absence of one.
	catalogueComplete
)

func (o catalogueOutcome) resolved() bool { return o == catalogueComplete }

// catalogueResult is one prefix traversal's typed outcome.
type catalogueResult struct {
	Outcome catalogueOutcome
	Rows    int
}

// prefixGroup coalesces catalogue-prefix traversals across a whole job.
//
// Two token workers that nominate candidates in the same missing prefix must
// join one fetch, not race into two identical complete-bucket downloads. The
// coalescing is job-wide rather than per-call because the workers are
// concurrent by design.
//
// # Why success is memoized and failure is not
//
// The first version removed every entry the moment its call finished, so a
// worker that computed its missing-prefix plan slightly earlier could arrive
// just after the entry vanished and traverse the same bucket again — the
// coalescing only covered exact overlap in time. A verified complete traversal
// is now remembered for the life of the job.
//
// An incomplete, unavailable or invalid traversal is deliberately NOT
// remembered. Caching it would turn one transient peer problem into a prefix
// this search can never resolve, and a candidate inside it into a video that is
// permanently, silently invisible.
//
// Memoization is bounded in-memory job state. There is no durable
// prefix-completion ledger, in SQLite or anywhere else.
type prefixGroup struct {
	mu       sync.Mutex
	inFlight map[string]*prefixCall
	resolved map[string]catalogueResult
}

type prefixCall struct {
	done   chan struct{}
	result catalogueResult
	err    error
}

func newPrefixGroup() *prefixGroup {
	return &prefixGroup{
		inFlight: map[string]*prefixCall{},
		resolved: map[string]catalogueResult{},
	}
}

// resolve traverses prefix, joins a traversal already running for it, or
// returns the memoized result of one that completed.
//
// A waiter wakes on the job's context as well as on the call it joined, so
// cancellation — a page replacing its search, a contribution downgrade, the
// budget running out — never leaves a worker parked on a channel that will not
// close (WO-100 §3).
func (g *prefixGroup) resolve(ctx context.Context, prefix string, fn func() (catalogueResult, error)) (catalogueResult, error) {
	g.mu.Lock()
	if done, ok := g.resolved[prefix]; ok {
		g.mu.Unlock()
		return done, nil
	}
	if call, running := g.inFlight[prefix]; running {
		g.mu.Unlock()
		select {
		case <-call.done:
			return call.result, call.err
		case <-ctx.Done():
			return catalogueResult{Outcome: catalogueUnavailable}, ctx.Err()
		}
	}
	call := &prefixCall{done: make(chan struct{})}
	g.inFlight[prefix] = call
	g.mu.Unlock()

	call.result, call.err = fn()

	g.mu.Lock()
	delete(g.inFlight, prefix)
	if call.err == nil && call.result.Outcome.resolved() {
		g.resolved[prefix] = call.result
	}
	g.mu.Unlock()
	close(call.done)
	return call.result, call.err
}
