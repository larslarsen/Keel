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
	// TikTok-only fields (WO-063 Mirror). Empty/nil on YouTube rows.
	Hashtags   []string `json:"hashtags,omitempty"`
	SoundID    *string  `json:"sound_id,omitempty"`
	DwellPct   *float64 `json:"dwell_pct,omitempty"`
	Engagement *string  `json:"engagement,omitempty"`
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

// HelloPayload / HelloAckPayload live in hello.go (WO-081).

// ErrorPayload is ERROR body.
type ErrorPayload struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
	// Detail carries structured state alongside a failure, for the cases
	// where the error itself is not the whole answer. A failed contribution
	// change (WO-077) is the motivating one: the interface must show what
	// policy is actually running now, which a message string cannot express
	// and which the user needs more urgently than the reason it failed.
	Detail any `json:"detail,omitempty"`
}

// ContributionRequiredDetail is ErrorPayload.Detail for a refusal carrying
// CodeContributionRequired (WO-085).
//
// Structured rather than only a sentence, because the interface has to do two
// things with it that a string cannot support: decide which control to disable,
// and offer the route to the setting that would enable it. The message stays
// human-readable for clients too old to read this.
type ContributionRequiredDetail struct {
	// Capability names the entitlement, not the RPC — an entitlement can gate
	// more than one RPC later, and the control the UI disables belongs to the
	// entitlement.
	Capability string `json:"capability"`
	// RequiredLevel is the lowest contribution level that grants it.
	RequiredLevel int `json:"required_level"`
	// EffectiveLevel is the policy actually running, which after a failed or
	// in-flight transition is not necessarily the stored choice (WO-077).
	EffectiveLevel int `json:"effective_level"`
}

// CapDistributedSearch is ContributionRequiredDetail.Capability for
// user-triggered distributed peer search.
const CapDistributedSearch = "distributed_search"

// CodeNetworkBusy marks an RPC declined because the swarm is mid-replacement
// (WO-077). Distinct from a network being down: the correct client behaviour
// is to retry shortly, not to report the peer network as unavailable.
const CodeNetworkBusy = "network_busy"

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

// ScrollHistoryPayload is SCROLL_HISTORY request (WO-063 TikTok Mirror).
type ScrollHistoryPayload struct {
	Platform string `json:"platform"`
	Limit    int    `json:"limit"`
}

// ScrollHistoryItem is one consumed clip in scroll order.
type ScrollHistoryItem struct {
	VideoID     string   `json:"video_id"`
	Title       string   `json:"title"`
	ChannelID   *string  `json:"channel_id"`
	ChannelName *string  `json:"channel_name"`
	ObservedAt  int64    `json:"observed_at"`
	SlotIndex   int      `json:"slot_index"`
	Hashtags    []string `json:"hashtags"`
	SoundID     *string  `json:"sound_id"`
	DwellPct    *float64 `json:"dwell_pct"`
	Engagement  *string  `json:"engagement"`
	Platform    string   `json:"platform"`
}

// ScrollHistoryResultPayload is SCROLL_HISTORY_RESULT body.
type ScrollHistoryResultPayload struct {
	Items []ScrollHistoryItem `json:"items"`
	// HashtagCounts: tag → how many history items carried it (local only).
	HashtagCounts map[string]int64 `json:"hashtag_counts"`
	// SoundCounts: sound_id → count (local only; never exported).
	SoundCounts map[string]int64 `json:"sound_counts"`
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
	// Plan is the daemon's canonical render plan for this query (WO-097 §1).
	// Present on every local search, including when peer search is disabled or
	// unavailable, because it is how the interface knows what the words and
	// tokens *are* — the extension never retokenizes a query.
	Plan *QueryPlanWire `json:"plan,omitempty"`
}

// QueryPlanWire is the render plan the daemon hands the interface (WO-097 §1,
// consumed by WO-095).
//
// It carries normalized display words — the query, in other words — because
// there is no way to draw a search box's word bars without them. That crossing
// is the local native bridge only: the words must not be logged, persisted,
// broadcast to another page, or placed in browser storage (DESIGN_v2 §2.1).
//
// What it deliberately does not carry is token *text*. A token is an opaque id
// plus a character range and a color slot, which is everything a renderer needs
// and nothing a log or a screenshot could turn back into a three-gram. The
// ranges index Normalized, so a fragment is drawn by slicing the string the
// plan already contains rather than by re-deriving anything.
type QueryPlanWire struct {
	Normalized string          `json:"normalized"`
	Words      []PlanWordWire  `json:"words"`
	Tokens     []PlanTokenWire `json:"tokens"`
}

