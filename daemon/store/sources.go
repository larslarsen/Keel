// SPDX-License-Identifier: Apache-2.0
// Which corpus a served payload is drawn from (WO-084).
//
// This replaced a `mirrorOnly bool` that ran through every builder in this
// package, and the replacement is not cosmetic. A boolean can only *choose* a
// source, so the Level-2 answer it could express was "other people's rows
// instead of mine". WO-084's Level 2 is the union: a node serves the complete
// eligible contents of a bucket, which is its own locally derived
// neighbourhoods **and** the claims it holds on behalf of peers, with nothing
// in the response marking which is which.
//
// Two independent flags rather than one enum for exactly that reason. A caller
// that wants both gets both queries run and merged, and there is no spelling of
// this type that means "own instead of imported" — the failure WO-084 calls out
// as the naive fix.
package store

// SourceSet selects the corpora a served payload may be built from.
//
// The zero value serves nothing, which is the Level-1 answer and the safe
// default for a builder called with an unset policy.
type SourceSet struct {
	// Local includes material derived from this node's own `impressions`.
	Local bool
	// Peers includes material this node imported from other nodes and holds
	// on their behalf.
	Peers bool
}

// PeerSources serves only what other people published. Kept as a named value
// because tests and the export/CLI paths still have a legitimate use for it;
// it is no longer any level's graph policy.
var PeerSources = SourceSet{Peers: true}

// LocalSources serves only this node's own material.
var LocalSources = SourceSet{Local: true}

// AllSources is Level 2's graph and catalogue policy: the complete eligible
// contents of a bucket, local and imported together.
var AllSources = SourceSet{Local: true, Peers: true}

// Empty reports whether this set selects nothing at all.
func (s SourceSet) Empty() bool { return !s.Local && !s.Peers }
