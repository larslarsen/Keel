// SPDX-License-Identifier: Apache-2.0
// Distributed token-shard search (WO-059): the network half. See
// daemon/store/shard.go for the tokenizer and the local shard index this
// file serves and fetches.
package swarm

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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
	// The literal "1" is this key's own domain string and is not the key
	// scheme; store.KeySchemeVersion is what fences scheme 1 from scheme 2
	// here (WO-097 §5), so a scheme-2 node never finds a scheme-1 provider
	// record and never fetches token data generated under the other rule.
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

// handleShardRequest answers one stream: a shard number in, a signed
// store.ShardPack of every (video, matched tokens) pair this node holds in
// that shard out.
//
// The peer never learns which token was wanted — only that this node asked
// for shard G, which by construction holds many tokens (store.ShardM groups
// them precisely so no single shard identifies one). This mirrors
// handleCatalogueRequest's Level-3 gating: below that level the reply is
// built from peer_catalogue only, never this node's own impressions.
//
// Signed (WO-067), unlike WO-059's original bare-array reply: FetchShard
// unions replies from several peers, and an unsigned reply cannot be
// distinguished from a forged one when two peers disagree about a video.
func (n *Node) handleShardRequest(s network.Stream) {
	defer s.Close()
	if !n.mayServeBlocks() {
		return
	}
	// The most open-ended demand a peer can place on this node, and so the
	// one the WO-085 limiter exists for most directly: shard requests are
	// driven by other people's arbitrary searches, not by their watching.
	release, ok := n.serve.admit(s.Conn().RemotePeer())
	if !ok {
		return
	}
	defer release()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	line, err := bufio.NewReader(io.LimitReader(s, 64)).ReadString('\n')
	if err != nil && err != io.EOF {
		return
	}
	shard, nonce, ok := parseShardRequest(trimLine(line))
	if !ok {
		return
	}
	rows, offset, err := n.st.ShardRows(shard, n.cfg.Policy.CatalogueSources(), nonce)
	if err != nil {
		n.logf("shard %d: %v", shard, err)
		return
	}
	written, err := n.servePagedResponse(s, fmt.Sprintf("%d", shard), len(rows), offset,
		func(index, start, count int) (any, string, error) {
			pack, err := n.st.SignShardPage(shard, index, offset, rows[start:start+count])
			if err != nil {
				return nil, "", err
			}
			return pack, pack.ContentSHA256, nil
		})
	if err == nil {
		// Logged on success, not only on failure. Without a positive signal there is
		// no way to tell "no one asked" from "asked and refused" — which is exactly
		// what made intermittent search coverage undiagnosable from this side.
		n.logf("shard %d: served %d rows in %d bytes to %s", shard, len(rows), written, s.Conn().RemotePeer())
	}
	n.commitPagedServe(written, err, fmt.Sprintf("shard %d", shard))
}

// parseShardRequest reads "<shard>" or "<shard> <nonce>".
//
// The nonce is optional so a malformed or absent one degrades to offset zero
// rather than refusing the request. It carries no token, title or id — it only
// moves where a partial traversal begins (store.PageStart).
func parseShardRequest(line string) (shard int, nonce uint64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 || len(fields) > 2 {
		return 0, 0, false
	}
	shard, ok = parseShard(fields[0])
	if !ok {
		return 0, 0, false
	}
	if len(fields) == 2 {
		// A nonce that does not parse is ignored, not fatal: it is a hint
		// about traversal order, never part of what is being asked for.
		nonce, _ = strconv.ParseUint(fields[1], 10, 64)
	}
	return shard, nonce, true
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

// requestNonce is a fresh random traversal offset seed for one shard or
// catalogue request.
//
// crypto/rand rather than math/rand: the value is observable by the peer
// answering, and a predictable sequence would let it correlate a node's
// successive requests with each other. On the vanishingly unlikely read
// failure this falls back to zero, which costs traversal spread and nothing
// else.
func requestNonce() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return binary.BigEndian.Uint64(b[:])
}

