// SPDX-License-Identifier: Apache-2.0
// Local suggestion engine (WO-023).
//
// Recommends from the co-recommendation graph the corpus already contains: an
// edge A→B means "B was observed in A's rail". No peer data, no network, no
// engagement signal — see DESIGN_BOOTSTRAP §4.
//
// The walk is random-walk-with-restart, computed by power iteration rather
// than by actually sampling. Same distribution, deterministic, and testable —
// a sampled walk would make every test flaky for no benefit.
package store

import (
	"database/sql"
	"math"
	"sort"

	"github.com/keel-app/keel/daemon/bridge"
)

const (
	// walkIterations is enough for the mass to settle at these alphas; the
	// ranking stops changing well before this on graphs of this size.
	walkIterations = 24
	// maxSuggestions caps the response.
	maxSuggestions = 50
)

type edge struct {
	to     string
	weight float64
}

// alphaForEntropy maps the 0–100 slider to a restart probability.
//
// High restart keeps the walker on top of the seed, so only immediate
// neighbours accumulate mass — that is "focus". Low restart lets it wander
// several hops out, which is "serendipity".
func alphaForEntropy(entropy int) float64 {
	if entropy < 0 {
		entropy = 0
	}
	if entropy > 100 {
		entropy = 100
	}
	return 0.85 - (float64(entropy)/100.0)*0.70 // 0.85 → 0.15
}

// loadGraph builds the weighted adjacency from observed rails.
//
// Weight combines how often the pair was seen with how high B sat in A's rail:
// a video repeatedly at slot 0 is a stronger signal than one glimpsed once at
// slot 18. Slot weight decays gently so a deep-but-frequent pair still counts.
func (s *Store) loadGraph() (map[string][]edge, int, error) {
	rows, err := s.db.Query(`
SELECT context_video_id, video_id, COUNT(*) AS n, AVG(slot_index) AS avg_slot
FROM impressions
WHERE surface = 'WATCH_NEXT'
  AND context_video_id IS NOT NULL AND context_video_id != ''
  AND context_video_id != video_id
GROUP BY context_video_id, video_id`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	g := make(map[string][]edge)
	edges := 0
	for rows.Next() {
		var from, to string
		var n int64
		var avgSlot sql.NullFloat64
		if err := rows.Scan(&from, &to, &n, &avgSlot); err != nil {
			return nil, 0, err
		}
		slotBoost := 1.0
		if avgSlot.Valid {
			slotBoost = 1.0 / (1.0 + avgSlot.Float64/8.0)
		}
		g[from] = append(g[from], edge{to: to, weight: float64(n) * slotBoost})
		edges++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Normalize each node's outgoing weights to a probability distribution.
	for from, es := range g {
		var sum float64
		for _, e := range es {
			sum += e.weight
		}
		if sum <= 0 {
			continue
		}
		for i := range es {
			es[i].weight /= sum
		}
		g[from] = es
	}
	return g, edges, nil
}

// Suggest ranks videos reachable from seed in the co-recommendation graph.
//
// If seed is empty the walk starts from the most recently observed context
// video, so the page has something useful to show without being asked.
func (s *Store) Suggest(seed string, entropy, limit int) (*bridge.SuggestResultPayload, error) {
	if limit <= 0 || limit > maxSuggestions {
		limit = maxSuggestions
	}
	out := &bridge.SuggestResultPayload{
		SeedVideoID: seed,
		Entropy:     entropy,
		Suggestions: []bridge.Suggestion{},
	}

	if seed == "" {
		if err := s.db.QueryRow(`
SELECT context_video_id FROM impressions
WHERE surface = 'WATCH_NEXT' AND context_video_id IS NOT NULL AND context_video_id != ''
ORDER BY observed_at DESC LIMIT 1`).Scan(&seed); err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		out.SeedVideoID = seed
	}
	if seed == "" {
		return out, nil // empty corpus: nothing to walk
	}
	out.SeedTitle = s.titleForVideo(seed)

	g, edgeCount, err := s.loadGraph()
	if err != nil {
		return nil, err
	}
	// Merge imported peer edges. This is the whole reason to import: one
	// person's watching produces very few graph roots, so depth has to come
	// from somewhere else (DESIGN_SHARING §5).
	pg, peerCount, err := s.peerGraph()
	if err != nil {
		return nil, err
	}
	if peerCount > 0 {
		g = mergeGraphs(g, pg)
		edgeCount += peerCount
	}
	out.GraphNodes = len(g)
	out.GraphEdges = edgeCount
	if len(g[seed]) == 0 {
		return out, nil // seed has no observed rail
	}

	alpha := alphaForEntropy(entropy)
	rank := map[string]float64{seed: 1.0}
	// via tracks the strongest first hop reaching each node, so the UI can say
	// which observed rail led there rather than asserting a reason.
	via := map[string]string{}

	for i := 0; i < walkIterations; i++ {
		next := make(map[string]float64, len(rank))
		next[seed] += alpha
		for node, mass := range rank {
			es := g[node]
			if len(es) == 0 {
				// Dangling node: return its mass to the seed rather than losing it.
				next[seed] += (1 - alpha) * mass
				continue
			}
			for _, e := range es {
				add := (1 - alpha) * mass * e.weight
				next[e.to] += add
				if node == seed {
					via[e.to] = seed
				} else if _, ok := via[e.to]; !ok {
					via[e.to] = node
				}
			}
		}
		rank = next
	}

	blocked, err := s.blocklistSet()
	if err != nil {
		return nil, err
	}

	var ranked []scored
	for id, sc := range rank {
		if id == seed || sc <= 0 {
			continue
		}
		ranked = append(ranked, scored{id, sc})
	}

	meta, err := s.videoMeta(ids(ranked))
	if err != nil {
		return nil, err
	}

	// Serendipity: at high entropy, damp by popularity so the walk surfaces
	// niche material instead of falling into gravity wells. This — not the
	// crypto — is what delivers the anti-popularity promise (§3).
	pop := float64(entropy) / 100.0
	for i := range ranked {
		m := meta[ranked[i].id]
		if m == nil {
			continue
		}
		if pop > 0 && m.views > 0 {
			ranked[i].score /= math.Pow(math.Log10(m.views+10), pop)
		}
	}

	sort.Slice(ranked, func(a, b int) bool {
		if ranked[a].score != ranked[b].score {
			return ranked[a].score > ranked[b].score
		}
		return ranked[a].id < ranked[b].id // stable for tests
	})

	for _, r := range ranked {
		m := meta[r.id]
		if m == nil {
			// No metadata: the video arrived as a stringless graph edge and its
			// catalogue row has not caught up. The suggestion is still real —
			// the walk found it — so it is surfaced with an empty title rather
			// than dropped. Dropping would make fetched graph data useless
			// until catalogue sync exists.
			//
			// Except when the user has blocked channels. The channel of an
			// unlabelled video is unknown, so the blocklist cannot be applied
			// to it, and silently showing something the user asked never to see
			// is worse than briefly hiding something they did not. Fail closed
			// on an explicit instruction; fail open where none was given.
			if len(blocked) > 0 {
				continue
			}
			out.Suggestions = append(out.Suggestions, bridge.Suggestion{
				VideoID: r.id,
				Score:   r.score,
			})
			if len(out.Suggestions) >= limit {
				break
			}
			continue
		}
		if m.channelID != nil && blocked[*m.channelID] {
			continue
		}
		sg := bridge.Suggestion{
			VideoID:   r.id,
			Title:     m.title,
			ChannelID: m.channelID,
			Score:     r.score,
			Seen:      m.seen,
		}
		if m.views > 0 {
			v := m.views
			sg.ViewCount = &v
		}
		if m.duration > 0 {
			d := m.duration
			sg.DurationS = &d
		}
		// A video with no local observation reached the walk only through an
		// imported edge. Flagging it keeps provenance visible rather than
		// presenting a peer's graph as your own.
		sg.FromPeer = m.seen == 0
		if v, ok := via[r.id]; ok && v != "" {
			vv := v
			sg.ViaVideoID = &vv
			sg.ViaTitle = s.titleForVideo(v)
		}
		out.Suggestions = append(out.Suggestions, sg)
		if len(out.Suggestions) >= limit {
			break
		}
	}
	return out, nil
}

// scored is one candidate and its walk mass.
type scored struct {
	id    string
	score float64
}

func ids(rs []scored) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.id)
	}
	return out
}

