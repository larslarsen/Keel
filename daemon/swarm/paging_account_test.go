// SPDX-License-Identifier: Apache-2.0
// WO-092: paged catalogue/shard replies count only a complete logical
// response — header, any written pages, and a fully written signed terminal.
package swarm

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/keel-app/keel/daemon/store"
)

func pagedTestNode(t *testing.T, name string) (*Node, *store.Store) {
	t.Helper()
	st := newStore(t, name)
	return &Node{st: st, serve: newServeLimiter()}, st
}

func encodedFrameLen(t *testing.T, frame any) int {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw) + 1
}

func leaveServeBudget(t *testing.T, n *Node, remain int) {
	t.Helper()
	used := serveByteBudget - remain
	if used <= 0 {
		return
	}
	if !n.serve.chargeBytes(used) {
		t.Fatal("could not reserve the remaining serve budget")
	}
}

func paddedPage(pad int) func(int, int, int) (any, string, error) {
	body := strings.Repeat("p", pad)
	return func(index, start, count int) (any, string, error) {
		return map[string]any{"t": "page", "i": index, "pad": body}, "digest", nil
	}
}

func activityCounts(t *testing.T, st *store.Store) (answered, bytes int64) {
	t.Helper()
	answered, bytes, _, err := st.ContributionActivity()
	if err != nil {
		t.Fatal(err)
	}
	return answered, bytes
}

func serveAndCommit(n *Node, w io.Writer, total int, page func(int, int, int) (any, string, error)) (int, error) {
	written, err := n.servePagedResponse(w, "bucket", total, 0, page)
	n.commitPagedServe(written, err, "paged-test")
	return written, err
}

func TestPagedHeaderBudgetRefusalCountsNothing(t *testing.T) {
	n, st := pagedTestNode(t, "paged-header.sqlite")
	leaveServeBudget(t, n, 0)
	written, err := serveAndCommit(n, &countingWriter{}, 1, paddedPage(32))
	if !errors.Is(err, errFrameBudget) {
		t.Fatalf("header refusal: err=%v", err)
	}
	if written != 0 {
		t.Fatalf("header refusal returned written=%d, want 0", written)
	}
	if answered, bytes := activityCounts(t, st); answered != 0 || bytes != 0 {
		t.Fatalf("header refusal counted answered=%d bytes=%d", answered, bytes)
	}
}

func TestPagedPageBudgetThenTerminalCountsWrittenFrames(t *testing.T) {
	n, st := pagedTestNode(t, "paged-page.sqlite")
	header := store.PageHeader{Kind: "header", SchemaVersion: 1, Bucket: "bucket", Total: 1}
	pageFrame, _, err := paddedPage(400)(0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	term, err := n.st.SignTerminal("bucket", 1, 0, false, store.ReasonBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	headerN := encodedFrameLen(t, header)
	pageN := encodedFrameLen(t, pageFrame)
	termN := encodedFrameLen(t, term)
	if pageN <= termN {
		t.Fatalf("test page (%d) must be larger than the empty-digest terminal (%d)", pageN, termN)
	}
	leaveServeBudget(t, n, headerN+termN)

	cw := &countingWriter{}
	written, err := serveAndCommit(n, cw, 1, paddedPage(400))
	if err != nil {
		t.Fatalf("page refusal with written terminal: %v", err)
	}
	want := headerN + termN
	if written != want || cw.n != want {
		t.Fatalf("written=%d wire=%d, want header+terminal %d (page %d must not be included)", written, cw.n, want, pageN)
	}
	if answered, bytes := activityCounts(t, st); answered != 1 || bytes != int64(want) {
		t.Fatalf("incomplete-but-terminated response counted answered=%d bytes=%d, want 1/%d", answered, bytes, want)
	}
}

func TestPagedTerminalBudgetRefusalCountsNothing(t *testing.T) {
	n, st := pagedTestNode(t, "paged-terminal.sqlite")
	header := store.PageHeader{Kind: "header", SchemaVersion: 1, Bucket: "bucket", Total: 1}
	pageFrame, _, err := paddedPage(32)(0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	headerN := encodedFrameLen(t, header)
	pageN := encodedFrameLen(t, pageFrame)
	leaveServeBudget(t, n, headerN+pageN)

	cw := &countingWriter{}
	written, err := serveAndCommit(n, cw, 1, paddedPage(32))
	if !errors.Is(err, errFrameBudget) {
		t.Fatalf("terminal refusal: err=%v", err)
	}
	if written != headerN+pageN || cw.n != headerN+pageN {
		t.Fatalf("terminal refusal written=%d wire=%d, want earlier frames %d", written, cw.n, headerN+pageN)
	}
	if answered, bytes := activityCounts(t, st); answered != 0 || bytes != 0 {
		t.Fatalf("terminal refusal counted answered=%d bytes=%d", answered, bytes)
	}
}

func TestPagedShortAndErrorWritesCountNothing(t *testing.T) {
	n, st := pagedTestNode(t, "paged-short.sqlite")
	if written, err := serveAndCommit(n, shortWriter{n: 1}, 1, paddedPage(32)); !errors.Is(err, io.ErrShortWrite) || written != 0 {
		t.Fatalf("short write: written=%d err=%v", written, err)
	}
	if answered, bytes := activityCounts(t, st); answered != 0 || bytes != 0 {
		t.Fatalf("short write counted answered=%d bytes=%d", answered, bytes)
	}

	n2, st2 := pagedTestNode(t, "paged-err.sqlite")
	if written, err := serveAndCommit(n2, errWriter{err: errors.New("boom")}, 1, paddedPage(32)); err == nil || written != 0 {
		t.Fatalf("error write: written=%d err=%v", written, err)
	}
	if answered, bytes := activityCounts(t, st2); answered != 0 || bytes != 0 {
		t.Fatalf("error write counted answered=%d bytes=%d", answered, bytes)
	}
}

func TestPagedCompleteResponseCountsExactBytes(t *testing.T) {
	n, st := pagedTestNode(t, "paged-full.sqlite")
	cw := &countingWriter{}
	written, err := serveAndCommit(n, cw, 1, paddedPage(32))
	if err != nil {
		t.Fatal(err)
	}
	if written != cw.n || written <= 0 {
		t.Fatalf("complete response written=%d wire=%d", written, cw.n)
	}
	if answered, bytes := activityCounts(t, st); answered != 1 || bytes != int64(written) {
		t.Fatalf("complete response counted answered=%d bytes=%d, want 1/%d", answered, bytes, written)
	}
}
