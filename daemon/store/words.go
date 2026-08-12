// SPDX-License-Identifier: Apache-2.0
// Word-level corpus telemetry (WO-068).
//
// Separate from the character n-gram tokenizer (ShardK): words are
// space-delimited tokens under a shared normalization rule, counted into
// HyperLogLog + Count-Min sketches for display-only global stats. Nothing
// here keys a fetch or a search stop condition — that stays on k=3 tokens
// (WO-059/067). Transport is direct on-demand peer fetch (swarm), not gossip.
package store

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// WordSketchP is the HLL precision for word and graph telemetry sketches.
// Same size class as TokenSketchP (256 registers) so a pack stays small
// enough for an on-demand stream reply.
const WordSketchP = TokenSketchP

// KindWord counts distinct normalized words observed in held titles.
const KindWord SketchKind = "word"

// KindGraph counts distinct video ids treated as "graphs" for the
// per-word percentage denominator (graphs containing w / graphs total).
const KindGraph SketchKind = "graph"

// cmsDepth/cmsWidth size the Count-Min sketch that approximates, per word,
// how many local graphs contain it. Queried only with words the local UI
// already has (never sent on the wire); peers exchange the whole sketch.
const (
	cmsDepth = 3
	cmsWidth = 512
)

// WordStopwords is the display-time exclusion set (WO-060 constant). Words
// still enter local sketches so cardinality is honest; the UI and top-word
// paths drop these so "the: 900,000" never dominates a headline.
//
// Sorted for binary search. Letters-only, already lowercased — matches
// NormalizeWord output.
var WordStopwords = []string{
	"a", "an", "and", "are", "as", "at", "be", "by",
	"episode", "for", "from", "full", "hd", "how", "i", "in", "is", "it",
	"live", "new", "of", "official", "on", "or", "part",
	"that", "the", "this", "to", "video", "vs", "was", "what", "with", "you",
}

// IsStopword reports whether w (already NormalizeWord'd) is display-excluded.
func IsStopword(w string) bool {
	i := sort.SearchStrings(WordStopwords, w)
	return i < len(WordStopwords) && WordStopwords[i] == w
}

