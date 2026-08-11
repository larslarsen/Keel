// SPDX-License-Identifier: Apache-2.0
// Distributed token-shard search (WO-059): the network half. See
// daemon/store/shard.go for the tokenizer and the local shard index this
// file serves and fetches.
package swarm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/network"
	mh "github.com/multiformats/go-multihash"

	"github.com/keel-app/keel/daemon/store"
)

// shardCID maps a shard number to the DHT key its holders announce under.
//
// Its own domain string so a shard provider record can never be confused with
// a graph or catalogue one — the same reasoning prefixCID's doc comment gives
// for those two, applied to the third dataset.
func shardCID(shard int) (cid.Cid, error) {
	sum, err := mh.Sum([]byte(fmt.Sprintf("keel/shard/1/ks%d/%d", store.KeySchemeVersion, shard)), mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

// handleShardRequest answers one stream: a shard number in, every
// (video, matched tokens) pair this node holds in that shard out.
//
// The peer never learns which token was wanted — only that this node asked
// for shard G, which by construction holds many tokens (store.ShardM groups
// them precisely so no single shard identifies one). This mirrors
// handleCatalogueRequest's Level-3 gating: below that level the reply is
// built from peer_catalogue only, never this node's own impressions.
func (n *Node) handleShardRequest(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	line, err := bufio.NewReader(io.LimitReader(s, 32)).ReadString('\n')
	if err != nil && err != io.EOF {
		return
	}
	shard, ok := parseShard(trimLine(line))
	if !ok {
		return
	}
	entries, err := n.st.ShardSlice(shard, !n.cfg.ServeOwnObservations)
	if err != nil {
		n.logf("shard %d: %v", shard, err)
		return
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return
	}
	_, _ = s.Write(raw)
}

func parseShard(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n >= store.ShardM {
			return 0, false
		}
	}
	return n, true
}

// FetchShard retrieves every video this node can find that carries `token`,
// by fetching the whole shard `token` groups into from several peers and
// keeping only entries actually tagged with `token` — the tag-self-filter
// WO-059 requires: a peer's shard slice may hold other videos only because
// unrelated tokens hash into the same shard, and dropping those silently
// (rather than treating their absence-of-token as "no match") is what stops
// one thin peer from nulling a search another peer could have answered.
//
// Stop condition is deliberately simpler than the full design's target-based
// one (which needs a network-wide distinct count this pass does not build,
// see handoff/WO-067): poll distinct providers, union what each contributes,
// and stop after 3 consecutive peers add nothing new or after 20 peers,
// whichever comes first. Both numbers are the same shape of backstop the full
// design keeps even once a target exists ("saturating below target → keep
// going... disk slider as backstop") — this just doesn't have the target half.
func (n *Node) FetchShard(ctx context.Context, token string) (map[string][]string, error) {
	out := map[string][]string{}
	if !n.cfg.Fetch {
		return out, nil
	}
	shard := store.ShardOf(token)
	c, err := shardCID(shard)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	const (
		maxPeers         = 20
		saturationStreak = 3
	)
	tried := 0
	misses := 0
	for p := range n.dht.FindProvidersAsync(ctx, c, maxPeers) {
		if p.ID == n.host.ID() || len(p.Addrs) == 0 {
			continue
		}
		raw, err := n.requestOn(ctx, p, fmt.Sprintf("%d", shard), ShardProtocol)
		if err != nil {
			continue
		}
		var entries []store.ShardEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			continue
		}
		tried++
		gained := 0
		for _, e := range entries {
			hasToken := false
			for _, t := range e.Tokens {
				if t == token {
					hasToken = true
					break
				}
			}
			if !hasToken {
				continue
			}
			if _, seen := out[e.VideoID]; !seen {
				gained++
			}
			out[e.VideoID] = e.Tokens
		}
		n.remember(p)
		if gained == 0 {
			misses++
			if misses >= saturationStreak {
				break
			}
		} else {
			misses = 0
		}
		if tried >= maxPeers {
			break
		}
		select {
		case <-ctx.Done():
			return out, nil
		default:
		}
	}
	return out, nil
}

// PeerSearch finds videos matching every token in a query, across peers.
//
// Tokenizing the whole query and intersecting each token's result set is
// Construction B's conjunction (WO-059): no peer ever sees more than one
// shard fetch's worth of information, and a shard groups many tokens, so no
// single fetch identifies the token, let alone the query.
func (n *Node) PeerSearch(ctx context.Context, query string) ([]string, error) {
	tokens := store.TokenizeQuery(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	var result map[string]bool
	for _, tok := range tokens {
		hits, err := n.FetchShard(ctx, tok)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(hits))
		for id := range hits {
			set[id] = true
		}
		if result == nil {
			result = set
			continue
		}
		for id := range result {
			if !set[id] {
				delete(result, id)
			}
		}
		if len(result) == 0 {
			break
		}
	}
	out := make([]string, 0, len(result))
	for id := range result {
		out = append(out, id)
	}
	return out, nil
}
