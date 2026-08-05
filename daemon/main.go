// SPDX-License-Identifier: Apache-2.0
// Keel desktop host: native-messaging stdio bridge → SQLite.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keel-app/keel/daemon/bridge"
	"github.com/keel-app/keel/daemon/store"
)

const version = "0.1.0"

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
		return reply(out, env.ID, "STATS_RESULT", stats)
	case "EXPORT":
		return handleExport(env, out, st)
	case "WIPE":
		return handleWipe(env, out, st)
	case "SEARCH":
		return handleSearch(env, out, st)
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
	case "EXPLAIN_VIDEO":
		return handleExplainVideo(env, out, st)
	default:
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: fmt.Sprintf("unknown type %q", env.Type),
			Code:    "unknown_type",
		})
	}
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
	if len(b) > bridge.MaxHostToBrowser {
		log.Printf("drop oversized response %d bytes", len(b))
		return nil
	}
	return bridge.WriteMessage(out, b)
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

// handleSuggest ranks the co-recommendation graph (WO-023). Read-only.
func handleSuggest(env *bridge.Envelope, out io.Writer, st *store.Store) error {
	var p bridge.SuggestPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reply(out, env.ID, "ERROR", bridge.ErrorPayload{
			Message: "invalid SUGGEST payload",
			Code:    "bad_payload",
		})
	}
	res, err := st.Suggest(p.SeedVideoID, p.Entropy, p.Limit)
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
