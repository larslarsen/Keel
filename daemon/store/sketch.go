// SPDX-License-Identifier: Apache-2.0
// Cardinality sketches (WO-052 Part 2).
//
// DESIGN_BOOTSTRAP §5d names one question as the gate before STAR: how many
// *distinct* edges the network gains per additional user. If edges dedup hard
// across people, the published aggregate is a few TB and fits the free channels
// of DESIGN_v2 §7.3. If they do not, it grows with the raw funnel stream and the
// distribution shape has to change.
//
// One machine cannot answer it — the appendix in DESIGN_BOOTSTRAP measured 0.58
// distinct edges per impression locally, but that ratio is dominated by one
// person meeting new videos, not by cross-user overlap.
//
// Two machines can answer it, and they do not have to publish anything to do so.
// Each builds a HyperLogLog over its own edge keys; the sketches are exchanged
// and merged; the merged cardinality is |A ∪ B|. Against |A| + |B| that gives
// the overlap directly.
//
// Why this is safe to exchange:
//
//   - A sketch holds 2^p small integers, each the largest leading-zero run seen
//     for a hash bucket. It answers "roughly how many distinct items" and
//     nothing else.
//   - It cannot be enumerated. There is no operation that recovers a member,
//     because members were never stored — only a running maximum per bucket.
//   - Membership cannot be tested either: a register is evidence that *some*
//     key hashed into that bucket, and every bucket collects vast numbers of
//     possible keys.
//
// So this measures the network without any node publishing an observation,
// which is what makes it usable during bootstrap when STAR cannot run for lack
// of a population.
package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/bits"
)

// sketchP is the register-index width. 2^14 registers gives ~0.8% standard
// error at a few KB — small enough to exchange freely, accurate enough that an
// overlap fraction is not swamped by estimator noise.
const sketchP = 14

const sketchM = 1 << sketchP

// SketchKind selects which key an observation contributes.
//
// Both are needed to answer §5d. The measurement tuple is what actually gets
// published, but if dedup fails only because `day_bucket` splits every edge
// across days, that is a fixable bucketing decision rather than a fact about
// the graph — and the edge-level sketch is what distinguishes the two.
type SketchKind string

const (
	// KindEdge counts distinct (from, to) pairs — the graph itself.
	KindEdge SketchKind = "edge"
	// KindTuple counts distinct §6.2 measurement tuples, including surface,
	// slot bucket, day bucket and cohort.
	KindTuple SketchKind = "tuple"
)

// Sketch is a HyperLogLog over observation keys.
type Sketch struct {
	Kind      SketchKind `json:"kind"`
	P         uint8      `json:"p"`
	Registers []byte     `json:"-"`
	// Encoded carries the registers over the wire; JSON of a 16 KB byte array
	// is wasteful, and base64 keeps a sketch pasteable into an issue.
	Encoded string `json:"registers"`
}

// NewSketch returns an empty sketch.
func NewSketch(kind SketchKind) *Sketch {
	return &Sketch{Kind: kind, P: sketchP, Registers: make([]byte, sketchM)}
}

// Add records one key.
func (sk *Sketch) Add(key string) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	x := h.Sum64()

	// FNV-1a avalanches poorly in its high bits for short, similar inputs —
	// and video ids are exactly that. Taking the register index straight from
	// the top bits collapsed 10,000 distinct keys onto ~870 registers, which
	// silently under-counts rather than failing. The MurmurHash3 finalizer
	// restores avalanche across the whole word.
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33

	// Top P bits choose the register; the rest supplies the leading-zero run.
	idx := x >> (64 - sketchP)
	rest := x<<sketchP | (1 << (sketchP - 1)) // sentinel bit bounds the run
	rank := uint8(bits.LeadingZeros64(rest)) + 1
	if rank > sk.Registers[idx] {
		sk.Registers[idx] = rank
	}
}

// Count estimates the number of distinct keys added.
func (sk *Sketch) Count() uint64 {
	m := float64(len(sk.Registers))
	alpha := 0.7213 / (1 + 1.079/m)

	sum := 0.0
	zeros := 0
	for _, r := range sk.Registers {
		sum += math.Pow(2, -float64(r))
		if r == 0 {
			zeros++
		}
	}
	est := alpha * m * m / sum

	// Linear counting is far more accurate while most registers are still
	// empty, which is the regime a single node's corpus actually sits in.
	if est <= 2.5*m && zeros > 0 {
		est = m * math.Log(m/float64(zeros))
	}
	return uint64(est + 0.5)
}

