//go:build integration

package engine

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/docker"
	_ "kurukshetra/pkg/fakes"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

func needDocker(t *testing.T) {
	t.Helper()
	c, err := docker.New()
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skip(err)
	}
	if err := c.EnsureFakeNodeImage(ctx); err != nil {
		t.Skip(err)
	}
}

func loadSmoke(t *testing.T) scenario.Scenario {
	sc, err := scenario.Load(filepath.Join("..", "..", "examples", "fake-smoke.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// assertNoLeaks checks that nothing of runs "<scenario>-<seed>-*" is left.
// It only looks at this test's runs: other packages' tests run in parallel.
func assertNoLeaks(t *testing.T, scenario string, seed int64) {
	t.Helper()
	c, _ := docker.New()
	defer func() { _ = c.Close() }()
	left, err := c.ListRunPrefix(context.Background(), fmt.Sprintf("%s-%d-", scenario, seed))
	if err != nil || len(left) != 0 {
		t.Fatalf("leaked docker objects: %v %v", left, err)
	}
}

func TestRunFakeSmoke(t *testing.T) {
	needDocker(t)
	root := t.TempDir()
	info, err := (&Runner{}).Run(context.Background(), loadSmoke(t), 1, core.RunOptions{OutRoot: root, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.Result.Verdict != core.VerdictPass {
		t.Fatalf("verdict %+v", info.Result)
	}
	for _, f := range []string{core.TraceFile, core.HistoryFile, core.ResultFile, core.FaultsFile, core.MetaFile, core.ScenarioFile,
		"logs/n1.log", "logs/n2.log", "logs/n3.log", "logs/engine.log", "logs/engine-timings.json"} {
		if _, err := os.Stat(filepath.Join(info.Dir, f)); err != nil {
			t.Error(err)
		}
	}
	evs, err := trace.ReadFile(filepath.Join(info.Dir, core.TraceFile))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[trace.Kind]int{}
	for _, e := range evs {
		seen[e.Kind]++
	}
	for _, k := range []trace.Kind{trace.RunStart, trace.RunEnd, trace.MsgSend, trace.MsgDeliver, trace.MsgDrop,
		trace.Partition, trace.Heal, trace.Crash, trace.Restart} {
		if seen[k] == 0 {
			t.Errorf("no %s in trace: %v", k, seen)
		}
	}
	ops, err := history.ReadFile(filepath.Join(info.Dir, core.HistoryFile))
	if err != nil || len(ops) == 0 {
		t.Fatal("empty history", err)
	}
	if _, err := history.Pairs(ops); err != nil {
		t.Fatal(err)
	}
	if info.Meta.Steps < 1500 || info.Meta.Runner != RunnerName {
		t.Fatalf("meta %+v", info.Meta)
	}
	assertNoLeaks(t, "fake-smoke", 1)
}

func TestRunCancelCleansUp(t *testing.T) {
	needDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	info, err := (&Runner{}).Run(ctx, loadSmoke(t), 2, core.RunOptions{OutRoot: t.TempDir(), Quiet: true})
	if err == nil || info.Result.Verdict != core.VerdictError {
		t.Fatalf("want ERROR, got %v %+v", err, info.Result)
	}
	assertNoLeaks(t, "fake-smoke", 2)
}

func TestRunMissingImage(t *testing.T) {
	needDocker(t)
	sc := loadSmoke(t)
	sc.Image = "kuru/does-not-exist"
	info, err := (&Runner{}).Run(context.Background(), sc, 1, core.RunOptions{OutRoot: t.TempDir(), Quiet: true})
	if err == nil || info.Result.Verdict != core.VerdictError {
		t.Fatal("want error")
	}
	r, _ := core.ReadResult(info.Dir)
	if r.Verdict != core.VerdictError || r.Error == "" {
		t.Fatalf("%+v", r)
	}
	assertNoLeaks(t, "fake-smoke", 1)
}

func TestReplayFidelity(t *testing.T) {
	needDocker(t)
	root := t.TempDir()
	sc := loadSmoke(t)
	orig, err := (&Runner{}).Run(context.Background(), sc, 3, core.RunOptions{OutRoot: root, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := (&Runner{}).Run(context.Background(), sc, 3, core.RunOptions{ReplayFrom: orig.Dir, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dir != orig.Dir+"-replay-1" || rep.Meta.ReplayOf != orig.Dir || rep.Result.Fidelity == nil {
		t.Fatalf("%+v", rep)
	}
	if *rep.Result.Fidelity < 0.95 {
		t.Fatalf("fidelity %.3f < 0.95", *rep.Result.Fidelity)
	}
	if _, err := os.Stat(filepath.Join(rep.Dir, "logs", "replay-fidelity.json")); err != nil {
		t.Fatal(err)
	}
	assertNoLeaks(t, "fake-smoke", 3)
}

func TestRunTCPMode(t *testing.T) {
	needDocker(t)
	sc, err := scenario.Load(filepath.Join("..", "..", "examples", "engine-tcp-smoke.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := (&Runner{}).Run(context.Background(), sc, 1, core.RunOptions{OutRoot: t.TempDir(), Quiet: true})
	if err != nil || info.Result.Verdict != core.VerdictPass {
		t.Fatalf("%v %+v", err, info.Result)
	}
	evs, _ := trace.ReadFile(filepath.Join(info.Dir, core.TraceFile))
	var tcpSends, replies int
	for _, e := range evs {
		if e.Kind == trace.MsgSend && e.Meta["mode"] == "tcp" && e.Meta["dir"] == "" {
			tcpSends++
		}
		if e.Kind == trace.MsgDeliver && e.Meta["dir"] == "reply" {
			replies++
		}
	}
	if tcpSends < 100 || replies < 100 {
		t.Fatalf("tcp sends %d replies %d", tcpSends, replies)
	}
	assertNoLeaks(t, "engine-tcp-smoke", 1)
}

// TestNoGoroutineLeaks runs the engine three times and checks the goroutine
// count returns to its starting level (idle HTTP connections are closed).
func TestNoGoroutineLeaks(t *testing.T) {
	needDocker(t)
	sc := loadSmoke(t)
	sc.DurationSteps, sc.QuietSteps = 300, 100
	runtime.GC()
	before := runtime.NumGoroutine()
	for seed := int64(20); seed < 23; seed++ {
		if _, err := (&Runner{}).Run(context.Background(), sc, seed, core.RunOptions{OutRoot: t.TempDir(), Quiet: true}); err != nil {
			t.Fatal(err)
		}
	}
	var after int
	for i := 0; i < 50; i++ {
		http.DefaultClient.CloseIdleConnections()
		runtime.GC()
		if after = runtime.NumGoroutine(); after <= before+2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	t.Fatalf("goroutines %d -> %d after 3 runs\n%s", before, after, buf[:n])
}
