// SPDX-License-Identifier: Apache-2.0
// The owner's account of its own death has to reach the browser.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOwnerLogTail: the owner is a detached process whose stdout and stderr go
// to a log beside the database. Nothing read that file back, so a failure to
// start reached the user as "owner_unavailable: EOF" while the actual reason
// sat in a file they had no reason to know existed.
func TestOwnerLogTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner.log")

	if got := ownerLogTail(filepath.Join(dir, "absent.log"), 6); got != "" {
		t.Errorf("missing log returned %q, want empty", got)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ownerLogTail(path, 6); got != "" {
		t.Errorf("empty log returned %q, want empty", got)
	}

	body := "keel: starting\r\n\r\nkeel: store: unable to open database file\r\nkeel: exiting\r\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ownerLogTail(path, 6)
	for _, want := range []string{"unable to open database file", "keel: exiting"} {
		if !strings.Contains(got, want) {
			t.Errorf("tail %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Errorf("tail must be one line for an error string, got %q", got)
	}

	// Bounded in both directions: only the last maxLines, and only the last
	// window of a log an owner has been appending to for weeks.
	var big strings.Builder
	for i := 0; i < 5000; i++ {
		big.WriteString("keel: line ")
		big.WriteString(strings.Repeat("x", 40))
		big.WriteString("\n")
	}
	big.WriteString("keel: the last thing\n")
	if err := os.WriteFile(path, []byte(big.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got = ownerLogTail(path, 6)
	if !strings.Contains(got, "the last thing") {
		t.Errorf("tail lost the final line: %q", got)
	}
	if len(got) > 4096 {
		t.Errorf("tail is %d bytes; it goes into an error string shown to a user", len(got))
	}
	if n := strings.Count(got, " | ") + 1; n > 6 {
		t.Errorf("tail kept %d lines, want at most 6", n)
	}
}

// TestWithOwnerLogKeepsTheCause: the wrapper must add the log without hiding
// the error it wraps, so errors.Is still works upstream.
func TestWithOwnerLogKeepsTheCause(t *testing.T) {
	dir := t.TempDir()
	p := ownerPaths{log: filepath.Join(dir, "owner.log")}
	cause := errors.New("EOF")

	if got := withOwnerLog(p, cause); got.Error() != "EOF" {
		t.Errorf("with no log, want the bare cause, got %q", got)
	}
	if err := os.WriteFile(p.log, []byte("keel: store: database is locked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := withOwnerLog(p, cause)
	if !errors.Is(got, cause) {
		t.Error("wrapping lost the underlying error")
	}
	if !strings.Contains(got.Error(), "database is locked") {
		t.Errorf("the owner's reason did not survive: %q", got)
	}
}
