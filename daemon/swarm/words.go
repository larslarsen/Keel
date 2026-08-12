// SPDX-License-Identifier: Apache-2.0
// Word-level corpus telemetry transport (WO-068).
//
// Direct on-demand fetch of each peer's WordTelemetry pack — not gossip.
// Display-only: never drives a search stop condition (token sketches do).
// Poison defense is multi-peer median of cardinality estimates; no signing
// (messages carry no author, and a wrong percentage cannot break search).
package swarm

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/keel-app/keel/daemon/store"
)

// WordTelemetryProtocol is the stream id for one whole-node word/graph pack.
// Key scheme versioned like other keel protocols so a precision or CMS shape
// change cannot mix with older peers.
var WordTelemetryProtocol = keelProtocol("word-telemetry", "1.0.0", store.KeySchemeVersion)

// maxWordTelemetryPeers caps how many peers one UI request will dial.
const maxWordTelemetryPeers = 7

type wordPeerPack struct {
	words uint64
	pack  *store.WordTelemetry
}

// wordTelemetryCorpus is the SourceSet the word-telemetry pack is always built
// with: everything this node holds, at every level.
//
// Named rather than written inline so the two call sites read as a decision
// instead of a copied flag (WO-077), and deliberately *not*
// Policy.CatalogueSources: this one is not the served-corpus selector every
// other builder shares. A Level-1 node has no catalogue sources at all and
// still answers this, because the pack is a fixed-shape aggregate rather than a
// bucket of material.
var wordTelemetryCorpus = store.AllSources

// handleWordTelemetry serves this node's local pack. Request body is ignored
// (one short line); reply is the JSON WordTelemetry wire form — registers
// only, never word strings.
func (n *Node) handleWordTelemetry(s network.Stream) {
	defer s.Close()
	// Its own capability, not block service: Level 1 answers this (WO-077).
	if !n.mayExchangeWordTelemetry() {
		return
	}
	// Limited like every other serve path (WO-085). This one is on at Level 1,
	// which is exactly why the limiter is not tied to the contribution level:
	// a node that serves no blocks at all still answers this stream, and still
	// has to survive a peer that asks for it in a loop.
	release, ok := n.serve.admit(s.Conn().RemotePeer())
	if !ok {
		return
	}
	defer release()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))
	// Drain a short request line so clients can share requestOn's write shape.
	_, _ = io.CopyN(io.Discard, s, 32)

	// wordTelemetryCorpus, NOT the served-corpus selector (WO-077, WO-084).
	//
	// This pack is a fixed-shape HLL/CMS aggregate — no plaintext words, ids,
	// edges or query — so including locally observed titles is what makes the
	// global word statistic actually cover this node's corpus. Passing
	// mirror-only here (as this did when the flag was borrowed from block
	// service) silently excluded every node from a statistic it was itself
	// reading. ARCHITECTURE_CURRENT §3 requires the disclosure be stated in
	// the consent copy rather than avoided by under-reporting; "no plaintext
	// words" is not zero disclosure, since a guessed word can be tested
	// against a CMS.
	pack, err := n.st.LocalWordTelemetry(wordTelemetryCorpus)
	if err != nil {
		n.logf("word telemetry: %v", err)
		return
	}
	pack.PrepareWire()
	raw, err := json.Marshal(pack)
	if err != nil {
		return
	}
	if !n.serve.chargeBytes(len(raw)) {
		n.logf("word telemetry: over the serving byte budget, dropping the reply")
		return
	}
	_, _ = s.Write(raw)
}

// WordStats is the daemon→extension answer for one query's corpus bars.
type WordStats struct {
	// DistinctWords is the swarm-merged HLL estimate (headline).
	DistinctWords uint64 `json:"distinct_words"`
	// DistinctGraphs is the denominator HLL estimate.
	DistinctGraphs uint64 `json:"distinct_graphs"`
	// Peers is how many peer packs were accepted after median filtering.
	Peers int `json:"peers"`
	// Available is false when the swarm cannot fetch (Level 1 / no node).
	Available bool `json:"available"`
	// Words is one entry per non-stopword query word, in query order.
	Words []WordStat `json:"words"`
}

// WordStat is one top-tier bar plus nested char-token sub-bars.
type WordStat struct {
	Word string `json:"word"`
	// Pct is graphs-containing-word / graphs-total * 100. Nil when unknown.
	Pct *float64 `json:"pct"`
	// Tokens are ShardK char n-grams of this word (existing tokenize).
	Tokens []TokenCoverage `json:"tokens"`
}

