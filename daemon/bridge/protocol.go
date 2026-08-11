// SPDX-License-Identifier: Apache-2.0
package bridge

import (
	"encoding/json"
	"fmt"
)

const ProtocolV = 2

// Envelope is the Keel Bridge message shape (DESIGN_v2 §8.1).
type Envelope struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Chunk   *Chunk          `json:"chunk,omitempty"`
}

type Chunk struct {
	Index int `json:"index"`
	Total int `json:"total"`
}

// ParseEnvelope validates framing JSON into an Envelope.
func ParseEnvelope(data []byte) (*Envelope, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("non-JSON payload")
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if env.V != ProtocolV {
		return nil, fmt.Errorf("unsupported protocol version %d", env.V)
	}
	if env.ID == "" {
		return nil, fmt.Errorf("missing id")
	}
	if env.Type == "" {
		return nil, fmt.Errorf("missing type")
	}
	if env.Payload == nil {
		env.Payload = json.RawMessage("{}")
	}
	return &env, nil
}

// NewEnvelope builds a response that echoes correlation id.
func NewEnvelope(id, typ string, payload any) (*Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Envelope{V: ProtocolV, ID: id, Type: typ, Payload: raw}, nil
}

// Encode serializes an envelope to JSON bytes.
func (e *Envelope) Encode() ([]byte, error) {
	return json.Marshal(e)
}

// Impression matches DESIGN_v2 §4.2.
type Impression struct {
	PageLoadID       string  `json:"page_load_id"`
	ObservedAt       int64   `json:"observed_at"`
	Surface          string  `json:"surface"`
	ContextVideoID   *string `json:"context_video_id"`
	ContextQueryHash *string `json:"context_query_hash"`
	// ContextTitle is the title of the video being watched.
	//
	// Its id is already recorded as ContextVideoID, so this adds no disclosure
	// — it is a public fact about an id already held. Without it the interface
	// can only name the current video if it happened to be recommended
	// elsewhere first, which is often not the case.
	ContextTitle *string `json:"context_title"`
	// Platform this was observed on: "yt", "tt". Absent means YouTube, which is
	// every row written before TikTok existed.
	Platform       string  `json:"platform,omitempty"`
	SlotIndex      int     `json:"slot_index"`
	VideoID        string  `json:"video_id"`
	ChannelID      *string `json:"channel_id"` // null when DOM omits channel links
	ChannelUnknown bool    `json:"channel_unknown"`
	// ChannelName is the display name the extension read off the card (may be
	// an @handle or a plain name). Nullable — the DOM often omits it.
	ChannelName *string  `json:"channel_name"`
	Title       string   `json:"title"`
	DurationS   *float64 `json:"duration_s"`
	ViewCount   *float64 `json:"view_count"`
	PublishedAt *string  `json:"published_at"`
	Badges      []string `json:"badges"`
}

// ValidateImpression returns an error if required fields are missing.
func ValidateImpression(imp *Impression) error {
	if imp.PageLoadID == "" {
		return fmt.Errorf("page_load_id required")
	}
	if imp.ObservedAt == 0 {
		return fmt.Errorf("observed_at required")
	}
	switch imp.Surface {
	case "WATCH_NEXT", "HOME", "SEARCH", "CHANNEL", "SHORTS":
	default:
		return fmt.Errorf("bad surface")
	}
	if imp.SlotIndex < 0 {
		return fmt.Errorf("slot_index")
	}
	if imp.VideoID == "" || imp.Title == "" {
		return fmt.Errorf("missing identity fields")
	}
	// channel_id may be null; treat empty string as null
	if imp.ChannelID != nil && *imp.ChannelID == "" {
		imp.ChannelID = nil
	}
	if imp.ChannelID == nil {
		imp.ChannelUnknown = true
	}
	if imp.Badges == nil {
		imp.Badges = []string{}
	}
	if imp.Surface == "WATCH_NEXT" && (imp.ContextVideoID == nil || *imp.ContextVideoID == "") {
		return fmt.Errorf("WATCH_NEXT needs context_video_id")
	}
	return nil
}

// ImpressionsPayload is the body of type IMPRESSIONS.
type ImpressionsPayload struct {
	Impressions []Impression `json:"impressions"`
}

