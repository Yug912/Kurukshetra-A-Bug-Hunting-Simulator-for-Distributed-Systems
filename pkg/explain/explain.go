// Package explain turns a failing run folder into a short plain-English
// root-cause story: which faults were active, who was leader, which operation
// broke which guarantee, and why. It is rule-based: it only combines the
// checkers' bad ops with the fault timeline and leader notes in the trace.
package explain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/trace"
)

// File is the name of the story written into the run folder.
const File = "explain.md"

// Window is one fault as it actually happened, from the trace.
type Window struct {
	Kind     string     `json:"kind"` // partition | crash | pause | clock_skew
	Start    int64      `json:"start"`
	End      int64      `json:"end"` // -1 = until the end of the run
	Node     string     `json:"node,omitempty"`
	Groups   [][]string `json:"groups,omitempty"`
	OffsetMs int64      `json:"offset_ms,omitempty"`
	// PrevMs is the node's clock offset just before this skew (0 at start and
	// after a restart). The jump is OffsetMs - PrevMs; negative = backwards.
	PrevMs int64 `json:"prev_offset_ms,omitempty"`
}

// JumpMs is how far a clock skew moved the node's clock (negative = backwards).
func (w Window) JumpMs() int64 { return w.OffsetMs - w.PrevMs }

// Covers reports whether step s falls inside the window.
func (w Window) Covers(s int64) bool { return s >= w.Start && (w.End < 0 || s <= w.End) }

// Overlaps reports whether the window intersects [a, b].
func (w Window) Overlaps(a, b int64) bool { return w.Start <= b && (w.End < 0 || w.End >= a) }

// LeaderChange is one leader note from the trace.
type LeaderChange struct {
	Step   int64  `json:"step"`
	Leader string `json:"leader"`
	Term   string `json:"term,omitempty"`
}

// BadOp is one operation a checker blamed: its invoke and completion.
type BadOp struct {
	Checker  string     `json:"checker"`
	Invoke   history.Op `json:"invoke"`
	Complete history.Op `json:"complete"`
}

// Facts is everything the story is built from. It is also what --llm sends.
type Facts struct {
	Dir         string              `json:"dir"`
	Scenario    string              `json:"scenario"`
	Seed        int64               `json:"seed"`
	Verdict     string              `json:"verdict"`
	Env         map[string]string   `json:"bug_flags,omitempty"`
	Minimal     bool                `json:"minimal"` // the shrinker's minimal run
	Failing     []core.CheckResult  `json:"failing_checks"`
	BadOps      []BadOp             `json:"bad_ops"`
	Faults      []Window            `json:"faults"`
	Leaders     []LeaderChange      `json:"leaders,omitempty"`
	Expected    map[int]*history.Op `json:"-"` // bad read index → the write it should have seen
	ExpectedOps []history.Op        `json:"expected_writes,omitempty"`
	EndStep     int64               `json:"end_step"`
}

// Resolve picks the run to explain: the shrinker's minimal run (<dir>-min)
// when dir has a minimal.json and that run exists, else dir itself.
func Resolve(dir string) string {
	dir = filepath.Clean(dir)
	if strings.HasSuffix(dir, "-min") {
		return dir
	}
	if _, err := os.Stat(filepath.Join(dir, "minimal.json")); err == nil {
		if _, err := os.Stat(filepath.Join(dir+"-min", core.ResultFile)); err == nil {
			return dir + "-min"
		}
	}
	return dir
}

// Gather reads a run folder.
func Gather(dir string) (Facts, error) {
	f := Facts{Dir: dir, Expected: map[int]*history.Op{}}
	res, err := core.ReadResult(dir)
	if err != nil {
		return f, fmt.Errorf("read result: %w", err)
	}
	f.Scenario, f.Seed, f.Verdict = res.Scenario, res.Seed, res.Verdict
	if m, err := core.ReadMeta(dir); err == nil {
		f.Minimal = m.FaultsFrom != "" && strings.HasSuffix(filepath.Clean(dir), "-min")
	}
	if sc, err := readScenarioEnv(dir); err == nil {
		f.Env = sc
	}
	for _, c := range res.Checks {
		if c.Verdict == core.VerdictFail {
			f.Failing = append(f.Failing, c)
		}
	}
	ops, err := history.ReadFile(filepath.Join(dir, core.HistoryFile))
	if err != nil {
		return f, fmt.Errorf("read history: %w", err)
	}
	evs, err := trace.ReadFile(filepath.Join(dir, core.TraceFile))
	if err != nil {
		return f, fmt.Errorf("read trace: %w", err)
	}
	f.Faults, f.Leaders, f.EndStep = timeline(evs)
	f.BadOps = badOps(f.Failing, ops)
	for i, b := range f.BadOps {
		if b.Invoke.F != history.Read {
			continue
		}
		if w := expectedWrite(b, f.Failing, ops); w != nil {
			f.Expected[i] = w
			f.ExpectedOps = append(f.ExpectedOps, *w)
		}
	}
	return f, nil
}

