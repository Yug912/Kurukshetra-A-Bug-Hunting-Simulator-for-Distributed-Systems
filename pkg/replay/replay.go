// Package replay rebuilds the message decisions and delivery order of a
// recorded run (a ReplayGuide for the scheduler) and scores how faithfully a
// replay reproduced it. It is pure: the engine runs the replay.
package replay

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/scheduler"
	"kurukshetra/pkg/trace"
)

// ShiftTolerance is how many steps a replayed decision may move and still match.
const ShiftTolerance = 2

// DefaultWaitSteps is how long the scheduler waits for a recorded message that
// has not arrived yet before moving on.
const DefaultWaitSteps = 30

// IsStartupID reports whether id belongs to startup traffic ("<from>-<to>-s<n>"),
// which passes straight through and is not a scheduler decision.
func IsStartupID(id string) bool {
	i := strings.LastIndexByte(id, '-')
	return i >= 0 && strings.HasPrefix(id[i+1:], "s")
}

// BuildGuide extracts the recorded decision, decision step and per-step
// delivery order of every message decided by the scheduler.
func BuildGuide(evs []trace.Event) *scheduler.ReplayGuide {
	g := &scheduler.ReplayGuide{
		Decisions: map[string]scheduler.Decision{},
		Steps:     map[string]int64{},
		Order:     map[int64][]string{},
		WaitSteps: DefaultWaitSteps,
	}
	ordered := map[string]bool{}
	for id, d := range Decisions(evs) {
		if IsStartupID(id) {
			continue
		}
		g.Steps[id] = d.Step
		g.Decisions[id] = toDecision(d.Kind)
	}
	for _, e := range evs {
		if _, ok := g.Steps[e.MsgID]; !ok || ordered[e.MsgID] {
			continue
		}
		if e.Kind == trace.MsgDeliver || e.Kind == trace.MsgDup {
			ordered[e.MsgID] = true
			g.Order[e.Step] = append(g.Order[e.Step], e.MsgID)
		}
	}
	return g
}

func toDecision(kind string) scheduler.Decision {
	switch {
	case kind == "dup":
		return scheduler.Decision{Fate: scheduler.Duplicate}
	case strings.HasPrefix(kind, "delay:"):
		n, _ := strconv.ParseInt(strings.TrimPrefix(kind, "delay:"), 10, 64)
		return scheduler.Decision{Fate: scheduler.Delay, Steps: n}
	case strings.HasPrefix(kind, "drop:"):
		return scheduler.Decision{Fate: scheduler.Drop, Reason: strings.TrimPrefix(kind, "drop:")}
	}
	return scheduler.Decision{Fate: scheduler.Deliver}
}

// IsExtraID reports whether id was marked as having no counterpart in the
// original run ("<from>-<to>-x<n>").
func IsExtraID(id string) bool {
	i := strings.LastIndexByte(id, '-')
	return i >= 0 && strings.HasPrefix(id[i+1:], "x")
}

// NoMessageFaults reports whether the recorded run disabled message faults
// (run_start meta no_message_faults=true).
func NoMessageFaults(evs []trace.Event) bool {
	for _, e := range evs {
		if e.Kind == trace.RunStart {
			return e.Meta["no_message_faults"] == "true"
		}
	}
	return false
}

// NextDir returns "<runDir>-replay-<n>" for the first n >= 1 that does not exist.
func NextDir(runDir string) string {
	runDir = filepath.Clean(runDir)
	for n := 1; ; n++ {
		d := fmt.Sprintf("%s-replay-%d", runDir, n)
		if _, err := os.Stat(d); os.IsNotExist(err) {
			return d
		}
	}
}

