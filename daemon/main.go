// SPDX-License-Identifier: Apache-2.0
// Keel desktop host: native-messaging proxy + single local daemon owner.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
	"github.com/keel-app/keel/daemon/swarm"
)

const version = "0.1.0"

// builtAt is when this binary was written, read from the executable itself.
//
// Reported to the interface because "is the running daemon the one I just
// built?" has come up repeatedly, and the answer has never been visible. The
// browser keeps a native-messaging process alive across extension reloads, so a
// rebuilt binary is not necessarily the one answering.
func builtAt() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fi.ModTime().Format("2006-01-02 15:04:05")
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("keel: ")
	// Native messaging: protocol on stdin/stdout. Logs must not go to stdout.
	log.SetOutput(os.Stderr)

	switch cmd, rest := dispatch(runtime.GOOS, os.Args[1:]); cmd {
	case "install":
		os.Exit(runInstall(rest))
	case "uninstall":
		os.Exit(runUninstall(rest))
	case "owner":
		os.Exit(runOwnerCommand(rest))
	case "keys":
		os.Exit(runKeys(rest))
	case "bundle":
		os.Exit(runBundle(rest))
	case "sketch":
		os.Exit(runSketch(rest))
	case "seed":
		os.Exit(runSeed(rest))
	case "version":
		fmt.Println("keel-host", version)
		os.Exit(0)
	case "help":
		usage()
		os.Exit(0)
	}

	os.Exit(runProxy())
}

// dispatch decides what an invocation means, from the arguments alone.
//
// Subcommands are matched explicitly rather than with flag.Parse: the browser
// launches this binary with the caller's origin as argv[1], so generic flag
// parsing would misread a normal native-messaging start.
//
// On Windows, no arguments at all means install (WO-091). That is the
// double-click, and it is the only install route available to someone who
// cannot open a terminal. A browser always passes its origin or manifest path,
// so a native-messaging launch still has arguments and still becomes a proxy —
// installing can never happen underneath a running browser.
func dispatch(goos string, args []string) (cmd string, rest []string) {
	if len(args) == 0 {
		if goos == "windows" {
			return "install", nil
		}
		return "", nil
	}
	switch name := strings.TrimLeft(args[0], "-"); name {
	case "install", "uninstall", "owner", "keys", "bundle", "sketch", "seed", "version", "help":
		return name, args[1:]
	}
	return "", args
}

// syncWriter serializes writes from multiple goroutines onto one underlying
// writer. See the comment above main's run() call for why this exists.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func run(in io.Reader, out io.Writer, st *store.Store) {
	runContext(context.Background(), in, out, st)
}

// bridgeSession tracks HELLO negotiation for one native/owner connection (WO-081).
type bridgeSession struct {
	helloOK bool
	caps    map[string]int
	// jobs are this session's running streaming searches (WO-095 §3). Scoped
	// to the session on purpose: a job's events go to the connection that
	// started it and to no other, so two browsers on one machine never see
	// each other's search activity. Keyed by the client-minted search id.
	jobMu sync.Mutex
	jobs  map[string]*searchJob
	// onReady fires once, when negotiation succeeds. The owner uses it to join
	// a session to the broadcast hub only after both sides have agreed an API
	// and capability set — see serveOwner.
	onReady func()
}

func runContext(ctx context.Context, in io.Reader, out io.Writer, st *store.Store) {
	runSession(ctx, in, out, st, nil)
}

// runSession is runContext with a negotiation hook. Separate rather than a
// wider runContext so the plain native-messaging path cannot forget to pass one.
func runSession(
	ctx context.Context, in io.Reader, out io.Writer, st *store.Store, onReady func(),
) {
	sess := &bridgeSession{onReady: onReady}
	// A session that goes away takes its searches with it (WO-095 §4). Work
	// whose results nobody can receive must not keep spending other people's
	// serving budget.
	defer sess.cancelAllJobs()
	for {
		raw, err := bridge.ReadMessage(in)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return
			}
			log.Printf("read error (connection continues if possible): %v", err)
			// Framing errors on stdin are usually fatal for the stream.
			return
		}
		if err := handleRawContext(ctx, raw, out, st, sess); err != nil {
			log.Printf("handle: %v", err)
		}
	}
}

func handleRaw(raw []byte, out io.Writer, st *store.Store) error {
	// Tests call handleRaw without a live session; treat as already negotiated
	// with full daemon caps so RPC handlers stay exercisable in isolation.
	sess := &bridgeSession{helloOK: true, caps: bridge.DaemonCaps()}
	return handleRawContext(context.Background(), raw, out, st, sess)
}

