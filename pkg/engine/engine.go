// Package engine runs one scenario with one seed on Docker: it starts the
// cluster behind the proxy, drives the scheduler and fault plan, runs the
// workload, checks the history and writes the run folder. It registers itself
// as the "docker" runner.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/docker"
	"kurukshetra/pkg/faults"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/netproxy"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/scheduler"
	"kurukshetra/pkg/trace"
)

// RunnerName is the name the engine registers under.
const RunnerName = "docker"

// HealthTimeout is how long nodes get to answer /kuru/health.
var HealthTimeout = 30 * time.Second

// FinalReadTimeout bounds the final reads after healing.
var FinalReadTimeout = 60 * time.Second

func init() {
	core.RegisterRunner(RunnerName, func() (core.Runner, error) { return &Runner{}, nil })
}

// Runner implements core.Runner on Docker.
type Runner struct {
	// Log receives progress lines (default os.Stderr unless opts.Quiet).
	Log io.Writer
}

// run holds the state of one execution.
type run struct {
	sc     scenario.Scenario
	seed   int64
	opts   core.RunOptions
	dir    string
	src    *rng.Source
	plan   []core.Fault
	meta   core.Meta
	start  time.Time
	logw   io.Writer
	logf   *os.File
	timing *timings

	ctl    *docker.Controller
	runID  string
	tw     *trace.Writer
	hr     *history.FileRecorder
	proxy  *netproxy.Proxy
	clock  *scheduler.Clock
	nodes  map[string]docker.Node
	sched  *scheduler.Scheduler
	exec   *faults.Executor
	guide  *scheduler.ReplayGuide
	errMu  sync.Mutex
	errCnt int
}

// Run implements core.Runner.
func (r *Runner) Run(ctx context.Context, sc scenario.Scenario, seed int64, opts core.RunOptions) (info core.RunInfo, err error) {
	sc.ApplyDefaults()
	if err := sc.Validate(); err != nil {
		return core.RunInfo{}, err
	}
	if opts.OutRoot == "" {
		opts.OutRoot = "runs"
	}
	x := &run{sc: sc, seed: seed, opts: opts, src: rng.New(seed), start: time.Now(), timing: newTimings()}
	x.dir = core.RunDir(opts.OutRoot, sc.Name, seed)
	if opts.ReplayFrom != "" {
		if err := x.prepareReplay(); err != nil {
			return core.RunInfo{}, err
		}
	}
	if err := x.prepareDir(r.Log); err != nil {
		return core.RunInfo{}, err
	}
	defer x.logf.Close()

	err = x.execute(ctx)
	info, ferr := x.finish(err)
	if err == nil {
		err = ferr
	}
	return info, err
}

func (x *run) logln(format string, a ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Since(x.start).Round(time.Millisecond), fmt.Sprintf(format, a...))
	if x.logw != nil {
		_, _ = io.WriteString(x.logw, line)
	}
}

// onError records a non-fatal error (usually a delivery to a crashed or paused
// node) in logs/engine.log only; a count is printed at the end of the run.
func (x *run) onError(err error) {
	x.errMu.Lock()
	x.errCnt++
	x.errMu.Unlock()
	if x.logf != nil {
		fmt.Fprintf(x.logf, "[%s] warn: %v\n", time.Since(x.start).Round(time.Millisecond), err)
	}
}

