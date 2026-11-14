// Package rng derives every random decision of a run from one seed.
package rng

import (
	"hash/fnv"
	"math/rand/v2"
)

// Stream names.
const (
	StreamMessages = "messages"
	StreamFaults   = "faults"
	StreamWorkload = "workload"
	StreamShrink   = "shrink"
)

// Source holds a run's seed.
type Source struct{ seed int64 }

// New returns a Source for seed.
func New(seed int64) *Source { return &Source{seed: seed} }

// Seed returns the seed.
func (s *Source) Seed() int64 { return s.seed }

func fnv64a(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// Stream returns an independent generator for a named subsystem. Same seed+name => same sequence.
func (s *Source) Stream(name string) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(s.seed), fnv64a(name)))
}

// Keyed returns a generator for one specific item (e.g. one message ID). Same seed+stream+key => same sequence,
// independent of the order items arrive in. This is what makes message decisions replayable.
func (s *Source) Keyed(stream, key string) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(s.seed)^fnv64a(stream), fnv64a(key)))
}

// Between returns a uniform value in [lo,hi]; it returns lo if lo>=hi.
func Between(r *rand.Rand, rg [2]int64) int64 {
	lo, hi := rg[0], rg[1]
	if lo >= hi {
		return lo
	}
	return lo + r.Int64N(hi-lo+1)
}
