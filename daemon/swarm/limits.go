// SPDX-License-Identifier: Apache-2.0
// Per-node serving limits (WO-085 §5).
//
// The contribution level decides *whether* this node answers other people.
// Nothing in it decides *how much*, and it must not: a level gate is an
// honest-client product contract, and a modified client can simply ask anyway.
// So every serving path passes through this limiter, at every level — the
// word-telemetry stream at Level 1 exactly as much as the shard stream at
// Level 2.
//
// Three separate bounds, because they fail differently:
//
//   - Concurrency, global and per peer. Bounds the work in flight. Without the
//     per-peer half, one peer opening a hundred streams occupies every global
//     slot and the node is unavailable to everyone else while still, formally,
//     within its concurrency budget.
//   - Request rate per peer, as a token bucket. Bounds sustained demand that
//     concurrency alone permits: a client that opens one stream at a time but
//     never stops costs nothing in slots and everything in disk.
//   - Bytes served, as a rolling window over all peers. The one bound that is
//     about the resource the user actually pays for. It is global rather than
//     per peer on purpose: a modified client can present as many peer ids as
//     it likes, so a per-peer byte budget bounds nothing an attacker cannot
//     multiply. Sybil identities are cheap; the uplink is not.
//
// Refusal is silence: the handler closes the stream without a reply, which is
// what a requester already sees from a node that holds nothing in the bucket
// (see handleBlockRequest). Distinguishing "rate limited" from "nothing here"
// on the wire would tell a prober how much of the limit is left and would give
// a serving node a second, quieter identity signal — how loaded it is.
package swarm

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// maxConcurrentServes bounds inbound serve streams in flight across all
	// peers. Each one holds a SQLite read and up to a bucket's worth of JSON
	// in memory, so this is chosen against memory rather than against CPU.
	maxConcurrentServes = 8

	// maxConcurrentServesPerPeer keeps one requester from taking every slot.
	// Two rather than one because a genuine client legitimately overlaps a
	// graph fetch with the catalogue fetch that labels it (see syncCatalogue).
	maxConcurrentServesPerPeer = 2

	// serveBurstPerPeer and serveRefillPerPeer are the token bucket: one
	// request per peer per interval sustained, with a burst on top.
	//
	// The burst is large because honest bulk demand is genuinely bursty and
	// multiplied: a graph bucket reply triggers a catalogue fetch for every
	// prefix its targets fall in (syncCatalogue), and a client catching up on
	// a cold cache walks many neighbourhoods back to back — three or more
	// requests per neighbourhood, as fast as the disk allows. A burst tuned to
	// a single watch page would refuse that legitimate client while barely
	// inconveniencing a modified one, which is the wrong trade in both
	// directions. The bounds that stop an attacker are the concurrency caps
	// and the byte budget; this one is for fairness between peers.
	serveBurstPerPeer  = 64
	serveRefillPerPeer = 100 * time.Millisecond

	// serveByteBudget over serveByteWindow bounds sustained upload. 64 MiB per
	// minute is far above any honest client's demand — a whole prefix bucket
	// is orders of magnitude smaller — and far below what an unbounded
	// serving loop would push over a home uplink.
	serveByteBudget = 64 << 20
	serveByteWindow = time.Minute

	// maxTrackedPeers bounds the limiter's own memory. Peer state is cheap but
	// unbounded requesters are not, and a modified client can mint identities
	// for free — the sweep below is what keeps that from becoming the leak.
	maxTrackedPeers = 4096
)

// serveLimiter admits or refuses one inbound serve request.
//
// One instance per node, constructed in newNode so no node can exist without
// one. Every method is safe from any goroutine; libp2p calls stream handlers
// concurrently.
type serveLimiter struct {
	mu sync.Mutex

	inflight int
	peers    map[peer.ID]*peerLimit

	// bytesUsed is the rolling window's consumption; windowStart is when it
	// began. A fixed window rather than a leaky bucket because the bound that
	// matters is "how much can leave this machine in a minute", and a fixed
	// window states that directly.
	bytesUsed   int64
	windowStart time.Time

	// now is time.Now, replaceable in tests so a rate-limit test does not have
	// to sleep through a real refill interval.
	now func() time.Time
}

