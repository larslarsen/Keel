// SPDX-License-Identifier: Apache-2.0
package main

import (
	"strings"
	"testing"
)

// TestOwnerSDDLDirectoryGrantIsInheritable is the root cause of a full day of
// "Access is denied" on keel.sqlite.
//
// "D:P" blocks inheritance from the parent. An ACE with no inheritance flags
// means the directory has no inheritable entries either — so a file created
// inside inherits nothing from anywhere and Windows falls back to the default
// DACL of whichever process created it. An installer run through SmartScreen
// can be elevated; the file it creates is then openable by Administrators and
// denied to the ordinary user, and the browser launches the daemon unelevated.
// The daemon is locked out of its own database in its own directory.
func TestOwnerSDDLDirectoryGrantIsInheritable(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-1001"

	dir := ownerSDDL(sid, true)
	if !strings.Contains(dir, "OICI") {
		t.Errorf("a directory grant must be inheritable by the files created in it: %s", dir)
	}
	if !strings.HasPrefix(dir, "D:P(") {
		t.Errorf("the DACL must stay protected: %s", dir)
	}
	if !strings.Contains(dir, sid) {
		t.Errorf("the grant does not name the user: %s", dir)
	}
	if strings.Count(dir, "(A;") != 1 {
		t.Errorf("exactly one allow entry, for one user: %s", dir)
	}

	// A file or a named pipe has nothing to inherit it, and the flags are
	// meaningless there — which is why the caller says which it holds.
	file := ownerSDDL(sid, false)
	if strings.Contains(file, "OICI") {
		t.Errorf("a file grant must not carry inheritance flags: %s", file)
	}
	if file == dir {
		t.Error("the directory and file forms are identical; the isDir argument does nothing")
	}
	if !strings.HasPrefix(file, "D:P(") || !strings.Contains(file, sid) {
		t.Errorf("file grant malformed: %s", file)
	}
}