// NormalizeWord lowercases and keeps a-z only. Empty if nothing remains.
// Shared rule every node must apply identically (WO-060) so HLL/CMS merges
// are comparable — not a vocabulary, just the transform.
func NormalizeWord(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// WordsFromText splits text under the shared word rule (same letter runs as
// splitWords / normalize). Order preserved; duplicates kept — callers that
// want set semantics dedupe themselves.
func WordsFromText(text string) []string {
	return splitWords(text)
}

// CountMin is a fixed-size frequency sketch. Add once per (graph, word);
// Estimate is an upper bound on how many times the key was added.
//
// Wire form is raw little-endian uint32 counters, depth*width long. Merge is
// register-wise max — union of multi-sets under the usual CMS approximation.
type CountMin struct {
	// counters length is always cmsDepth*cmsWidth when non-nil.
	counters []uint32
}

// NewCountMin returns an empty sketch.
func NewCountMin() *CountMin {
	return &CountMin{counters: make([]uint32, cmsDepth*cmsWidth)}
}

// CountMinFromBytes rebuilds a sketch from its wire form.
func CountMinFromBytes(raw []byte) (*CountMin, error) {
	want := cmsDepth * cmsWidth * 4
	if len(raw) != want {
		return nil, fmt.Errorf("count-min wire length %d, want %d", len(raw), want)
	}
	c := NewCountMin()
	for i := range c.counters {
		c.counters[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	return c, nil
}

// Bytes is the fixed wire encoding.
func (c *CountMin) Bytes() []byte {
	if c == nil || len(c.counters) == 0 {
		return make([]byte, cmsDepth*cmsWidth*4)
	}
	out := make([]byte, len(c.counters)*4)
	for i, v := range c.counters {
		binary.LittleEndian.PutUint32(out[i*4:], v)
	}
	return out
}

func cmsHash(key string, row int) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte{byte(row)})
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}

// Add increments every row for key by 1.
func (c *CountMin) Add(key string) {
	if c == nil {
		return
	}
	if len(c.counters) != cmsDepth*cmsWidth {
		c.counters = make([]uint32, cmsDepth*cmsWidth)
	}
	for d := 0; d < cmsDepth; d++ {
		j := int(cmsHash(key, d) % cmsWidth)
		c.counters[d*cmsWidth+j]++
	}
}

// Estimate is the minimum row counter for key (standard CMS point query).
func (c *CountMin) Estimate(key string) uint32 {
	if c == nil || len(c.counters) != cmsDepth*cmsWidth {
		return 0
	}
	min := ^uint32(0)
	for d := 0; d < cmsDepth; d++ {
		j := int(cmsHash(key, d) % cmsWidth)
		v := c.counters[d*cmsWidth+j]
		if v < min {
			min = v
		}
	}
	if min == ^uint32(0) {
		return 0
	}
	return min
}

// Merge folds other in by element-wise saturating sum. Each peer's CMS
// counts local graphs-containing-word; summing approximates the multi-set
// union when catalogues barely overlap, and WordPct clamps to the graph
// HLL so mirror duplication cannot push a bar past 100%. Max-merge would
// under-count disjoint peers (the common small-swarm case).
func (c *CountMin) Merge(other *CountMin) error {
	if other == nil {
		return fmt.Errorf("nil count-min")
	}
	if len(c.counters) != len(other.counters) {
		return fmt.Errorf("count-min size mismatch")
	}
	for i, v := range other.counters {
		sum := uint64(c.counters[i]) + uint64(v)
		if sum > math.MaxUint32 {
			sum = math.MaxUint32
		}
		c.counters[i] = uint32(sum)
	}
	return nil
}

// WordTelemetry is this node's local word/graph cardinality pack (WO-068).
// Served whole on direct fetch; never carries word strings.
type WordTelemetry struct {
	Words  *Sketch   `json:"-"`
	Graphs *Sketch   `json:"-"`
	Freq   *CountMin `json:"-"`
	// Wire fields — filled on marshal / read on unmarshal.
	WordRegisters  []byte `json:"word_registers"`
	GraphRegisters []byte `json:"graph_registers"`
	FreqCounters   []byte `json:"freq"`
	P              uint8  `json:"p"`
}

// NewWordTelemetry empty pack at WordSketchP.
func NewWordTelemetry() *WordTelemetry {
	return &WordTelemetry{
		Words:  NewSketchP(KindWord, WordSketchP),
		Graphs: NewSketchP(KindGraph, WordSketchP),
		Freq:   NewCountMin(),
		P:      WordSketchP,
	}
}

// PrepareWire copies live structures into JSON-safe fields.
func (w *WordTelemetry) PrepareWire() {
	if w.Words != nil {
		w.WordRegisters = append([]byte(nil), w.Words.Registers...)
		w.P = w.Words.P
	}
	if w.Graphs != nil {
		w.GraphRegisters = append([]byte(nil), w.Graphs.Registers...)
	}
	if w.Freq != nil {
		w.FreqCounters = w.Freq.Bytes()
	}
}

// Hydrate rebuilds live structures from wire fields.
func (w *WordTelemetry) Hydrate() error {
	if w.P == 0 {
		w.P = WordSketchP
	}
	if len(w.WordRegisters) != 1<<w.P {
		return fmt.Errorf("word registers length %d, want %d", len(w.WordRegisters), 1<<w.P)
	}
	if len(w.GraphRegisters) != 1<<w.P {
		return fmt.Errorf("graph registers length %d, want %d", len(w.GraphRegisters), 1<<w.P)
	}
	w.Words = &Sketch{Kind: KindWord, P: w.P, Registers: append([]byte(nil), w.WordRegisters...)}
	w.Graphs = &Sketch{Kind: KindGraph, P: w.P, Registers: append([]byte(nil), w.GraphRegisters...)}
	cm, err := CountMinFromBytes(w.FreqCounters)
	if err != nil {
		return err
	}
	w.Freq = cm
	return nil
}

// Merge folds other into w (HLL max-union + CMS max-union).
func (w *WordTelemetry) Merge(other *WordTelemetry) error {
	if other == nil {
		return fmt.Errorf("nil word telemetry")
	}
	if err := w.Words.Merge(other.Words); err != nil {
		return err
	}
	if err := w.Graphs.Merge(other.Graphs); err != nil {
		return err
	}
	return w.Freq.Merge(other.Freq)
}

// DistinctWords is the HLL estimate of vocabulary size.
func (w *WordTelemetry) DistinctWords() uint64 {
	if w == nil || w.Words == nil {
		return 0
	}
	return w.Words.Count()
}

// DistinctGraphs is the HLL estimate of graph (video) count.
func (w *WordTelemetry) DistinctGraphs() uint64 {
	if w == nil || w.Graphs == nil {
		return 0
	}
	return w.Graphs.Count()
}

// WordGraphCount estimates how many graphs contain word (CMS).
func (w *WordTelemetry) WordGraphCount(word string) uint64 {
	if w == nil || w.Freq == nil {
		return 0
	}
	nw := NormalizeWord(word)
	if nw == "" {
		return 0
	}
	return uint64(w.Freq.Estimate(nw))
}

// WordPct is graphs-containing-word / graphs-total as a 0–100 percentage.
// ok is false when there is no graph denominator yet (empty corpus).
func (w *WordTelemetry) WordPct(word string) (pct float64, ok bool) {
	g := w.DistinctGraphs()
	if g == 0 {
		return 0, false
	}
	c := w.WordGraphCount(word)
	if c > g {
		// CMS overestimate can exceed HLL graph count; clamp for display.
		c = g
	}
	return 100 * float64(c) / float64(g), true
}

// addTitle records one held video's title into the pack. Each distinct
// normalized word is added once per video (graph membership, not term
// frequency inside the title).
func (w *WordTelemetry) addTitle(videoID, title string) {
	if videoID == "" {
		return
	}
	w.Graphs.Add(videoID)
	seen := map[string]bool{}
	for _, raw := range WordsFromText(title) {
		nw := NormalizeWord(raw)
		if nw == "" || seen[nw] {
			continue
		}
		seen[nw] = true
		w.Words.Add(nw)
		w.Freq.Add(nw)
	}
}

// LocalWordTelemetry builds the pack from the titles `sources` selects.
//
// Unlike the catalogue/shard/yield paths, this is *not* driven by
// Policy.CatalogueSources: every level answers it over its whole corpus
// (store.AllSources), because the pack is a fixed-shape HLL/CMS aggregate with
// no plaintext words, ids, edges or query, and a node that excluded itself
// would be absent from a global statistic it is itself reading. See
// swarm/words.go's callers, which name that decision rather than passing a
// borrowed flag.
func (s *Store) LocalWordTelemetry(sources SourceSet) (*WordTelemetry, error) {
	all, err := s.heldCatalogue(sources)
	if err != nil {
		return nil, err
	}
	w := NewWordTelemetry()
	for _, c := range all {
		w.addTitle(c.VideoID, c.Title)
	}
	w.PrepareWire()
	return w, nil
}

// QueryWords splits a search string into display words (normalized, non-empty),
// preserving first-seen order. Stopwords are kept here so the UI can decide
// whether to show them; use FilterStopwords for headline lists.
func QueryWords(query string) []string {
	raw := WordsFromText(query)
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, r := range raw {
		nw := NormalizeWord(r)
		if nw == "" || seen[nw] {
			continue
		}
		seen[nw] = true
		out = append(out, nw)
	}
	return out
}

// FilterStopwords drops display-excluded words.
func FilterStopwords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if !IsStopword(w) {
			out = append(out, w)
		}
	}
	return out
}

// CharTokensForWord returns the ShardK character n-grams of an isolated
// normalized word, using the same tokenize/normalize path titles use.
// For a single word w, normalize produces " w " so windows are well-defined.
func CharTokensForWord(word string) []string {
	return uniqueSorted(tokenize(word, ShardK))
}
