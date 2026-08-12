// SPDX-License-Identifier: Apache-2.0
// Token-sketch gossip (WO-067): the network side of the global per-keyword
// count. See daemon/store/sketch_store.go for the persistence and drift-
// scheduling this publishes from and merges into.
//
// Separate topic from yield-vector gossip (daemon/swarm/yield.go), even
// though both join the same shared *pubsub.PubSub (swarm.go) and mirror
// live.go's overall shape: the two carry differently-shaped payloads (a
// fixed bit-vector vs. a batch of token->sketch pairs) and are rate-limited
// on different schedules (yield on a flat interval; sketch gossip is
// per-token and demand-driven — see RecordTokenSearch's drift scheduling),
// so keeping them as independent topics keeps each easy to reason about and
// tune on its own.
package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/store"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

// SketchTopic carries per-token cardinality sketches, one {index, sketch}
// pair per message.
const SketchTopic = "keel/sketch/1"

const (
	// sketchTickInterval governs both sides of the rate limit: how often
	// this node checks for due tokens to publish, and the window over which
	// the per-peer receive cap resets.
	sketchTickInterval = 30 * time.Second
	// maxSketchGossipPerTick bounds outgoing bandwidth regardless of how
	// many tokens are tracked — the publish-side half of "rate limit the
	// gossip network" (WO-067).
	maxSketchGossipPerTick = 20
	// maxSketchMessagesPerPeerPerTick bounds how many sketch messages
	// arriving via one mesh neighbor get merged per tick — the receive-side
	// half. Generous relative to maxSketchGossipPerTick because
	// ReceivedFrom is a direct mesh neighbor that may be legitimately
	// relaying many other nodes' messages, not just its own; this exists to
	// cap a flood, not to police normal relay traffic.
	maxSketchMessagesPerPeerPerTick = 200
	// maxSketchMessageBytes bounds one message before it is even
	// unmarshaled — a generous multiple of the expected JSON+base64 size of
	// a TokenSketchP sketch (~256 raw bytes), so a legitimately-sized
	// message is never at risk while an oversized one costs nothing to
	// reject.
	maxSketchMessageBytes = 4096
)

// sketchGossipMsg is the wire shape of one gossiped sketch. Short field
// names deliberately, since this travels on gossipsub, unlike the rest of
// the codebase's more verbose wire structs.
type sketchGossipMsg struct {
	I int    `json:"i"` // dictionary index, see tokendict.go
	P uint8  `json:"p"` // must equal store.TokenSketchP — see validateSketchMessage
	R []byte `json:"r"` // registers; encoding/json base64-encodes []byte automatically
}

// SketchIndex tracks per-peer receive volume for the anti-flood rate limit.
// Unlike YieldIndex there is no need to remember anything else per peer —
// received sketches are merged straight into daemon/store's persisted state
// (the source of truth), not held in memory keyed by sender.
type SketchIndex struct {
	mu      sync.Mutex
	perPeer map[peer.ID]int
	topic   *pubsub.Topic
	sub     *pubsub.Subscription
	self    peer.ID
	logf    func(string, ...any)
}

// startSketch joins SketchTopic on the shared pubsub instance. Additive like
// startLive/startYield: failure costs the global-count feature, not the node.
func (n *Node) startSketch(ctx context.Context) error {
	if n.ps == nil {
		return fmt.Errorf("pubsub not available")
	}
	if err := n.ps.RegisterTopicValidator(SketchTopic, validateSketchMessage); err != nil {
		return err
	}
	topic, err := n.ps.Join(SketchTopic)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		return err
	}

	n.sketch = &SketchIndex{
		perPeer: map[peer.ID]int{},
		topic:   topic,
		sub:     sub,
		self:    n.host.ID(),
		logf:    n.logf,
	}
	go n.sketch.consume(ctx, n.st)
	go n.publishSketchLoop(ctx)
	n.logf("sketch gossip subscribed to %s", SketchTopic)
	return nil
}

// validateSketchMessage rejects anything malformed before it reaches a
// subscriber, same reasoning as validateLiveMessage/validateYieldMessage.
// The size check runs before JSON unmarshal so an oversized flood is cheap
// to reject.
func validateSketchMessage(_ context.Context, _ peer.ID, msg *pubsub.Message) bool {
	if len(msg.Data) > maxSketchMessageBytes {
		return false
	}
	var m sketchGossipMsg
	if err := json.Unmarshal(msg.Data, &m); err != nil {
		return false
	}
	if m.I < 0 || m.I >= store.TokenDictSize {
		return false
	}
	// P must match the network-agreed precision exactly, or Merge would
	// simply fail later — reject now rather than pay a wasted merge attempt,
	// and reject rather than accept-and-resize, since resizing would not be
	// the sender's actual data.
	if m.P != store.TokenSketchP {
		return false
	}
	if len(m.R) != 1<<store.TokenSketchP {
		return false
	}
	return true
}

func (si *SketchIndex) consume(ctx context.Context, st Store) {
	for {
		msg, err := si.sub.Next(ctx)
		if err != nil {
			return
		}
		if msg.ReceivedFrom == si.self {
			continue
		}
		if si.overLimit(msg.ReceivedFrom) {
			continue
		}
		var m sketchGossipMsg
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			continue // validator already checked this; defense in depth only
		}
		sk := &store.Sketch{Kind: store.KindToken, P: m.P, Registers: m.R}
		if err := st.MergeTokenSketch(m.I, sk); err != nil {
			si.logf("sketch: merge from %s: %v", msg.ReceivedFrom, err)
		}
	}
}

// overLimit enforces the per-peer receive cap, resetting the count on each
// tick boundary rather than a sliding window — simple, and the tick period
// is already the unit everything else here is rate-limited in.
func (si *SketchIndex) overLimit(p peer.ID) bool {
	si.mu.Lock()
	defer si.mu.Unlock()
	si.perPeer[p]++
	return si.perPeer[p] > maxSketchMessagesPerPeerPerTick
}

func (si *SketchIndex) resetLimits() {
	si.mu.Lock()
	defer si.mu.Unlock()
	si.perPeer = map[peer.ID]int{}
}

// publishSketchLoop is the demand-driven half: unlike yield's flat interval,
// what gets published each tick is whichever tokens RecordTokenSearch's
// drift scheduling marked due — see daemon/store/sketch_store.go.
func (n *Node) publishSketchLoop(ctx context.Context) {
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
	t := time.NewTicker(sketchTickInterval)
	defer t.Stop()
	for {
		n.publishDueSketches(ctx)
		n.sketch.resetLimits()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (n *Node) publishDueSketches(ctx context.Context) {
	// Used to publish whenever the subsystem existed, regardless of policy —
	// so a node that served nothing still advertised three-gram sizes for
	// blocks nobody could fetch from it (WO-077). Since that ticket Level 1
	// does not join the topic at all; this also stops the next tick during a
	// downgrade, before the node is torn down.
	if n.sketch == nil || !n.mayGossipSearchTelemetry() {
		return
	}
	due, err := n.st.DueTokenSketches(maxSketchGossipPerTick)
	if err != nil {
		n.logf("sketch: %v", err)
		return
	}
	for _, row := range due {
		raw, err := json.Marshal(sketchGossipMsg{I: row.TokenIndex, P: row.Sketch.P, R: row.Sketch.Registers})
		if err != nil {
			continue
		}
		if err := n.sketch.topic.Publish(ctx, raw); err != nil {
			n.logf("sketch: publish: %v", err)
			continue
		}
		if err := n.st.MarkTokenGossiped(row.TokenIndex); err != nil {
			n.logf("sketch: mark gossiped: %v", err)
		}
	}
}
