// SPDX-License-Identifier: Apache-2.0
// Keel desktop host: native-messaging stdio bridge → SQLite.
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
	"strings"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
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

	// Subcommands (WO-020). Checked explicitly rather than with flag.Parse:
	// the browser launches this binary with the caller's origin as argv[1], so
	// generic flag parsing would misread a normal native-messaging start.
	if len(os.Args) > 1 {
		switch strings.TrimLeft(os.Args[1], "-") {
		case "install":
			os.Exit(runInstall(os.Args[2:]))
		case "uninstall":
			os.Exit(runUninstall(os.Args[2:]))
		case "keys":
			os.Exit(runKeys(os.Args[2:]))
		case "bundle":
			os.Exit(runBundle(os.Args[2:]))
		case "sketch":
			os.Exit(runSketch(os.Args[2:]))
		case "seed":
			os.Exit(runSeed(os.Args[2:]))
		case "version":
			fmt.Println("keel-host", version)
			os.Exit(0)
		case "help":
			usage()
			os.Exit(0)
		}
	}

	dbPath := os.Getenv("KEEL_DB")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store open: %v", err)
	}
	defer st.Close()

	// The swarm runs for the lifetime of the connection. Cancelling on return
	// stops the announce loop and any in-flight prewarm.
	//
	// **Started in the background, and that is not an optimisation.** Bringing
	// up libp2p bootstraps against the public DHT, which can be slow, blocked by
	// a firewall, or simply unreachable. Doing that before the message loop
	// meant the daemon did not answer HELLO until the network cooperated — so a
	// machine with no route to the DHT recorded nothing at all, silently, and
	// the panel reported an empty page.
	//
	// Everything local works with no network whatsoever (§2). Startup must
	// never wait on a peer.
	swarmCtx, stopSwarm := context.WithCancel(context.Background())
	defer stopSwarm()
	go startSwarm(swarmCtx, st)
	defer func() {
		if swarmNode != nil {
			_ = swarmNode.Close()
		}
	}()

	run(os.Stdin, os.Stdout, st)
}

func run(in io.Reader, out io.Writer, st *store.Store) {
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
		if err := handleRaw(raw, out, st); err != nil {
			log.Printf("handle: %v", err)
		}
	}
}

