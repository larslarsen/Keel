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
// noticed, and cancelling afterwards cannot un-download it.
//
// # Why reserved and committed are different numbers
//
// The second implementation subtracted a read's whole grant up front. That made
// a *lease* look like a spend: while one reader held the last reservation,
// another would see nothing remaining and latch exhaustion — cancelling every
// stream — even though the first reader was about to short-read and refund most
// of it. The job then reported `budget` with usable allowance still unspent
// (WO-101 §1).
//
// So three quantities are tracked, with the invariant
// `committed + reserved <= limit`:
//
//   - committed: bytes reads have actually consumed;
//   - reserved: bytes leased to reads currently in flight; and
//   - the remainder, which is what a new read may lease.
//
// When every remaining byte is leased but not yet committed, a new read WAITS
// for a settlement rather than declaring the budget spent. Only committed bytes
// reaching the limit is exhaustion, and only then does the job stop.
type budgetMeter struct {
	mu        sync.Mutex
	limit     int64
	committed int64
	reserved  int64
	// release is closed and replaced on every settlement, so readers waiting
	// for leased capacity wake without polling.
	release chan struct{}
	// exhausted latches, so the reason survives the cancellation that follows
	// it. Without the latch, a budget stop and a page cancel are
	// indistinguishable at the terminal, and one is a visibly incomplete
	// success while the other is not a completion at all.
	exhausted bool
	cancel    context.CancelFunc
}

func newBudgetMeter(limit int64, cancel context.CancelFunc) *budgetMeter {
	return &budgetMeter{limit: limit, cancel: cancel, release: make(chan struct{})}
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

// reserve leases up to want bytes, waiting if the remaining capacity is
// currently leased to another in-flight read.
//
// Returns ErrSearchBudget only when committed bytes have reached the ceiling
// and nothing is outstanding to refund — that is the one state in which the
// budget really has prevented further work.
func (m *budgetMeter) reserve(ctx context.Context, want int) (int, error) {
	if m == nil {
		return want, nil
	}
	for {
		m.mu.Lock()
		if m.exhausted {
			m.mu.Unlock()
			return 0, ErrSearchBudget
		}
		free := m.limit - m.committed - m.reserved
		if free > 0 {
			grant := int64(want)
			if grant > free {
				grant = free
			}
			m.reserved += grant
			m.mu.Unlock()
			return int(grant), nil
		}
		if m.reserved == 0 {
			// Nothing is outstanding, so nothing can come back. The ceiling has
			// genuinely prevented further work.
			m.exhausted = true
			cancel := m.cancel
			m.mu.Unlock()
			// Never under the lock: the cancellation runs arbitrary callbacks,
			// and one of them settling a read would deadlock against us.
			if cancel != nil {
				cancel()
			}
			return 0, ErrSearchBudget
		}
		wait := m.release
		m.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// settle commits what a read actually consumed and returns the rest of its
// lease, waking anyone waiting for capacity.
func (m *budgetMeter) settle(grant, used int) {
	if m == nil || grant <= 0 {
		return
	}
	if used < 0 {
		used = 0
	}
	if used > grant {
		used = grant
	}
	m.mu.Lock()
	m.reserved -= int64(grant)
	m.committed += int64(used)
	close(m.release)
	m.release = make(chan struct{})
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

// used is committed bytes: what reads actually consumed, never what was leased.
func (m *budgetMeter) used() int64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.committed
}

// budgetReader is the reader every search-caused response is read through.
//
// Every byte that crosses the wire for a search passes here exactly once,
// whatever the caller later decides about the response — so malformed,
// rejected and explicitly incomplete replies all cost what they actually read.
// A budget charging only for *accepted* responses is one a hostile peer walks
// straight through by sending garbage.
type budgetReader struct {
	ctx context.Context
	r   io.Reader
	m   *budgetMeter
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if len(p) > budgetReadChunk {
		p = p[:budgetReadChunk]
	}
	grant, err := b.m.reserve(b.ctx, len(p))
	if err != nil {
		return 0, err
	}
	if grant == 0 {
		return 0, ErrSearchBudget
	}
	n, readErr := b.r.Read(p[:grant])
	if n < 0 {
		n = 0
	}
	b.m.settle(grant, n)
	return n, readErr
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
