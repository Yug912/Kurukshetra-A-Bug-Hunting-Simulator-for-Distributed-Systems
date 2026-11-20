package explain

import (
	"fmt"
	"sort"
	"strings"

	"kurukshetra/pkg/history"
)

// Story renders the facts as a short Markdown root-cause story.
func Story(f Facts) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# Why %s seed %d %s\n\n", f.Scenario, f.Seed, verdictWord(f.Verdict))
	if f.Verdict != "FAIL" {
		w("Nothing to explain: the verdict is %s, so no checker found a broken guarantee.\n", f.Verdict)
		return b.String()
	}
	if f.Minimal {
		w("This is the shrinker's **minimal** run: every fault below is needed to trigger the bug.\n\n")
	}
	if len(f.Env) > 0 {
		w("Bug flags switched on: %s.\n\n", strings.Join(flagList(f.Env), ", "))
	}

	// 1. Verdict.
	w("## What broke\n\n")
	for _, c := range f.Failing {
		w("- **%s** failed: %s.\n", c.Checker, c.Reason)
	}
	w("\n")

	// 2. The faults, in time order (only those around the blamed ops when there are many).
	shownFaults, hidden := f.relevantFaults()
	if hidden > 0 {
		w("## Faults around the failure\n\n")
	} else {
		w("## Faults in this run\n\n")
	}
	if len(f.Faults) == 0 {
		w("No faults were injected.\n\n")
	}
	for _, x := range shownFaults {
		w("- %s\n", describeWindow(x, f.EndStep))
	}
	if hidden > 0 {
		w("- (%d other faults earlier or elsewhere in the run are left out; see faults.json.)\n", hidden)
	}
	if len(f.Leaders) > 0 {
		w("- Leader changes: %s.\n", leaderList(f.Leaders))
	}
	w("\n")

	// 3. The culprit ops, one paragraph each (at most three).
	w("## Root cause\n\n")
	shown := 0
	for i, op := range f.BadOps {
		if shown == 3 {
			w("(%d more blamed operation(s) follow the same pattern.)\n\n", len(f.BadOps)-shown)
			break
		}
		w("%s\n\n", f.explainOp(i, op))
		shown++
	}
	if shown == 0 {
		w("The checker did not point at a single operation; see result.json and linearizability.html.\n\n")
	}
	return b.String()
}

func verdictWord(v string) string {
	if v == "FAIL" {
		return "failed"
	}
	return "did not fail (" + v + ")"
}

func flagList(env map[string]string) []string {
	var out []string
	for k, v := range env {
		out = append(out, fmt.Sprintf("`%s=%s`", k, v))
	}
	sort.Strings(out)
	return out
}

func leaderList(ls []LeaderChange) string {
	var parts []string
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%s at step %d", l.Leader, l.Step))
	}
	if len(parts) > 6 {
		parts = append(parts[:6], fmt.Sprintf("… (%d in all)", len(ls)))
	}
	return strings.Join(parts, ", ")
}

func until(end int64) string {
	if end < 0 {
		return "the end of the run"
	}
	return fmt.Sprintf("step %d", end)
}