func handleRawContext(ctx context.Context, raw []byte, out io.Writer, st *store.Store, sess *bridgeSession) error {
	if sess == nil {
		sess = &bridgeSession{}
	}
	env, err := bridge.ParseEnvelope(raw)
	if err != nil {
		log.Printf("drop malformed envelope: %v", err)
		// Cannot correlate — best-effort ERROR without id
		return writeEnv(out, &bridge.Envelope{
			V:       bridge.ProtocolV,
			ID:      "0",
			Type:    "ERROR",
			Payload: mustJSON(bridge.ErrorPayload{Message: err.Error(), Code: "bad_envelope"}),
		})
	}

	if env.Type == "HELLO" {
		return handleHello(env, out, sess)
	}
	if !sess.helloOK {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "HELLO required before application RPCs",
			Code:    bridge.CodeHelloRequired,
		})
	}
	if need := bridge.RPCCapability(env.Type); need != "" {
		if rev, ok := sess.caps[need]; !ok || rev < 1 {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
				Message: fmt.Sprintf("capability %q not negotiated", need),
				Code:    bridge.CodeCapabilityUnavailable,
			})
		}
	}

	switch env.Type {
	case "IMPRESSIONS":
		return handleImpressions(env, out, st)
	case "LIVE_SIGHTINGS":
		return handleLiveSightings(env, out)
	case "STATS":
		stats, err := st.Stats()
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		// Whether the network is doing anything is otherwise only visible in
		// the daemon's log, which nobody reads. A user who turns on sharing
		// deserves to see whether it connected to anything at all.
		return reply(out, env.ID, "STATS_RESULT", struct {
			*bridge.StatsPayload
			Swarm  map[string]any `json:"swarm"`
			Daemon map[string]any `json:"daemon"`
		}{stats, swarmStatus(), map[string]any{
			"version":  version,
			"built_at": builtAt(),
		}})
	case "EXPORT":
		return handleExport(env, out, st)
	case "WIPE":
		return handleWipe(env, out, st)
	case "SEARCH":
		return handleSearch(env, out, st)
	case "PEER_SEARCH":
		// Revision 3 turns this into a job start; revision 2 keeps its atomic
		// reply and receives no events (WO-095 §3). The client's own revision
		// selects the path — it sends a search_id only when it negotiated 3 and
		// is prepared to consume events, so a revision-2 client can never be
		// handed a stream it would log as a series of unsolicited envelopes.
		if sess.caps[bridge.CapPeerSearch] >= bridge.PeerSearchRevStreaming {
			var probe bridge.SearchPayload
			if json.Unmarshal(env.Payload, &probe) == nil && probe.SearchID != "" {
				return handlePeerSearchStart(ctx, env, out, st, sess)
			}
		}
		return handlePeerSearchContext(ctx, env, out, st)
	case "PEER_SEARCH_CANCEL":
		return handlePeerSearchCancel(env, out, sess)
	case "WORD_STATS":
		return handleWordStatsContext(ctx, env, out, st)
	case "SUGGEST":
		return handleSuggest(env, out, st)
	case "SCROLL_HISTORY":
		return handleScrollHistory(env, out, st)
	case "EXPORT_BUNDLE":
		return handleExportBundle(env, out, st)
	case "IMPORT_BUNDLE":
		return handleImportBundle(env, out, st)
	case "SET_COHORT":
		var p struct {
			Locale string `json:"locale"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
				Message: "locale required", Code: "bad_payload",
			})
		}
		c, err := st.SetCohort(p.Locale)
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "COHORT_RESULT", map[string]any{"cohort": c})
	case "PEERS":
		return handlePeers(env, out, st)
	case "FORGET_PEER":
		return handleForgetPeer(env, out, st)
	case "AGGREGATE_SUMMARY":
		sum, err := st.AggregateSummary(cohortFor(st))
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "AGGREGATE_SUMMARY_RESULT", sum)
	case "THUMBNAIL":
		var p struct {
			VideoID string `json:"video_id"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: "video_id required", Code: "bad_payload"})
		}
		url, err := st.Thumbnail(p.VideoID)
		if err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: err.Error(), Code: "thumb_failed"})
		}
		return reply(out, env.ID, "THUMBNAIL_RESULT", map[string]any{"video_id": p.VideoID, "data_url": url})
	case "GET_NETWORK_CONSENT":
		return reply(out, env.ID, "NETWORK_CONSENT_RESULT", contributionPayload(supervisor.state(st)))
	case "SET_NETWORK_CONSENT":
		return handleSetNetworkConsent(ctx, env, out, st)
	case "GET_CONTRIBUTION":
		return reply(out, env.ID, "CONTRIBUTION_RESULT", contributionPayload(supervisor.state(st)))
	case "SET_CONTRIBUTION":
		// Keep the correlated result and owner-wide terminal event in the same
		// order as policy transitions. supervisor.apply serializes replacement,
		// but without this outer bridge lock a second session could complete and
		// broadcast a newer level before the first handler published its event.
		contributionBridgeMu.Lock()
		defer contributionBridgeMu.Unlock()
		var p struct {
			Level int `json:"level"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: "level required", Code: "bad_payload"})
		}
		// Reconfigures the running swarm, not just SQLite (WO-077). Returns
		// the state either way: after a failed transition the effective level
		// is the thing the user most needs to see, so an error still carries
		// what is actually running rather than only a message.
		before := supervisor.state(st)
		state, err := supervisor.apply(context.Background(), st, p.Level)
		if err != nil {
			writeErr := reply(out, env.ID, "ERROR", bridge.ErrorPayload{
				Message: err.Error(), Code: "contribution_failed", Detail: contributionPayload(state),
			})
			if state != before {
				publishContributionStatus(ctx, state)
			}
			return writeErr
		}
		writeErr := reply(out, env.ID, "CONTRIBUTION_RESULT", contributionPayload(state))
		if state != before {
			publishContributionStatus(ctx, state)
		}
		return writeErr
	case "GET_CONTRIBUTION_IMPACT":
		return handleGetContributionImpact(env, out, st)
	case "RESET_CONTRIBUTION_IMPACT":
		if err := st.ResetContributionActivity(); err != nil {
			return replyErr(out, env.ID, err)
		}
		return handleGetContributionImpact(env, out, st)
	case "GET_DISK_BUDGET":
		used, items, err := st.CacheUsage()
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "DISK_BUDGET_RESULT", map[string]any{
			"budget_bytes": st.DiskBudget(), "used_bytes": used, "items": items,
			"min_bytes": store.MinDiskBudget,
		})
	case "SET_DISK_BUDGET":
		var p struct {
			Bytes int64 `json:"bytes"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: "bytes required", Code: "bad_payload"})
		}
		b, err := st.SetDiskBudget(p.Bytes)
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		used, items, _ := st.CacheUsage()
		return reply(out, env.ID, "DISK_BUDGET_RESULT", map[string]any{
			"budget_bytes": b, "used_bytes": used, "items": items, "min_bytes": store.MinDiskBudget,
		})
	case "ANALYSIS":
		res, err := st.Analysis()
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "ANALYSIS_RESULT", res)
	case "GET_BLOCKLIST":
		list, err := st.ListBlocklist()
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "BLOCKLIST_RESULT", bridge.BlocklistPayload{Blocklist: list})
	case "BLOCK_CHANNEL":
		return handleBlockChannel(env, out, st, true)
	case "UNBLOCK_CHANNEL":
		return handleBlockChannel(env, out, st, false)
	case "QUEUE_ADD":
		var p bridge.QueuePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.VideoID == "" {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
				Message: "video_id required", Code: "bad_payload"})
		}
		if err := st.AddToQueue(p.VideoID, p.Platform, time.Now().UnixMilli()); err != nil {
			return replyErr(out, env.ID, err)
		}
		return replyQueue(out, env.ID, st)
	case "QUEUE_LIST":
		return replyQueue(out, env.ID, st)
	case "QUEUE_ADVANCE":
		var p bridge.QueuePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.VideoID == "" {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
				Message: "video_id required", Code: "bad_payload"})
		}
		next, err := st.AdvanceQueue(p.VideoID)
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		items, err := st.ListQueue()
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "QUEUE_RESULT",
			bridge.QueueResultPayload{Items: items, Next: next})
	case "QUEUE_REMOVE":
		var p bridge.QueueIndexPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
				Message: "index required", Code: "bad_payload"})
		}
		if err := st.RemoveFromQueue(p.Index); err != nil {
			return replyErr(out, env.ID, err)
		}
		return replyQueue(out, env.ID, st)
	case "QUEUE_REORDER":
		var p bridge.QueueIndexPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
				Message: "from and to required", Code: "bad_payload"})
		}
		if err := st.ReorderQueue(p.From, p.To); err != nil {
			return replyErr(out, env.ID, err)
		}
		return replyQueue(out, env.ID, st)
	case "EXPLAIN_VIDEO":
		return handleExplainVideo(env, out, st)
	case "GET_SELECTORS":
		return handleGetSelectors(env, out)
	case "LIVE_SEARCH":
		return handleLiveSearch(env, out, st)
	default:
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: fmt.Sprintf("unknown type %q", env.Type),
			Code:    "unknown_type",
		})
	}
}

