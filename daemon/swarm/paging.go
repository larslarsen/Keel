// SPDX-License-Identifier: Apache-2.0
// The transport half of bounded logical responses (WO-097 §6). See
// daemon/store/paging.go for the frames themselves and why they exist.
//
// One request, one logical response, several frames on the same stream. The
// requester names a broad bucket — a shard number, a catalogue prefix — and
// never a token, candidate id or narrower key; the provider answers with a
// header, bounded signed pages, and an authenticated terminal that says
// whether the traversal finished. A stream that dies mid-response has no valid
// terminal and is reported as incomplete, never as an empty success.
package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/keel-app/keel/daemon/store"
)

// pagedResponse is one validated logical response as the requester sees it.
type pagedResponse struct {
	Header   store.PageHeader
	Pages    []json.RawMessage
	Terminal *store.PageTerminal
	// Bytes is what this response actually cost on the wire, which is what a
	// job's aggregate resource backstop has to be measured in (WO-095 §5). Row
	// counts are not a substitute: a shard of long titles and a shard of short
	// ones are the same number of rows and wildly different downloads.
	Bytes int
}

// Complete reports whether the provider traversed the whole bucket. False
// means the provider stopped early and said so — a budget or page-cap
// termination — which is information the caller must keep rather than round
// off to "that is everything".
func (r *pagedResponse) Complete() bool { return r.Terminal != nil && r.Terminal.Complete }

// servePagedResponse writes one logical response: header, pages, terminal.
//
// `page` builds and signs the frame covering rotated rows [start, start+count)
// and returns it with its digest. The digests accumulate into the terminal, so
// a requester can detect a dropped, duplicated or reordered frame without
// trusting frame order alone.
//
// The serving byte budget is charged per frame and is the thing that ends a
// traversal early. When it does, the terminal still goes out, still signed,
// with complete=false and a reason — the whole point of the exercise is that
// running out of budget is visible rather than indistinguishable from a small
// bucket.
func (n *Node) servePagedResponse(s io.Writer, bucket string, total, offset int,
	page func(index, start, count int) (any, string, error)) (int, error) {

	header := store.PageHeader{
		Kind:          "header",
		SchemaVersion: 1,
		Bucket:        bucket,
		Total:         total,
		Offset:        offset,
	}
	written, err := n.writeFrame(s, header)
	if err != nil {
		// A refused or failed header is not a logical response. Do not start
		// pages or a terminal, and do not count anything (WO-092).
		return 0, err
	}

	digests := []string{}
	complete := true
	reason := store.ReasonComplete
	index := 0
	for start := 0; start < total; start += store.MaxPageEntries {
		if index >= store.MaxResponsePages {
			complete, reason = false, store.ReasonPageCap
			break
		}
		count := store.MaxPageEntries
		if start+count > total {
			count = total - start
		}
		frame, digest, err := page(index, start, count)
		if err != nil {
			return written, err
		}
		nbytes, err := n.writeFrame(s, frame)
		if errors.Is(err, errFrameBudget) {
			complete, reason = false, store.ReasonBudget
			break
		}
		if err != nil {
			return written, err
		}
		written += nbytes
		digests = append(digests, digest)
		index++
	}

	terminal, err := n.st.SignTerminal(bucket, total, len(digests), complete, reason, digests)
	if err != nil {
		return written, err
	}
	nbytes, err := n.writeFrame(s, terminal)
	if err != nil {
		// Earlier frames may have left the machine, but without a fully
		// written signed terminal there is no valid logical response.
		return written, err
	}
	written += nbytes
	return written, nil
}

// errFrameBudget is a serving-budget refusal of one frame. The frame was not
// written. A page refusal can still become a valid incomplete response if the
// signed terminal is then fully written; a header or terminal refusal cannot.
var errFrameBudget = errors.New("serving budget refused the frame")

// writeFrame marshals and writes one newline-delimited frame, charging the
// serving byte budget first. A budget refusal returns errFrameBudget and zero
// bytes so callers never add a sentinel into a wire total.
func (n *Node) writeFrame(s io.Writer, frame any) (int, error) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return 0, err
	}
	if !n.serve.chargeBytes(len(raw) + 1) {
		return 0, errFrameBudget
	}
	frameBytes := append(raw, '\n')
	if err := writeFull(s, frameBytes); err != nil {
		return 0, err
	}
	return len(frameBytes), nil
}

// commitPagedServe records one answered request only when servePagedResponse
// wrote a complete logical response, including its terminal.
func (n *Node) commitPagedServe(written int, err error, logPrefix string) {
	if err != nil {
		n.logf("%s: %v", logPrefix, err)
		return
	}
	if written <= 0 {
		return
	}
	if recErr := n.st.RecordContributionServe(written); recErr != nil {
		n.logf("%s: recording contribution activity: %v", logPrefix, recErr)
	}
}

// errNoResponse marks a failure that happened before any bytes came back —
// connect, stream open, or request write (WO-101 §3).
//
// The distinction matters downstream: "nobody answered" and "somebody answered
// with garbage" are different facts about a prefix, and collapsing them let an
// invalid reply be recorded as an absent provider.
var errNoResponse = errors.New("no response from peer")