// StatsPayload is STATS_RESULT body.
type StatsPayload struct {
	Total              int64            `json:"total"`
	BySurface          map[string]int64 `json:"by_surface"`
	FirstObservedAt    *int64           `json:"first_observed_at"`
	LastObservedAt     *int64           `json:"last_observed_at"`
	ExtractionFailures int64            `json:"extraction_failures"`
	// ChannelUnknown is rows with channel_unknown=1 or null channel_id (WO-013).
	ChannelUnknown int64 `json:"channel_unknown"`
	// ChannelKnown is rows with a non-null channel_id.
	ChannelKnown int64 `json:"channel_known"`
}

// HelloPayload is HELLO body.
type HelloPayload struct {
	Client  string `json:"client"`
	Version string `json:"version"`
}

// HelloAckPayload is HELLO_ACK body.
type HelloAckPayload struct {
	Server  string `json:"server"`
	Version string `json:"version"`
	OK      bool   `json:"ok"`
}

// ErrorPayload is ERROR body.
type ErrorPayload struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// ExportResultPayload is EXPORT_RESULT body (WO-012).
// Corpus is written to Path; bridge never carries the full dump.
type ExportResultPayload struct {
	Path  string `json:"path"`
	Rows  int64  `json:"rows"`
	Bytes int64  `json:"bytes"`
}

// WipeResultPayload is WIPE_RESULT body (WO-012).
type WipeResultPayload struct {
	Deleted int64 `json:"deleted"`
}

// BlocklistPayload is GET_BLOCKLIST / BLOCK_CHANNEL / UNBLOCK_CHANNEL result (WO-016).
type BlocklistPayload struct {
	Blocklist []string `json:"blocklist"`
}

// ChannelBlockPayload is BLOCK_CHANNEL / UNBLOCK_CHANNEL request body.
type ChannelBlockPayload struct {
	ChannelID string `json:"channel_id"`
}

// QueuePayload is the QUEUE_ADD request body (WO-064).
//
// The watch queue is user intent rather than observation, which is why it is a
// separate table and a separate set of verbs: nothing here was recorded from a
// page, and nothing here is ever published.
type QueuePayload struct {
	VideoID  string `json:"video_id"`
	Platform string `json:"platform"`
}

// QueueIndexPayload addresses an entry by its position in the current order.
//
// By position rather than by video id because that is what the interface shows
// — the user points at a row, and a row is a position.
type QueueIndexPayload struct {
	Index int `json:"index"`
	From  int `json:"from"`
	To    int `json:"to"`
}

// QueueItem is one entry, carrying whatever the corpus already knows about the
// video so the panel does not have to ask again per row.
type QueueItem struct {
	VideoID  string  `json:"video_id"`
	Title    string  `json:"title"`
	Position int     `json:"position"`
	AddedAt  int64   `json:"added_at"`
	Platform string  `json:"platform"`
	Duration float64 `json:"duration_s,omitempty"`
}

// QueueResultPayload is the QUEUE_RESULT body: every queue verb answers with
// the whole resulting queue, so the caller never has to infer what changed.
//
// Next is set only by QUEUE_ADVANCE, and only when the finished video was
// queued and something follows it. Null means "do not navigate" — which is the
// answer for a video that was never in the queue, and for the last one in it.
type QueueResultPayload struct {
	Items []QueueItem `json:"items"`
	Next  *QueueItem  `json:"next,omitempty"`
}

// ExplainVideoPayload is EXPLAIN_VIDEO request (WO-018).
type ExplainVideoPayload struct {
	VideoID string `json:"video_id"`
}

// ExplainContext is one WATCH_NEXT co-occurrence parent.
type ExplainContext struct {
	ContextVideoID string  `json:"context_video_id"`
	Title          *string `json:"title"` // null if corpus has no title for this id
	Count          int64   `json:"count"`
	MedianSlot     float64 `json:"median_slot_index"`
}

// SlotBucket is one slot_index frequency.
type SlotBucket struct {
	Slot  int   `json:"slot"`
	Count int64 `json:"count"`
}