func describeWindow(x Window, _ int64) string {
	switch x.Kind {
	case "partition":
		var g []string
		for _, grp := range x.Groups {
			g = append(g, strings.Join(grp, ","))
		}
		return fmt.Sprintf("Step %d → %s: network partition `%s`.", x.Start, until(x.End), strings.Join(g, " | "))
	case "crash":
		return fmt.Sprintf("Step %d → %s: %s crashed (killed, restarted later with its data).", x.Start, until(x.End), x.Node)
	case "pause":
		return fmt.Sprintf("Step %d → %s: %s paused (frozen, then resumed).", x.Start, until(x.End), x.Node)
	case "clock_skew":
		dir := "forward"
		if x.JumpMs() < 0 {
			dir = "backward"
		}
		return fmt.Sprintf("Step %d: %s's clock jumped %s by %d ms (offset %+d → %+d ms).", x.Start, x.Node, dir, abs(x.JumpMs()), x.PrevMs, x.OffsetMs)
	}
	return fmt.Sprintf("Step %d: %s.", x.Start, x.Kind)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// steps renders an op's time span: "step 715" or "steps 700–715".
func steps(a, b int64) string {
	if a == b {
		return fmt.Sprintf("step %d", a)
	}
	return fmt.Sprintf("steps %d–%d", a, b)
}

func describeOp(o, done history.Op) string {
	switch o.F {
	case history.Read:
		got := done.Value
		if got == "" {
			got = "nothing (missing)"
		} else {
			got = "`" + got + "`"
		}
		return fmt.Sprintf("client %d read `%s` from %s at %s and got %s", o.Client, o.Key, o.Node, steps(o.Step, done.Step), got)
	case history.Write:
		return fmt.Sprintf("client %d wrote `%s=%s` via %s at %s (%s)", o.Client, o.Key, o.Value, o.Node, steps(o.Step, done.Step), done.Type)
	default:
		return fmt.Sprintf("client %d did CAS `%s` `%s`→`%s` via %s at %s (%s)", o.Client, o.Key, o.Expect, o.Value, o.Node, steps(o.Step, done.Step), done.Type)
	}
}

// explainOp writes the paragraph for one blamed op.
func (f Facts) explainOp(i int, b BadOp) string {
	o, done := b.Invoke, b.Complete
	var s strings.Builder
	fmt.Fprintf(&s, "**%s** flagged this: %s.", b.Checker, describeOp(o, done))
	if w := f.Expected[i]; w != nil {
		fmt.Fprintf(&s, " But client %d's write of `%s` (via %s) had already been acknowledged at step %d, so a correct store could not return the older value.", w.Client, w.Value, w.Node, w.Step)
	}
	leader, since := f.LeaderAt(o.Step)
	if leader != "" {
		if leader == o.Node {
			fmt.Fprintf(&s, " %s was the leader (since step %d).", o.Node, since)
		} else {
			fmt.Fprintf(&s, " The leader at that time was %s (since step %d), not %s.", leader, since, o.Node)
		}
	}
	part, iso := f.isolated(o.Node, o.Step, done.Step)
	skew := f.skewOn(o.Node, done.Step)
	crash := f.downAround(o.Step, done.Step+300)

	s.WriteString("\n\n**Why:** ")
	switch {
	case b.Checker == "durability":
		s.WriteString(f.whyLostWrite(o, done, crash, part, iso))
	case o.F == history.Read && iso && skew != nil && skew.JumpMs() < 0 && skew.Start >= part.Start:
		fmt.Fprintf(&s, "%s was cut off from the majority by the partition at step %d, and its clock jumped back %d ms at step %d. A lease read trusts the local clock: after the jump, %s thought its leadership lease was still valid and served the read from local state, while the majority had already elected a new leader and accepted newer writes. Lease reads are only safe if clocks cannot go backwards.", o.Node, part.Start, -skew.JumpMs(), skew.Start, o.Node)
	case o.F == history.Read && iso:
		fmt.Fprintf(&s, "%s was on the minority side of the partition that started at step %d, so it could not hear about newer writes, yet it still answered the read from its local copy. A linearizable read must be confirmed by a majority (through the leader's log or a read-index/quorum check); a node that cannot reach a majority must refuse or time out instead.", o.Node, part.Start)
	case o.F == history.Read:
		fmt.Fprintf(&s, "%s answered from a local copy that had not caught up with the latest acknowledged write. Reads served without a quorum check can lag behind the log, even without a partition (for example right after a crash, restart or delayed messages).", o.Node)
	default:
		s.WriteString("No sequential order of the operations explains every result the clients saw, so the store let two clients observe the key in incompatible states.")
	}
	return s.String()
}

func (f Facts) whyLostWrite(o, done history.Op, crash *Window, part Window, iso bool) string {
	switch {
	case crash != nil && crash.Node == o.Node:
		return fmt.Sprintf("%s acknowledged the write at step %d, then crashed at step %d. The write had not reached a majority yet, so the rest of the cluster elected a new leader without it, and the final reads show it is gone. A write may only be acknowledged after it is committed on a majority.", o.Node, done.Step, crash.Start)
	case iso:
		return fmt.Sprintf("%s acknowledged the write at step %d while it was cut off from the majority by the partition that started at step %d. The majority never stored it, elected a new leader, and the write was overwritten when the partition healed. A write may only be acknowledged after it is committed on a majority.", o.Node, done.Step, part.Start)
	}
	return fmt.Sprintf("%s acknowledged the write at step %d, but no read ever saw it and the final reads after healing do not contain it: the store acknowledged a write it later discarded.", o.Node, done.Step)
}

// maxFaultsListed is how many faults Story lists before it keeps only those
// that overlap the blamed ops.
const maxFaultsListed = 8

// relevantFaults returns every fault when there are few; otherwise the faults
// active during a blamed op (or in the 300 steps before it) on the nodes
// involved, plus every partition overlapping that span. hidden counts the rest.
func (f Facts) relevantFaults() (shown []Window, hidden int) {
	if len(f.Faults) <= maxFaultsListed || len(f.BadOps) == 0 {
		return f.Faults, 0
	}
	for _, x := range f.Faults {
		keep := false
		for _, b := range f.BadOps {
			a, z := b.Invoke.Step-300, b.Complete.Step
			if x.Kind == "partition" && x.Overlaps(a, z) {
				keep = true
			}
			if x.Node == b.Invoke.Node && x.Start >= a && x.Start <= z {
				keep = true // what happened to the node that answered, just before it answered
			}
			if (x.Kind == "crash" || x.Kind == "pause") && x.Overlaps(a, z) {
				keep = true
			}
		}
		if keep {
			shown = append(shown, x)
		}
	}
	return shown, len(f.Faults) - len(shown)
}

// skewOn returns the latest clock skew on node at or before step s.
func (f Facts) skewOn(node string, s int64) *Window {
	var last *Window
	for i := range f.Faults {
		x := f.Faults[i]
		if x.Kind == "clock_skew" && x.Node == node && x.Start <= s {
			last = &f.Faults[i]
		}
	}
	return last
}

// downAround returns the first crash or pause overlapping [a, b].
func (f Facts) downAround(a, b int64) *Window {
	for i := range f.Faults {
		x := f.Faults[i]
		if (x.Kind == "crash" || x.Kind == "pause") && x.Overlaps(a, b) {
			return &f.Faults[i]
		}
	}
	return nil
}
