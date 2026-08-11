// SPDX-License-Identifier: Apache-2.0
// Yield-vector gossip (WO-067): the network side of daemon/store/yield.go.
//
// Mirrors live.go's shape deliberately — same "gossip what is small and
// ephemeral" posture, joined on the shared *pubsub.PubSub (swarm.go) rather
// than its own instance. Unlike the live index, there is nothing to merge
// across senders: each peer's yield vector simply replaces whatever this
// node last heard from that peer, since it is a full snapshot every time,
// not an incremental fact.
package swarm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/store"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

// YieldTopic carries yield vectors: a bit per dictionary token saying
// "worth fetching from me". No per-item query, so — like LiveTopic — every
// node subscribes regardless of contribution level; publishing is what a
// level gates, not receiving.
const YieldTopic = "keel/yield/1"

// yieldPublishInterval bounds how often this node re-publishes its own
// vector. A vector changes only when the local corpus does, which is slow
// relative to this interval — the point of a fixed period is predictable,
// bounded bandwidth (WO-067's "rate limit the gossip network"), not
// freshness on every write.
const yieldPublishInterval = 10 * time.Minute

// YieldIndex holds the most recent yield vector heard from each peer.
type YieldIndex struct {
	mu     sync.RWMutex
	byPeer map[peer.ID][]byte

	topic *pubsub.Topic
	sub   *pubsub.Subscription
	self  peer.ID
	logf  func(string, ...any)
}

// startYield joins YieldTopic on the shared pubsub instance and begins
// publishing/consuming. Additive like startLive: failure here costs the
// yield-screening optimization, not the node.
func (n *Node) startYield(ctx context.Context) error {
	if n.ps == nil {
		return fmt.Errorf("pubsub not available")
	}
	if err := n.ps.RegisterTopicValidator(YieldTopic, validateYieldMessage); err != nil {
		return err
	}
	topic, err := n.ps.Join(YieldTopic)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		return err
	}

	n.yield = &YieldIndex{
		byPeer: map[peer.ID][]byte{},
		topic:  topic,
		sub:    sub,
		self:   n.host.ID(),
		logf:   n.logf,
	}
	go n.yield.consume(ctx)
	go n.publishYieldLoop(ctx)
	n.logf("yield gossip subscribed to %s", YieldTopic)
	return nil
}

// validateYieldMessage rejects anything not shaped like a real yield vector
// before it reaches a subscriber — same reasoning as validateLiveMessage:
// a malformed flood costs one hop, not network-wide propagation.
func validateYieldMessage(_ context.Context, _ peer.ID, msg *pubsub.Message) bool {
	return len(msg.Data) == store.YieldVectorBytes
}

func (yi *YieldIndex) consume(ctx context.Context) {
	for {
		msg, err := yi.sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == yi.self {
			continue
		}
		yi.mu.Lock()
		yi.byPeer[msg.ReceivedFrom] = msg.Data
		yi.mu.Unlock()
	}
}

// publishYieldLoop periodically computes and publishes this node's own
// vector. A background push on a fixed schedule, decoupled from any single
// search — publishing must never be triggered by or correlated with a
// specific fetch, or the timing itself would leak query activity.
func (n *Node) publishYieldLoop(ctx context.Context) {
	// Publishing into an empty mesh loses the message: gossipsub only
	// reaches peers already in the topic's mesh at publish time, and a
	// freshly connected peer needs a moment (gossipsub's own heartbeat)
	// before it is actually meshed, not merely connected. Wait briefly for
	// at least one connected peer before the first publish; if none shows up
	// in time, the periodic ticker below retries regardless, so this is a
	// head start, not a requirement.
	deadline := time.Now().Add(15 * time.Second)
	for n.Peers() == 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
	if n.Peers() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	n.publishYield(ctx)
	t := time.NewTicker(yieldPublishInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.publishYield(ctx)
		}
	}
}

func (n *Node) publishYield(ctx context.Context) {
	if n.yield == nil || !n.cfg.Serve {
		// Publishing what this node holds is the same disclosure class as
		// serving a shard, so it follows Serve, not Fetch — a Level 1 node
		// (no Serve) still receives everyone else's vectors, same asymmetry
		// as the live index.
		return
	}
	vec, err := n.st.LocalYieldVector(!n.cfg.ServeOwnObservations)
	if err != nil {
		n.logf("yield: %v", err)
		return
	}
	if err := n.yield.topic.Publish(ctx, vec); err != nil {
		n.logf("yield: publish: %v", err)
	}
}

// yieldGet reports whether token is worth fetching from p, per the last
// vector heard from it. known=false means no vector has ever been received
// from p — the caller should treat that exactly as it did before yield
// gossip existed: try the fetch, no evidence either way.
func (n *Node) yieldGet(p peer.ID, token string) (yield bool, known bool) {
	if n.yield == nil {
		return false, false
	}
	idx, ok := store.TokenDictIndex(token)
	if !ok {
		return false, false
	}
	n.yield.mu.RLock()
	vec, seen := n.yield.byPeer[p]
	n.yield.mu.RUnlock()
	if !seen {
		return false, false
	}
	return store.YieldBitSet(vec, idx), true
}