// handleHello is the only application frame accepted before negotiation.
// A successful ack arms the session; a failed one leaves helloOK false so
// every later RPC is rejected with hello_required (WO-081).
func handleHello(env *bridge.Envelope, out io.Writer, sess *bridgeSession) error {
	if sess.helloOK {
		return reply(out, env.ID, "HELLO_ACK", bridge.HelloAckPayload{
			Server:        "keel-daemon",
			Version:       version,
			DaemonVersion: version,
			Compatible:    false,
			OK:            false,
			Code:          bridge.CodeDuplicateHello,
			Reason:        "duplicate HELLO on an already-negotiated session",
		})
	}
	var p bridge.HelloPayload
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			ack := bridge.HelloAckPayload{
				Server:        "keel-daemon",
				Version:       version,
				DaemonVersion: version,
				Code:          bridge.CodeInvalidCapability,
				Reason:        "invalid HELLO payload",
			}
			return reply(out, env.ID, "HELLO_ACK", ack)
		}
	}
	ack := bridge.NegotiateHello(p, version)
	if ack.Compatible {
		sess.helloOK = true
		sess.caps = ack.Capabilities
		if sess.onReady != nil {
			sess.onReady()
		}
	}
	// The ack goes out either way. An incompatible client still needs the code
	// and reason to render actionable copy; what it does not get is a
	// negotiated session or a place in the broadcast hub.
	return reply(out, env.ID, "HELLO_ACK", ack)
}

var contributionBridgeMu sync.Mutex

// handleLiveSearch answers from the in-memory live index.
//
// The query is matched against records this node already holds, so nothing is
// sent anywhere — the whole point of gossiping an index small enough to hold in
// full (DESIGN_v2 §7.5).
// replyQueue answers every queue mutation with the resulting queue.
//
// The caller always needs the new order, and returning it here means the
// interface never has to guess what a mutation did or issue a second call to
// find out.
func replyQueue(out io.Writer, id string, st *store.Store) error {
	items, err := st.ListQueue()
	if err != nil {
		return replyErr(out, id, err)
	}
	return reply(out, id, "QUEUE_RESULT", bridge.QueueResultPayload{Items: items})
}

