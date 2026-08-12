// SPDX-License-Identifier: Apache-2.0
// Windows owner-credential access-mask rules (WO-091 QA).
//
// Deliberately free of a build tag. The Windows security calls cannot run on a
// Linux CI host, but the access-mask arithmetic is where the bug was, and that
// is ordinary integer logic that every platform can check.
package main

// Windows access-mask bits. Named here rather than taken from
// golang.org/x/sys/windows because that package exports GENERIC_ALL but has no
// FILE_ALL_ACCESS, and this file must compile on every platform.
const (
	maskGenericAll = 0x10000000

	// fileAllAccess is FILE_ALL_ACCESS:
	// STANDARD_RIGHTS_REQUIRED (0x000F0000) | SYNCHRONIZE (0x00100000) | 0x1FF.
	fileAllAccess = 0x001F01FF
)

// grantsFullControl reports whether an ACE mask is a full-control grant.
//
// It accepts two spellings of the same permission because Windows rewrites the
// one we ask for. secureOwnerPath sets the DACL from the SDDL string
// "D:P(A;;GA;;;<SID>)", and GA is GENERIC_ALL — but a DACL containing generic
// rights is mapped through the object type's generic mapping when it is applied
// to a file, so what comes back from GetNamedSecurityInfo is FILE_ALL_ACCESS.
//
// Testing the read-back mask for GENERIC_ALL therefore never matched, and the
// daemon rejected the very credential it had just written with "owner secret has
// an unexpected access rule" on every run after the first. The check is not
// weakened by accepting both: the security property — that no one but the
// current user has any access at all — is carried by the protected DACL, the
// single-ACE requirement and the SID comparison, not by this mask.
func grantsFullControl(mask uint32) bool {
	if mask&maskGenericAll == maskGenericAll {
		return true
	}
	return mask&fileAllAccess == fileAllAccess
}
