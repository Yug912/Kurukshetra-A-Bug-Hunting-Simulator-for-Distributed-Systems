// Package faults generates the deterministic fault plan of a run and executes
// it against the proxy (partitions), Docker (crash/pause) and nodes' HTTP hooks
// (clock skew).
package faults

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

// Partition strategies.
const (
	IsolateOne       = "isolate-one"
	MajorityMinority = "majority-minority"
	Bridge           = "bridge"
)

// MaxDown is the most nodes that may be crashed or paused at once: floor((n-1)/2).
func MaxDown(n int) int { return (n - 1) / 2 }

// Plan returns the fault plan for sc. It is a pure function of the seed and the
// scenario and draws only from src.Stream(rng.StreamFaults). Each fault type
// gets its own generator seeded from that stream in a fixed order (partition,
// crash, pause, clock_skew), so enabling or tuning one type never reshuffles
// the others.
func Plan(sc scenario.Scenario, src *rng.Source) []core.Fault {
	root := src.Stream(rng.StreamFaults)
	sub := func() *rand.Rand { return rand.New(rand.NewPCG(root.Uint64(), root.Uint64())) }
	rp, rc, rpa, rs := sub(), sub(), sub(), sub()
	nodes := scenario.NodeNames(sc.Nodes)
	f := sc.Faults

	var plan []core.Fault
	plan = append(plan, partitions(rp, nodes, f, sc.DurationSteps)...)
	down := downs(rc, nodes, trace.Crash, f.CrashEvery, f.CrashLen, sc.DurationSteps, nil)
	plan = append(plan, down...)
	plan = append(plan, downs(rpa, nodes, trace.Pause, f.PauseEvery, f.PauseLen, sc.DurationSteps, down)...)
	plan = append(plan, skews(rs, nodes, f, sc.DurationSteps)...)
	core.SortFaults(plan)
	return plan
}

// walk returns the start steps next = prev + Between(every) below limit.
func walk(r *rand.Rand, every scenario.Range, limit int64, visit func(step int64) int64) {
	if !every.Enabled() {
		return
	}
	for step := rng.Between(r, every); step < limit; step += rng.Between(r, every) {
		step = visit(step)
		if step < 0 {
			return
		}
	}
}

func partitions(r *rand.Rand, nodes []string, f scenario.Faults, limit int64) []core.Fault {
	n := len(nodes)
	if n < 2 || !f.PartitionLen.Enabled() {
		return nil
	}
	strategies := []string{IsolateOne}
	if MaxDown(n) >= 2 { // below 5 nodes majority-minority equals isolate-one
		strategies = append(strategies, MajorityMinority)
	}
	if n >= 3 {
		strategies = append(strategies, Bridge)
	}
	var out []core.Fault
	var healAt int64 = -1
	walk(r, f.PartitionEvery, limit, func(step int64) int64 {
		strat := strategies[r.IntN(len(strategies))]
		length := max(rng.Between(r, f.PartitionLen), 1)
		perm := r.Perm(n)
		if step <= healAt { // never overlap: start right after the previous heal
			step = healAt + 1
		}
		if step >= limit {
			return -1
		}
		fl := core.Fault{Step: step, Kind: trace.Partition, DurationSteps: length}
		pick := func(idx []int) []string {
			var g []string
			for _, i := range idx {
				g = append(g, nodes[i])
			}
			sort.Strings(g)
			return g
		}
		switch strat {
		case IsolateOne:
			fl.Groups = [][]string{pick(perm[:1]), pick(perm[1:])}
		case MajorityMinority:
			k := MaxDown(n)
			fl.Groups = [][]string{pick(perm[:k]), pick(perm[k:])}
		case Bridge:
			rest := perm[1:]
			half := len(rest) / 2
			fl.Groups = [][]string{pick(rest[:half]), pick(rest[half:])}
			fl.Nodes = pick(perm[:1])
		}
		healAt = step + length
		out = append(out, fl)
		return step
	})
	return out
}

// downs generates crash or pause faults, skipping any that would put more than
// MaxDown nodes down at once (counting `existing` faults too) or take down a
// node that is already down.
func downs(r *rand.Rand, nodes []string, kind trace.Kind, every, length scenario.Range, limit int64, existing []core.Fault) []core.Fault {
	maxDown := MaxDown(len(nodes))
	if maxDown < 1 || !length.Enabled() {
		return nil
	}
	all := append([]core.Fault(nil), existing...)
	var out []core.Fault
	walk(r, every, limit, func(step int64) int64 {
		node := nodes[r.IntN(len(nodes))]
		l := max(rng.Between(r, length), 1)
		f := core.Fault{Step: step, Kind: kind, Nodes: []string{node}, DurationSteps: l}
		if canGoDown(all, f, maxDown) {
			all = append(all, f)
			out = append(out, f)
		}
		return step
	})
	return out
}