// handleSetNetworkConsent records or withdraws the WO-089 network disclosure
// decision, and moves the network to match it in the same call.
//
// The two halves are inseparable on purpose. A grant that only wrote a row
// would leave the user looking at "accepted" with no network until they
// restarted the daemon, which is the precise failure WO-077 fixed for
// contribution levels; a withdrawal that only wrote a row would leave a node
// running after the user revoked permission, which is worse. So the durable
// record and the running state move together, in the order that fails safe:
// grant persists first and then starts, withdraw stops first and then persists.
func handleSetNetworkConsent(ctx context.Context, env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p struct {
		// Accepted is the decision. Absent or false is a decline, which is a
		// valid answer and not an error.
		Accepted bool `json:"accepted"`
		// Revision the client says it displayed. Required for an acceptance:
		// the client must name the disclosure it actually rendered, so a stale
		// screen cannot accept wording it never showed.
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid SET_NETWORK_CONSENT payload",
			Code:    "bad_payload",
		})
	}

	// Serialized with contribution changes: both decide whether a node exists,
	// and interleaving them could publish a status that describes neither.
	contributionBridgeMu.Lock()
	defer contributionBridgeMu.Unlock()
	before := supervisor.state(st)

	if !p.Accepted {
		// Withdraw: stop first, then forget. The reverse order would leave a
		// window in which the record says "no" while the host is still up, and
		// a crash inside that window would restart into a running network with
		// no consent behind it.
		supervisor.stopForWithdrawnConsent()
		if err := st.WithdrawNetworkConsent(); err != nil {
			return replyErr(out, env.ID, err)
		}
		state := supervisor.state(st)
		writeErr := reply(out, env.ID, "NETWORK_CONSENT_RESULT", contributionPayload(state))
		if state != before {
			publishContributionStatus(ctx, state)
		}
		return writeErr
	}

	if _, err := st.GrantNetworkConsent(p.Revision); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: err.Error(),
			Code:    "consent_rejected",
			Detail:  contributionPayload(supervisor.state(st)),
		})
	}
	// Durable before effective, matching the downgrade ordering in
	// contribution_runtime.go: a crash between the two restarts into a network
	// the user has agreed to, never into one they have not.
	state := supervisor.resumeAfterConsent(context.Background(), st)
	writeErr := reply(out, env.ID, "NETWORK_CONSENT_RESULT", contributionPayload(state))
	if state != before {
		publishContributionStatus(ctx, state)
	}
	return writeErr
}

func handleLiveSearch(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(env.Payload) > 0 {
		_ = json.Unmarshal(env.Payload, &p)
	}
	if networkBusy() {
		return replyNetworkBusy(out, env.ID)
	}
	n := currentSwarmNode()
	if n == nil || n.Live() == nil {
		// Two different unavailabilities, and the interface needs to tell them
		// apart (WO-089). "Live starts at Level 2" is a setting the user can
		// change; "the network is not up" is a machine state they cannot. The
		// old copy said only the latter, which after WO-089 would have been a
		// plain lie to every default-level user.
		reason := "not connected to the network yet"
		code := ""
		if allowed, _ := liveAllowed(st); !allowed {
			reason = "Live starts at Broad sharing: the shared feed is built " +
				"from livestream sightings people publish, so it is available " +
				"to the levels that publish them."
			code = bridge.CodeContributionRequired
		}
		return reply(out, env.ID, "LIVE_RESULT", map[string]any{
			"query": p.Query, "streams": []any{}, "indexed": 0,
			"available":      false,
			"reason":         reason,
			"code":           code,
			"required_level": store.LevelBroad,
		})
	}
	li := n.Live()
	hits := li.Search(p.Query, p.Limit)
	return reply(out, env.ID, "LIVE_RESULT", map[string]any{
		"query": p.Query, "streams": hits, "indexed": li.Size(), "available": true,
	})
}

func handleExplainVideo(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.ExplainVideoPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.VideoID == "" {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid EXPLAIN_VIDEO payload",
			Code:    "bad_payload",
		})
	}
	res, err := st.ExplainVideo(p.VideoID)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	return reply(out, env.ID, "EXPLAIN_RESULT", res)
}

func handleBlockChannel(env *bridge.Envelope, out io.Writer, st *store.Store, block bool) error {
	var p bridge.ChannelBlockPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid channel payload",
			Code:    "bad_payload",
		})
	}
	var err error
	if block {
		err = st.BlockChannel(p.ChannelID)
	} else {
		err = st.UnblockChannel(p.ChannelID)
	}
	if err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: err.Error(),
			Code:    "blocklist_failed",
		})
	}
	list, err := st.ListBlocklist()
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	return reply(out, env.ID, "BLOCKLIST_RESULT", bridge.BlocklistPayload{Blocklist: list})
}

// handleExport writes the full corpus to ~/Downloads (or KEEL_EXPORT_DIR)
// and returns only path/rows/bytes on the bridge (WO-012).
func handleExport(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	dir, err := store.DownloadsDir()
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	// Filename uses wall clock; safe for concurrent exports within a second.
	name := fmt.Sprintf("keel-export-%s.json", time.Now().UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)
	rows, nbytes, err := st.ExportToFile(path, version)
	if err != nil {
		log.Printf("export failed: %v", err)
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: err.Error(),
			Code:    "export_failed",
		})
	}
	return reply(out, env.ID, "EXPORT_RESULT", bridge.ExportResultPayload{
		Path:  path,
		Rows:  rows,
		Bytes: nbytes,
	})
}

func handleWipe(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	n, err := st.Wipe()
	if err != nil {
		log.Printf("wipe failed: %v", err)
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: err.Error(),
			Code:    "wipe_failed",
		})
	}
	return reply(out, env.ID, "WIPE_RESULT", bridge.WipeResultPayload{Deleted: n})
}