// readScenarioEnv returns the BUG_* flags from scenario.yaml (a tiny parser:
// the env block is flat "KEY: value" lines).
func readScenarioEnv(dir string) (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, core.ScenarioFile))
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "BUG_") {
			k, v, ok := strings.Cut(t, ":")
			if ok {
				env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	if len(env) == 0 {
		return nil, errors.New("no bug flags")
	}
	return env, nil
}

// timeline pairs fault start/end events and collects leader notes.
func timeline(evs []trace.Event) ([]Window, []LeaderChange, int64) {
	var out []Window
	var leaders []LeaderChange
	var end int64
	openPart := -1
	openNode := map[string]int{} // "crash/n1" → index into out
	offset := map[string]int64{} // node → current clock offset
	for _, e := range evs {
		if e.Step > end {
			end = e.Step
		}
		switch e.Kind {
		case trace.Partition:
			out = append(out, Window{Kind: "partition", Start: e.Step, End: -1, Groups: parseGroups(e.Meta["groups"])})
			openPart = len(out) - 1
		case trace.Heal:
			if openPart >= 0 {
				out[openPart].End = e.Step
				openPart = -1
			}
		case trace.Crash, trace.Pause:
			out = append(out, Window{Kind: string(e.Kind), Start: e.Step, End: -1, Node: e.Node})
			openNode[string(e.Kind)+"/"+e.Node] = len(out) - 1
		case trace.Restart, trace.Resume:
			k := "crash/"
			if e.Kind == trace.Resume {
				k = "pause/"
			}
			if e.Kind == trace.Restart {
				offset[e.Node] = 0 // a restarted node comes back with offset 0
			}
			if i, ok := openNode[k+e.Node]; ok {
				out[i].End = e.Step
				delete(openNode, k+e.Node)
			}
		case trace.ClockSkew:
			if e.Meta["unsupported"] == "true" {
				continue
			}
			off, _ := strconv.ParseInt(e.Meta["offset_ms"], 10, 64)
			out = append(out, Window{Kind: "clock_skew", Start: e.Step, End: -1, Node: e.Node, OffsetMs: off, PrevMs: offset[e.Node]})
			offset[e.Node] = off
		case trace.Note:
			if l := e.Meta["leader"]; l != "" {
				leaders = append(leaders, LeaderChange{Step: e.Step, Leader: l, Term: e.Meta["term"]})
			}
		}
	}
	return out, leaders, end
}

func parseGroups(s string) [][]string {
	if s == "" {
		return nil
	}
	var g [][]string
	for _, part := range strings.Split(s, "|") {
		g = append(g, strings.Split(strings.TrimSpace(part), ","))
	}
	return g
}

// badOps pairs every blamed history index with its invoke and completion.
func badOps(failing []core.CheckResult, ops []history.Op) []BadOp {
	byIndex := make(map[int]history.Op, len(ops))
	for _, o := range ops {
		byIndex[o.Index] = o
	}
	var out []BadOp
	seen := map[int]bool{}
	for _, c := range failing {
		for _, idx := range c.BadOps {
			o, ok := byIndex[idx]
			if !ok || o.Type != history.Invoke || seen[idx] {
				continue
			}
			seen[idx] = true
			b := BadOp{Checker: c.Checker, Invoke: o, Complete: o}
			for _, later := range ops { // the client's next completion
				if later.Index > o.Index && later.Client == o.Client && later.Type != history.Invoke {
					b.Complete = later
					break
				}
			}
			out = append(out, b)
		}
		// durability lists completions only: synthesise the invoke.
		for _, idx := range c.BadOps {
			o, ok := byIndex[idx]
			if !ok || o.Type == history.Invoke || seen[idx] {
				continue
			}
			seen[idx] = true
			b := BadOp{Checker: c.Checker, Invoke: o, Complete: o}
			for j := len(ops) - 1; j >= 0; j-- {
				p := ops[j]
				if p.Index < o.Index && p.Client == o.Client && p.Type == history.Invoke {
					b.Invoke = p
					break
				}
			}
			if !containsInvoke(out, b.Invoke.Index) {
				out = append(out, b)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Invoke.Step < out[j].Invoke.Step })
	return out
}

func containsInvoke(bs []BadOp, idx int) bool {
	for _, b := range bs {
		if b.Invoke.Index == idx {
			return true
		}
	}
	return false
}

var expectedRe = regexp.MustCompile(`stale read of (\S+) on (\S+): got (.+?), expected (.+?)(?: \(and|$)`)

// expectedWrite finds the completed write whose value the stale read should
// have returned, from the checker's "expected X" (the first if several).
func expectedWrite(b BadOp, failing []core.CheckResult, ops []history.Op) *history.Op {
	for _, c := range failing {
		m := expectedRe.FindStringSubmatch(c.Reason)
		if m == nil || m[1] != b.Invoke.Key {
			continue
		}
		want := strings.TrimSpace(strings.Split(m[4], " or ")[0])
		for i := len(ops) - 1; i >= 0; i-- {
			o := ops[i]
			if o.Type == history.OK && o.Key == b.Invoke.Key && o.Value == want && o.F != history.Read && o.Step <= b.Complete.Step {
				return &ops[i]
			}
		}
	}
	return nil
}

// LeaderAt returns the leader known at step s ("" if none yet).
func (f Facts) LeaderAt(s int64) (string, int64) {
	l, since := "", int64(-1)
	for _, c := range f.Leaders {
		if c.Step <= s {
			l, since = c.Leader, c.Step
		}
	}
	return l, since
}

// isolated reports whether node sat on the minority side of a partition
// active during [a, b].
func (f Facts) isolated(node string, a, b int64) (Window, bool) {
	for _, w := range f.Faults {
		if w.Kind != "partition" || !w.Overlaps(a, b) {
			continue
		}
		for _, g := range w.Groups {
			for _, n := range g {
				if n == node && len(g)*2 <= totalNodes(w.Groups) {
					return w, true
				}
			}
		}
	}
	return Window{}, false
}

func totalNodes(groups [][]string) int {
	n := 0
	for _, g := range groups {
		n += len(g)
	}
	return n
}