// Merge folds another sketch in, giving the union's cardinality.
//
// This is the whole reason HyperLogLog is the right structure here: the union
// of two sketches is the register-wise maximum, which is exact. No approximation
// is introduced by merging — only the estimator itself is approximate.
func (sk *Sketch) Merge(other *Sketch) error {
	if other == nil {
		return fmt.Errorf("nil sketch")
	}
	if sk.Kind != other.Kind {
		return fmt.Errorf("cannot merge %q with %q — different keys", sk.Kind, other.Kind)
	}
	if sk.P != other.P || len(sk.Registers) != len(other.Registers) {
		return fmt.Errorf("sketches have different precision")
	}
	for i, r := range other.Registers {
		if r > sk.Registers[i] {
			sk.Registers[i] = r
		}
	}
	return nil
}

// MarshalJSON encodes the registers as base64.
func (sk *Sketch) MarshalJSON() ([]byte, error) {
	type alias Sketch
	a := alias(*sk)
	a.Encoded = base64.StdEncoding.EncodeToString(sk.Registers)
	return json.Marshal(a)
}

// UnmarshalJSON restores the registers from base64.
func (sk *Sketch) UnmarshalJSON(b []byte) error {
	type alias Sketch
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(a.Encoded)
	if err != nil {
		return fmt.Errorf("sketch registers are not valid base64: %w", err)
	}
	if len(raw) != 1<<a.P {
		return fmt.Errorf("sketch has %d registers, want %d", len(raw), 1<<a.P)
	}
	*sk = Sketch(a)
	sk.Registers = raw
	return nil
}

// sketchKey renders one observation under the selected kind.
func sketchKey(kind SketchKind, from, to, surface, slotBucket, dayBucket, cohort string) string {
	if kind == KindEdge {
		return from + "\x00" + to
	}
	return from + "\x00" + to + "\x00" + surface + "\x00" +
		slotBucket + "\x00" + dayBucket + "\x00" + cohort
}

// EdgeSketch builds a sketch over this node's aggregated observations.
//
// It reads the same rows EdgeObservations publishes, so the number it reports
// describes exactly what would be contributed — not the raw impression log.
func (s *Store) EdgeSketch(kind SketchKind, cohort string) (*Sketch, error) {
	obs, err := s.EdgeObservations(cohort)
	if err != nil {
		return nil, err
	}
	sk := NewSketch(kind)
	for _, o := range obs {
		sk.Add(sketchKey(kind, o.From, o.To, o.Surface, o.SlotBucket, o.DayBucket, o.Cohort))
	}
	return sk, nil
}

// OverlapReport is the answer §5d asks for.
type OverlapReport struct {
	Kind SketchKind `json:"kind"`
	A    uint64     `json:"a"`
	B    uint64     `json:"b"`
	// Union is measured directly by merging. Intersection is derived, and is
	// the noisy figure — see Overlap.
	Union        uint64  `json:"union"`
	Intersection uint64  `json:"intersection"`
	Fraction     float64 `json:"overlap_fraction"`
	// NewPerNode is what the distribution estimate actually needs: how many
	// distinct keys the second node adds that the first did not have.
	NewPerNode uint64 `json:"new_from_second_node"`
}

// Overlap compares two sketches.
//
// Intersection is inclusion-exclusion: |A| + |B| - |A ∪ B|. Each term carries
// its own error, so when the true overlap is small the difference of two large
// numbers is dominated by noise and the figure should not be quoted precisely.
// Union and NewPerNode are the trustworthy outputs, and NewPerNode is the one
// that decides the scaling question.
func Overlap(a, b *Sketch) (*OverlapReport, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("both sketches required")
	}
	union := NewSketch(a.Kind)
	if err := union.Merge(a); err != nil {
		return nil, err
	}
	if err := union.Merge(b); err != nil {
		return nil, err
	}

	ca, cb, cu := a.Count(), b.Count(), union.Count()

	var inter uint64
	if ca+cb > cu {
		inter = ca + cb - cu
	}
	var newFromB uint64
	if cu > ca {
		newFromB = cu - ca
	}
	var frac float64
	if cb > 0 {
		frac = float64(inter) / float64(cb)
	}

	return &OverlapReport{
		Kind: a.Kind, A: ca, B: cb, Union: cu,
		Intersection: inter, Fraction: frac, NewPerNode: newFromB,
	}, nil
}