func handleImpressions(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.ImpressionsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		log.Printf("drop bad IMPRESSIONS payload: %v", err)
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid IMPRESSIONS payload",
			Code:    "bad_payload",
		})
	}
	n, err := st.PutImpressions(p.Impressions)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	announceLive(p.Impressions)
	// §5d's prewarm: the observer has just told us which video is being
	// watched, which is the earliest possible moment to start fetching its
	// neighbourhood — well before the panel asks for suggestions.
	for _, imp := range p.Impressions {
		if imp.ContextVideoID != nil {
			prewarm(st, *imp.ContextVideoID)
			break
		}
	}
	return reply(out, env.ID, "IMPRESSIONS_ACK", map[string]any{
		"inserted": n,
		"received": len(p.Impressions),
	})
}

// handleLiveSightings keeps rendered stream discovery out of the durable
// impression catalogue. Page, slot and sender context are dropped here.
func handleLiveSightings(env *bridge.Envelope, out io.Writer) error {
	var p bridge.LiveSightingsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: "invalid LIVE_SIGHTINGS payload", Code: "bad_payload"})
	}
	n := currentSwarmNode()
	if n == nil || n.Live() == nil || !n.MayPublishLive() {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: "Live sharing starts at Broad sharing", Code: bridge.CodeContributionRequired,
			Detail: bridge.ContributionRequiredDetail{Capability: "live", RequiredLevel: store.LevelBroad, EffectiveLevel: supervisor.effectiveLevel()}})
	}
	accepted := 0
	for i := range p.Sightings {
		s := &p.Sightings[i]
		if err := bridge.ValidateLiveSighting(s); err != nil {
			continue
		}
		r := swarm.LiveRecord{Platform: "tt", LiveLocator: s.LiveLocator, ChannelID: s.ChannelID, Title: s.Title, SeenAt: s.ObservedAt, StartedAt: s.ObservedAt}
		if !swarm.ValidLiveRecord(r) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		published := n.PublishLive(ctx, r)
		cancel()
		if published {
			accepted++
		}
	}
	return reply(out, env.ID, "LIVE_SIGHTINGS_ACK", map[string]any{"accepted": accepted, "rejected": len(p.Sightings) - accepted})
}

func reply(out io.Writer, id, typ string, payload any) error {
	env, err := bridge.NewEnvelope(id, typ, payload)
	if err != nil {
		return err
	}
	return writeEnv(out, env)
}

func replyErr(out io.Writer, id string, err error) error {
	return reply(out, id, "ERROR", bridge.ErrorPayload{Message: err.Error(), Code: "internal"})
}

func writeEnv(out io.Writer, env *bridge.Envelope) error {
	b, err := env.Encode()
	if err != nil {
		return err
	}
	if len(b) <= bridge.MaxHostToBrowser {
		return bridge.WriteMessage(out, b)
	}
	// Response exceeds the 1 MiB host→browser native-messaging cap. Dropping
	// it (returning nil) leaves the client's request() promise hanging until
	// its 8s timeout with no error — a silent failure. Instead reply with a
	// small ERROR envelope carrying the same correlation id, so the client
	// rejects cleanly and the interface can say why.
	log.Printf("oversized response %d bytes (cap %d) for id %s type %s",
		len(b), bridge.MaxHostToBrowser, env.ID, env.Type)
	errEnv, e := bridge.NewEnvelope(env.ID, "ERROR", bridge.ErrorPayload{
		Message: fmt.Sprintf("response %d bytes exceeds 1 MiB native-messaging limit", len(b)),
		Code:    "response_too_large",
	})
	if e != nil {
		return e
	}
	eb, e := errEnv.Encode()
	if e != nil {
		return e
	}
	return bridge.WriteMessage(out, eb)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// usage documents the subcommands. Native messaging itself takes no arguments.
func usage() {
	fmt.Print(`keel-host — Keel desktop host

Usually launched by the browser; not run by hand.
On Windows, running it with no arguments installs (that is the double-click).

  keel-host install   -extension-id <id>[,<id>]  register with detected browsers
                      [-firefox-id keel@local] [-all] [-dry-run]
  keel-host uninstall [-dry-run]                 remove host manifests
  keel-host owner status                         show whether the local owner runs
  keel-host owner stop                           stop the local owner cleanly

  keel-host bundle export [-out FILE]            write your aggregated corpus
  keel-host bundle import FILE|URL                 merge someone else's bundle
  keel-host bundle peers                         list imported bundles
  keel-host bundle forget NODE-ID                remove one
  keel-host keys                                show your signing fingerprint
  keel-host bundle summary                       what would leave this device

  keel-host version
`)
}

// handleSearch runs a catalogue search (WO-022). Read-only.
func handleSearch(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.SearchPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid SEARCH payload",
			Code:    "bad_payload",
		})
	}
	res, err := st.SearchVideos(p.Query, p.Limit)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	// Every local search carries the canonical render plan, including when peer
	// search is off, unavailable or unentitled (WO-095 §1). The extension never
	// retokenizes a query: one tokenizer, in the daemon, is what stops the
	// interface drawing bars that disagree with the work being done — which is
	// exactly what the page's own three-character chopping used to do.
	res.Plan = planWire(store.BuildQueryPlan(p.Query))
	return reply(out, env.ID, "SEARCH_RESULT", res)
}