// shardClaim is what one peer said about one video within a shard reply:
// whether its tag list included the token being fetched, and whether that
// claim came signed.
type shardClaim struct {
	hasToken bool
	signed   bool
}

// shouldStopOnSaturation is FetchShard's stop-condition decision (WO-067),
// pulled out as a pure function for the same reason resolveShardEntries is:
// testing it directly is deterministic, where testing it through real DHT
// provider ordering would not be — provider order isn't something a test
// controls, so a scenario needing "three empty peers, then a fourth that
// has the answer" can't be constructed reliably against the real network
// stack.
//
// Without a known target (haveTarget=false — nothing gossiped or searched
// for this token before), this is pure saturation: a miss streak stops the
// search, the pre-WO-067 behavior. With a target, a miss streak alone is not
// enough — "counts almost never decrease" (WO-059's own design), so three
// quiet peers more likely means bad luck in who was tried than an empty
// rest-of-network. Saturation only stops the search once found has actually
// reached target.
func shouldStopOnSaturation(misses, saturationStreak int, haveTarget bool, found int, target uint64) bool {
	if misses < saturationStreak {
		return false
	}
	return !haveTarget || uint64(found) >= target
}

// resolveShardEntries folds one peer's (already signature-verified) shard
// pack into the running cross-peer state, applying WO-067's poison rule, and
// returns how many videos this pack newly added to out.
//
// A pure function on purpose — kept separate from FetchShard's network loop
// so the disagreement/override/poison logic can be tested directly against
// crafted packs, without needing two real peers to naturally disagree (which
// would mean engineering a title-collision by hand, since a video's tag set
// is deterministic from its title).
func resolveShardEntries(entries []store.ShardEntry, token string, signed bool,
	known map[string]shardClaim, poisoned map[string]bool, out map[string][]string) (gained int) {
	for _, e := range entries {
		if poisoned[e.VideoID] {
			continue
		}
		hasToken := false
		for _, t := range e.Tokens {
			if t == token {
				hasToken = true
				break
			}
		}
		prior, seen := known[e.VideoID]
		switch {
		case !seen:
			known[e.VideoID] = shardClaim{hasToken: hasToken, signed: signed}
		case prior.hasToken == hasToken:
			if signed && !prior.signed {
				known[e.VideoID] = shardClaim{hasToken: hasToken, signed: true}
			}
		case signed && !prior.signed:
			// The new, signed claim overrides the old, unsigned one — not a
			// poison signal, just a stronger source correcting a weaker one.
			known[e.VideoID] = shardClaim{hasToken: hasToken, signed: true}
		case !signed && prior.signed:
			// The existing signed claim already wins; this unsigned dispute
			// changes nothing.
			hasToken = prior.hasToken
		default:
			// Both signed, or both unsigned, and they disagree: neither side
			// can be trusted over the other.
			poisoned[e.VideoID] = true
			delete(out, e.VideoID)
			continue
		}
		if !hasToken {
			delete(out, e.VideoID)
			continue
		}
		if _, already := out[e.VideoID]; !already {
			gained++
		}
		out[e.VideoID] = e.Tokens
	}
	return gained
}

