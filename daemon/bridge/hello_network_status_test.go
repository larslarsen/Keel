// SPDX-License-Identifier: Apache-2.0
// WO-093 §4: network_status:1 must be purely additive.
package bridge

import "testing"

func TestNetworkStatusNegotiatesAdditively(t *testing.T) {
	if got := DaemonCaps()[CapNetworkStatus]; got != 1 {
		t.Fatalf("daemon offers network_status:%d, want 1", got)
	}

	// Current pair: the extension gets the typed health payload.
	current := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1, CapNetworkConsent: 1},
		Optional:      map[string]int{CapNetworkStatus: 1},
	}, "0.1.0")
	if !current.Compatible {
		t.Fatalf("current pair incompatible: %+v", current)
	}
	if got := current.Capabilities[CapNetworkStatus]; got != 1 {
		t.Fatalf("negotiated network_status:%d, want 1", got)
	}

	// Older extension, new daemon: it never asks, so it never receives the
	// capability, and it goes on ignoring an additive object in the reply.
	old := NegotiateHello(HelloPayload{
		ClientVersion: "0.1.0",
		API:           &APIRange{Min: 1, Max: 1},
		Required:      map[string]int{CapCore: 1, CapNetworkConsent: 1},
		Optional:      map[string]int{CapQueue: 1},
	}, "0.1.0")
	if !old.Compatible {
		t.Fatalf("an extension that predates network_status must still connect: %+v", old)
	}
	if _, ok := old.Capabilities[CapNetworkStatus]; ok {
		t.Fatal("network_status was granted to a client that did not offer it")
	}

	// New extension, old daemon: absence is the signal. The extension must be
	// able to tell "not negotiated" from "negotiated and zero peers", which is
	// only possible because this stays optional and simply does not appear.
	if RPCCapability("GET_STATS") == CapNetworkStatus {
		t.Fatal("network_status must gate no RPC; it revisions an existing reply")
	}
}