// peerLimit is one requester's concurrency and rate state.
type peerLimit struct {
	inflight int
	tokens   float64
	last     time.Time
	seen     time.Time
}

func newServeLimiter() *serveLimiter {
	return &serveLimiter{
		peers: map[peer.ID]*peerLimit{},
		now:   time.Now,
	}
}

// admit reserves a serve slot for one request from p.
//
// The returned release must be called exactly once when the request finishes,
// and only when ok is true. A refusal consumes no token: a client that is
// already over its limit should not be pushed further behind by asking again,
// or a brief overload becomes a permanent lockout.
func (l *serveLimiter) admit(p peer.ID) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.inflight >= maxConcurrentServes {
		return nil, false
	}
	now := l.now()
	pl := l.peerState(p, now)
	if pl.inflight >= maxConcurrentServesPerPeer {
		return nil, false
	}
	if !pl.take(now) {
		return nil, false
	}

	l.inflight++
	pl.inflight++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.inflight--
			pl.inflight--
		})
	}, true
}

// chargeBytes reports whether n more bytes may be sent, and records them.
//
// Called with the reply already built but not yet written, so an oversized
// reply is dropped rather than truncated: half a bucket is not a smaller
// answer, it is a wrong one — the requester cannot tell it apart from a peer
// that genuinely holds only half, and the anonymity claim of "the whole bucket
// or nothing" depends on that never happening.
func (l *serveLimiter) chargeBytes(n int) bool {
	if n <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if l.windowStart.IsZero() || now.Sub(l.windowStart) >= serveByteWindow {
		l.windowStart, l.bytesUsed = now, 0
	}
	if l.bytesUsed+int64(n) > serveByteBudget {
		return false
	}
	l.bytesUsed += int64(n)
	return true
}

// peerState returns p's limit state, creating it if needed. Caller holds mu.
func (l *serveLimiter) peerState(p peer.ID, now time.Time) *peerLimit {
	pl := l.peers[p]
	if pl == nil {
		if len(l.peers) >= maxTrackedPeers {
			l.sweep(now)
		}
		pl = &peerLimit{tokens: serveBurstPerPeer, last: now}
		l.peers[p] = pl
	}
	pl.seen = now
	return pl
}

// sweep drops idle peer state so a churn of identities cannot grow the map
// without bound. Entries with work in flight are never dropped — their release
// closure still refers to them.
func (l *serveLimiter) sweep(now time.Time) {
	cutoff := now.Add(-serveByteWindow)
	for id, pl := range l.peers {
		if pl.inflight == 0 && pl.seen.Before(cutoff) {
			delete(l.peers, id)
		}
	}
	if len(l.peers) < maxTrackedPeers {
		return
	}
	// Still full: every tracked peer is recent or busy, which is itself the
	// overload case. Drop the idle ones regardless of age rather than let the
	// map grow — a dropped entry only restores that peer's burst allowance,
	// and the global bounds above still hold.
	for id, pl := range l.peers {
		if pl.inflight == 0 {
			delete(l.peers, id)
		}
	}
}

// take refills the bucket for elapsed time and spends one token.
func (pl *peerLimit) take(now time.Time) bool {
	if !pl.last.IsZero() {
		if elapsed := now.Sub(pl.last); elapsed > 0 {
			pl.tokens += float64(elapsed) / float64(serveRefillPerPeer)
			if pl.tokens > serveBurstPerPeer {
				pl.tokens = serveBurstPerPeer
			}
		}
	}
	pl.last = now
	if pl.tokens < 1 {
		return false
	}
	pl.tokens--
	return true
}