// FetchShard retrieves every video this node can find that carries `token`,
// by fetching the whole shard `token` groups into from several peers and
// keeping only entries actually tagged with `token` — the tag-self-filter
// WO-059 requires: a peer's shard slice may hold other videos only because
// unrelated tokens hash into the same shard, and dropping those silently
// (rather than treating their absence-of-token as "no match") is what stops
// one thin peer from nulling a search another peer could have answered.
//
// Cross-peer poison detection (WO-067): a video's tag set is a deterministic
// function of its title (tokenize is pure), so two peers who both claim to
// hold the same video in the same shard reply must agree on whether it
// carries the token. A peer simply not mentioning a video it doesn't hold is
// not disagreement — only a direct contradiction between two claims is. When
// one side is signed and the other isn't, the signed claim wins outright
// (no poison, just an override); when both are signed, or both are
// unsigned, and they still disagree, the video is dropped and stays dropped
// — a later peer agreeing with either side does not undo it. This is
// per-search only: nothing here persists across calls, matching how nothing
// else in this path persists either.
//
// Stop condition (WO-067): once this node has a known target for the token
// (store.TokenEstimate, fed by gossiped sketches — see sketch_store.go), a
// stale miss streak below that target does not stop the search — "counts
// almost never decrease" (WO-059's own design), so three quiet peers more
// likely means bad luck in who was tried than an empty rest-of-network.
// Saturation only stops the search once the target is reached; the hard
// maxPeers cap is the real backstop either way, target or not. Without a
// target (nothing gossiped or searched for this token before), this falls
// back to pure saturation — the pre-WO-067 behavior.
//
// Every real search feeds back into the same target via RecordTokenSearch
// (sketch_store.go's drift scheduling), regardless of how the search ended,
// unless nothing was found and nothing was expected — that carries no signal
// worth recording.
func (n *Node) FetchShard(ctx context.Context, token string) (map[string][]string, error) {
	out := map[string][]string{}
	if !n.cfg.Policy.Fetch {
		return out, nil
	}
	// The shard corpus exists to answer distributed search and is fetched from
	// nowhere else, so the entitlement belongs on the primitive too (WO-085) —
	// not only on PeerSearch, which is today's single caller. A future caller
	// reaching for shards has to decide about the entitlement rather than
	// inherit an exemption from where it happened to be checked.
	if !n.MayDistributedSearch() {
		return out, ErrDistributedSearchNotPermitted
	}
	shard := store.ShardOf(token)
	c, err := shardCID(shard)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	target, haveTarget := n.st.TokenEstimate(token)
	defer func() {
		if len(out) == 0 && !haveTarget {
			return
		}
		ids := make([]string, 0, len(out))
		for id := range out {
			ids = append(ids, id)
		}
		if err := n.st.RecordTokenSearch(token, ids); err != nil {
			n.logf("shard: record search: %v", err)
		}
	}()

	const (
		maxPeers         = 20
		saturationStreak = 3
	)
	tried := 0
	misses := 0
	claims := map[string]shardClaim{}
	poisoned := map[string]bool{}
	for p := range n.dht.FindProvidersAsync(ctx, c, maxPeers) {
		if p.ID == n.host.ID() || len(p.Addrs) == 0 {
			continue
		}
		// Yield screening (WO-067): a known-low-yield peer is skipped before
		// spending a full shard fetch on it. Unknown (no gossip received yet
		// from this peer) behaves exactly as before yield gossip existed —
		// try it; the screen only ever removes candidates it has positive
		// evidence against, never candidates it merely hasn't heard about.
		if yield, known := n.yieldGet(p.ID, token); known && !yield {
			continue
		}
		entries, signed, complete, err := n.fetchShardPages(ctx, p, shard)
		if err != nil {
			n.logf("shard %d from %s: %v", shard, p.ID, err)
			continue
		}
		tried++
		gained := resolveShardEntries(entries, token, signed, claims, poisoned, out)
		n.remember(p)
		if !complete {
			// An explicitly incomplete traversal is not evidence that the
			// network is exhausted — the peer stopped on its own budget and
			// still holds rows it did not send (WO-097 §6). Letting it feed the
			// saturation streak would turn "this peer is rate-limited" into
			// "stop searching", which is the silent-truncation failure wearing
			// a different hat. It resets the streak only when it actually
			// gained something; a partial answer that added nothing simply
			// leaves the streak where it was.
			n.logf("shard %d from %s: peer ended the traversal incomplete", shard, p.ID)
			if gained > 0 {
				misses = 0
			}
		} else if gained == 0 {
			misses++
			if shouldStopOnSaturation(misses, saturationStreak, haveTarget, len(out), target) {
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

// fetchShardPages retrieves one peer's whole logical response for a shard and
// combines its pages into one answer (WO-097 §6).
//
// Every page is verified on its own and then checked against the signed
// terminal's ordered digest list, so a dropped, duplicated or reordered frame
// is rejected rather than silently reassembled into a plausible-looking shard.
// `signed` describes the response as a whole and is true only when every page
// carried a signature — resolveShardEntries uses it to decide whose claim wins
// when two peers contradict each other, and a response that is signed in parts
// is not a signed response.
//
// Entries from all pages are returned as one peer answer. Page boundaries are
// an artifact of how the answer travelled; nothing above this function knows
// or cares that there was more than one frame, which is what keeps a page from
// surfacing as a new query token or a second bar (WO-095 §5).
func (n *Node) fetchShardPages(ctx context.Context, p peer.AddrInfo, shard int) (entries []store.ShardEntry, signed, complete bool, err error) {
	entries, signed, complete, _, err = n.fetchShardPagesCounted(ctx, p, shard)
	return entries, signed, complete, err
}

// fetchShardPagesCounted is fetchShardPages plus the wire cost, which a
// streaming job needs for its aggregate resource backstop (WO-095 §5).
func (n *Node) fetchShardPagesCounted(ctx context.Context, p peer.AddrInfo, shard int) (entries []store.ShardEntry, signed, complete bool, bytes int, err error) {
	resp, err := n.requestPaged(ctx, p, fmt.Sprintf("%d %d", shard, requestNonce()), ShardProtocol)
	if err != nil {
		return nil, false, false, 0, err
	}
	bytes = resp.Bytes

	digests := make([]string, 0, len(resp.Pages))
	entries = []store.ShardEntry{}
	signed = true
	seen := map[string]bool{}
	for i, raw := range resp.Pages {
		var pack store.ShardPack
		if err := json.Unmarshal(raw, &pack); err != nil {
			return nil, false, false, 0, err
		}
		if pack.Shard != shard {
			return nil, false, false, 0, fmt.Errorf("page %d answers shard %d, not %d", i, pack.Shard, shard)
		}
		if pack.Index != i {
			return nil, false, false, 0, fmt.Errorf("page arrived at position %d claiming index %d", i, pack.Index)
		}
		if err := store.VerifyShardPack(&pack); err != nil {
			return nil, false, false, 0, err
		}
		if pack.Signature == "" {
			signed = false
		}
		digests = append(digests, pack.ContentSHA256)
		for _, e := range pack.Entries {
			// A row repeated across pages would double-count into the caller's
			// gain accounting. The traversal is a rotation over a deduplicated
			// ordering, so a duplicate means a misbehaving provider, not a
			// large bucket.
			if seen[e.VideoID] {
				continue
			}
			seen[e.VideoID] = true
			entries = append(entries, e)
		}
	}
	if err := pageDigestsMatch(digests, resp.Terminal); err != nil {
		return nil, false, false, 0, err
	}
	return entries, signed, resp.Complete(), bytes, nil
}

// ResolveCandidateTitles downloads the complete broad catalogue/string prefix
// buckets needed to label a set of candidate ids, coalescing candidates that
// share a prefix (WO-097 §9).
//
// Two properties are load-bearing and neither is an optimization:
//
//   - the request names a prefix bucket, never a candidate id, and the whole
//     bucket comes back. Fetching only the rows a search matched would bind the
//     fetch pattern to the query — the correlation catalogue.go rule 1 exists
//     to prevent — and it is why the return value is a count of rows imported
//     rather than the titles themselves. The caller reads titles back out of
//     the store afterwards, where cover rows and wanted rows are
//     indistinguishable.
//   - candidates sharing a prefix produce one traversal, not one each.
//     MissingCataloguePrefixes already collapses ids to buckets and drops ids
//     already held, so the set of buckets requested depends on this node's
//     cache at bucket granularity and on nothing finer.
//
// Rows arriving here may be cached under the existing public-catalogue rules.
// Only the ids a search actually produced may enter its matcher or counters —
// that boundary belongs to the caller (WO-095 §2), not to this function.
func (n *Node) ResolveCandidateTitles(ctx context.Context, ids []string) (int, error) {
	if !n.cfg.Policy.Fetch || len(ids) == 0 {
		return 0, nil
	}
	prefixes, err := n.st.MissingCataloguePrefixes(ids, n.prefixBits())
	if err != nil || len(prefixes) == 0 {
		return 0, err
	}
	rows := 0
	for _, p := range prefixes {
		select {
		case <-ctx.Done():
			return rows, ctx.Err()
		default:
		}
		got, err := n.fetchCataloguePrefixQuiet(ctx, p)
		if err != nil {
			continue
		}
		rows += got.Rows
	}
	return rows, nil
}

// TokenProgress is one query token's coverage against its gossiped target,
// for the search UI's per-token indicator (WO-067). TokenIndex, not the
// token text itself: the daemon never needs to send the extension the
// actual substring to render a progress indicator, and not sending it means
// nothing downstream of the daemon — logs, the render layer — ever handles
// query content it doesn't need. See tokendict.go for what the index means.
type TokenProgress struct {
	TokenIndex int
	Fetched    int
	Target     uint64
	Known      bool
}

// PeerSearch finds videos matching every token in a query, across peers.
//
// Tokenizing the whole query and intersecting each token's result set is
// Construction B's conjunction (WO-059): no peer ever sees more than one
// shard fetch's worth of information, and a shard groups many tokens, so no
// single fetch identifies the token, let alone the query.
//
// WO-070: with zero connected peers, every token's FetchShard would still
// walk the DHT provider lookup and shard-fetch machinery before giving up —
// each one bounded by requestTimeout (20s), so a multi-token query could
// take minutes to conclude "nothing." A node with no peers at all has
// nothing to fetch from by construction, so this returns immediately rather
// than discovering that the slow way per token.
// WO-085: distributed search is reciprocal. The entitlement check is the very
// first statement, before tokenizing and before the zero-peer fast path, so a
// node without it cannot reach a peer by any route through this function —
// including a caller that skipped the daemon's own check.
func (n *Node) PeerSearch(ctx context.Context, query string) ([]string, []TokenProgress, error) {
	if !n.MayDistributedSearch() {
		return nil, nil, ErrDistributedSearchNotPermitted
	}
	tokens := store.TokenizeQuery(query)
	if len(tokens) == 0 {
		return nil, nil, nil
	}
	if n.Peers() == 0 {
		return nil, nil, nil
	}
	progress := make([]TokenProgress, 0, len(tokens))
	var result map[string]bool
	for i, tok := range tokens {
		// Must be read BEFORE FetchShard, not after: FetchShard's own defer
		// folds this search's results into the estimate via
		// RecordTokenSearch, so reading afterward would report a target
		// that already includes what was just found — self-referentially
		// inflated toward "complete" regardless of true coverage.
		target, known := n.st.TokenEstimate(tok)
		hits, err := n.FetchShard(ctx, tok)
		if err != nil {
			return nil, nil, err
		}
		// One entry per token FETCHED, always. It used to be one per token that
		// happened to have a dictionary index, so a token with no entry was
		// fetched and then reported nothing — the interface drew fewer bars than
		// there were tokens being fetched, with nothing to say a bar was
		// missing. The count of bars is meant to be the count of the work.
		//
		// A token outside the dictionary gets a negative index derived from its
		// position: distinct within this query, never colliding with a real
		// dictionary index, and unmistakably not one.
		idx, ok := store.TokenDictIndex(tok)
		if !ok {
			idx = -1 - i
			known = false
		}
		progress = append(progress, TokenProgress{
			TokenIndex: idx, Fetched: len(hits), Target: target, Known: known,
		})

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
	return out, progress, nil
}
