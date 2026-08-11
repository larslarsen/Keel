// SPDX-License-Identifier: Apache-2.0
// Version negotiation and "am I behind the network" detection (WO-061).
//
// WO-060 made the key-deriving constants deterministic and put the scheme in
// the protocol id, so an incompatible node cannot open a stream. That is the
// static half. This is the live half: what a node *does* when it meets peers on
// other versions, and how a person finds out they need to update.
//
// The rule everywhere is: never silently partition. A version difference either
// connects and degrades with a warning, or refuses with a reason someone can
// read. "Connects but finds nothing" is the one outcome that is not allowed,
// because it is indistinguishable from an empty network (WO-058) and can
// survive for months without anyone noticing.
//
// # Why identify, and not a Keel handshake
//
// The version rides in libp2p's identify AgentVersion — the string every libp2p
// node already exchanges on connect. No new protocol, no extra round trip, and
// nothing to forget to call: by the time a peer is connected, its version is in
// the peerstore. A bespoke handshake would be a second source of truth for
// something libp2p already carries, and every call site that skipped it would
// be a silent partition waiting to happen.
//
// # Why this is a local count and not consensus
//
// "Behind the network" is decided from the peers *this* node has actually seen.
// No global state, no agreement protocol, no blockchain — consistent with
// WO-060's rule that network state may inform a person's decision to update but
// never flips a protocol constant by itself.
package swarm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/store"
)

// keySchemeVersion is the key scheme this build derives keys under (WO-060).
func keySchemeVersion() int { return store.KeySchemeVersion }

// agentPrefix marks a libp2p peer as running Keel.
//
// The public DHT is full of nodes that are not: bootstrap servers, IPFS
// gateways, other applications. Anything not carrying this prefix is not
// counted, in either direction — it is neither a peer we are behind nor one we
// are incompatible with.
const agentPrefix = "keel/"

// AgentVersion is what this build announces to peers: app version and key
// scheme, the two numbers that decide compatibility.
//
// Format: keel/<app>/ks<scheme>. Both parts are needed because they break
// compatibility differently — a newer app is a reason to update, a different
// key scheme is a reason the two nodes cannot exchange anything at all.
func AgentVersion(app string) string {
	if app == "" {
		app = "unknown"
	}
	return fmt.Sprintf("%s%s/ks%d", agentPrefix, app, keySchemeVersion())
}

// parseAgent pulls the app version and key scheme out of an agent string.
//
// ok is false for anything that is not a Keel node, and for Keel strings from a
// future format we do not understand. Both are treated the same way — not
// counted — because guessing at an unfamiliar version is how a bad update
// banner gets shown to everybody.
func parseAgent(s string) (app string, scheme int, ok bool) {
	if !strings.HasPrefix(s, agentPrefix) {
		return "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(s, agentPrefix), "/")
	if len(parts) != 2 || parts[0] == "" || !strings.HasPrefix(parts[1], "ks") {
		return "", 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(parts[1], "ks"))
	if err != nil || n < 0 {
		return "", 0, false
	}
	return parts[0], n, true
}

// compareVersions orders dotted numeric versions: -1, 0, or 1.
//
// Missing components count as zero, so 0.2 and 0.2.0 are the same version.
// Anything non-numeric compares as zero rather than erroring — a malformed
// version should not be able to claim it is newer than everyone.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// VersionView is what this node has observed about the versions around it.
type VersionView struct {
	// Keel peers on our key scheme: the ones we can actually exchange with.
	Compatible int `json:"compatible"`
	// Keel peers on a different key scheme. They are running Keel and they are
	// unreachable — the single most important number here, because it is the
	// case that would otherwise look like an empty network.
	Incompatible int `json:"incompatible"`
	// Compatible peers running a newer app version.
	Newer int `json:"newer"`
	// The newest app version seen on any Keel peer, compatible or not.
	LatestSeen string `json:"latest_seen,omitempty"`
	// Most Keel peers are ahead: worth telling the user, not worth blocking on.
	UpdateAdvised bool `json:"update_advised"`
	// Most Keel peers derive keys differently: this node cannot serve or fetch
	// from them at all, so the update is not optional if they want the network.
	UpdateRequired bool `json:"update_required"`
}

// Versions summarises the versions of currently connected Keel peers.
//
// Only connected peers are counted, deliberately. A version remembered from a
// peer met last week says nothing about whether this node is behind *now*, and
// stale entries would make the banner sticky long after everyone updated.
func (n *Node) Versions(app string) VersionView {
	v := VersionView{}
	ours := keySchemeVersion()
	for _, p := range n.host.Network().Peers() {
		other, scheme, ok := n.peerAgent(p)
		if !ok {
			continue
		}
		if scheme != ours {
			v.Incompatible++
		} else {
			v.Compatible++
			// "unknown" on either side is not comparable. Treating it as a
			// version number would let a build with an unset version make
			// every peer it meets appear to be behind.
			if app != "" && app != "unknown" && other != "unknown" &&
				compareVersions(other, app) > 0 {
				v.Newer++
			}
		}
		if other != "unknown" && (v.LatestSeen == "" || compareVersions(other, v.LatestSeen) > 0) {
			v.LatestSeen = other
		}
	}

	total := v.Compatible + v.Incompatible
	if total == 0 {
		return v
	}
	// A strict majority, so one stray node on a development build cannot
	// summon an update banner for everyone it meets. With a single peer the
	// majority is that peer — which is correct: if the only other install you
	// have met is ahead of you, you are behind.
	v.UpdateAdvised = v.Newer*2 > total
	v.UpdateRequired = v.Incompatible*2 > total
	return v
}

// peerAgent reads a connected peer's advertised version from the peerstore.
func (n *Node) peerAgent(p peer.ID) (app string, scheme int, ok bool) {
	raw, err := n.host.Peerstore().Get(p, "AgentVersion")
	if err != nil {
		return "", 0, false
	}
	s, _ := raw.(string)
	return parseAgent(s)
}
