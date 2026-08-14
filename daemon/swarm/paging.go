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
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
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
func (n *Node) servePagedResponse(s network.Stream, bucket string, total, offset int,
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
		return written, err
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
		written += nbytes
		if err != nil {
			return written, err
		}
		if nbytes < 0 {
			// Budget refusal: the frame was not written, so it must not be
			// counted in the terminal either.
			complete, reason = false, store.ReasonBudget
			break
		}
		digests = append(digests, digest)
		index++
	}

	terminal, err := n.st.SignTerminal(bucket, total, len(digests), complete, reason, digests)
	if err != nil {
		return written, err
	}
	nbytes, err := n.writeFrame(s, terminal)
	if nbytes > 0 {
		written += nbytes
	}
	return written, err
}

// writeFrame marshals and writes one newline-delimited frame, charging the
// serving byte budget first. It returns -1 bytes (and no error) when the
// budget refuses the frame, so the caller can terminate the response honestly
// instead of dropping the whole reply the way the pre-WO-097 paths did.
func (n *Node) writeFrame(s network.Stream, frame any) (int, error) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return 0, err
	}
	if !n.serve.chargeBytes(len(raw) + 1) {
		return -1, nil
	}
	if _, err := s.Write(append(raw, '\n')); err != nil {
		return 0, err
	}
	return len(raw) + 1, nil
}

// requestPaged opens one stream and reads a whole logical response from it.
func (n *Node) requestPaged(ctx context.Context, p peer.AddrInfo, key string, proto protocol.ID) (*pagedResponse, error) {
	if err := n.host.Connect(ctx, p); err != nil {
		return nil, err
	}
	s, err := n.host.NewStream(ctx, p.ID, proto)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(requestTimeout))

	if _, err := io.WriteString(s, key+"\n"); err != nil {
		return nil, err
	}
	if err := s.CloseWrite(); err != nil {
		return nil, err
	}
	counted := &countingReader{r: io.LimitReader(s, maxBlockBytes)}
	resp, err := readPagedResponse(counted)
	if resp != nil {
		resp.Bytes = counted.n
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