// peerSearchTimeout bounds one PEER_SEARCH request's internal work. WO-070:
// this was 30s, which is longer than the extension's 8s client-side request
// timeout (extension/lib/native.js request()) by construction — the daemon
// could never reply in time for a genuinely slow case, only for a fast one,
// which defeats having a server-side bound at all. Also shared by
// handleWordStats (WO-068) for the same reason; that handler has the same
// synchronous-on-the-bridge-thread shape this fix gives handlePeerSearch,
// but restructuring it is out of this ticket's scope.
const peerSearchTimeout = 6 * time.Second

// contributionRequiredMessage is the one refusal wording both PEER_SEARCH
// entry points use (WO-085). Shared so the atomic path and the streaming job
// cannot drift into telling the user two different things about one setting.
const contributionRequiredMessage = "searching other people's recommendations needs broad sharing: " +
	"distributed search runs on the machines that also answer it, so it is " +
	"available to the levels that serve. Local search, suggestions, " +
	"pre-walk, Live and word statistics are unaffected."

// handlePeerSearch searches the swarm's token shards for a query (WO-059),
// distinct from handleSearch's purely local catalogue lookup.
//
// Runs on its own goroutine (WO-070, same reasoning as handleSuggest/
// WO-069): a synchronous handler here blocks every other RPC for as long as
// the shard fetches take, and PeerSearch's own per-token loop can run well
// past the client's 8s cap on a query with several tokens even when peers
// exist. The zero-peers case is additionally fast-pathed inside PeerSearch
// itself (swarm/shard.go) rather than only here, since any caller benefits
// from not walking dead fetches when there is nothing to fetch from.
func handlePeerSearch(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	return handlePeerSearchContext(context.Background(), env, out, st)
}

func handlePeerSearchContext(requestCtx context.Context, env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.SearchPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid PEER_SEARCH payload",
			Code:    "bad_payload",
		})
	}
	if networkBusy() {
		return replyNetworkBusy(out, env.ID)
	}
	// The entitlement is checked here, before the node is touched, so a
	// Level-1 refusal cannot reach a peer by any path (WO-085). swarm.PeerSearch
	// refuses too — this is not the only guard, it is the one that can say why
	// in terms the interface can act on.
	if allowed, level := distributedSearchLevel(st); !allowed {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: contributionRequiredMessage,
			Code:    bridge.CodeContributionRequired,
			Detail: bridge.ContributionRequiredDetail{
				Capability:     bridge.CapDistributedSearch,
				RequiredLevel:  store.LevelBroad,
				EffectiveLevel: level,
			},
		})
	}
	n := currentSwarmNode()
	if n == nil {
		// Mirrors handleLiveSearch: no swarm running means unavailable, not
		// an empty result — the interface must not read this as "the network
		// has nothing for this query."
		return reply(out, env.ID, "PEER_SEARCH_RESULT", bridge.PeerSearchResultPayload{
			Query: p.Query, Hits: []bridge.SearchHit{}, Available: false,
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(requestCtx, peerSearchTimeout)
		defer cancel()
		ids, progress, err := n.PeerSearch(ctx, p.Query)
		if err != nil {
			_ = replyErr(out, env.ID, err)
			return
		}
		hits, err := st.TitlesFor(ids)
		if err != nil {
			_ = replyErr(out, env.ID, err)
			return
		}
		if p.Limit > 0 && len(hits) > p.Limit {
			hits = hits[:p.Limit]
		}
		wireProgress := make([]bridge.TokenProgress, 0, len(progress))
		for _, tp := range progress {
			wireProgress = append(wireProgress, bridge.TokenProgress{
				TokenIndex: tp.TokenIndex, Fetched: tp.Fetched, Target: tp.Target, Known: tp.Known,
			})
		}
		_ = reply(out, env.ID, "PEER_SEARCH_RESULT", bridge.PeerSearchResultPayload{
			Query: p.Query, Hits: hits, Available: true, Progress: wireProgress,
		})
	}()

	select {
	case <-done:
	case <-requestCtx.Done():
		return requestCtx.Err()
	case <-time.After(peerSearchTimeout + time.Second):
		// peerSearchTimeout already bounds PeerSearch's own ctx, so this
		// should only fire if something downstream of it (TitlesFor, a slow
		// disk) runs long — the extra second is slack for that, not a
		// second copy of the same budget. Same reasoning as handleSuggest's
		// guard: reply now with an attributable error rather than leave the
		// client to hit its own 8s timeout with no daemon-side signal.
		log.Printf("PEER_SEARCH %s exceeded %v, replying with timeout rather than blocking further", env.ID, peerSearchTimeout)
		_ = reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: fmt.Sprintf("peer search exceeded %v", peerSearchTimeout),
			Code:    "peer_search_timeout",
		})
	}
	return nil
}

