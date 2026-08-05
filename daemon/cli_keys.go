// SPDX-License-Identifier: Apache-2.0
// Key inspection (WO-038).
//
// A signature is only worth something if a person can compare the key against
// one they were given out of band. That comparison needs the key to be visible
// and short enough to read aloud — the same reason SSH shows a fingerprint
// rather than a full public key.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// fingerprint renders a public key as grouped hex, short enough to check by eye.
func fingerprint(pubB64 string) string {
	raw, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return "(unreadable)"
	}
	sum := sha256.Sum256(raw)
	h := hex.EncodeToString(sum[:8])
	var parts []string
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.Join(parts, ":")
}

// runKeys handles `keel-host keys`.
func runKeys(args []string) int {
	st, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	nodeID, err := st.NodeID()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pub, err := st.PublicKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	fmt.Printf("node id     %s\n", nodeID)
	fmt.Printf("fingerprint %s\n", fingerprint(pub))
	fmt.Printf("public key  %s\n", pub)
	fmt.Println()
	fmt.Println("The fingerprint identifies the key used to sign published releases.")
	fmt.Println("A signature proves the release was not altered and came from this key;")
	fmt.Println("it does not prove the key belongs to a person or that observations are true.")
	fmt.Println()
	fmt.Println("The private key never leaves this machine.")
	return 0
}
