// SPDX-License-Identifier: Apache-2.0
package bridge

import "fmt"

// Bridge API and capability schema revisions (WO-081 / ARCHITECTURE_CURRENT §6).
//
// Envelope v:2 is only the bootstrap frame. Application compatibility is the
// intersection of API ranges and named positive-integer capability revisions.
const (
	APIMin = 1
	APIMax = 1

	CapCore                = "core"
	CapSelectors           = "selectors"
	CapTikTok              = "tiktok"
	CapScrollHistory       = "scroll_history"
	CapPeerSearch          = "peer_search"
	CapWordStats           = "word_stats"
	CapQueue               = "queue"
	CapContributionRuntime = "contribution_runtime"
	// CapNetworkConsent is WO-089's gate. Required, not optional — see below.
	CapNetworkConsent = "network_consent"
	// CapContributionImpact is WO-086's feedback panel. Brand new — unlike
	// peer_search/distributed_search, there is no legacy revision of this RPC
	// to reconcile, so one name serves both HELLO negotiation and
	// ContributionRequiredDetail.Capability.
	CapContributionImpact = "contribution_impact"

	// Stable HELLO_ACK / ERROR codes.
	CodeOK                    = "ok"
	CodeMissingCore           = "missing_core"
	CodeAPINonOverlap         = "api_non_overlap"
	CodeInvalidCapability     = "invalid_capability"
	CodeDuplicateHello        = "duplicate_hello"
	CodeHelloRequired         = "hello_required"
	CodeCapabilityUnavailable = "capability_unavailable"
)

// PeerSearchRevReciprocal is the peer_search revision at which distributed
// search became a Level-2+ entitlement (WO-085).
//
// The revision, rather than a new capability name, is what carries this:
// PEER_SEARCH's request and reply shapes are unchanged, and what changed is
// the contract around when the daemon will answer it. A client that negotiates
// revision 1 is talking to — or is — a build from before the boundary existed,
// and the two must not disagree about whether the control should be offered:
//
//   - New extension, old daemon (negotiated 1): the daemon still answers at
//     Level 1, so the extension leaves the control enabled. Presenting it as
//     level-gated would be a UI-only restriction of a daemon that has no such
//     rule, which is the disagreement this revision exists to prevent.
//   - Old extension, new daemon (negotiated 1): the daemon enforces anyway and
//     replies CodeContributionRequired. The old client cannot render the
//     reciprocal copy, but it shows the message rather than silently failing —
//     the enforcement never depends on the client's revision.
//   - Both new (negotiated 2): the control follows the effective contribution
//     level, refreshed by CONTRIBUTION_STATUS.
const PeerSearchRevReciprocal = 2

// CodeContributionRequired marks an RPC refused because the running
// contribution level does not entitle this node to it (WO-085).
//
// Distinct from CodeCapabilityUnavailable, which means the two builds never
// negotiated the feature, and from CodeNetworkBusy, which means "ask again in
// a moment". This one is a user-actionable setting, and the only one of the
// three whose remedy is a choice rather than an update or a wait.
const CodeContributionRequired = "contribution_required"

// DaemonCaps is every capability this binary can offer, at its highest revision.
func DaemonCaps() map[string]int {
	return map[string]int{
		CapCore:                1,
		CapSelectors:           1,
		CapTikTok:              1,
		CapScrollHistory:       1,
		CapPeerSearch:          PeerSearchRevReciprocal,
		CapWordStats:           1,
		CapQueue:               1,
		CapContributionRuntime: 1,
		CapNetworkConsent:      1,
		CapContributionImpact:  1,
	}
}

// HelloPayload is the first application frame after envelope v:2.
//
// Legacy clients may send only Client/Version; Negotiate treats a missing
// required.core as incompatible (fail closed).
type HelloPayload struct {
	Client        string         `json:"client,omitempty"`
	Version       string         `json:"version,omitempty"` // legacy alias
	ClientVersion string         `json:"client_version,omitempty"`
	API           *APIRange      `json:"api,omitempty"`
	Required      map[string]int `json:"required,omitempty"`
	Optional      map[string]int `json:"optional,omitempty"`
}

