package fakes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

func outRoot(opts core.RunOptions) string {
	if opts.OutRoot == "" {
		return "runs"
	}
	return opts.OutRoot
}

// SampleDir locates testdata/sample-run: $KURU_SAMPLE_DIR, else <dir with go.mod>/testdata/sample-run
// found by walking up from the working directory.
func SampleDir() (string, error) {
	if d := os.Getenv("KURU_SAMPLE_DIR"); d != "" {
		return d, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "sample-run"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("fakes: go.mod not found above working directory; set KURU_SAMPLE_DIR")
		}
		dir = parent
	}
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// SampleRunner ("fake-sample") copies testdata/sample-run into the run dir.
type SampleRunner struct{}

// Run implements core.Runner.
func (SampleRunner) Run(ctx context.Context, sc scenario.Scenario, seed int64, opts core.RunOptions) (core.RunInfo, error) {
	src, err := SampleDir()
	if err != nil {
		return core.RunInfo{}, err
	}
	dir := core.RunDir(outRoot(opts), sc.Name, seed)
	if err := os.RemoveAll(dir); err != nil {
		return core.RunInfo{}, err
	}
	if err := copyDir(src, dir); err != nil {
		return core.RunInfo{}, err
	}
	meta, err := core.ReadMeta(dir)
	if err != nil {
		return core.RunInfo{}, err
	}
	meta.Seed = seed
	meta.Runner = "fake-sample"
	res, err := core.ReadResult(dir)
	if err != nil {
		return core.RunInfo{}, err
	}
	res.Seed = seed
	if err := core.WriteMeta(dir, meta); err != nil {
		return core.RunInfo{}, err
	}
	if err := core.WriteResult(dir, res); err != nil {
		return core.RunInfo{}, err
	}
	faults, err := core.ReadFaults(filepath.Join(dir, core.FaultsFile))
	if err != nil {
		return core.RunInfo{}, err
	}
	return core.RunInfo{Dir: dir, Result: res, Faults: faults, Meta: meta}, nil
}

// FaultsRunner ("fake-faults") needs no Docker: it only builds or loads a fault plan
// and FAILs iff the plan isolates n1 in a partition AND crashes n3.
type FaultsRunner struct{}

// PlanSize is the number of faults FaultsRunner generates.
const PlanSize = 8

// GeneratePlan builds the fake-faults plan for seed over nodes.
func GeneratePlan(seed int64, nodes []string) []core.Fault {
	r := rng.New(seed).Stream(rng.StreamFaults)
	kinds := []trace.Kind{trace.Partition, trace.Crash, trace.Pause, trace.ClockSkew}
	// Each kind walks its own shuffled node order, so repeated faults of a kind hit different nodes.
	order := map[trace.Kind][]int{}
	for _, k := range kinds {
		order[k] = rng.New(seed).Keyed(rng.StreamFaults, "nodes/"+string(k)).Perm(len(nodes))
	}
	used := map[trace.Kind]int{}
	next := func(k trace.Kind) string {
		n := nodes[order[k][used[k]%len(nodes)]]
		used[k]++
		return n
	}
	var plan []core.Fault
	step := int64(0)
	for i := 0; i < PlanSize; i++ {
		step += rng.Between(r, [2]int64{60, 180})
		f := core.Fault{Step: step, Kind: kinds[i%len(kinds)]} // every kind twice per plan
		switch f.Kind {
		case trace.Partition:
			f.DurationSteps = rng.Between(r, [2]int64{100, 300})
			alone := next(f.Kind)
			var rest []string
			for _, n := range nodes {
				if n != alone {
					rest = append(rest, n)
				}
			}
			f.Groups = [][]string{{alone}, rest}
		case trace.Crash, trace.Pause:
			f.DurationSteps = rng.Between(r, [2]int64{50, 150})
			f.Nodes = []string{next(f.Kind)}
		case trace.ClockSkew:
			f.Nodes = []string{next(f.Kind)}
			f.OffsetMs = rng.Between(r, [2]int64{-500, 500})
			f.DurationSteps = rng.Between(r, [2]int64{100, 200})
		}
		plan = append(plan, f)
	}
	core.SortFaults(plan)
	return plan
}

// PlanFails reports whether plan isolates n1 in a partition group AND crashes n3.
func PlanFails(plan []core.Fault) bool {
	var iso, crash bool
	for _, f := range plan {
		switch f.Kind {
		case trace.Partition:
			for _, g := range f.Groups {
				if len(g) == 1 && g[0] == "n1" {
					iso = true
				}
			}
		case trace.Crash:
			crash = crash || slices.Contains(f.Nodes, "n3")
		}
	}
	return iso && crash
}

// Run implements core.Runner.
func (FaultsRunner) Run(ctx context.Context, sc scenario.Scenario, seed int64, opts core.RunOptions) (core.RunInfo, error) {
	nodes := scenario.NodeNames(sc.Nodes)
	var plan []core.Fault
	if opts.FaultsFrom != "" {
		p, err := core.ReadFaults(opts.FaultsFrom)
		if err != nil {
			return core.RunInfo{}, err
		}
		plan = p
		core.SortFaults(plan)
	} else {
		plan = GeneratePlan(seed, nodes)
	}
	dir := core.RunDir(outRoot(opts), sc.Name, seed)
	if err := os.RemoveAll(dir); err != nil {
		return core.RunInfo{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, core.LogsDir), 0o755); err != nil {
		return core.RunInfo{}, err
	}
	start := time.Now()
	if err := core.WriteFaults(filepath.Join(dir, core.FaultsFile), plan); err != nil {
		return core.RunInfo{}, err
	}
	scb, err := yaml.Marshal(sc)
	if err != nil {
		return core.RunInfo{}, err
	}
	for name, data := range map[string][]byte{core.ScenarioFile: scb, core.TraceFile: nil, core.HistoryFile: nil} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return core.RunInfo{}, err
		}
	}
	check := core.CheckResult{Checker: "fake-faults", Verdict: core.VerdictPass}
	if PlanFails(plan) {
		check.Verdict = core.VerdictFail
		check.Reason = "plan isolates n1 and crashes n3"
	}
	res := core.Result{Seed: seed, Scenario: sc.Name, Checks: []core.CheckResult{check}}
	res.Verdict = core.CombineVerdict(res.Checks)
	meta := core.Meta{
		Scenario: sc.Name, Seed: seed, Nodes: nodes, Mode: sc.Mode, Runner: "fake-faults",
		StartedAt: start.UTC(), DurationMs: time.Since(start).Milliseconds(), Steps: sc.DurationSteps,
		FaultsFrom: opts.FaultsFrom, Version: core.Version,
	}
	if err := core.WriteResult(dir, res); err != nil {
		return core.RunInfo{}, err
	}
	if err := core.WriteMeta(dir, meta); err != nil {
		return core.RunInfo{}, err
	}
	if !opts.Quiet {
		fmt.Fprintf(os.Stderr, "fake-faults: %s seed %d -> %s (%s)\n", sc.Name, seed, res.Verdict, dir)
	}
	return core.RunInfo{Dir: dir, Result: res, Faults: plan, Meta: meta}, nil
}
