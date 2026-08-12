// SPDX-License-Identifier: Apache-2.0
package main

import "testing"

// TestGrantsFullControlAcceptsMappedFileRights is the QA failure from WO-091:
// "owner secret has an unexpected access rule" on every install after the first.
//
// The DACL is written as SDDL "D:P(A;;GA;;;<SID>)", but Windows maps generic
// rights to the object type's specific rights when the DACL is applied to a
// file. The ACE that comes back therefore holds FILE_ALL_ACCESS, and the
// validator was comparing it against GENERIC_ALL — a value a file ACE cannot
// hold. The daemon rejected its own credential.
func TestGrantsFullControlAcceptsMappedFileRights(t *testing.T) {
	accept := map[string]uint32{
		"FILE_ALL_ACCESS as Windows stores it": fileAllAccess,
		"GENERIC_ALL as the SDDL asks for it":  maskGenericAll,
		"generic and specific together":        maskGenericAll | fileAllAccess,
	}
	for name, mask := range accept {
		if !grantsFullControl(mask) {
			t.Errorf("%s (%#08x) rejected", name, mask)
		}
	}

	reject := map[string]uint32{
		"no access":                 0,
		"read only":                 0x00120089, // FILE_GENERIC_READ
		"read and write, no more":   0x00120089 | 0x00120116,
		"full control minus write":  fileAllAccess &^ 0x0002,
		"full control minus delete": fileAllAccess &^ 0x00010000,
	}
	for name, mask := range reject {
		if grantsFullControl(mask) {
			t.Errorf("%s (%#08x) accepted as full control", name, mask)
		}
	}
}
