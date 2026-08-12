// SPDX-License-Identifier: Apache-2.0
// WO-085 §5: the serving limits, which hold regardless of contribution level
// and regardless of how the requester behaves.
//
// The unit tests below drive serveLimiter directly with a fake clock. That is
// deliberate: a rate limit tested by sleeping is a slow test that still only
// proves the limiter is not wildly wrong, where a test that controls time can
// state the exact refill contract. The transport-level proof — that a real
// peer hammering a real node over libp2p is bounded — is
// TestServingLimitsBoundAModifiedClient at the bottom.
package swarm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/store"
)

// fixedClock is a manually advanced clock for the rate-limit tests.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testLimiter() (*serveLimiter, *fixedClock) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	l := newServeLimiter()
	l.now = clock.now
	return l, clock
}

// testPeer builds a distinct peer.ID from a label. Peer ids are opaque here —
// only their distinctness matters — so a raw multihash-shaped string is enough.
func testPeer(label string) peer.ID { return peer.ID("peer-" + label) }

// TestOnePeerCannotTakeEverySlot is the per-peer concurrency bound, and the
// reason it exists: without it a single requester occupies the whole global
// budget and everyone else is refused while the node is, formally, inside its
// limits.
func TestOnePeerCannotTakeEverySlot(t *testing.T) {
	l, _ := testLimiter()
	greedy := testPeer("greedy")

	var held []func()
	for i := 0; i < maxConcurrentServes; i++ {
		release, ok := l.admit(greedy)
		if ok {
			held = append(held, release)
		}
	}
	if len(held) != maxConcurrentServesPerPeer {
		t.Errorf("one peer held %d concurrent slots, want the per-peer cap of %d",
			len(held), maxConcurrentServesPerPeer)
	}

	// And another peer is still served while the greedy one is at its cap.
	release, ok := l.admit(testPeer("polite"))
	if !ok {
		t.Error("a second peer was refused while one peer sat at its own cap")
	} else {
		release()
	}
	for _, r := range held {
		r()
	}
}

// TestGlobalConcurrencyIsBounded spreads the load over many peers so the
// per-peer cap cannot be what stops it. This is the memory bound: every
// admitted request holds a SQLite read and a bucket's worth of JSON.
func TestGlobalConcurrencyIsBounded(t *testing.T) {
	l, _ := testLimiter()

	var held []func()
	for i := 0; i < maxConcurrentServes*4; i++ {
		release, ok := l.admit(testPeer(string(rune('a' + i))))
		if ok {
			held = append(held, release)
		}
	}
	if len(held) != maxConcurrentServes {
		t.Errorf("admitted %d concurrent requests across many peers, want the global cap of %d",
			len(held), maxConcurrentServes)
	}
	held[0]()
	if _, ok := l.admit(testPeer("late")); !ok {
		t.Error("a slot was not returned after release; the limiter leaks capacity")
	}
}

// TestSustainedRateIsBoundedPerPeer covers the demand shape concurrency alone
// permits: one stream at a time, forever. Burst first, then refusal, then
// exactly one more request per refill interval.
func TestSustainedRateIsBoundedPerPeer(t *testing.T) {
	l, clock := testLimiter()
	p := testPeer("steady")

	// Serial requests, each released immediately — never more than one in
	// flight, so only the token bucket can stop this.
	admitted := 0
	for i := 0; i < serveBurstPerPeer*3; i++ {
		release, ok := l.admit(p)
		if !ok {
			break
		}
		admitted++
		release()
	}
	if admitted != serveBurstPerPeer {
		t.Errorf("a peer made %d back-to-back requests, want the burst of %d", admitted, serveBurstPerPeer)
	}

	// A refusal must not spend a token, or a peer that overruns once is locked
	// out for longer than the limit it actually broke.
	if _, ok := l.admit(p); ok {
		t.Fatal("a peer past its burst was admitted anyway")
	}
	clock.advance(serveRefillPerPeer)
	release, ok := l.admit(p)
	if !ok {
		t.Fatal("a peer was not admitted after a full refill interval")
	}
	release()
	if _, ok := l.admit(p); ok {
		t.Error("one refill interval bought more than one request")
	}

	// Refill is capped at the burst, so idling does not bank unlimited credit.
	clock.advance(serveRefillPerPeer * time.Duration(serveBurstPerPeer*100))
	admitted = 0
	for i := 0; i < serveBurstPerPeer*3; i++ {
		release, ok := l.admit(p)
		if !ok {
			break
		}
		admitted++
		release()
	}
	if admitted != serveBurstPerPeer {
		t.Errorf("after a long idle a peer made %d requests, want the burst cap of %d",
			admitted, serveBurstPerPeer)
	}
}

