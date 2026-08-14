// SPDX-License-Identifier: Apache-2.0
package bridge

import "testing"

func TestNegotiateCompatibleFull(t *testing.T) {
	ack := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional: map[string]int{
			CapSelectors: 1, CapTikTok: 1, CapScrollHistory: 1,
			CapPeerSearch: 1, CapWordStats: 1, CapQueue: 1,
			CapContributionRuntime: 1,
		},
	}, "0.1.0")
	if !ack.Compatible || !ack.OK || ack.Code != CodeOK {
		t.Fatalf("want compatible ok, got %+v", ack)
	}
	if ack.API != 1 || ack.Capabilities[CapCore] != 1 {
		t.Fatalf("api/core: %+v", ack)
	}
	for _, c := range []string{CapSelectors, CapPeerSearch, CapQueue, CapContributionRuntime} {
		if ack.Capabilities[c] != 1 {
			t.Fatalf("missing optional %s: %+v", c, ack.Capabilities)
		}
	}
}

func TestNegotiateMissingCore(t *testing.T) {
	ack := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{},
	}, "0.1.0")
	if ack.Compatible || ack.Code != CodeMissingCore {
		t.Fatalf("got %+v", ack)
	}
}

func TestNegotiateLegacyHelloFailsClosed(t *testing.T) {
	// Old extension: only client/version, no required map.
	ack := NegotiateHello(HelloPayload{Client: "keel-extension", Version: "0.1.0"}, "0.1.0")
	if ack.Compatible || ack.Code != CodeMissingCore {
		t.Fatalf("legacy must fail closed: %+v", ack)
	}
}

func TestNegotiateAPINonOverlap(t *testing.T) {
	ack := NegotiateHello(HelloPayload{
		ClientVersion: "9.0.0",
		API:           &APIRange{Min: 99, Max: 99},
		Required:      map[string]int{CapCore: 1},
	}, "0.1.0")
	if ack.Compatible || ack.Code != CodeAPINonOverlap {
		t.Fatalf("got %+v", ack)
	}
}

func TestNegotiateInvalidCapabilityRevision(t *testing.T) {
	ack := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 0},
	}, "0.1.0")
	if ack.Compatible || ack.Code != CodeInvalidCapability {
		t.Fatalf("got %+v", ack)
	}
}

func TestNegotiateOptionalAbsence(t *testing.T) {
	// Client asks only for core + queue; peer_search absent from optional.
	ack := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional:      map[string]int{CapQueue: 1},
	}, "0.1.0")
	if !ack.Compatible {
		t.Fatalf("got %+v", ack)
	}
	if _, ok := ack.Capabilities[CapPeerSearch]; ok {
		t.Fatalf("peer_search should be absent: %+v", ack.Capabilities)
	}
	if ack.Capabilities[CapQueue] != 1 {
		t.Fatalf("queue: %+v", ack.Capabilities)
	}
}

func TestRPCCapabilityMap(t *testing.T) {
	if RPCCapability("IMPRESSIONS") != "" {
		t.Fatal("core RPCs must not require optional caps")
	}
	if RPCCapability("PEER_SEARCH") != CapPeerSearch {
		t.Fatal("PEER_SEARCH")
	}
	if RPCCapability("SET_CONTRIBUTION") != CapContributionRuntime {
		t.Fatal("SET_CONTRIBUTION")
	}
	if RPCCapability("GET_NETWORK_CONSENT") != CapNetworkConsent {
		t.Fatal("GET_NETWORK_CONSENT")
	}
	if RPCCapability("SET_NETWORK_CONSENT") != CapNetworkConsent {
		t.Fatal("SET_NETWORK_CONSENT")
	}
}