// TokenCoverage is one bottom-tier sub-bar from the gossiped token sketch.
type TokenCoverage struct {
	// TokenIndex is the opaque dictionary slot (never the token text).
	TokenIndex int `json:"token_index"`
	// Estimate is the gossiped network-wide distinct-video count for the token.
	Estimate uint64 `json:"estimate"`
	// Known is false when this node has never held/heard a sketch for it.
	Known bool `json:"known"`
}

// FetchWordStats builds local telemetry, pulls packs from connected peers,
// median-filters peer distinct-word estimates for poison resistance, then
// folds surviving packs into one union used for per-word percentages.
// Token sub-bar data comes from the local gossiped TokenEstimate only
// (push-only WO-067 path — never a word-stat fetch).
func (n *Node) FetchWordStats(ctx context.Context, query string) (WordStats, error) {
	out := WordStats{Available: true, Words: []WordStat{}}
	if !n.cfg.Policy.Fetch {
		out.Available = false
		return out, nil
	}

	// Same reasoning as handleWordTelemetry: the local half of the union is
	// this node's own corpus, so excluding it would make the displayed
	// percentages describe everyone except the person reading them.
	local, err := n.st.LocalWordTelemetry(wordTelemetryCorpus)
	if err != nil {
		return out, err
	}

	var packs []wordPeerPack
	for _, pid := range n.host.Network().Peers() {
		if len(packs) >= maxWordTelemetryPeers {
			break
		}
		if ctx.Err() != nil {
			break
		}
		p, err := n.fetchWordTelemetry(ctx, pid)
		if err != nil || p == nil {
			continue
		}
		packs = append(packs, wordPeerPack{words: p.DistinctWords(), pack: p})
	}

	merged := store.NewWordTelemetry()
	_ = merged.Merge(local)

	survivors := medianFilterWordPacks(packs)
	out.Peers = len(survivors)
	for _, sp := range survivors {
		_ = merged.Merge(sp.pack)
	}

	out.DistinctWords = merged.DistinctWords()
	out.DistinctGraphs = merged.DistinctGraphs()

	words := store.FilterStopwords(store.QueryWords(query))
	for _, w := range words {
		ws := WordStat{Word: w, Tokens: []TokenCoverage{}}
		if pct, ok := merged.WordPct(w); ok {
			r := math.Round(pct*10) / 10
			ws.Pct = &r
		}
		for _, tok := range store.CharTokensForWord(w) {
			idx, ok := store.TokenDictIndex(tok)
			if !ok {
				continue
			}
			est, known := n.st.TokenEstimate(tok)
			ws.Tokens = append(ws.Tokens, TokenCoverage{
				TokenIndex: idx, Estimate: est, Known: known,
			})
		}
		out.Words = append(out.Words, ws)
	}
	return out, nil
}

func (n *Node) fetchWordTelemetry(ctx context.Context, pid peer.ID) (*store.WordTelemetry, error) {
	info := n.host.Peerstore().PeerInfo(pid)
	raw, err := n.requestOn(ctx, info, "w", WordTelemetryProtocol)
	if err != nil {
		return nil, err
	}
	var pack store.WordTelemetry
	if err := json.Unmarshal(raw, &pack); err != nil {
		return nil, err
	}
	if err := pack.Hydrate(); err != nil {
		return nil, err
	}
	return &pack, nil
}

// medianFilterWordPacks keeps packs whose distinct-word estimate sits within
// a band of the peer median. With fewer than 3 peers, keep all — median
// filtering needs a real middle. Band is generous (median/4 .. median*4,
// floored) so honest HLL noise stays and only gross inflation is dropped.
func medianFilterWordPacks(packs []wordPeerPack) []wordPeerPack {
	if len(packs) < 3 {
		return packs
	}
	vals := make([]uint64, len(packs))
	for i, p := range packs {
		vals[i] = p.words
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	med := vals[len(vals)/2]
	if med == 0 {
		return packs
	}
	lo := med / 4
	if lo == 0 {
		lo = 1
	}
	hi := med * 4
	out := make([]wordPeerPack, 0, len(packs))
	for _, p := range packs {
		if p.words >= lo && p.words <= hi {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return packs
	}
	return out
}
