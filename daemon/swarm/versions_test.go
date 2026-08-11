// SPDX-License-Identifier: Apache-2.0
package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"

	"github.com/keel-app/keel/daemon/store"
)

func TestParseAgent(t *testing.T) {
	for _, tc := range []struct {
		in     string
		app    string
		scheme int
		ok     bool
	}{
		{"keel/0.1.0/ks1", "0.1.0", 1, true},
		{"keel/0.2.0/ks2", "0.2.0", 2, true},
		{"keel/unknown/ks1", "unknown", 1, true},

		// Everything else on the public DHT. Counting these would put a number
		// in front of the user that has nothing to do with Keel.
		{"go-ipfs/0.8.0", "", 0, false},
		{"kubo/0.29.0/", "", 0, false},
		{"", "", 0, false},
		// Keel-shaped but not parseable: a future format, or something
		// pretending. Not counted either way.
		{"keel/0.1.0", "", 0, false},
		{"keel//ks1", "", 0, false},
		{"keel/0.1.0/ksX", "", 0, false},
		{"keel/0.1.0/ks1/extra", "", 0, false},
	} {
		app, scheme, ok := parseAgent(tc.in)
		if ok != tc.ok || app != tc.app || scheme != tc.scheme {
			t.Errorf("parseAgent(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.in, app, scheme, ok, tc.app, tc.scheme, tc.ok)
		}
	}

	// The string we announce must be one we can read back, or every node
	// silently fails to recognise every other node.
	app, scheme, ok := parseAgent(AgentVersion("0.1.0"))
	if !ok || app != "0.1.0" || scheme != store.KeySchemeVersion {
		t.Errorf("AgentVersion round-trip = (%q, %d, %v)", app, scheme, ok)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.2.0", -1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "0.9.9", 1},
		// Trailing zeros are not a different version.
		{"0.2", "0.2.0", 0},
		{"0.2.1", "0.2", 1},
		// Double digits are numbers, not text: "0.10.0" is after "0.9.0".
		{"0.10.0", "0.9.0", 1},
		// Garbage must not out-rank a real version, in either direction.
		{"banana", "0.1.0", -1},
		{"0.1.0", "banana", 1},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestVersionsIgnoresNonKeelPeers is the difference between a useful number and
// a frightening one. A node on the public DHT connects to dozens of strangers
// within seconds; if any of them counted, the update banner would be driven by
// IPFS traffic (the same mistake WO-055 fixed for the peer count).
func TestVersionsIgnoresNonKeelPeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st := newStore(t, "ver.sqlite")
	n, err := Start(ctx, st, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	// A plain libp2p host: connected, and not Keel.
	stranger, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.UserAgent("kubo/0.29.0"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()
	if err := stranger.Connect(ctx, n.AddrInfo()); err != nil {
		t.Fatal(err)
	}

	// identify runs on connect; give it a moment to populate the peerstore.
	var v VersionView
	for i := 0; i < 50; i++ {
		v = n.Versions("0.1.0")
		if len(n.host.Network().Peers()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v.Compatible != 0 || v.Incompatible != 0 || v.Newer != 0 {
		t.Errorf("a non-Keel peer was counted: %+v", v)
	}
	if v.UpdateAdvised || v.UpdateRequired {
		t.Errorf("a non-Keel peer triggered an update prompt: %+v", v)
	}
}

// TestVersionsSeesNewerAndIncompatiblePeers covers WO-061's whole point: the
// two mismatches must be distinguishable, because they need different answers.
// A newer peer means "you could update"; a peer on another key scheme means
// "you cannot talk to them at all", which is the case that would otherwise be
// mistaken for an empty network.
func TestVersionsSeesNewerAndIncompatiblePeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st := newStore(t, "ver2.sqlite")
	n, err := Start(ctx, st, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	connect := func(agent string) {
		t.Helper()
		h, err := libp2p.New(
			libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
			libp2p.UserAgent(agent),
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.Close() })
		if err := h.Connect(ctx, n.AddrInfo()); err != nil {
			t.Fatal(err)
		}
	}

	// Two peers ahead of us on our own key scheme, one on a scheme we cannot
	// derive keys for.
	connect(AgentVersion("0.9.0"))
	connect(AgentVersion("0.9.0"))
	connect("keel/0.9.0/ks9")

	var v VersionView
	for i := 0; i < 100; i++ {
		v = n.Versions("0.1.0")
		if v.Compatible+v.Incompatible >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if v.Compatible != 2 {
		t.Errorf("compatible = %d, want 2 (%+v)", v.Compatible, v)
	}
	if v.Incompatible != 1 {
		t.Errorf("incompatible = %d, want 1 (%+v)", v.Incompatible, v)
	}
	if v.Newer != 2 {
		t.Errorf("newer = %d, want 2 (%+v)", v.Newer, v)
	}
	if v.LatestSeen != "0.9.0" {
		t.Errorf("latest seen = %q, want 0.9.0", v.LatestSeen)
	}
	// 2 of 3 are ahead: advised. Only 1 of 3 is unreachable: not required.
	if !v.UpdateAdvised {
		t.Errorf("majority of peers are newer but no update advised: %+v", v)
	}
	if v.UpdateRequired {
		t.Errorf("a minority on another key scheme forced a required update: %+v", v)
	}
}

// TestUnknownVersionNeverMakesPeersLookNewer guards a build with an unset
// version string. Comparing "" numerically would make it zero, every real peer
// would out-rank it, and every such build would nag its user forever.
func TestUnknownVersionNeverMakesPeersLookNewer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st := newStore(t, "ver3.sqlite")
	n, err := Start(ctx, st, isolated(true, t))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.UserAgent(AgentVersion("0.9.0")),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.Connect(ctx, n.AddrInfo()); err != nil {
		t.Fatal(err)
	}

	var v VersionView
	for i := 0; i < 100; i++ {
		v = n.Versions("")
		if v.Compatible > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v.Compatible != 1 {
		t.Fatalf("peer not seen: %+v", v)
	}
	if v.Newer != 0 || v.UpdateAdvised {
		t.Errorf("a build with no version decided it was behind: %+v", v)
	}
}