// classifyPagedError maps a PROVIDER failure onto what it established.
//
// Budget termination is deliberately not in this switch. It is not a fact about
// a provider at all — the peer may have been answering perfectly well and this
// node stopped listening — so callers must test ErrSearchBudget and
// short-circuit BEFORE asking this function anything (WO-102 §1). The previous
// version answered `unavailable` for it, which let the traversal treat a
// terminated job as one more failed peer and carry on down the fallback list.
//
// It is left out rather than given a value on purpose: a value would invite
// exactly the ranking this must not participate in.
func classifyPagedError(err error) catalogueOutcome {
	if errors.Is(err, errNoResponse) {
		return catalogueUnavailable
	}
	return catalogueInvalid
}

// requestPaged opens one stream and reads a whole logical response from it.
func (n *Node) requestPaged(ctx context.Context, p peer.AddrInfo, key string, proto protocol.ID) (*pagedResponse, error) {
	if err := n.host.Connect(ctx, p); err != nil {
		return nil, fmt.Errorf("%w: %v", errNoResponse, err)
	}
	s, err := n.host.NewStream(ctx, p.ID, proto)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoResponse, err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	if _, err := io.WriteString(s, key+"\n"); err != nil {
		return nil, fmt.Errorf("%w: %v", errNoResponse, err)
	}
	if err := s.CloseWrite(); err != nil {
		return nil, fmt.Errorf("%w: %v", errNoResponse, err)
	}
	// Reset promptly on cancellation rather than waiting for the deadline
	// above (WO-100 §1). Budget exhaustion cancels the job context, and "the
	// budget is spent" has to mean the transfers stop, not that they stop
	// growing while the open ones drain.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Reset()
		case <-watchDone:
		}
	}()

	// Metered inside the reader, so a reservation is taken BEFORE bytes are
	// read and no response can overshoot the ceiling by the difference between
	// the budget and the transport cap (WO-100 §1). Non-search callers have no
	// meter on their context and read unmetered.
	counted := &countingReader{r: io.LimitReader(s, maxBlockBytes)}
	var reader io.Reader = counted
	if m := meterFrom(ctx); m != nil {
		reader = &budgetReader{ctx: ctx, r: counted, m: m}
	}
	resp, err := readPagedResponse(reader)
	if resp != nil {
		resp.Bytes = counted.n
	}
	if err != nil && counted.n == 0 && !errors.Is(err, ErrSearchBudget) {
		// The stream opened and then produced nothing. That is an absent
		// response, not a malformed one.
		err = fmt.Errorf("%w: %v", errNoResponse, err)
	}
	return resp, err
}

// countingReader records how many bytes a response actually cost.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// readPagedResponse decodes and validates a framed logical response.
//
// json.Decoder rather than line splitting: it consumes concatenated JSON
// values natively, so a frame containing a newline inside a string literal
// cannot desynchronize the reader.
func readPagedResponse(r io.Reader) (*pagedResponse, error) {
	dec := json.NewDecoder(r)
	out := &pagedResponse{Pages: []json.RawMessage{}}
	sawHeader := false

	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("malformed response frame: %w", err)
		}
		var kind struct {
			Kind string `json:"t"`
		}
		if err := json.Unmarshal(raw, &kind); err != nil {
			return nil, fmt.Errorf("frame has no type: %w", err)
		}
		switch kind.Kind {
		case "header":
			if sawHeader {
				return nil, fmt.Errorf("response carries a second header frame")
			}
			if err := json.Unmarshal(raw, &out.Header); err != nil {
				return nil, err
			}
			sawHeader = true
		case "page":
			out.Pages = append(out.Pages, raw)
		case "end":
			var t store.PageTerminal
			if err := json.Unmarshal(raw, &t); err != nil {
				return nil, err
			}
			out.Terminal = &t
		default:
			return nil, fmt.Errorf("unknown response frame type %q", kind.Kind)
		}
		if out.Terminal != nil {
			break
		}
	}

	if !sawHeader {
		return nil, fmt.Errorf("response began without a header frame")
	}
	if err := store.VerifyTerminal(out.Terminal); err != nil {
		return nil, err
	}
	if len(out.Pages) != out.Terminal.Pages {
		return nil, fmt.Errorf("response carried %d pages, terminal claims %d",
			len(out.Pages), out.Terminal.Pages)
	}
	return out, nil
}

// pageDigestsMatch checks the ordered per-page digests against the signed
// terminal — the check that turns "some frames arrived" into "exactly these
// frames, in this order, arrived".
func pageDigestsMatch(got []string, terminal *store.PageTerminal) error {
	if len(got) != len(terminal.PageDigests) {
		return fmt.Errorf("verified %d pages against %d terminal digests",
			len(got), len(terminal.PageDigests))
	}
	for i := range got {
		if got[i] != terminal.PageDigests[i] {
			return fmt.Errorf("page %d does not match the digest the terminal lists for it", i)
		}
	}
	return nil
}
