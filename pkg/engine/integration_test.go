//go:build integration

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	defer c.Close()
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
	defer c.Close()
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