// ExplainResultPayload is EXPLAIN_RESULT body (WO-018).
// Observational only — co-occurrence counts, not causal claims.
type ExplainResultPayload struct {
	VideoID          string           `json:"video_id"`
	Title            *string          `json:"title"`
	TotalImpressions int64            `json:"total_impressions"`
	FirstObservedAt  *int64           `json:"first_observed_at"`
	LastObservedAt   *int64           `json:"last_observed_at"`
	HomeImpressions  int64            `json:"home_impressions"`
	Contexts         []ExplainContext `json:"contexts"`
	SlotHistogram    []SlotBucket     `json:"slot_histogram"`
}

// SearchPayload is the SEARCH request body (WO-022).
type SearchPayload struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// SearchHit is one video in the local catalogue.
//
// The catalogue is the deduplicated video-level view of the corpus: one row per
// video_id, carrying the metadata YouTube published rather than any observation
// of a person (DESIGN_BOOTSTRAP §1).
type SearchHit struct {
	VideoID     string   `json:"video_id"`
	Title       string   `json:"title"`
	ChannelID   *string  `json:"channel_id"`
	DurationS   *float64 `json:"duration_s"`
	ViewCount   *float64 `json:"view_count"`
	PublishedAt *string  `json:"published_at"`
	Seen        int64    `json:"seen"`
	LastSeenAt  int64    `json:"last_seen_at"`
}

// SearchResultPayload is SEARCH_RESULT body.
type SearchResultPayload struct {
	Query     string      `json:"query"`
	Hits      []SearchHit `json:"hits"`
	Total     int64       `json:"total"`
	Truncated bool        `json:"truncated"`
}

// PeerSearchResultPayload is PEER_SEARCH_RESULT body (WO-059).
//
// A separate type from SearchResultPayload, not a shared one, because the
// two searches answer different questions: SEARCH is what this device has
// already seen, PEER_SEARCH is what the network holds regardless of local
// history. Reusing SearchHit's shape keeps the panel's existing render path
// usable for both; Total/Truncated are absent because peer search has no
// single-source count to report — see Available.
type PeerSearchResultPayload struct {
	Query string      `json:"query"`
	Hits  []SearchHit `json:"hits"`
	// Available is false when the swarm isn't running or fetching is off
	// (contribution Level 1) — distinct from a true empty result, so the
	// interface can say why rather than imply the network has nothing.
	Available bool `json:"available"`
}

// SuggestPayload is the SUGGEST request body (WO-023).
//
// Entropy is the 0–100 focus↔serendipity control from
// User Utility Architecture §3. It maps onto the walk itself: low entropy
// restarts often and stays near the seed; high entropy wanders further and
// prefers less-viewed nodes.
type SuggestPayload struct {
	// Platform to walk. Empty means YouTube.
	Platform    string `json:"platform"`
	SeedVideoID string `json:"seed_video_id"`
	Entropy     int    `json:"entropy"`
	Limit       int    `json:"limit"`
}

// Suggestion is one ranked recommendation.
type Suggestion struct {
	VideoID   string   `json:"video_id"`
	Title     string   `json:"title"`
	ChannelID *string  `json:"channel_id"`
	ViewCount *float64 `json:"view_count"`
	DurationS *float64 `json:"duration_s"`
	// PublishedAt is YouTube's own relative wording — "2w ago", "1mo ago" —
	// carried through unchanged. It is what people read a video's age from, so
	// reformatting it would only make it less familiar.
	PublishedAt *string `json:"published_at"`
	Score       float64 `json:"score"`
	Seen        int64   `json:"seen"`
	// Why is the strongest observed path to this video, for the funnel
	// inspector's sake. Observational only — never a claim about intent.
	ViaVideoID *string `json:"via_video_id"`
	ViaTitle   *string `json:"via_title"`
	// FromPeer marks a video this machine never observed — it is in the graph
	// only because an imported bundle put it there.
	FromPeer bool `json:"from_peer"`
}

// SuggestResultPayload is SUGGEST_RESULT body.
// PlatformYouTube is the default for anything that does not say otherwise.
const PlatformYouTube = "yt"

// KnownPlatforms are the platforms this build understands. A record naming
// anything else is rejected rather than stored — an unknown platform would
// silently escape every platform-scoped query.
var KnownPlatforms = map[string]bool{"yt": true, "tt": true}