// Fidelity compares a replay with its original.
type Fidelity struct {
	Total   int `json:"total"`   // recorded message decisions
	Matched int `json:"matched"` // same decision within ShiftTolerance steps
	Shifted int `json:"shifted"` // same decision, more than ShiftTolerance steps away
	Changed int `json:"changed"` // different decision
	Missed  int `json:"missed"`  // recorded message never seen in the replay
	Extra   int `json:"extra"`   // replay messages not in the original

	FaultTotal   int `json:"fault_total"`
	FaultMatched int `json:"fault_matched"` // fault events identical (step, kind, node, groups, offset)

	Score float64 `json:"score"` // (Matched + FaultMatched) / (Total + FaultTotal)
}

// MessageScore is Matched/Total (1 if nothing was recorded).
func (f Fidelity) MessageScore() float64 { return ratio(f.Matched, f.Total) }

// FaultScore is FaultMatched/FaultTotal (1 if there were no faults).
func (f Fidelity) FaultScore() float64 { return ratio(f.FaultMatched, f.FaultTotal) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 1
	}
	return float64(a) / float64(b)
}

// String renders the breakdown on one line.
func (f Fidelity) String() string {
	return fmt.Sprintf("fidelity %.1f%%: messages %d/%d matched (%.1f%%), %d shifted, %d changed, %d missed, %d extra; faults %d/%d",
		f.Score*100, f.Matched, f.Total, f.MessageScore()*100, f.Shifted, f.Changed, f.Missed, f.Extra, f.FaultMatched, f.FaultTotal)
}

func isFault(k trace.Kind) bool {
	switch k {
	case trace.Partition, trace.Heal, trace.Crash, trace.Restart, trace.Pause, trace.Resume, trace.ClockSkew:
		return true
	}
	return false
}

func faultKey(e trace.Event) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s|%s", e.Step, e.Kind, e.Node, e.Meta["groups"], e.Meta["bridge"], e.Meta["offset_ms"])
}

// Compare scores a replay trace against the original trace.
func Compare(orig, rep []trace.Event) Fidelity {
	var f Fidelity
	od, rd := Decisions(orig), Decisions(rep)
	for id, o := range od {
		if IsStartupID(id) {
			continue
		}
		f.Total++
		r, ok := rd[id]
		switch {
		case !ok:
			f.Missed++
		case r.Kind != o.Kind:
			f.Changed++
		case abs(r.Step-o.Step) <= ShiftTolerance:
			f.Matched++
		default:
			f.Shifted++
		}
	}
	for id := range rd {
		if _, ok := od[id]; !ok && !IsStartupID(id) {
			f.Extra++
		}
	}
	want := map[string]int{}
	for _, e := range orig {
		if isFault(e.Kind) {
			want[faultKey(e)]++
			f.FaultTotal++
		}
	}
	for _, e := range rep {
		if isFault(e.Kind) && want[faultKey(e)] > 0 {
			want[faultKey(e)]--
			f.FaultMatched++
		}
	}
	f.Score = ratio(f.Matched+f.FaultMatched, f.Total+f.FaultTotal)
	return f
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Original is what a replay needs from the recorded run folder.
type Original struct {
	Dir    string
	Meta   core.Meta
	Result core.Result
	Trace  []trace.Event
}

// Load reads meta.json, result.json (optional) and trace.jsonl of a run folder.
func Load(dir string) (Original, error) {
	o := Original{Dir: filepath.Clean(dir)}
	var err error
	if o.Meta, err = core.ReadMeta(dir); err != nil {
		return o, fmt.Errorf("replay: %w", err)
	}
	if o.Trace, err = trace.ReadFile(filepath.Join(dir, core.TraceFile)); err != nil {
		return o, fmt.Errorf("replay: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, core.FaultsFile)); err != nil {
		return o, fmt.Errorf("replay: %w", err)
	}
	o.Result, _ = core.ReadResult(dir)
	return o, nil
}

// SortedSteps returns the steps that have a recorded delivery order (for tests and debugging).
func SortedSteps(g *scheduler.ReplayGuide) []int64 {
	out := make([]int64, 0, len(g.Order))
	for s := range g.Order {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