// TestNegotiateSelectsLowerOfTheTwoRevisions pins what "highest mutually
// supported revision" means when each side advertises a single integer.
//
// The integer is a *maximum*, and both sides are assumed to still speak every
// revision below it — so the highest both support is the lower of the two. That
// reading is what makes the field useful: without it a client one revision
// ahead of the daemon would have to either fail closed (lockstep releases, the
// thing WO-081 exists to avoid) or guess.
func TestNegotiateSelectsLowerOfTheTwoRevisions(t *testing.T) {
	offered := DaemonCaps()
	if offered[CapQueue] != 1 {
		t.Skipf("this test assumes the daemon offers queue:1, got %d", offered[CapQueue])
	}

	// Client ahead of the daemon: negotiate down to what the daemon has.
	ahead := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional:      map[string]int{CapQueue: 7},
	}, "0.1.0")
	if !ahead.Compatible {
		t.Fatalf("a client ahead on an optional capability must still connect: %+v", ahead)
	}
	if got := ahead.Capabilities[CapQueue]; got != 1 {
		t.Errorf("queue negotiated to %d, want the daemon's 1", got)
	}

	// Client behind the daemon: negotiate down to what the client has. Same
	// rule, and the direction that actually ships — the browser store updates
	// the extension on its own schedule.
	behind := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional:      map[string]int{CapQueue: 1},
	}, "0.1.0")
	if !behind.Compatible {
		t.Fatalf("a client behind on an optional capability must still connect: %+v", behind)
	}
	if got := behind.Capabilities[CapQueue]; got != 1 {
		t.Errorf("queue negotiated to %d, want the client's 1", got)
	}

	// A required capability negotiates by the same rule rather than by exact
	// match, or every core revision bump would be a flag day.
	req := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 9},
	}, "0.1.0")
	if !req.Compatible {
		t.Fatalf("a client ahead on core must negotiate down, not fail: %+v", req)
	}
	if got := req.Capabilities[CapCore]; got != offered[CapCore] {
		t.Errorf("core negotiated to %d, want %d", got, offered[CapCore])
	}
}

// TestPeerSearchRevisionNegotiatesTheReciprocalContract is WO-085's
// compatibility half.
//
// The two builds must not disagree about whether the control exists. What
// carries that is peer_search's revision, not a new capability name: the RPC's
// shapes are unchanged and only the rule about when the daemon answers moved.
// A client that negotiates 1 is talking to, or is, a build from before the
// boundary — so it must not present the control as level-gated, because that
// daemon would have answered.
func TestPeerSearchRevisionNegotiatesTheReciprocalContract(t *testing.T) {
	// The daemon offers its highest revision; negotiation is what brings a
	// client down to the contract it can actually honour. WO-095 raised the
	// ceiling to the streaming revision without changing what revision 2
	// means, which is the property the rest of this test pins.
	if got := DaemonCaps()[CapPeerSearch]; got != PeerSearchRevStreaming {
		t.Fatalf("daemon offers peer_search:%d, want %d", got, PeerSearchRevStreaming)
	}

	current := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional:      map[string]int{CapPeerSearch: PeerSearchRevReciprocal},
	}, "0.1.0")
	if got := current.Capabilities[CapPeerSearch]; got != PeerSearchRevReciprocal {
		t.Errorf("an extension whose ceiling is the reciprocal revision negotiated "+
			"peer_search:%d, want %d — a daemon that streamed at a client which "+
			"only asked for the atomic reply would send it unsolicited envelopes",
			got, PeerSearchRevReciprocal)
	}

	// An extension from before WO-085 still connects and still gets peer
	// search — at revision 1, which is its signal to leave the control alone.
	// Enforcement does not depend on this: the daemon refuses at Level 1
	// whatever revision was negotiated (see handlePeerSearchContext).
	old := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1},
		Optional:      map[string]int{CapPeerSearch: 1},
	}, "0.1.0")
	if !old.Compatible {
		t.Fatalf("a pre-WO-085 extension no longer connects: %+v", old)
	}
	if got := old.Capabilities[CapPeerSearch]; got != 1 {
		t.Errorf("a pre-WO-085 extension negotiated peer_search:%d, want 1", got)
	}
}