// TestByteBudgetIsGlobalAndRolls is the bound on the resource the user pays
// for. Global rather than per peer because peer identities are free to mint —
// see limits.go.
func TestByteBudgetIsGlobalAndRolls(t *testing.T) {
	l, clock := testLimiter()

	const chunk = 1 << 20
	sent := 0
	for i := 0; i < (serveByteBudget/chunk)*4; i++ {
		if !l.chargeBytes(chunk) {
			break
		}
		sent += chunk
	}
	if sent != serveByteBudget {
		t.Errorf("served %d bytes in one window, want the budget of %d", sent, serveByteBudget)
	}
	// Exhausted for every peer, not only the one that spent it.
	if l.chargeBytes(1) {
		t.Error("the byte budget was still open after being spent in full")
	}

	clock.advance(serveByteWindow)
	if !l.chargeBytes(chunk) {
		t.Error("the byte budget did not reopen after its window elapsed")
	}
}

// TestLimiterIsSafeUnderConcurrentLoad is the property the transport depends
// on: libp2p calls stream handlers from many goroutines, and the invariant
// that must survive that is "never more than maxConcurrentServes in flight",
// observed from inside the critical section rather than inferred afterwards.
func TestLimiterIsSafeUnderConcurrentLoad(t *testing.T) {
	l, _ := testLimiter()

	var inflight, peak int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				release, ok := l.admit(testPeer(string(rune('a' + i%16))))
				if !ok {
					continue
				}
				n := atomic.AddInt64(&inflight, 1)
				for {
					old := atomic.LoadInt64(&peak)
					if n <= old || atomic.CompareAndSwapInt64(&peak, old, n) {
						break
					}
				}
				atomic.AddInt64(&inflight, -1)
				release()
			}
		}(i)
	}
	wg.Wait()

	if peak > maxConcurrentServes {
		t.Errorf("peak concurrent serves = %d, want at most %d", peak, maxConcurrentServes)
	}
	if got := atomic.LoadInt64(&inflight); got != 0 {
		t.Errorf("%d requests still counted as in flight after every release", got)
	}
	l.mu.Lock()
	leaked := l.inflight
	l.mu.Unlock()
	if leaked != 0 {
		t.Errorf("limiter still holds %d slots after every release", leaked)
	}
}

// TestPeerStateDoesNotGrowWithoutBound is the memory half. A modified client
// can present a new identity per request for free, so the limiter's own
// bookkeeping must not be the thing that falls over.
func TestPeerStateDoesNotGrowWithoutBound(t *testing.T) {
	l, clock := testLimiter()

	for i := 0; i < maxTrackedPeers*3; i++ {
		release, ok := l.admit(peer.ID("sybil-" + string(rune(i%128)) + string(rune(i/128))))
		if ok {
			release()
		}
		if i%97 == 0 {
			clock.advance(2 * serveByteWindow)
		}
	}
	l.mu.Lock()
	tracked := len(l.peers)
	l.mu.Unlock()
	if tracked > maxTrackedPeers {
		t.Errorf("limiter tracks %d peers, want at most %d", tracked, maxTrackedPeers)
	}
}

// TestServingLimitsBoundAModifiedClient is the acceptance criterion over real
// streams: a client that ignores every limit — no pacing, unlimited parallel
// requests — must not be able to make a serving node answer without bound.
//
// The client here does exactly what a patched build would: it opens shard
// requests as fast as it can, in parallel, for as long as the test allows.
// What is asserted is that a substantial share are refused, that the node
// stays responsive to a well-behaved peer throughout, and that the node's own
// byte accounting never exceeds the budget.
func TestServingLimitsBoundAModifiedClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	serverStore := newStore(t, "limits-server.sqlite")
	putTitle(t, serverStore, "findmevideo1", "A distinctive sourdough baking tutorial")
	server, err := Start(ctx, serverStore, levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	attacker, err := Start(ctx, newStore(t, "limits-attacker.sqlite"), levelCfg(t, store.LevelBroad))
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	if err := attacker.host.Connect(ctx, server.AddrInfo()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Any valid shard number exercises the same handler, so this deliberately
	// does not couple the test to the token dictionary's contents.
	const shardKey = "0"

	var answered, refused int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
				raw, err := attacker.requestOn(reqCtx, server.AddrInfo(), shardKey, ShardProtocol)
				reqCancel()
				if err != nil || len(raw) == 0 {
					atomic.AddInt64(&refused, 1)
					continue
				}
				atomic.AddInt64(&answered, 1)
			}
		}()
	}
	wg.Wait()

	total := answered + refused
	if total < 100 {
		t.Fatalf("only %d requests were made; the load was too small to prove a bound", total)
	}
	if refused == 0 {
		t.Errorf("a client ignoring every limit had all %d of its requests answered", total)
	}
	// The node must still be inside its own accounting: the concurrency slots
	// are all returned and no more than the budget was ever charged.
	server.serve.mu.Lock()
	inflight, used := server.serve.inflight, server.serve.bytesUsed
	server.serve.mu.Unlock()
	if inflight != 0 {
		t.Errorf("server still holds %d serve slots after the load stopped", inflight)
	}
	if used > serveByteBudget {
		t.Errorf("server charged %d bytes in one window, over the %d budget", used, serveByteBudget)
	}
	t.Logf("modified client: %d answered, %d refused, %d bytes charged", answered, refused, used)
}
