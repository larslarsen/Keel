// SPDX-License-Identifier: Apache-2.0
// Overlap sketches (WO-052 Part 2).
//
// This exists to settle one question, quoted from DESIGN_BOOTSTRAP §5d:
//
//	"Cross-user dedup factor — the gate before STAR. ... One machine cannot
//	measure it ... Resolve this before committing STAR."
//
// Two installs can resolve it without either publishing an observation:
//
//	node A$  keel-host sketch > a.json
//	node B$  keel-host sketch > b.json
//	         keel-host sketch compare a.json b.json
//
// The file that moves between them holds no video ids, no edges and no counts —
// only 16 KB of small integers (see store/sketch.go). It is safe to paste into
// an issue or a chat.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/keel-app/keel/daemon/store"
)

func sketchUsage() {
	fmt.Println(`keel-host sketch [--tuple]            emit this node's sketch as JSON
keel-host sketch compare A.json B.json  compare two sketches

  --tuple   sketch full measurement tuples (surface, slot, day, cohort)
            instead of plain (from, to) edges

A sketch reports roughly how many distinct edges a node has seen. It cannot be
searched, reversed, or tested for membership — it carries no video ids at all.`)
}

func runSketch(args []string) int {
	if len(args) > 0 && args[0] == "compare" {
		return runSketchCompare(args[1:])
	}

	kind := store.KindEdge
	for _, a := range args {
		switch a {
		case "--tuple":
			kind = store.KindTuple
		case "--help", "-h", "help":
			sketchUsage()
			return 0
		default:
			fmt.Fprintf(os.Stderr, "unknown argument %q\n", a)
			sketchUsage()
			return 2
		}
	}

	st, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	sk, err := st.EdgeSketch(kind, st.Cohort())
	if err != nil {
		fmt.Fprintf(os.Stderr, "build sketch: %v\n", err)
		return 1
	}

	out, err := json.MarshalIndent(sk, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "\n%s sketch: ~%d distinct keys\n", kind, sk.Count())
	return 0
}

func readSketch(path string) (*store.Sketch, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sk store.Sketch
	if err := json.Unmarshal(raw, &sk); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &sk, nil
}

func runSketchCompare(args []string) int {
	if len(args) != 2 {
		sketchUsage()
		return 2
	}
	a, err := readSketch(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, err := readSketch(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rep, err := store.Overlap(a, b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	fmt.Printf("kind                 %s\n", rep.Kind)
	fmt.Printf("node A               %d distinct\n", rep.A)
	fmt.Printf("node B               %d distinct\n", rep.B)
	fmt.Printf("union                %d\n", rep.Union)
	fmt.Printf("shared (approx)      %d\n", rep.Intersection)
	fmt.Printf("B adds               %d new keys\n", rep.NewPerNode)
	if rep.B > 0 {
		fmt.Printf("overlap              %.1f%% of B was already in A\n", rep.Fraction*100)
	}
	fmt.Println()

	// The interpretation, printed here so the number is not misread. This is
	// the decision DESIGN_BOOTSTRAP §5d says to make before committing STAR.
	switch {
	case rep.B == 0:
		fmt.Println("Node B is empty — nothing to compare.")
	case rep.Fraction >= 0.5:
		fmt.Println("Edges dedup hard across users. The aggregate grows far slower than")
		fmt.Println("the funnel stream, so STAR output should fit the free channels in")
		fmt.Println("DESIGN_v2 §7.3.")
	case rep.Fraction >= 0.15:
		fmt.Println("Partial dedup. The aggregate grows sub-linearly but not cheaply;")
		fmt.Println("size the distribution plan on the measured rate, not on hope.")
	default:
		fmt.Println("Little cross-user dedup at this sample size. If this holds up across")
		fmt.Println("more nodes, the aggregate tracks the funnel stream's linear growth and")
		fmt.Println("the L2 distribution shape has to change before STAR is committed.")
	}
	fmt.Println()
	fmt.Println("Caveat: two nodes is a sample of two, and 'shared' is derived by")
	fmt.Println("inclusion-exclusion, so it is the noisiest figure here. 'B adds' is the")
	fmt.Println("number to trust and the one that scales.")
	return 0
}
