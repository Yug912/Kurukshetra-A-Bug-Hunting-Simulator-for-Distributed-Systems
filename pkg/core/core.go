// Package core holds the interfaces, registries and run-folder layout shared by
// the engine (Person A) and the workload/checkers/shrinker (Person B).
//
// Run folder layout: <root>/<scenario>/<seed>/ containing the files named below.
package core

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"time"

	"kurukshetra/pkg/history"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

// File names inside a run folder.
const (
	TraceFile    = "trace.jsonl"
	HistoryFile  = "history.jsonl"
	ResultFile   = "result.json"
	FaultsFile   = "faults.json" // the fault plan actually used
	MetaFile     = "meta.json"
	ScenarioFile = "scenario.yaml" // copy of the scenario
	LogsDir      = "logs"          // logs/<node>.log
)

// Verdicts.
const (
	VerdictPass    = "PASS"
	VerdictFail    = "FAIL"
	VerdictError   = "ERROR"
	VerdictUnknown = "UNKNOWN"
)

// RunDir returns <root>/<scenario>/<seed>.
func RunDir(root, scenario string, seed int64) string {
	return filepath.Join(root, scenario, strconv.FormatInt(seed, 10))
}

// Node is one cluster member as seen by clients.
type Node struct {
	Name      string `json:"name"`       // n1..nN
	ClientURL string `json:"client_url"` // e.g. http://127.0.0.1:49153 (reachable from the kuru process)
}

// Cluster is the set of nodes under test.
type Cluster struct{ Nodes []Node }

// StepClock reports the current logical step.
type StepClock interface{ Step() int64 }

// Workload generates client traffic.
type Workload interface {
	// Run sends client traffic until ctx is cancelled, recording every op.
	Run(ctx context.Context, cl Cluster, r *rand.Rand, clock StepClock, rec history.Recorder) error
	// FinalReads reads every key from every node once (Op.Final=true) after faults are healed.
	FinalReads(ctx context.Context, cl Cluster, clock StepClock, rec history.Recorder) error
}

// WorkloadFactory builds a Workload from config.
type WorkloadFactory func(cfg scenario.Workload) (Workload, error)

// CheckResult is one checker's verdict.
type CheckResult struct {
	Checker   string            `json:"checker"`
	Verdict   string            `json:"verdict"` // PASS | FAIL | UNKNOWN
	Reason    string            `json:"reason,omitempty"`
	BadOps    []int             `json:"bad_ops,omitempty"`   // history Op.Index values
	Artifacts map[string]string `json:"artifacts,omitempty"` // name -> file name inside run dir
}

// Checker judges a history.
type Checker interface {
	Name() string
	// Check judges a history. runDir may be "" (then write no artifacts).
	Check(ops []history.Op, runDir string) CheckResult
}

// CheckerFactory builds a Checker.
type CheckerFactory func() Checker

// Result is the content of result.json.
type Result struct {
	Seed     int64         `json:"seed"`
	Scenario string        `json:"scenario"`
	Verdict  string        `json:"verdict"` // PASS | FAIL | ERROR | UNKNOWN
	Error    string        `json:"error,omitempty"`
	Checks   []CheckResult `json:"checks"`
	Fidelity *float64      `json:"fidelity,omitempty"` // only for replays
}

// Fault is one scheduled fault in faults.json.
type Fault struct {
	Step          int64      `json:"step"`
	Kind          trace.Kind `json:"kind"` // partition | crash | pause | clock_skew
	Nodes         []string   `json:"nodes,omitempty"`
	Groups        [][]string `json:"groups,omitempty"` // partition only; nodes in different groups cannot talk
	DurationSteps int64      `json:"duration_steps"`
	OffsetMs      int64      `json:"offset_ms,omitempty"`
}

// Meta is the content of meta.json.
type Meta struct {
	Scenario   string    `json:"scenario"`
	Seed       int64     `json:"seed"`
	Nodes      []string  `json:"nodes"`
	Mode       string    `json:"mode"`
	Runner     string    `json:"runner"`
	StartedAt  time.Time `json:"started_at"`
	DurationMs int64     `json:"duration_ms"`
	Steps      int64     `json:"steps"`
	ReplayOf   string    `json:"replay_of,omitempty"`
	FaultsFrom string    `json:"faults_from,omitempty"`
	Version    string    `json:"version"`
}

// RunOptions tunes a single run.
type RunOptions struct {
	OutRoot         string // default "runs"
	FaultsFrom      string // path to a faults.json to use instead of generating the plan
	NoMessageFaults bool   // disable drop/dup/delay (used by the shrinker)
	ReplayFrom      string // run dir to replay (engine only)
	Keep            bool   // keep containers after the run
	Quiet           bool
}

// RunInfo describes a finished run.
type RunInfo struct {
	Dir    string
	Result Result
	Faults []Fault
	Meta   Meta
}

// Runner executes one scenario with one seed.
type Runner interface {
	Run(ctx context.Context, sc scenario.Scenario, seed int64, opts RunOptions) (RunInfo, error)
}

// RunnerFactory builds a Runner.
type RunnerFactory func() (Runner, error)

// CombineVerdict: any FAIL => FAIL; else any UNKNOWN => UNKNOWN; else PASS (empty => UNKNOWN).
func CombineVerdict(checks []CheckResult) string {
	if len(checks) == 0 {
		return VerdictUnknown
	}
	v := VerdictPass
	for _, c := range checks {
		switch c.Verdict {
		case VerdictFail:
			return VerdictFail
		case VerdictPass:
		default:
			v = VerdictUnknown
		}
	}
	return v
}

// Version is the kuru version string written into meta.json.
var Version = "0.0.0-dev"

func errorf(format string, a ...any) error { return fmt.Errorf("core: "+format, a...) }
