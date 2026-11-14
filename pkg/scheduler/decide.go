package scheduler

import (
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
)

// Fate is what happens to one message.
type Fate string

// Message fates.
const (
	Deliver   Fate = "deliver"
	Drop      Fate = "drop"
	Duplicate Fate = "dup"
	Delay     Fate = "delay"
)

// Decision is the fate of one message, plus the delay length for Delay and the
// drop reason (replay only; empty for PRNG drops).
type Decision struct {
	Fate   Fate
	Steps  int64
	Reason string
}

// Decide returns the fate of message id. It is a pure function of the seed,
// the scenario's message-fault settings and the id: it draws only from
// src.Keyed(rng.StreamMessages, id), always in the same order.
func Decide(src *rng.Source, f scenario.Faults, id string) Decision {
	r := src.Keyed(rng.StreamMessages, id)
	drop, dup, delay := r.Float64(), r.Float64(), r.Float64()
	steps := rng.Between(r, f.DelaySteps)
	switch {
	case drop < f.Drop:
		return Decision{Fate: Drop}
	case dup < f.Dup:
		return Decision{Fate: Duplicate}
	case delay < f.DelayProb && steps > 0:
		return Decision{Fate: Delay, Steps: steps}
	}
	return Decision{Fate: Deliver}
}
