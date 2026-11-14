package shrink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/scenario"
)

// Output file names written into the original run dir.
const (
	MinimalFile = "minimal.json"
	LogFile     = "shrink.log"
	MinSuffix   = "-min"
)

// Options configures a shrink.
type Options struct {
	Runner   core.Runner
	MaxRuns  int       // trial budget (default 60); cached trials are free
	Parallel int       // trials run at once (default 1)
	Log      io.Writer // progress lines (may be nil)
}

// Trial is one candidate schedule that was tried.
type Trial struct {
	N               int          `json:"n"` // 1-based run number; 0 if served from cache or skipped
	Phase           string       `json:"phase"`
	Faults          []core.Fault `json:"faults"`
	NoMessageFaults bool         `json:"no_message_faults"`
	Verdict         string       `json:"verdict"` // FAIL, PASS, ..., "cached:<v>", "skipped (budget)"
	Error           string       `json:"error,omitempty"`
	dir             string
}

// Result is what Shrink found.
type Result struct {
	Original        []core.Fault
	Minimal         []core.Fault
	NoMessageFaults bool
	Runs            int    // actual runner invocations
	MinDir          string // <run-dir>-min, a run of the minimal schedule
	Trials          []Trial
}

// Summary renders the one-line result, e.g.
// "minimal: 2 faults (from 9) — partition n1 | n2,n3 at step 410 for 120; crash n3 at step 900 for 80 — 19 runs".
func (r Result) Summary() string {
	descs := make([]string, len(r.Minimal))
	for i, f := range r.Minimal {
		descs[i] = core.DescribeFault(f)
	}
	what := strings.Join(descs, "; ")
	if what == "" {
		what = "no faults needed"
	}
	s := fmt.Sprintf("minimal: %d faults (from %d) — %s — %d runs", len(r.Minimal), len(r.Original), what, r.Runs)
	if r.NoMessageFaults {
		s += " (message faults off)"
	}
	return s
}

// shrinker holds the state of one Shrink call.
type shrinker struct {
	opts  Options
	sc    scenario.Scenario
	seed  int64
	tmp   string
	noMsg bool

	mu     sync.Mutex
	cache  map[string]*Trial
	runs   int
	trials []Trial
}

// Shrink minimizes the fault schedule of the FAIL run in runDir. It writes
// minimal.json and shrink.log into runDir and leaves a run of the minimal
// schedule in <runDir>-min.
func Shrink(ctx context.Context, runDir string, opts Options) (Result, error) {
	if opts.Runner == nil {
		return Result{}, errors.New("shrink: no runner")
	}
	if opts.MaxRuns <= 0 {
		opts.MaxRuns = 60
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 1
	}
	runDir = filepath.Clean(runDir)
	res, err := core.ReadResult(runDir)
	if err != nil {
		return Result{}, fmt.Errorf("shrink: %w", err)
	}
	if res.Verdict != core.VerdictFail {
		return Result{}, fmt.Errorf("shrink: run %s has verdict %s; only FAIL runs can be shrunk", runDir, res.Verdict)
	}
	meta, err := core.ReadMeta(runDir)
	if err != nil {
		return Result{}, fmt.Errorf("shrink: %w", err)
	}
	sc, err := scenario.Load(filepath.Join(runDir, core.ScenarioFile))
	if err != nil {
		return Result{}, fmt.Errorf("shrink: %w", err)
	}
	original, err := core.ReadFaults(filepath.Join(runDir, core.FaultsFile))
	if err != nil {
		return Result{}, fmt.Errorf("shrink: %w", err)
	}
	core.SortFaults(original)
	tmp, err := os.MkdirTemp(filepath.Dir(runDir), ".shrink-")
	if err != nil {
		return Result{}, fmt.Errorf("shrink: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	s := &shrinker{opts: opts, sc: sc, seed: meta.Seed, tmp: tmp, cache: map[string]*Trial{}}
	minimal := s.run(ctx, original)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	out := Result{Original: original, Minimal: minimal, NoMessageFaults: s.noMsg, Runs: s.runs, Trials: s.trials}
	if err := s.writeOutputs(runDir, &out); err != nil {
		return out, err
	}
	return out, nil
}

// run executes the three phases and returns the minimal schedule.
func (s *shrinker) run(ctx context.Context, original []core.Fault) []core.Fault {
	// Phase 1: are message faults needed at all?
	s.noMsg = s.batch(ctx, "messages-off", [][]core.Fault{original}, true)[0]
	// Phase 2: ddmin over the fault list.
	c := DDMin(original, func(cands [][]core.Fault) []bool { return s.batch(ctx, "ddmin", cands, s.noMsg) })
	// Phase 3: halve each fault's duration while it still fails.
	c = append([]core.Fault(nil), c...)
	for i := range c {
		for c[i].DurationSteps > 1 && ctx.Err() == nil {
			cand := append([]core.Fault(nil), c...)
			cand[i].DurationSteps /= 2
			if !s.batch(ctx, "shorten", [][]core.Fault{cand}, s.noMsg)[0] {
				break
			}
			c = cand
		}
	}
	core.SortFaults(c)
	return c
}

// key is the cache key of a candidate: canonical JSON of the sorted faults plus the flag.
func key(faults []core.Fault, noMsg bool) string {
	c := append([]core.Fault{}, faults...)
	core.SortFaults(c)
	b, _ := json.Marshal(struct {
		F []core.Fault
		M bool
	}{c, noMsg})
	return string(b)
}

// batch tries candidates (up to Parallel at once) and reports which still FAIL.
func (s *shrinker) batch(ctx context.Context, phase string, cands [][]core.Fault, noMsg bool) []bool {
	out := make([]bool, len(cands))
	sem := make(chan struct{}, s.opts.Parallel)
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			out[i] = s.try(ctx, phase, c, noMsg)
		}()
	}
	wg.Wait()
	return out
}