// PlanWordWire is one display-word occurrence. WordID identifies the word
// *value*, so repeated occurrences share one id, one target and one count.
type PlanWordWire struct {
	WordID   int    `json:"word_id"`
	Word     string `json:"word"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Stopword bool   `json:"stopword"`
}

// PlanTokenWire is one token occurrence of the fixed query grid: an opaque id,
// the characters it covers, the word fragments it colors, and the word its one
// bar sits under.
type PlanTokenWire struct {
	TokenID   int `json:"token_id"`
	ColorSlot int `json:"color_slot"`
	Start     int `json:"start"`
	End       int `json:"end"`
	// Discovery is whether this token will be fetched from peers. False for a
	// token whose letters are all stopword — it still renders, so a person can
	// see that it is deliberately not network work.
	Discovery bool `json:"discovery"`
	// BarWordID is the deterministic placement of this token's bar: the first
	// word whose letters it covers. -1 when it covers none.
	BarWordID int                `json:"bar_word_id"`
	Fragments []PlanFragmentWire `json:"fragments"`
}

// PlanFragmentWire is the part of one word a token covers — the intersection
// that lets a cross-space token color both of the words it touches.
type PlanFragmentWire struct {
	WordID int `json:"word_id"`
	Start  int `json:"start"`
	End    int `json:"end"`
}

// WordTargetWire is one word's frozen search target from the retained
// telemetry snapshot (WO-097 §7, §8).
type WordTargetWire struct {
	WordID int    `json:"word_id"`
	Word   string `json:"word"`
	// Target is the overlap-adjusted estimate — the denominator a word bar
	// draws against. Actual counts are allowed to exceed it (WO-095 §7): it is
	// an estimate, not a ceiling.
	Target uint64 `json:"target"`
	// Raw is the unadjusted summed estimate, for diagnostics and corpus stats.
	// Never the search target: on mirrored corpora it can be unreachable.
	Raw uint64 `json:"raw"`
	// Known false means show the found count and "target unknown" — never a
	// fabricated marker.
	Known bool `json:"known"`
	// Uncertain marks a target whose overlap correction could not be measured.
	Uncertain bool `json:"uncertain"`
	// SnapshotAgeMS is how old the retained round behind this target is.
	SnapshotAgeMS int64 `json:"snapshot_age_ms"`
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
	// Available is false when the swarm isn't running — distinct from a true
	// empty result, so the interface can say why rather than imply the network
	// has nothing. Not entitled to search at all is a third answer again, and
	// arrives as an ERROR carrying CodeContributionRequired (WO-085) rather
	// than as this flag: "you have not opted in" is a setting the user can
	// change, where this one is a machine state they cannot.
	Available bool `json:"available"`
	// Progress is this search's per-token coverage against WO-067's
	// gossiped target, one entry per distinct token the query tokenized to.
	// Order carries no meaning — see TokenProgress.
	Progress []TokenProgress `json:"progress"`
}

// TokenProgress is one query token's fetched-vs-target coverage (WO-067),
// for the search UI's progress indicator.
//
// TokenIndex, never the token text: the daemon has no reason to send the
// extension the actual substring just to render a progress bar, and not
// sending it means nothing downstream — logs, the render layer, a
// screenshot — ever handles query content that a color-coded bar doesn't
// need. The index is otherwise meaningless outside the daemon's own
// tokenizer; the extension only uses it to pick a stable color per token
// within one render, never to recover what it names.
type TokenProgress struct {
	TokenIndex int    `json:"token_index"`
	Fetched    int    `json:"fetched"`
	Target     uint64 `json:"target"`
	// Known is false when nothing has been gossiped or searched for this
	// token before — Fetched/Target should read as "search this deep, no
	// completeness signal available" rather than a fraction.
	Known bool `json:"known"`
}

// ContributionImpactPayload is GET_CONTRIBUTION_IMPACT_RESULT body (WO-086).
//
// Aggregate numbers only. The counts are two different kinds: the six
// corpus-state fields plus ConnectedPeers/KeelPeers are recomputed fresh on
// every call and persist nowhere; RequestsAnswered/BytesServed/SinceDay are
// the one thing the daemon persists for this feature, as coarse running
// totals with no peer id, query, prefix/bucket identifier or per-request
// timestamp behind them — see daemon/store/contribution_impact.go.
type ContributionImpactPayload struct {
	RequestsAnswered int64  `json:"requests_answered"`
	BytesServed      int64  `json:"bytes_served"`
	SinceDay         string `json:"since_day"`

	GraphClaimsLocal      int `json:"graph_claims_local"`
	GraphClaimsPeerCached int `json:"graph_claims_peer_cached"`
	CatalogueLocal        int `json:"catalogue_local"`
	CataloguePeerCached   int `json:"catalogue_peer_cached"`
	BucketsAnnounced      int `json:"buckets_announced"`
	ShardsAnnounced       int `json:"shards_announced"`

	ConnectedPeers int `json:"connected_peers"`
	KeelPeers      int `json:"keel_peers"`

	// Available is false only when no swarm node is running, mirroring
	// PeerSearchResultPayload — distinct from the CodeContributionRequired
	// refusal below Level 2, which never reaches this payload shape at all.
	Available bool `json:"available"`
}

// WordStatsPayload is WORD_STATS request body (WO-068): the query whose
// words the UI wants corpus-frequency bars for. Telemetry only — does not
// trigger shard or word-bucket fetches.
type WordStatsPayload struct {
	Query string `json:"query"`
}

// WordStatsResultPayload is WORD_STATS_RESULT (WO-068).
type WordStatsResultPayload struct {
	DistinctWords  uint64         `json:"distinct_words"`
	DistinctGraphs uint64         `json:"distinct_graphs"`
	Peers          int            `json:"peers"`
	Available      bool           `json:"available"`
	Words          []WordStatWire `json:"words"`
}

// WordStatWire is one query word's global % plus nested char-token coverage.
type WordStatWire struct {
	Word   string              `json:"word"`
	Pct    *float64            `json:"pct"`
	Tokens []TokenCoverageWire `json:"tokens"`
}

// TokenCoverageWire is one bottom-tier char-token sub-bar (opaque index).
type TokenCoverageWire struct {
	TokenIndex int    `json:"token_index"`
	Estimate   uint64 `json:"estimate"`
	Known      bool   `json:"known"`
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
