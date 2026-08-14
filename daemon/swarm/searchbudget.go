// SPDX-License-Identifier: Apache-2.0
// The aggregate resource meter and the prefix-coalescing resolver for one
// streaming search (WO-099 §4, §5, §6).
package swarm

import (
	"context"
	"sync"
)

// budgetMeter is one search's hard aggregate ceiling on network payload.
//
// # Why it is metered at the reader and not at the caller
//
// The first implementation added up the bytes `fetchShardPagesCounted`
// returned. That silently excluded two things that dominate a real search:
// the complete broad catalogue buckets fetched to resolve candidate titles, and
// every byte read from a response that was then rejected as malformed or
// unsigned. A budget that does not charge for rejected reads is a budget a
// hostile peer can walk straight through — send garbage, cost nothing.
//
// So the meter lives at the shared paged-reader boundary, where every byte a
// search causes to cross the wire passes exactly once, whatever the caller
// eventually decides about the response.
//
// # Why exhaustion cancels rather than merely reporting
//
// Workers observe the balance independently. If exhaustion were only a flag
// each worker checked before its next request, four workers could each see
// "room for one more" and each spend the remainder in full. Cancelling the
// job's context is what makes the ceiling a ceiling: outstanding reads stop,
// and no worker starts another.
type budgetMeter struct {
	mu     sync.Mutex
	spent  int64
	limit  int64
	cancel context.CancelFunc
	// exhausted latches, so the reason survives the cancellation that follows
	// it. Without the latch a budget stop and a page cancel would be
	// indistinguishable at the terminal, and one is a visibly incomplete
	// success while the other is not a completion at all.
	exhausted bool
}

type budgetKey struct{}

// withBudget attaches a meter to every request made under ctx.
func withBudget(ctx context.Context, m *budgetMeter) context.Context {
	return context.WithValue(ctx, budgetKey{}, m)
}

// meterFrom returns the meter for this request, or nil when the caller is not a
// search — prewalk, seed and catalogue sync are not metered by a search budget.
func meterFrom(ctx context.Context) *budgetMeter {
	m, _ := ctx.Value(budgetKey{}).(*budgetMeter)
	return m
}

// charge adds bytes actually read and cancels the search once the ceiling is
// crossed. Called even for responses that are about to be rejected.
func (m *budgetMeter) charge(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	m.spent += int64(n)
	over := m.spent >= m.limit && !m.exhausted
	if over {
		m.exhausted = true
	}
	cancel := m.cancel
	m.mu.Unlock()
	if over && cancel != nil {
		cancel()
	}
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

// prefixGroup coalesces catalogue-prefix traversals across a whole job.
//
// Two token workers that nominate candidates in the same missing prefix must
// join one fetch, not race into two identical complete-bucket downloads. The
// coalescing is job-wide rather than per-call because the workers are
// concurrent by design: per-call coalescing (which is all
// MissingCataloguePrefixes gives on its own) collapses duplicates inside one
// worker's batch and does nothing about the other three.
//
// A failed traversal is NOT cached. Its candidates stay retryable, so a later
// eligible response can resolve them (WO-099 §5); caching the failure would
// turn one transient peer problem into a permanently invisible video.
type prefixGroup struct {
	mu      sync.Mutex
	inFlight map[string]*prefixCall
}

type prefixCall struct {
	done chan struct{}
	rows int
	err  error
}

func newPrefixGroup() *prefixGroup {
	return &prefixGroup{inFlight: map[string]*prefixCall{}}
}

// do runs fn for prefix, or waits for the traversal already running for it.
func (g *prefixGroup) do(prefix string, fn func() (int, error)) (int, error) {
	g.mu.Lock()
	if call, running := g.inFlight[prefix]; running {
		g.mu.Unlock()
		<-call.done
		return call.rows, call.err
	}
	call := &prefixCall{done: make(chan struct{})}
	g.inFlight[prefix] = call
	g.mu.Unlock()

	call.rows, call.err = fn()

	g.mu.Lock()
	delete(g.inFlight, prefix)
	g.mu.Unlock()
	close(call.done)
	return call.rows, call.err
}