// try runs one candidate unless it is cached or over budget.
func (s *shrinker) try(ctx context.Context, phase string, faults []core.Fault, noMsg bool) bool {
	k := key(faults, noMsg)
	s.mu.Lock()
	if t, ok := s.cache[k]; ok {
		s.record(Trial{Phase: phase, Faults: faults, NoMessageFaults: noMsg, Verdict: "cached:" + t.Verdict, dir: t.dir})
		s.mu.Unlock()
		return t.Verdict == core.VerdictFail
	}
	if s.runs >= s.opts.MaxRuns || ctx.Err() != nil {
		s.record(Trial{Phase: phase, Faults: faults, NoMessageFaults: noMsg, Verdict: "skipped (budget)"})
		s.mu.Unlock()
		return false
	}
	s.runs++
	n := s.runs
	s.mu.Unlock()

	t := Trial{N: n, Phase: phase, Faults: faults, NoMessageFaults: noMsg}
	verdict, dir, err := s.execute(ctx, n, faults, noMsg)
	t.Verdict, t.dir = verdict, dir
	if err != nil {
		t.Verdict, t.Error = core.VerdictError, err.Error()
	}
	s.mu.Lock()
	s.cache[k] = &t
	s.record(t)
	s.mu.Unlock()
	return t.Verdict == core.VerdictFail
}

// record appends a trial and logs it (caller holds s.mu).
func (s *shrinker) record(t Trial) {
	s.trials = append(s.trials, t)
	if s.opts.Log != nil {
		_, _ = fmt.Fprintln(s.opts.Log, trialLine(t))
	}
}

func trialLine(t Trial) string {
	id := "  -"
	if t.N > 0 {
		id = fmt.Sprintf("#%-3d", t.N)
	}
	descs := make([]string, len(t.Faults))
	for i, f := range t.Faults {
		descs[i] = core.DescribeFault(f)
	}
	line := fmt.Sprintf("%s %-12s %-18s faults=%d no_msg=%v [%s]", id, t.Phase, t.Verdict, len(t.Faults), t.NoMessageFaults, strings.Join(descs, "; "))
	if t.Error != "" {
		line += " error: " + t.Error
	}
	return line
}

// execute performs one run of the candidate in its own output root.
func (s *shrinker) execute(ctx context.Context, n int, faults []core.Fault, noMsg bool) (string, string, error) {
	root := filepath.Join(s.tmp, fmt.Sprintf("trial-%03d", n))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	path := filepath.Join(root, core.FaultsFile)
	if faults == nil {
		faults = []core.Fault{}
	}
	if err := core.WriteFaults(path, faults); err != nil {
		return "", "", err
	}
	info, err := s.opts.Runner.Run(ctx, s.sc, s.seed, core.RunOptions{OutRoot: root, FaultsFrom: path, NoMessageFaults: noMsg, Quiet: true})
	if err != nil {
		return "", info.Dir, err
	}
	return info.Result.Verdict, info.Dir, nil
}

// writeOutputs writes minimal.json and shrink.log and moves the minimal run to <runDir>-min.
func (s *shrinker) writeOutputs(runDir string, out *Result) error {
	if err := core.WriteFaults(filepath.Join(runDir, MinimalFile), out.Minimal); err != nil {
		return fmt.Errorf("shrink: %w", err)
	}
	err := s.keepMinimal(runDir, out)
	var b strings.Builder
	for _, t := range out.Trials {
		b.WriteString(trialLine(t) + "\n")
	}
	b.WriteString(out.Summary() + "\n")
	if werr := os.WriteFile(filepath.Join(runDir, LogFile), []byte(b.String()), 0o644); werr != nil && err == nil {
		err = fmt.Errorf("shrink: %w", werr)
	}
	return err
}

// keepMinimal moves the run of the minimal schedule to <runDir>-min.
func (s *shrinker) keepMinimal(runDir string, out *Result) error {
	t, ok := s.cache[key(out.Minimal, out.NoMessageFaults)]
	if !ok {
		// Not run on its own yet (e.g. nothing could be removed while message
		// faults stayed on): run it once more so it can be viewed and replayed.
		s.runs++
		out.Runs = s.runs
		v, dir, err := s.execute(context.Background(), s.runs, out.Minimal, out.NoMessageFaults)
		if err != nil {
			return fmt.Errorf("shrink: re-run minimal: %w", err)
		}
		t = &Trial{N: s.runs, Phase: "final", Faults: out.Minimal, NoMessageFaults: out.NoMessageFaults, Verdict: v, dir: dir}
	}
	if t.dir == "" {
		return nil
	}
	minDir := runDir + MinSuffix
	if err := os.RemoveAll(minDir); err != nil {
		return fmt.Errorf("shrink: %w", err)
	}
	if err := os.Rename(t.dir, minDir); err != nil {
		return fmt.Errorf("shrink: keep minimal run: %w", err)
	}
	if err := core.WriteFaults(filepath.Join(minDir, core.FaultsFile), out.Minimal); err != nil {
		return fmt.Errorf("shrink: %w", err)
	}
	if m, err := core.ReadMeta(minDir); err == nil {
		m.FaultsFrom = filepath.Join(runDir, MinimalFile)
		if err := core.WriteMeta(minDir, m); err != nil {
			return fmt.Errorf("shrink: %w", err)
		}
	}
	out.MinDir = minDir
	return nil
}
