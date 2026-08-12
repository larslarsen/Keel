// SPDX-License-Identifier: Apache-2.0
// Test-only seams into unexported node state, for this package's own tests.
//
// The subscription questions below are deliberately not part of the real API:
// nothing outside this package should be asking which pubsub topics a node
// happens to have joined. Whether a node is currently *serving* is a different
// matter and is exported properly as Node.Serving (WO-077).
package swarm

// JoinedSearchTopicsForTest reports whether the yield/sketch subsystems were
// constructed at all — the subscription question, distinct from whether
// publication is currently permitted.
func (n *Node) JoinedSearchTopicsForTest() (yield, sketch bool) {
	return n.yield != nil, n.sketch != nil
}

// LiveStartedForTest reports whether the live index was constructed.
func (n *Node) LiveStartedForTest() bool { return n.live != nil }

// MayAnnounceForTest reports whether provider records would be published.
func (n *Node) MayAnnounceForTest() bool { return n.mayAnnounce() }

// MayGossipSearchTelemetryForTest reports whether the three-gram topics would
// be originated on right now.
func (n *Node) MayGossipSearchTelemetryForTest() bool { return n.mayGossipSearchTelemetry() }