// prepareDir creates a fresh run folder and writes scenario.yaml, meta.json and faults.json.
func (x *run) prepareDir(console io.Writer) error {
	if err := os.RemoveAll(x.dir); err != nil {
		return fmt.Errorf("clear run dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(x.dir, core.LogsDir), 0o755); err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}
	f, err := os.Create(filepath.Join(x.dir, core.LogsDir, "engine.log"))
	if err != nil {
		return err
	}
	x.logf = f
	x.logw = f
	if !x.opts.Quiet {
		if console == nil {
			console = os.Stderr
		}
		x.logw = io.MultiWriter(f, console)
	}
	b, err := yaml.Marshal(x.sc)
	if err != nil {
		return fmt.Errorf("marshal scenario: %w", err)
	}
	if err := os.WriteFile(filepath.Join(x.dir, core.ScenarioFile), b, 0o644); err != nil {
		return fmt.Errorf("write scenario: %w", err)
	}
	x.meta = core.Meta{
		Scenario: x.sc.Name, Seed: x.seed, Nodes: scenario.NodeNames(x.sc.Nodes), Mode: x.sc.Mode,
		Runner: RunnerName, StartedAt: x.start.UTC().Truncate(time.Millisecond), Version: core.Version,
		FaultsFrom: x.opts.FaultsFrom, ReplayOf: x.opts.ReplayFrom,
	}
	if err := core.WriteMeta(x.dir, x.meta); err != nil {
		return err
	}
	switch {
	case x.opts.FaultsFrom != "":
		p, err := core.ReadFaults(x.opts.FaultsFrom)
		if err != nil {
			return fmt.Errorf("read faults: %w", err)
		}
		core.SortFaults(p)
		x.plan = p
	default:
		x.plan = faults.Plan(x.sc, x.src)
	}
	if err := faults.Validate(x.plan, x.meta.Nodes); err != nil {
		x.logln("warn: fault plan breaks a planning rule: %v", err)
	}
	if err := core.WriteFaults(filepath.Join(x.dir, core.FaultsFile), x.plan); err != nil {
		return fmt.Errorf("write faults: %w", err)
	}
	x.logln("run %s seed %d -> %s (%d faults)", x.sc.Name, x.seed, x.dir, len(x.plan))
	return nil
}

// execute runs the cluster. Cleanup of Docker objects is deferred here with a
// fresh context so it also happens on errors and Ctrl-C.
func (x *run) execute(ctx context.Context) (err error) {
	var werr error
	wl, err := core.NewWorkload(x.sc.Workload)
	if err != nil {
		return fmt.Errorf("workload: %w", err)
	}
	if x.tw, err = trace.NewWriter(filepath.Join(x.dir, core.TraceFile)); err != nil {
		return err
	}
	if x.hr, err = history.NewFileRecorder(filepath.Join(x.dir, core.HistoryFile)); err != nil {
		return err
	}
	x.clock = scheduler.NewClock()
	x.tw.Append(trace.Event{Step: 0, Kind: trace.RunStart, Meta: map[string]string{
		"seed": fmt.Sprint(x.seed), "scenario": x.sc.Name, "mode": x.sc.Mode}})

	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if cerr := x.teardown(cctx); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if err := x.startCluster(ctx); err != nil {
		return err
	}
	cluster := x.cluster()

	x.timing.begin("faults_and_workload")
	x.exec = faults.NewExecutor(x.proxy, x.ctl, x.nodeRefs(), x.proxy.Emit)
	x.sched = scheduler.New(x.sc, x.src, x.proxy, x.tw, x.exec, x.plan, scheduler.Options{
		NoMessageFaults: x.opts.NoMessageFaults, Replay: x.guide, Clock: x.clock, OnError: x.onError,
	})
	wctx, wcancel := context.WithCancel(ctx)
	var wwg sync.WaitGroup
	wwg.Add(1)
	go func() {
		defer wwg.Done()
		werr = wl.Run(wctx, cluster, x.src.Stream(rng.StreamWorkload), x.sched, x.hr)
	}()
	x.logln("running %d steps (%d ms/step)", x.sc.DurationSteps, x.sc.StepMs)
	serr := x.sched.Run(ctx, x.sc.DurationSteps)
	wcancel()
	wwg.Wait()
	if serr != nil {
		return fmt.Errorf("scheduler: %w", serr)
	}
	if werr != nil && !errors.Is(werr, context.Canceled) {
		x.logln("warn: workload: %v", werr)
	}

	x.timing.begin("heal_and_quiet")
	x.logln("healing all faults, quiet for %d steps", x.sc.QuietSteps)
	if err := x.exec.HealAll(ctx); err != nil {
		x.onError(fmt.Errorf("heal all: %w", err))
	}
	if err := x.sched.Drain(ctx, x.sc.QuietSteps); err != nil {
		return fmt.Errorf("quiet period: %w", err)
	}

	x.timing.begin("final_reads")
	if err := x.finalReads(ctx, wl, cluster); err != nil {
		return err
	}
	x.tw.Append(trace.Event{Step: x.clock.Step(), Kind: trace.RunEnd})
	x.meta.Steps = x.clock.Step()
	st := x.sched.Stats()
	x.logln("messages: %d delivered, %d dropped, %d duplicated, %d delayed, %d cut by partitions",
		st.Delivered, st.Dropped, st.Duplicated, st.Delayed, st.Partitioned)
	x.errMu.Lock()
	if x.errCnt > 0 {
		x.logln("%d delivery/fault errors (expected around crashes and pauses; see logs/engine.log)", x.errCnt)
	}
	x.errMu.Unlock()
	return nil
}

// finalReads keeps the clock ticking (quiet: deliver everything) while the
// workload reads every key from every node.
func (x *run) finalReads(ctx context.Context, wl core.Workload, cluster core.Cluster) error {
	fctx, cancel := context.WithTimeout(ctx, FinalReadTimeout)
	defer cancel()
	dctx, dcancel := context.WithCancel(fctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = x.sched.Drain(dctx, 1<<40)
	}()
	err := wl.FinalReads(fctx, cluster, x.sched, x.hr)
	dcancel()
	<-done
	if ctx.Err() != nil {
		return fmt.Errorf("final reads: %w", ctx.Err())
	}
	if err != nil {
		x.logln("warn: final reads: %v", err)
	}
	return nil
}

// cluster returns the client view of the nodes, sorted by name.
func (x *run) cluster() core.Cluster {
	var cl core.Cluster
	for _, name := range x.meta.Nodes {
		cl.Nodes = append(cl.Nodes, core.Node{Name: name, ClientURL: x.nodes[name].ClientURL})
	}
	return cl
}

func (x *run) nodeRefs() map[string]faults.NodeRef {
	out := map[string]faults.NodeRef{}
	for name, n := range x.nodes {
		out[name] = faults.NodeRef{ContainerID: n.ID, ClientURL: n.ClientURL}
	}
	return out
}

// NodeEnv returns the environment of one node: the scenario's extra env, then
// the protocol variables (which always win).
func NodeEnv(sc scenario.Scenario, node, peers string) map[string]string {
	env := map[string]string{}
	for k, v := range sc.Env {
		env[k] = v
	}
	env["NODE_ID"] = node
	env["PEER_LISTEN"] = fmt.Sprintf(":%d", sc.PeerPort)
	env["CLIENT_LISTEN"] = fmt.Sprintf(":%d", sc.ClientPort)
	env["PEERS"] = peers
	env["DATA_DIR"] = "/data"
	env["KURU_CLOCK_OFFSET_MS"] = "0"
	return env
}

func (x *run) startCluster(ctx context.Context) error {
	x.timing.begin("docker_setup")
	ctl, err := docker.New()
	if err != nil {
		return err
	}
	x.ctl = ctl
	if err := ctl.Ping(ctx); err != nil {
		return err
	}
	if err := ctl.EnsureImage(ctx, x.sc.Image); err != nil {
		return err
	}
	x.runID = docker.NewRunID(x.sc.Name, x.seed)
	netID, err := ctl.CreateNetwork(ctx, "kuru-"+x.runID, docker.Labels(x.runID, ""))
	if err != nil {
		return err
	}
	var opts []netproxy.Option
	opts = append(opts, netproxy.WithMode(x.sc.Mode))
	x.proxy, err = netproxy.New(x.meta.Nodes, nil, x.clock, x.tw, opts...)
	if err != nil {
		return err
	}

	x.timing.begin("start_containers")
	x.logln("starting %d x %s (run %s)", x.sc.Nodes, x.sc.Image, x.runID)
	x.nodes = map[string]docker.Node{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, len(x.meta.Nodes))
	for i, name := range x.meta.Nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := ctl.Start(ctx, docker.NodeSpec{
				RunID: x.runID, Name: name, Image: x.sc.Image, Network: netID,
				Env:      NodeEnv(x.sc, name, x.proxy.PeerEnv(name)),
				PeerPort: x.sc.PeerPort, ClientPort: x.sc.ClientPort,
				Volume: "kuru-" + x.runID + "-" + name,
			})
			if err != nil {
				errs[i] = fmt.Errorf("start node %s: %w", name, err)
				return
			}
			mu.Lock()
			x.nodes[name] = n
			mu.Unlock()
			x.proxy.SetTarget(name, n.PeerHostAddr)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}

	// Until the workload starts, peer traffic passes straight through at step 0
	// so nodes can elect a leader and become healthy.
	x.timing.begin("health")
	pctx, pcancel := context.WithCancel(ctx)
	pdone := make(chan struct{})
	go func() { defer close(pdone); netproxy.PassThrough(pctx, x.proxy) }()
	herr := x.waitHealthy(ctx)
	pcancel()
	<-pdone
	if herr != nil {
		return herr
	}
	x.logln("cluster healthy")
	return nil
}

func (x *run) waitHealthy(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make([]error, len(x.meta.Nodes))
	for i, name := range x.meta.Nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := docker.WaitHealthy(ctx, x.nodes[name].ClientURL, HealthTimeout); err != nil {
				errs[i] = fmt.Errorf("node %s not healthy after %s: %w", name, HealthTimeout, err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// teardown closes the proxy and writers, copies container logs and removes
// containers (unless Keep). It is safe after partial setup.
func (x *run) teardown(ctx context.Context) error {
	x.timing.begin("teardown")
	var errs []error
	if x.sched != nil {
		x.sched.Wait()
	}
	if x.proxy != nil {
		errs = append(errs, x.proxy.Close())
	}
	if x.tw != nil {
		errs = append(errs, x.tw.Close())
	}
	if x.hr != nil {
		errs = append(errs, x.hr.Close())
	}
	if x.ctl != nil {
		names := make([]string, 0, len(x.nodes))
		for n := range x.nodes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			f, err := os.Create(filepath.Join(x.dir, core.LogsDir, name+".log"))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if err := x.ctl.CopyLogs(ctx, x.nodes[name].ID, f); err != nil {
				x.logln("warn: %v", err)
			}
			f.Close()
		}
		if x.opts.Keep {
			x.logln("keeping containers of run %s (remove with `kuru cleanup`)", x.runID)
		} else if x.runID != "" {
			if err := x.ctl.CleanupRun(ctx, x.runID); err != nil {
				errs = append(errs, fmt.Errorf("cleanup run %s: %w", x.runID, err))
			}
		}
		x.ctl.Close()
	}
	return errors.Join(errs...)
}

// finish checks the history, writes result.json and the final meta.json.
func (x *run) finish(runErr error) (core.RunInfo, error) {
	x.timing.begin("check")
	info := core.RunInfo{Dir: x.dir, Faults: x.plan}
	var res core.Result
	if runErr != nil {
		x.logln("error: %v", runErr)
		res = core.Result{Seed: x.seed, Scenario: x.sc.Name, Verdict: core.VerdictError, Error: runErr.Error(), Checks: []core.CheckResult{}}
		if err := core.WriteResult(x.dir, res); err != nil {
			x.logln("warn: write result: %v", err)
		}
	} else {
		r, err := core.CheckRun(x.dir, x.sc.Checkers)
		if err != nil {
			res = core.Result{Seed: x.seed, Scenario: x.sc.Name, Verdict: core.VerdictError, Error: "check: " + err.Error(), Checks: []core.CheckResult{}}
			_ = core.WriteResult(x.dir, res)
			runErr = err
		} else {
			res = r
		}
	}
	if x.guide != nil && runErr == nil {
		if fr, err := x.scoreReplay(); err != nil {
			x.logln("warn: fidelity: %v", err)
		} else {
			res.Fidelity = &fr
			_ = core.WriteResult(x.dir, res)
		}
	}
	x.timing.end()
	x.meta.DurationMs = time.Since(x.start).Milliseconds()
	if x.clock != nil {
		x.meta.Steps = x.clock.Step()
	}
	if err := core.WriteMeta(x.dir, x.meta); err != nil && runErr == nil {
		runErr = err
	}
	_ = x.timing.write(filepath.Join(x.dir, core.LogsDir, "engine-timings.json"))
	x.logln("verdict %s", res.Verdict)
	info.Result, info.Meta = res, x.meta
	return info, runErr
}
