// Package explore runs one scenario over many seeds in parallel, keeps the
// failing runs, and writes a summary CSV.
package explore

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/scenario"
)

// SummaryFile is written under <out-root>/<scenario>/.
const SummaryFile = "summary.csv"

// CSVHeader is the header row of summary.csv.
var CSVHeader = []string{"seed", "verdict", "checks", "faults", "steps", "duration_ms", "dir"}

// Options tunes an exploration.
type Options struct {
	Runs            int   // number of seeds
	Parallel        int   // concurrent runs (default 4)
	StartSeed       int64 // first seed (default 1)
	Runner          string
	KeepPassing     bool // keep run dirs of passing runs
	StopAfterFails  int  // stop handing out seeds after this many FAILs (0 = never)
	NoMessageFaults bool
	OutRoot         string    // default "runs"
	Progress        io.Writer // live progress line (nil = none)
	ProgressEvery   time.Duration
}

// Outcome is the result of one seed.
type Outcome struct {
	Seed        int64
	Verdict     string
	Checks      string
	Faults      int
	Steps       int64
	DurationMs  int64
	Dir         string // empty if the passing dir was deleted
	Err         error
	Interrupted bool
}

// Summary describes a finished exploration.
type Summary struct {
	Total, Done                int
	Pass, Fail, Error, Unknown int
	Interrupted                int
	Failing                    []Outcome // FAIL and ERROR, by seed
	CSVPath                    string
	Elapsed                    time.Duration
	StoppedEarly               bool
	Runner                     string
	Scenario                   string
}

// Seeds returns the deterministic seed list S..S+N-1.
func Seeds(start int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = start + int64(i)
	}
	return out
}

// ChecksString renders "name:VERDICT,..." for the CSV.
func ChecksString(cs []core.CheckResult) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.Checker+":"+c.Verdict)
	}
	return strings.Join(parts, ";")
}

// Run explores sc over opts.Runs seeds.
func Run(ctx context.Context, sc scenario.Scenario, opts Options) (Summary, error) {
	if opts.Runs <= 0 {
		return Summary{}, errors.New("explore: --runs must be > 0")
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 4
	}
	if opts.StartSeed == 0 {
		opts.StartSeed = 1
	}
	if opts.OutRoot == "" {
		opts.OutRoot = "runs"
	}
	if opts.ProgressEvery <= 0 {
		opts.ProgressEvery = time.Second
	}
	sum := Summary{Total: opts.Runs, Runner: opts.Runner, Scenario: sc.Name}
	csvDir := filepath.Join(opts.OutRoot, sc.Name)
	if err := os.MkdirAll(csvDir, 0o755); err != nil {
		return sum, err
	}
	sum.CSVPath = filepath.Join(csvDir, SummaryFile)
	cw, err := openCSV(sum.CSVPath)
	if err != nil {
		return sum, err
	}

	// Seeds are handed out from a channel; feeding stops on Ctrl-C or after K fails.
	feedCtx, stopFeeding := context.WithCancel(ctx)
	defer stopFeeding()
	seeds := make(chan int64)
	go func() {
		defer close(seeds)
		for _, s := range Seeds(opts.StartSeed, opts.Runs) {
			select {
			case seeds <- s:
			case <-feedCtx.Done():
				return
			}
		}
	}()

	runners := make([]core.Runner, opts.Parallel)
	for i := range runners {
		if runners[i], err = core.NewRunner(opts.Runner); err != nil {
			_ = cw.close()
			return sum, err
		}
	}
	// A worker waits for its result to be counted before taking another seed,
	// so --stop-after-fails stops exactly.
	type result struct {
		o   Outcome
		ack chan struct{}
	}
	results := make(chan result)
	var workers sync.WaitGroup
	var werr error
	for _, r := range runners {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for seed := range seeds {
				if feedCtx.Err() != nil { // stopped while this seed was in hand
					continue
				}
				ack := make(chan struct{})
				results <- result{runOne(ctx, r, sc, seed, opts), ack}
				<-ack
			}
		}()
	}
	go func() { workers.Wait(); close(results) }()

	start := time.Now()
	tick := time.NewTicker(opts.ProgressEvery)
	defer tick.Stop()
	progress := func(final bool) {
		if opts.Progress == nil {
			return
		}
		el := time.Since(start)
		rate := 0.0
		if el > 0 {
			rate = float64(sum.Done) / el.Minutes()
		}
		eta := "-"
		if sum.Done > 0 && sum.Done < sum.Total {
			left := time.Duration(float64(el) / float64(sum.Done) * float64(sum.Total-sum.Done))
			eta = left.Round(time.Second).String()
		}
		end := "\r"
		if final {
			end = "\n"
		}
		_, _ = fmt.Fprintf(opts.Progress, "done %d/%d fail %d error %d rate %.0f/min eta %s   %s",
			sum.Done, sum.Total, sum.Fail, sum.Error, rate, eta, end)
	}
	for {
		select {
		case res, ok := <-results:
			if !ok {
				progress(true)
				sum.Elapsed = time.Since(start)
				sort.Slice(sum.Failing, func(i, j int) bool { return sum.Failing[i].Seed < sum.Failing[j].Seed })
				if err := cw.close(); err != nil && werr == nil {
					werr = err
				}
				if ctx.Err() != nil {
					sum.StoppedEarly = true
				}
				return sum, werr
			}
			o := res.o
			if o.Interrupted {
				sum.Interrupted++
				close(res.ack)
				continue
			}
			sum.Done++
			switch o.Verdict {
			case core.VerdictPass:
				sum.Pass++
			case core.VerdictFail:
				sum.Fail++
				sum.Failing = append(sum.Failing, o)
			case core.VerdictError:
				sum.Error++
				sum.Failing = append(sum.Failing, o)
			default:
				sum.Unknown++
			}
			if err := cw.write(o); err != nil && werr == nil {
				werr = err
			}
			if opts.StopAfterFails > 0 && sum.Fail >= opts.StopAfterFails && !sum.StoppedEarly {
				sum.StoppedEarly = true
				stopFeeding()
			}
			close(res.ack)
		case <-tick.C:
			progress(false)
		}
	}
}