type SuggestResultPayload struct {
	SeedVideoID string       `json:"seed_video_id"`
	SeedTitle   *string      `json:"seed_title"`
	Entropy     int          `json:"entropy"`
	Suggestions []Suggestion `json:"suggestions"`
	GraphNodes  int          `json:"graph_nodes"`
	GraphEdges  int          `json:"graph_edges"`
	// RailOnly reports that the corpus could not reach past the seed's own
	// rail, so these suggestions are the videos YouTube already showed. The
	// interface should say so rather than passing them off as its own.
	RailOnly bool `json:"rail_only"`
	// FromCorpus reports that the seed was a dead end, so these come from a
	// walk over everything the user has watched rather than from this video.
	FromCorpus bool `json:"from_corpus"`
}

// AnalysisRow is one ranked item in the analysis view (WO-024).
type AnalysisRow struct {
	Key        string   `json:"key"`
	Label      *string  `json:"label"`
	Count      int64    `json:"count"`
	Extra      *string  `json:"extra"`
	MedianSlot *float64 `json:"median_slot"`
}

// AnalysisPayload is ANALYSIS_RESULT body.
//
// Answers "what does YouTube push hardest at me" from the local corpus. Every
// number is an observation count, never an inference about intent.
type AnalysisPayload struct {
	TotalImpressions int64 `json:"total_impressions"`
	DistinctVideos   int64 `json:"distinct_videos"`
	DistinctChannels int64 `json:"distinct_channels"`
	WatchedVideos    int64 `json:"watched_videos"`
	// Imported totals, kept out of the counts above on purpose.
	PeerEdges     int64         `json:"peer_edges"`
	PeerSources   int64         `json:"peer_sources"`
	TopVideos     []AnalysisRow `json:"top_videos"`
	TopChannels   []AnalysisRow `json:"top_channels"`
	TopEdges      []AnalysisRow `json:"top_edges"`
	SlotHistogram []SlotBucket  `json:"slot_histogram"`
}

// EdgeObservation is the measurement tuple defined in DESIGN_v2 §6.2.
//
// This is the only shape in which observations leave the machine — as a STAR
// submission, a published dataset, or a bundle. Buckets are coarse on purpose:
// exact slots and timestamps compose into a fingerprint across releases.
type EdgeObservation struct {
	From       string `json:"from"` // video_id, or "__home__" for the home feed
	To         string `json:"to"`
	Surface    string `json:"surface"`
	SlotBucket string `json:"slot_bucket"` // "0" | "1" | "2" | "3-5" | "6-10" | "11+"
	DayBucket  string `json:"day_bucket"`  // YYYY-MM-DD, UTC
	Cohort     string `json:"cohort"`      // country + interface language (§6.3)
	Count      int64  `json:"count"`
}

// CatalogueEntry is video metadata with no observation attached — public fact
// about a public video (DESIGN_BOOTSTRAP §1).
type CatalogueEntry struct {
	VideoID     string   `json:"video_id"`
	Title       string   `json:"title"`
	ChannelID   *string  `json:"channel_id"`
	DurationS   *float64 `json:"duration_s"`
	ViewCount   *float64 `json:"view_count"`
	PublishedAt *string  `json:"published_at"`
}

// AggregateSummaryPayload is AGGREGATE_SUMMARY_RESULT body.
type AggregateSummaryPayload struct {
	RawImpressions   int64  `json:"raw_impressions"`
	EdgeObservations int64  `json:"edge_observations"`
	CatalogueEntries int64  `json:"catalogue_entries"`
	DistinctDays     int64  `json:"distinct_days"`
	Cohort           string `json:"cohort"`
	Note             string `json:"note"`
}

// BundleResultPayload is EXPORT_BUNDLE_RESULT / IMPORT_BUNDLE_RESULT body.
type BundleResultPayload struct {
	Path      string `json:"path"`
	NodeID    string `json:"node_id"`
	Edges     int64  `json:"edges"`
	Catalogue int64  `json:"catalogue"`
	Bytes     int64  `json:"bytes"`
}

// PeersPayload is PEERS_RESULT body.
type PeersPayload struct {
	NodeID string `json:"node_id"`
	Peers  []struct {
		Source string `json:"source"`
		Edges  int64  `json:"edges"`
	} `json:"peers"`
}
