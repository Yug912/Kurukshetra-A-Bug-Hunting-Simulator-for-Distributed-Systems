package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kurukshetra/pkg/history"
	"kurukshetra/pkg/scenario"
)

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ReadFaults reads a faults.json file.
func ReadFaults(path string) ([]Fault, error) {
	var f []Fault
	if err := readJSON(path, &f); err != nil {
		return nil, err
	}
	return f, nil
}

// SortFaults sorts faults by Step then Kind (stable).
func SortFaults(f []Fault) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Step != f[j].Step {
			return f[i].Step < f[j].Step
		}
		return f[i].Kind < f[j].Kind
	})
}

// WriteFaults writes pretty JSON, sorted by Step then Kind. The input slice is not modified.
func WriteFaults(path string, f []Fault) error {
	c := append([]Fault{}, f...)
	SortFaults(c)
	return writeJSON(path, c)
}

// DescribeFault renders a fault for humans, e.g. "partition n1 | n2,n3 at step 410 for 300".
func DescribeFault(f Fault) string {
	var who string
	if len(f.Groups) > 0 {
		gs := make([]string, len(f.Groups))
		for i, g := range f.Groups {
			gs[i] = strings.Join(g, ",")
		}
		who = strings.Join(gs, " | ")
	} else {
		who = strings.Join(f.Nodes, ",")
	}
	s := string(f.Kind)
	if who != "" {
		s += " " + who
	}
	if f.Kind == "clock_skew" || f.OffsetMs != 0 {
		s += fmt.Sprintf(" by %dms", f.OffsetMs)
	}
	return s + fmt.Sprintf(" at step %d for %d", f.Step, f.DurationSteps)
}

// ReadResult reads <dir>/result.json.
func ReadResult(dir string) (Result, error) {
	var r Result
	err := readJSON(filepath.Join(dir, ResultFile), &r)
	return r, err
}

// WriteResult writes <dir>/result.json.
func WriteResult(dir string, r Result) error {
	if r.Checks == nil {
		r.Checks = []CheckResult{}
	}
	return writeJSON(filepath.Join(dir, ResultFile), r)
}

// ReadMeta reads <dir>/meta.json.
func ReadMeta(dir string) (Meta, error) {
	var m Meta
	err := readJSON(filepath.Join(dir, MetaFile), &m)
	return m, err
}

// WriteMeta writes <dir>/meta.json.
func WriteMeta(dir string, m Meta) error {
	return writeJSON(filepath.Join(dir, MetaFile), m)
}

// CheckRun reads <dir>/history.jsonl, runs the named checkers (default: scenario's checkers from
// <dir>/scenario.yaml), writes <dir>/result.json (keeping seed/scenario/fidelity from any existing
// result.json) and returns it.
func CheckRun(dir string, names []string) (Result, error) {
	ops, err := history.ReadFile(filepath.Join(dir, HistoryFile))
	if err != nil {
		return Result{}, err
	}
	var sc scenario.Scenario
	scLoaded := false
	if b, err := os.ReadFile(filepath.Join(dir, ScenarioFile)); err == nil {
		if s, err := scenario.Parse(b); err == nil {
			sc, scLoaded = s, true
		} else if len(names) == 0 {
			return Result{}, err
		}
	} else if len(names) == 0 {
		return Result{}, err
	}
	if len(names) == 0 {
		names = sc.Checkers
	}
	res := Result{}
	if old, err := ReadResult(dir); err == nil {
		res.Seed, res.Scenario, res.Fidelity = old.Seed, old.Scenario, old.Fidelity
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Result{}, err
	} else {
		if m, err := ReadMeta(dir); err == nil {
			res.Seed, res.Scenario = m.Seed, m.Scenario
		} else if scLoaded {
			res.Scenario = sc.Name
		}
	}
	for _, n := range names {
		c, err := NewChecker(n)
		if err != nil {
			return Result{}, err
		}
		cr := c.Check(ops, dir)
		if cr.Checker == "" {
			cr.Checker = n
		}
		res.Checks = append(res.Checks, cr)
	}
	res.Verdict = CombineVerdict(res.Checks)
	if err := WriteResult(dir, res); err != nil {
		return Result{}, err
	}
	return res, nil
}