func runOne(ctx context.Context, r core.Runner, sc scenario.Scenario, seed int64, opts Options) Outcome {
	t0 := time.Now()
	info, err := r.Run(ctx, sc, seed, core.RunOptions{OutRoot: opts.OutRoot, NoMessageFaults: opts.NoMessageFaults, Quiet: true})
	o := Outcome{Seed: seed, Verdict: info.Result.Verdict, Checks: ChecksString(info.Result.Checks), Faults: len(info.Faults),
		Steps: info.Meta.Steps, DurationMs: time.Since(t0).Milliseconds(), Dir: info.Dir, Err: err}
	if o.Verdict == "" {
		o.Verdict = core.VerdictError
	}
	if ctx.Err() != nil && err != nil {
		// Interrupted by Ctrl-C: the runner cleaned up; the run is not a result.
		o.Interrupted = true
		if info.Dir != "" {
			_ = os.RemoveAll(info.Dir)
		}
		return o
	}
	if o.Verdict == core.VerdictPass && !opts.KeepPassing && info.Dir != "" {
		if os.RemoveAll(info.Dir) == nil {
			o.Dir = ""
		}
	}
	return o
}

// csvWriter appends rows to summary.csv from a single goroutine (Run's loop).
type csvWriter struct {
	f *os.File
	w *csv.Writer
}

func openCSV(path string) (*csvWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	cw := &csvWriter{f: f, w: csv.NewWriter(f)}
	if st.Size() == 0 {
		if err := cw.w.Write(CSVHeader); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return cw, nil
}

func (c *csvWriter) write(o Outcome) error {
	if err := c.w.Write([]string{
		strconv.FormatInt(o.Seed, 10), o.Verdict, o.Checks, strconv.Itoa(o.Faults),
		strconv.FormatInt(o.Steps, 10), strconv.FormatInt(o.DurationMs, 10), o.Dir,
	}); err != nil {
		return err
	}
	c.w.Flush()
	return c.w.Error()
}

func (c *csvWriter) close() error {
	c.w.Flush()
	return errors.Join(c.w.Error(), c.f.Close())
}

// Report renders the end-of-exploration summary with the next commands to try.
func Report(s Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d/%d runs in %s — %d pass, %d fail, %d error, %d unknown",
		s.Scenario, s.Done, s.Total, s.Elapsed.Round(time.Millisecond), s.Pass, s.Fail, s.Error, s.Unknown)
	if s.Interrupted > 0 {
		fmt.Fprintf(&b, ", %d interrupted", s.Interrupted)
	}
	if s.StoppedEarly {
		b.WriteString(" (stopped early)")
	}
	fmt.Fprintf(&b, "\nsummary: %s\n", s.CSVPath)
	if len(s.Failing) == 0 {
		b.WriteString("no failing seeds\n")
		return b.String()
	}
	var seeds []string
	for i, o := range s.Failing {
		if i == 20 {
			seeds = append(seeds, fmt.Sprintf("... (%d more in %s)", len(s.Failing)-20, SummaryFile))
			break
		}
		seeds = append(seeds, fmt.Sprintf("%d(%s)", o.Seed, o.Verdict))
	}
	fmt.Fprintf(&b, "failing seeds: %s\n", strings.Join(seeds, " "))
	shown := 0
	for _, o := range s.Failing {
		if o.Verdict != core.VerdictFail || o.Dir == "" {
			continue
		}
		if shown == 0 {
			b.WriteString("next:\n")
		}
		if shown == 3 {
			fmt.Fprintf(&b, "  ... and %d more\n", s.Fail-3)
			break
		}
		fmt.Fprintf(&b, "  kuru shrink %s && kuru replay %s\n", o.Dir, o.Dir)
		shown++
	}
	for _, o := range s.Failing {
		if o.Verdict == core.VerdictError && o.Dir != "" {
			fmt.Fprintf(&b, "errors: see %s\n", filepath.Join(o.Dir, core.LogsDir, "engine.log"))
			break
		}
	}
	return b.String()
}