func overlaps(a, b core.Fault) bool {
	return a.Step < b.Step+b.DurationSteps && b.Step < a.Step+a.DurationSteps
}

func canGoDown(plan []core.Fault, f core.Fault, maxDown int) bool {
	down := map[string]bool{f.Nodes[0]: true}
	for _, g := range plan {
		if (g.Kind != trace.Crash && g.Kind != trace.Pause) || !overlaps(f, g) {
			continue
		}
		if g.Nodes[0] == f.Nodes[0] {
			return false
		}
		down[g.Nodes[0]] = true
	}
	// Conservative: count every node down at any point of f's interval.
	return len(down) <= maxDown
}

func skews(r *rand.Rand, nodes []string, f scenario.Faults, limit int64) []core.Fault {
	if !f.ClockSkewMs.Enabled() {
		return nil
	}
	var out []core.Fault
	walk(r, f.ClockSkewEvery, limit, func(step int64) int64 {
		out = append(out, core.Fault{Step: step, Kind: trace.ClockSkew, Nodes: []string{nodes[r.IntN(len(nodes))]},
			OffsetMs: rng.Between(r, f.ClockSkewMs)})
		return step
	})
	return out
}

// Validate checks a plan against the planning rules for n nodes: known kinds,
// known nodes, partitions that do not overlap and split nodes into disjoint
// groups, and never more than MaxDown nodes crashed or paused at once.
func Validate(plan []core.Fault, nodes []string) error {
	known := map[string]bool{}
	for _, n := range nodes {
		known[n] = true
	}
	var parts, down []core.Fault
	for i, f := range plan {
		if i > 0 && (plan[i-1].Step > f.Step || plan[i-1].Step == f.Step && plan[i-1].Kind > f.Kind) {
			return fmt.Errorf("fault %d: plan not sorted", i)
		}
		for _, n := range f.Nodes {
			if !known[n] {
				return fmt.Errorf("fault %d: unknown node %q", i, n)
			}
		}
		switch f.Kind {
		case trace.Partition:
			seen := map[string]bool{}
			for _, n := range f.Nodes {
				seen[n] = true
			}
			for _, g := range f.Groups {
				for _, n := range g {
					if !known[n] || seen[n] {
						return fmt.Errorf("fault %d: bad or repeated node %q in groups", i, n)
					}
					seen[n] = true
				}
			}
			if len(f.Groups) < 2 || len(seen) != len(nodes) || f.DurationSteps <= 0 {
				return fmt.Errorf("fault %d: partition must cover all nodes in ≥2 groups with a duration", i)
			}
			for _, p := range parts {
				if overlaps(p, f) {
					return fmt.Errorf("fault %d: overlapping partitions", i)
				}
			}
			parts = append(parts, f)
		case trace.Crash, trace.Pause:
			if len(f.Nodes) != 1 || f.DurationSteps <= 0 {
				return fmt.Errorf("fault %d: %s needs one node and a duration", i, f.Kind)
			}
			if !canGoDown(down, f, MaxDown(len(nodes))) {
				return fmt.Errorf("fault %d: too many nodes down (%s)", i, core.DescribeFault(f))
			}
			down = append(down, f)
		case trace.ClockSkew:
			if len(f.Nodes) != 1 {
				return fmt.Errorf("fault %d: clock_skew needs one node", i)
			}
		default:
			return fmt.Errorf("fault %d: unknown kind %q", i, f.Kind)
		}
	}
	return nil
}

// GroupsString renders partition groups as "n1|n2,n3" (bridge node, if any, is in meta "bridge").
func GroupsString(groups [][]string) string {
	var gs []string
	for _, g := range groups {
		gs = append(gs, strings.Join(g, ","))
	}
	return strings.Join(gs, "|")
}

// BlockedLinks returns the directed links cut by a partition: every pair of
// nodes in different groups, both directions. Nodes outside all groups (the
// bridge) can talk to everyone.
func BlockedLinks(groups [][]string) map[[2]string]bool {
	out := map[[2]string]bool{}
	for i, a := range groups {
		for j, b := range groups {
			if i == j {
				continue
			}
			for _, x := range a {
				for _, y := range b {
					out[[2]string{x, y}] = true
				}
			}
		}
	}
	return out
}