// APIRange is an inclusive client-supported API revision span.
type APIRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// HelloAckPayload is HELLO_ACK body.
type HelloAckPayload struct {
	// Legacy fields kept so older UIs still print something useful.
	Server  string `json:"server,omitempty"`
	Version string `json:"version,omitempty"`

	DaemonVersion string         `json:"daemon_version"`
	API           int            `json:"api"`
	Compatible    bool           `json:"compatible"`
	OK            bool           `json:"ok"`
	Code          string         `json:"code,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Capabilities  map[string]int `json:"capabilities,omitempty"`
}

// NegotiateHello selects the highest mutually supported API and capabilities.
// Incompatible pairs still return a filled ack (compatible=false) so the client
// can render actionable copy without guessing.
func NegotiateHello(p HelloPayload, daemonVersion string) HelloAckPayload {
	ack := HelloAckPayload{
		Server:        "keel-daemon",
		Version:       daemonVersion,
		DaemonVersion: daemonVersion,
		Capabilities:  map[string]int{},
	}

	offered := DaemonCaps()

	// Reject non-positive capability revisions in the offer maps.
	if errCode, reason := validateCapMap(p.Required); errCode != "" {
		ack.Code, ack.Reason = errCode, reason
		return ack
	}
	if errCode, reason := validateCapMap(p.Optional); errCode != "" {
		ack.Code, ack.Reason = errCode, reason
		return ack
	}

	// API range. Missing api is treated as the legacy single-revision client
	// that only ever spoke API 1 — but still requires core via required map
	// or, for truly empty legacy HELLO, fails missing_core below.
	apiMin, apiMax := 0, 0
	if p.API != nil {
		apiMin, apiMax = p.API.Min, p.API.Max
	} else if p.Client != "" || p.Version != "" || p.ClientVersion != "" {
		// Pre-WO-081 HELLO: only client/version. Fail closed on capabilities
		// (no required.core) rather than silently accepting.
		apiMin, apiMax = 1, 1
	}
	if apiMin <= 0 || apiMax <= 0 || apiMin > apiMax {
		ack.Code = CodeAPINonOverlap
		ack.Reason = "desktop app update required: invalid API range"
		return ack
	}
	selMin := apiMin
	if selMin < APIMin {
		selMin = APIMin
	}
	selMax := apiMax
	if selMax > APIMax {
		selMax = APIMax
	}
	if selMin > selMax {
		ack.Code = CodeAPINonOverlap
		ack.Reason = "desktop app update required: no overlapping API revision"
		return ack
	}
	ack.API = selMax

	// Required capabilities: each must intersect with what we offer.
	if len(p.Required) == 0 {
		ack.Code = CodeMissingCore
		ack.Reason = "desktop app update required: missing required capability core"
		return ack
	}
	if _, ok := p.Required[CapCore]; !ok {
		ack.Code = CodeMissingCore
		ack.Reason = "desktop app update required: missing required capability core"
		return ack
	}
	for name, want := range p.Required {
		have, ok := offered[name]
		if !ok || have < 1 || want < 1 {
			if name == CapCore {
				ack.Code = CodeMissingCore
				ack.Reason = "desktop app update required: missing required capability core"
				return ack
			}
			ack.Code = CodeInvalidCapability
			ack.Reason = fmt.Sprintf("desktop app update required: required capability %q unavailable", name)
			return ack
		}
		// Highest mutually supported revision.
		rev := want
		if have < rev {
			rev = have
		}
		if rev < 1 {
			ack.Code = CodeInvalidCapability
			ack.Reason = fmt.Sprintf("desktop app update required: invalid revision for %q", name)
			return ack
		}
		ack.Capabilities[name] = rev
	}

	// Optional: include only mutual support; absence means unavailable.
	for name, want := range p.Optional {
		have, ok := offered[name]
		if !ok || have < 1 || want < 1 {
			continue
		}
		rev := want
		if have < rev {
			rev = have
		}
		if rev >= 1 {
			ack.Capabilities[name] = rev
		}
	}

	ack.Compatible = true
	ack.OK = true
	ack.Code = CodeOK
	ack.Reason = "ok"
	return ack
}

func validateCapMap(m map[string]int) (code, reason string) {
	for name, rev := range m {
		if name == "" || rev < 1 {
			return CodeInvalidCapability, fmt.Sprintf("invalid capability revision for %q", name)
		}
	}
	return "", ""
}

// RPCCapability maps an application RPC type to the optional capability that
// gates it. Empty string means core (allowed once HELLO succeeded).
func RPCCapability(typ string) string {
	switch typ {
	case "GET_SELECTORS":
		return CapSelectors
	case "SCROLL_HISTORY":
		return CapScrollHistory
	case "PEER_SEARCH":
		return CapPeerSearch
	case "WORD_STATS":
		return CapWordStats
	case "QUEUE_ADD", "QUEUE_LIST", "QUEUE_ADVANCE", "QUEUE_REMOVE", "QUEUE_REORDER":
		return CapQueue
	case "GET_CONTRIBUTION", "SET_CONTRIBUTION":
		return CapContributionRuntime
	case "GET_NETWORK_CONSENT", "SET_NETWORK_CONSENT":
		return CapNetworkConsent
	case "GET_CONTRIBUTION_IMPACT", "RESET_CONTRIBUTION_IMPACT":
		return CapContributionImpact
	default:
		// TikTok is a surface on IMPRESSIONS/SUGGEST, not a separate RPC.
		// CapTikTok is UI/selector-facing; daemon still accepts tt rows under core.
		return ""
	}
}
