// SPDX-License-Identifier: Apache-2.0
// The access-control string Keel applies to its own files (WO-091 QA).
//
// No build tag: this is string construction, and the bug in it was invisible on
// the only platform that could not run its tests.
package main

// ownerSDDL builds the security descriptor granting the current user, and only
// the current user, full control.
//
// inheritable is the whole point, and leaving it out cost a day of live QA.
//
// "D:P" protects the DACL: nothing is inherited from the parent. An ACE with no
// inheritance flags then means the directory has no inheritable entries either
// — so a file created inside inherits nothing from anywhere, and Windows falls
// back to the DEFAULT DACL of whichever process created it. When that process
// is elevated (an installer double-clicked through SmartScreen), the file it
// creates is openable by Administrators and denied to the ordinary user — and
// the browser launches the daemon unelevated. The daemon is then locked out of
// its own database, in its own directory, reporting "Access is denied" on a
// file Keel itself had just written.
//
// "OICI" — OBJECT_INHERIT | CONTAINER_INHERIT — makes the grant apply to files
// and subdirectories created later, whatever token creates them. It belongs on
// directories and is meaningless on a file or a named pipe, which is why the
// caller says which it has.
func ownerSDDL(sid string, inheritable bool) string {
	flags := ""
	if inheritable {
		flags = "OICI"
	}
	return "D:P(A;" + flags + ";GA;;;" + sid + ")"
}