// handleGetContributionImpact answers GET_CONTRIBUTION_IMPACT and, after a
// reset, RESET_CONTRIBUTION_IMPACT (WO-086).
//
// The Level-1 refusal here is defense in depth, not the extension's only
// guard: the extension already gates the panel client-side off
// effective_level, exactly the way it gates the Live tab (applyLiveEntitlement),
// so this only matters against a stale or buggy client that asks anyway.
func handleGetContributionImpact(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	if allowed, level := contributionImpactLevel(st); !allowed {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "contribution impact needs broad sharing: this panel shows " +
				"evidence that your copy is doing useful serving work, which only " +
				"exists once your node answers requests for other people.",
			Code: bridge.CodeContributionRequired,
			Detail: bridge.ContributionRequiredDetail{
				Capability:     bridge.CapContributionImpact,
				RequiredLevel:  store.LevelBroad,
				EffectiveLevel: level,
			},
		})
	}
	n := currentSwarmNode()
	if n == nil {
		return reply(out, env.ID, "CONTRIBUTION_IMPACT_RESULT", bridge.ContributionImpactPayload{Available: false})
	}
	snap, err := n.ContributionImpact()
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	answered, bytesServed, since, err := st.ContributionActivity()
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	return reply(out, env.ID, "CONTRIBUTION_IMPACT_RESULT", bridge.ContributionImpactPayload{
		RequestsAnswered:      answered,
		BytesServed:           bytesServed,
		SinceDay:              since,
		GraphClaimsLocal:      snap.GraphClaimsLocal,
		GraphClaimsPeerCached: snap.GraphClaimsPeerCached,
		CatalogueLocal:        snap.CatalogueLocal,
		CataloguePeerCached:   snap.CataloguePeerCached,
		BucketsAnnounced:      snap.BucketsAnnounced,
		ShardsAnnounced:       snap.ShardsAnnounced,
		ConnectedPeers:        n.Peers(),
		KeelPeers:             n.KeelPeers(),
		Available:             true,
	})
}

// handleWordStats answers WORD_STATS (WO-068): corpus-wide word % + nested
// char-token coverage for the query. Display-only; never triggers a shard
// or word-bucket fetch. Direct peer pack merge when the swarm is up.
func handleWordStats(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	return handleWordStatsContext(context.Background(), env, out, st)
}

func handleWordStatsContext(requestCtx context.Context, env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.WordStatsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid WORD_STATS payload",
			Code:    "bad_payload",
		})
	}
	if networkBusy() {
		return replyNetworkBusy(out, env.ID)
	}
	n := currentSwarmNode()
	if n == nil {
		// Local-only fallback so the UI can still show what this device has
		// observed without implying the swarm answered.
		local, err := st.LocalWordTelemetry(store.AllSources)
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "WORD_STATS_RESULT", wordStatsFromLocal(p.Query, local, st, false))
	}
	ctx, cancel := context.WithTimeout(requestCtx, peerSearchTimeout)
	defer cancel()
	ws, err := n.FetchWordStats(ctx, p.Query)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	wire := bridge.WordStatsResultPayload{
		DistinctWords:  ws.DistinctWords,
		DistinctGraphs: ws.DistinctGraphs,
		Peers:          ws.Peers,
		Available:      ws.Available,
		Words:          make([]bridge.WordStatWire, 0, len(ws.Words)),
	}
	for _, w := range ws.Words {
		ww := bridge.WordStatWire{Word: w.Word, Pct: w.Pct, Tokens: make([]bridge.TokenCoverageWire, 0, len(w.Tokens))}
		for _, t := range w.Tokens {
			ww.Tokens = append(ww.Tokens, bridge.TokenCoverageWire{
				TokenIndex: t.TokenIndex, Estimate: t.Estimate, Known: t.Known,
			})
		}
		wire.Words = append(wire.Words, ww)
	}
	return reply(out, env.ID, "WORD_STATS_RESULT", wire)
}

func wordStatsFromLocal(query string, local *store.WordTelemetry, st *store.Store, available bool) bridge.WordStatsResultPayload {
	out := bridge.WordStatsResultPayload{
		DistinctWords:  local.DistinctWords(),
		DistinctGraphs: local.DistinctGraphs(),
		Available:      available,
		Words:          []bridge.WordStatWire{},
	}
	for _, w := range store.FilterStopwords(store.QueryWords(query)) {
		ww := bridge.WordStatWire{Word: w, Tokens: []bridge.TokenCoverageWire{}}
		if pct, ok := local.WordPct(w); ok {
			r := math.Round(pct*10) / 10
			ww.Pct = &r
		}
		// One entry per three-gram, IN WORD ORDER, never skipped.
		//
		// This used to send store.CharTokensForWord — sorted and de-duplicated,
		// with tokens missing from the dictionary dropped. Two consequences: the
		// interface drew fewer bars than the word has three-grams, and the ones
		// it drew were in an order unrelated to the word, so a bar could not be
		// tied to the letters it came from without reimplementing the tokenizer
		// in JavaScript to undo the sort. Word order makes the nth bar the nth
		// three-gram, which is all the interface ever needed.
		//
		// TokenIndex stays the dictionary index, so one three-gram keeps one
		// colour wherever it appears; a three-gram the dictionary does not know
		// gets a negative index, distinct within the word and never a real one.
		for i, tok := range store.CharTokensInOrder(w) {
			idx, ok := store.TokenDictIndex(tok)
			if !ok {
				idx = -1 - i
			}
			tc := bridge.TokenCoverageWire{TokenIndex: idx}
			if st != nil {
				tc.Estimate, tc.Known = st.TokenEstimate(tok)
			}
			ww.Tokens = append(ww.Tokens, tc)
		}
		out.Words = append(out.Words, ww)
	}
	return out
}

// suggestTimeout bounds one SUGGEST computation, safely under the
// extension's 8s client-side request timeout (extension/lib/native.js
// request(), default 8000ms). WO-069: direct benchmarking of SuggestOn found
// it fast in every case tried (microseconds on an empty DB, under a
// millisecond on a synthetic ~40k-edge graph) — the ticket's specific claim
// that the graph walk itself blocks for seconds on a cold DB did not
// reproduce, so the real trigger for the reported timeout is still unknown.
// This guard, and moving the handler off the bridge thread below, are
// defensive regardless of that: they stop a genuinely slow or hung call from
// starving the single native-messaging pipe (nothing else could be answered
// until it returned) and from silently outliving the client's own timeout
// with no server-side signal.
const suggestTimeout = 6 * time.Second

