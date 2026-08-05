// SPDX-License-Identifier: Apache-2.0
// Bundle subcommands (WO-034).
//
// The browser UI is not the only way to move a bundle. These let someone
// export, import and inspect from a shell — useful for scripting, for people
// who never open the panel, and for testing without a browser at all.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keel-app/keel/daemon/store"
)

// openStore opens the same database the daemon uses.
func openStore() (*store.Store, error) {
	return store.Open(os.Getenv("KEEL_DB"))
}

// runBundle handles `keel-host bundle …`.
func runBundle(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: keel-host bundle export|import|peers")
		return 2
	}
	st, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	switch args[0] {
	case "export":
		fs := flag.NewFlagSet("bundle export", flag.ContinueOnError)
		outPath := fs.String("out", "", "output path (default: ~/Downloads/keel-bundle-<time>.json)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		path := *outPath
		if path == "" {
			dir, err := store.DownloadsDir()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			path = filepath.Join(dir,
				fmt.Sprintf("keel-bundle-%s.json", time.Now().UTC().Format("20060102T150405Z")))
		}
		res, err := st.ExportBundle(path, cohortFor(st))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("wrote %s\n", res.Path)
		fmt.Printf("  %d edge observations, %d catalogue entries, %d bytes\n",
			res.Edges, res.Catalogue, res.Bytes)
		fmt.Printf("  node id %s\n", res.NodeID)
		// Stated at the point of sharing, not buried in a doc.
		fmt.Println("\nThis file says which videos YouTube recommended to you, and after what.")
		fmt.Println("Anyone you send it to can see that. It contains no watch history,")
		fmt.Println("no timestamps and no account details.")
		return 0

	case "import":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: keel-host bundle import <path|https://url>")
			return 2
		}
		res, err := st.ImportBundle(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("imported %d edge(s) and %d catalogue entr(ies) from %s\n",
			res.Edges, res.Catalogue, res.NodeID)
		fmt.Println("Suggestions will now draw on them. Remove with: keel-host bundle forget " + res.NodeID)
		return 0

	case "forget":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: keel-host bundle forget <node-id>")
			return 2
		}
		n, err := st.ForgetPeer(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("removed %d edge(s) from %s\n", n, args[1])
		return 0

	case "peers":
		id, err := st.NodeID()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		peers, err := st.Peers()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("this install: %s\n", id)
		if len(peers) == 0 {
			fmt.Println("no imported bundles")
			return 0
		}
		for _, p := range peers {
			fmt.Printf("  %-24s %d edge(s)\n", p.Source, p.Edges)
		}
		return 0

	case "summary":
		sum, err := st.AggregateSummary(cohortFor(st))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("raw impressions   : %d\n", sum.RawImpressions)
		fmt.Printf("edge observations : %d\n", sum.EdgeObservations)
		fmt.Printf("catalogue entries : %d\n", sum.CatalogueEntries)
		fmt.Printf("cohort            : %s\n", sum.Cohort)
		fmt.Printf("\n%s\n", sum.Note)
		return 0
	}

	fmt.Fprintf(os.Stderr, "unknown bundle command %q\n", args[0])
	return 2
}