type vmeta struct {
	title     string
	channelID *string
	views     float64
	duration  float64
	seen      int64
}

// videoMeta fetches catalogue metadata for a set of ids in one pass.
func (s *Store) videoMeta(idList []string) (map[string]*vmeta, error) {
	out := map[string]*vmeta{}
	if len(idList) == 0 {
		return out, nil
	}
	// Union the local corpus with imported catalogue rows: a peer's edge is
	// useless if the video it points at has no title to show. Local metadata
	// wins where both exist, since it was observed here.
	rows, err := s.db.Query(`
SELECT video_id, MAX(title), MAX(channel_id), MAX(view_count), MAX(duration_s), COUNT(*) AS seen
FROM impressions GROUP BY video_id
UNION ALL
SELECT video_id, title, channel_id, view_count, duration_s, 0 AS seen
FROM peer_catalogue
WHERE video_id NOT IN (SELECT video_id FROM impressions)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := make(map[string]bool, len(idList))
	for _, id := range idList {
		want[id] = true
	}
	for rows.Next() {
		var id string
		var title, ch sql.NullString
		var views, dur sql.NullFloat64
		var seen int64
		if err := rows.Scan(&id, &title, &ch, &views, &dur, &seen); err != nil {
			return nil, err
		}
		if !want[id] {
			continue
		}
		m := &vmeta{title: title.String, views: views.Float64, duration: dur.Float64, seen: seen}
		if ch.Valid {
			v := ch.String
			m.channelID = &v
		}
		out[id] = m
	}
	return out, rows.Err()
}

func (s *Store) blocklistSet() (map[string]bool, error) {
	out := map[string]bool{}
	list, err := s.ListBlocklist()
	if err != nil {
		return nil, err
	}
	for _, c := range list {
		out[c] = true
	}
	return out, nil
}

// mergeGraphs combines local and peer adjacency, then renormalises.
//
// Peer edges are weighted equally with local ones. That gets depth fastest,
// which is the point of importing at all; the source is still recorded in
// peer_edges, so down-weighting later is a query change rather than a
// re-import (DESIGN_SHARING open question 1).
func mergeGraphs(local, peer map[string][]edge) map[string][]edge {
	acc := map[string]map[string]float64{}
	add := func(g map[string][]edge) {
		for from, es := range g {
			if acc[from] == nil {
				acc[from] = map[string]float64{}
			}
			for _, e := range es {
				acc[from][e.to] += e.weight
			}
		}
	}
	add(local)
	add(peer)

	out := make(map[string][]edge, len(acc))
	for from, tos := range acc {
		var sum float64
		for _, w := range tos {
			sum += w
		}
		if sum <= 0 {
			continue
		}
		es := make([]edge, 0, len(tos))
		for to, w := range tos {
			es = append(es, edge{to: to, weight: w / sum})
		}
		out[from] = es
	}
	return out
}
