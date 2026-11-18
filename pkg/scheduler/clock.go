// Package scheduler owns the logical clock. Every tick it fires due faults,
// releases delayed messages and decides the fate of every parked message using
// only the run's seed, so the same seed makes the same decisions.
package scheduler

import "sync/atomic"

// Clock is the run's logical clock (a step counter). It is created before the
// proxy so both share it; the Scheduler advances it.
type Clock struct{ step atomic.Int64 }

// NewClock returns a clock at step 0.
func NewClock() *Clock { return &Clock{} }

// Step implements core.StepClock.
func (c *Clock) Step() int64 { return c.step.Load() }

func (c *Clock) advance() int64 { return c.step.Add(1) }