func handleRaw(raw []byte, out io.Writer, st *store.Store) error {
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

	switch env.Type {
	case "HELLO":
		return reply(out, env.ID, "HELLO_ACK", bridge.HelloAckPayload{
			Server:  "keel-daemon",
			Version: version,
			OK:      true,
		})
	case "IMPRESSIONS":
		return handleImpressions(env, out, st)
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
		return handlePeerSearch(env, out, st)
	case "WORD_STATS":
		return handleWordStats(env, out, st)
	case "SUGGEST":
		return handleSuggest(env, out, st)
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
	case "GET_CONTRIBUTION":
		return reply(out, env.ID, "CONTRIBUTION_RESULT", map[string]any{
			"level":           st.ContributionLevel(),
			"max_implemented": store.MaxImplementedLevel,
		})
	case "SET_CONTRIBUTION":
		var p struct {
			Level int `json:"level"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: "level required", Code: "bad_payload"})
		}
		lv, err := st.SetContributionLevel(p.Level)
		if err != nil {
			return reply(out, env.ID, "ERROR", bridge.ErrorPayload{Message: err.Error(), Code: "bad_level"})
		}
		return reply(out, env.ID, "CONTRIBUTION_RESULT", map[string]any{
			"level":           lv,
			"max_implemented": store.MaxImplementedLevel,
		})
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
		return handleLiveSearch(env, out)
	default:
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: fmt.Sprintf("unknown type %q", env.Type),
			Code:    "unknown_type",
		})
	}
}

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

func handleLiveSearch(env *bridge.Envelope, out io.Writer) error {
	var p struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(env.Payload) > 0 {
		_ = json.Unmarshal(env.Payload, &p)
	}
	if swarmNode == nil || swarmNode.Live() == nil {
		// The feed is available at every level, so this means the swarm itself
		// did not start — no network, not a permission decision.
		return reply(out, env.ID, "LIVE_RESULT", map[string]any{
			"query": p.Query, "streams": []any{}, "indexed": 0,
			"available": false,
			"reason":    "not connected to the network yet",
		})
	}
	li := swarmNode.Live()
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

  keel-host install   -extension-id <id>[,<id>]  register with detected browsers
                      [-firefox-id keel@local] [-all] [-dry-run]
  keel-host uninstall [-dry-run]                 remove host manifests

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
	return reply(out, env.ID, "SEARCH_RESULT", res)
}

// peerSearchTimeout bounds one PEER_SEARCH request. Unlike Prewarm, this is
// user-triggered and answered synchronously over the bridge, so it needs its
// own bound rather than running in the background indefinitely.
const peerSearchTimeout = 30 * time.Second

// handlePeerSearch searches the swarm's token shards for a query (WO-059),
// distinct from handleSearch's purely local catalogue lookup.
func handlePeerSearch(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.SearchPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid PEER_SEARCH payload",
			Code:    "bad_payload",
		})
	}
	if swarmNode == nil {
		// Mirrors handleLiveSearch: no swarm running means unavailable, not
		// an empty result — the interface must not read this as "the network
		// has nothing for this query."
		return reply(out, env.ID, "PEER_SEARCH_RESULT", bridge.PeerSearchResultPayload{
			Query: p.Query, Hits: []bridge.SearchHit{}, Available: false,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), peerSearchTimeout)
	defer cancel()
	ids, progress, err := swarmNode.PeerSearch(ctx, p.Query)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	hits, err := st.TitlesFor(ids)
	if err != nil {
		return replyErr(out, env.ID, err)
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
	return reply(out, env.ID, "PEER_SEARCH_RESULT", bridge.PeerSearchResultPayload{
		Query: p.Query, Hits: hits, Available: true, Progress: wireProgress,
	})
}

// handleWordStats answers WORD_STATS (WO-068): corpus-wide word % + nested
// char-token coverage for the query. Display-only; never triggers a shard
// or word-bucket fetch. Direct peer pack merge when the swarm is up.
func handleWordStats(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.WordStatsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid WORD_STATS payload",
			Code:    "bad_payload",
		})
	}
	if swarmNode == nil {
		// Local-only fallback so the UI can still show what this device has
		// observed without implying the swarm answered.
		local, err := st.LocalWordTelemetry(false)
		if err != nil {
			return replyErr(out, env.ID, err)
		}
		return reply(out, env.ID, "WORD_STATS_RESULT", wordStatsFromLocal(p.Query, local, st, false))
	}
	ctx, cancel := context.WithTimeout(context.Background(), peerSearchTimeout)
	defer cancel()
	ws, err := swarmNode.FetchWordStats(ctx, p.Query)
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
		for _, tok := range store.CharTokensForWord(w) {
			idx, ok := store.TokenDictIndex(tok)
			if !ok {
				continue
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

// handleSuggest ranks the co-recommendation graph (WO-023). Read-only.
func handleSuggest(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.SuggestPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid SUGGEST payload",
			Code:    "bad_payload",
		})
	}
	// Hand the store whatever the swarm currently believes is live, so the walk
	// can rank running streams first. Local LIVE badges cover the no-peers case
	// on their own; this adds streams other people are seeing.
	if swarmNode != nil && swarmNode.Live() != nil {
		entries := swarmNode.Live().Search("", 5000)
		// The index deliberately keeps records for hours, so a stream can still
		// be found after it ends. Promoting one to the top of the panel is a
		// stronger claim than listing it, so only recent sightings qualify.
		cutoff := time.Now().Add(-store.LiveRecency).UnixMilli()
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			// SeenAt, not LastSeen. LastSeen is when gossip about this stream
			// last arrived, and records are re-announced as they age, so a
			// stream that ended hours ago keeps a warm LastSeen for as long as
			// anyone is still passing it around. Promoting on that put
			// six-hour-old streams at the top of the panel while the stated
			// rule was one hour.
			if e.SeenAt >= cutoff {
				ids = append(ids, e.VideoID)
			}
		}
		st.SetLiveVideos(ids)
	}

	res, err := st.SuggestOn(p.Platform, p.SeedVideoID, p.Entropy, p.Limit)
	if err != nil {
		return replyErr(out, env.ID, err)
	}
	return reply(out, env.ID, "SUGGEST_RESULT", res)
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
