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
	"github.com/keel-app/keel/daemon/swarm"
)

func seedUsage() {
	fmt.Println(`keel-host seed build <out.json> [count] [--peers-only]   build a pack from this node
keel-host seed import <pack.json>         load a pack

A pack holds the neighbourhoods of the most-recommended videos. Loading one
means those videos are answered locally instead of by asking the network, so
the lookups most people make most often are never visible to anyone.

Build follows the contribution level: a pack carries whatever this node already
serves on demand, so at level 2 that is its own aggregated blocks and the ones
it holds for peers, and at level 1 there is nothing to build. --peers-only drops
the local half.

A pack is not a bucket, and that difference is the whole warning: a bucket is
requested and answered whole, while a pack names the neighbourhoods it carries.
Anyone holding one sees which videos this node has edges for.`)
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
		// Default to what this node's own level already serves on demand
		// (WO-084): at Level 2 that is the union of local and imported claims,
		// at Level 1 it is nothing at all, since a Level-1 node serves no
		// buckets and a seed pack is bulk publication of exactly that material.
		// --peers-only builds the pre-WO-084 shape for anyone who wants a pack
		// carrying no locally derived claims.
		sources := swarm.PolicyForLevel(st.ContributionLevel()).GraphSources()
		for _, a := range args[2:] {
			if a == "--peers-only" {
				sources = store.PeerSources
			}
		}
		if sources.Empty() {
			fmt.Fprintln(os.Stderr,
				"build: this node's contribution level serves no blocks, so there is nothing to seed")
			return 1
		}
		pack, err := st.BuildSeedPack(count, st.Cohort(), sources)
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
		if !sources.Local {
			fmt.Println("contains only claims received from other nodes, not this node's own")
		} else {
			fmt.Println()
			fmt.Println("This pack includes THIS NODE'S OWN aggregated recommendation blocks —")
			fmt.Println("the same broad-bucket material a level 2 node serves on request, with the")
			fmt.Println("same shape: edge counts only, no watch order, timestamps or titles. A pack")
			fmt.Println("is not a bucket, though: it names the neighbourhoods it carries, so anyone")
			fmt.Println("holding it sees which videos this node has edges for. Publishing it cannot")
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