// handleSuggest ranks the co-recommendation graph (WO-023). Read-only.
//
// Runs on its own goroutine (WO-069): the native-messaging bridge reads and
// processes one message at a time (see run()), so a synchronous handler here
// blocked every other RPC — including a cheap STATS or HELLO — for as long as
// this one took. Returning nil immediately and replying from the goroutine
// once done (or once suggestTimeout fires, whichever is first) frees the
// bridge thread to keep reading. main() wraps stdout in syncWriter for
// exactly this: replies can now arrive out of request order and from more
// than one goroutine at once, and the underlying writer needs serializing to
// keep frames from interleaving.
func handleSuggest(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.SuggestPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid SUGGEST payload",
			Code:    "bad_payload",
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Hand the store whatever the swarm currently believes is live, so the
		// walk can rank running streams first. Local LIVE badges cover the
		// no-peers case on their own; this adds streams other people are seeing.
		if n := currentSwarmNode(); n != nil && n.Live() != nil {
			entries := n.Live().Search("", 5000)
			// The index deliberately keeps records for hours, so a stream can
			// still be found after it ends. Promoting one to the top of the
			// panel is a stronger claim than listing it, so only recent
			// sightings qualify.
			cutoff := time.Now().Add(-store.LiveRecency).UnixMilli()
			ids := make([]string, 0, len(entries))
			for _, e := range entries {
				// SeenAt, not LastSeen. LastSeen is when gossip about this
				// stream last arrived, and records are re-announced as they
				// age, so a stream that ended hours ago keeps a warm LastSeen
				// for as long as anyone is still passing it around. Promoting
				// on that put six-hour-old streams at the top of the panel
				// while the stated rule was one hour.
				if e.SeenAt >= cutoff {
					ids = append(ids, e.VideoID)
				}
			}
			st.SetLiveVideos(ids)
		}

		res, err := st.SuggestOn(p.Platform, p.SeedVideoID, p.Entropy, p.Limit)
		if err != nil {
			_ = replyErr(out, env.ID, err)
			return
		}
		_ = reply(out, env.ID, "SUGGEST_RESULT", res)
	}()

	select {
	case <-done:
	case <-time.After(suggestTimeout):
		// The goroutine above is not canceled — SuggestOn takes no context,
		// so there is nothing to signal it with — it keeps running and its
		// eventual reply is simply a second write under the same
		// correlation id, which the client has already stopped listening
		// for. Replying now, rather than leaving the client to hit its own
		// 8s timeout with no daemon-side signal at all, is the actual fix:
		// the client gets a clean, attributable error before its own timer
		// fires instead of a bare "timeout" with nothing behind it.
		log.Printf("SUGGEST %s exceeded %v, replying with timeout rather than blocking further", env.ID, suggestTimeout)
		_ = reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: fmt.Sprintf("suggestion walk exceeded %v", suggestTimeout),
			Code:    "suggest_timeout",
		})
	}
	return nil
}

// cohortFromEnv returns the coarse cohort for measurement tuples.
//
// DESIGN_v2 §6.3 restricts this to country plus interface language, and
// forbids anything richer — interest drift is a behavioural fingerprint and
// attaching it to edges would undo the privacy design. Until the extension
// reports the browser locale, "unknown" is the honest value; KEEL_COHORT allows
// testing without inventing one.
func cohortFor(st *store.Store) string {
	if c := os.Getenv("KEEL_COHORT"); c != "" {
		return store.NormalizeCohort(c)
	}
	return st.Cohort()
}

// handleExportBundle writes the aggregate layer next to the JSON export.
func handleExportBundle(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	dir, err := store.DownloadsDir()
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	name := fmt.Sprintf("keel-bundle-%s.json", time.Now().UTC().Format("20060102T150405Z"))
	res, err := st.ExportBundle(filepath.Join(dir, name), cohortFor(st))
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	return reply(out, env.ID, "EXPORT_BUNDLE_RESULT", res)
}

func handleImportBundle(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.Path == "" {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "path required", Code: "bad_payload",
		})
	}
	res, err := st.ImportBundle(p.Path)
	if err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: err.Error(), Code: "import_failed",
		})
	}
	return reply(out, env.ID, "IMPORT_BUNDLE_RESULT", res)
}

// handleScrollHistory returns consumed clips for the TikTok Mirror (WO-063).
func handleScrollHistory(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.ScrollHistoryPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid SCROLL_HISTORY payload", Code: "bad_payload",
		})
	}
	res, err := st.ScrollHistory(p.Platform, p.Limit)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	return reply(out, env.ID, "SCROLL_HISTORY_RESULT", res)
}

func handlePeers(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	peers, err := st.Peers()
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	id, err := st.NodeID()
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	payload := map[string]any{"node_id": id, "peers": peers}
	return reply(out, env.ID, "PEERS_RESULT", payload)
}

func handleForgetPeer(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.Source == "" {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "source required", Code: "bad_payload",
		})
	}
	n, err := st.ForgetPeer(p.Source)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	return reply(out, env.ID, "FORGET_PEER_RESULT", map[string]any{"removed": n})
}
