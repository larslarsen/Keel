// SPDX-License-Identifier: Apache-2.0
// Seed packs on the command line (WO-052).
//
// A seed pack is how the head of the watch distribution stops producing network
// queries. Every node loads the same pack, so downloading it reveals nothing
// about any individual, and afterwards the videos it covers are answered
// locally — no request, nothing for a peer to see.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/keel-app/keel/daemon/store"
)

func seedUsage() {
	fmt.Println(`keel-host seed build <out.json> [count] [--own]   build a pack from this node
keel-host seed import <pack.json>         load a pack

A pack holds the neighbourhoods of the most-recommended videos. Loading one
means those videos are answered locally instead of by asking the network, so
the lookups most people make most often are never visible to anyone.

Build follows the contribution level: below level 3 a pack contains only data
this node received from others, never what this user was recommended. --own
overrides that for whoever is bootstrapping the network from their own corpus,
and says so loudly, because such a pack discloses that person's funnel.`)
}

func runSeed(args []string) int {
	if len(args) == 0 {
		seedUsage()
		return 2
	}
	st, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	switch args[0] {
	case "build":
		if len(args) < 2 {
			seedUsage()
			return 2
		}
		count := 1000
		if len(args) > 2 {
			if n, err := strconv.Atoi(args[2]); err == nil && n > 0 {
				count = n
			}
		}
		mirrorOnly := st.ContributionLevel() < store.LevelCohort
		for _, a := range args[2:] {
			if a == "--own" {
				mirrorOnly = false
			}
		}
		pack, err := st.BuildSeedPack(count, st.Cohort(), mirrorOnly)
		if err != nil {
			fmt.Fprintf(os.Stderr, "build: %v\n", err)
			return 1
		}
		if err := pack.WriteSeedPack(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "write: %v\n", err)
			return 1
		}
		fi, _ := os.Stat(args[1])
		fmt.Printf("wrote %s — %d blocks", args[1], len(pack.Blocks))
		if fi != nil {
			fmt.Printf(", %d bytes", fi.Size())
		}
		fmt.Println()
		if mirrorOnly {
			fmt.Println("contains only data received from other nodes, not this node's own observations")
		} else {
			fmt.Println()
			fmt.Println("This pack is built from THIS NODE'S OWN observations — it describes what")
			fmt.Println("you were recommended. Publishing it is a level 3/4 disclosure and cannot")
			fmt.Println("be undone once anyone has a copy. Keep it local unless that is intended.")
		}
		return 0

	case "import":
		if len(args) < 2 {
			seedUsage()
			return 2
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		loaded, edges, err := st.ImportSeedPack(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "import: %v\n", err)
			return 1
		}
		fmt.Printf("loaded %d blocks, %d edges\n", loaded, edges)
		fmt.Println("those videos are now answered locally and will not be requested from peers")
		return 0

	default:
		seedUsage()
		return 2
	}
}
