// SPDX-License-Identifier: Apache-2.0
// The owner outlives the browser, so an upgrade has to be able to retire it.
package main

import "testing"

// TestOwnerIsStale: the check that decides whether a resident owner is the same
// binary as the proxy talking to it.
//
// It deliberately does not look at the version string. Releases reuse one
// version on purpose, so version cannot tell a new build from an old one — and
// that is the only question being asked here.
func TestOwnerIsStale(t *testing.T) {
	self := buildIdentity()
	if self == "" {
		t.Skip("cannot identify this test binary")
	}

	if ownerIsStale(self) {
		t.Error("an owner running this same binary was treated as stale")
	}
	if !ownerIsStale(self + "x") {
		t.Error("an owner running a different binary was not treated as stale")
	}
	// An owner that reports no build predates the field, which makes it older
	// than this process by construction. That is the upgrade case: before this
	// existed, the previous build stayed resident and kept answering.
	if !ownerIsStale("") {
		t.Error("an owner that reports no build was not treated as stale")
	}
}

// TestBuildIdentityIsStableAndSpecific: it must not change between calls within
// one process (that would make every connection retire the owner), and it must
// name the binary rather than the version.
func TestBuildIdentityIsStableAndSpecific(t *testing.T) {
	a, b := buildIdentity(), buildIdentity()
	if a != b {
		t.Fatalf("buildIdentity is not stable: %q then %q", a, b)
	}
	if a == version {
		t.Error("buildIdentity returned the version string; it must identify the binary")
	}
}
