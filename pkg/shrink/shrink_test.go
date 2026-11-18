package shrink

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/fakes"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

// fakeFaultsRun runs examples/fake-faults.yaml with the fake-faults runner and returns the run dir.
func fakeFaultsRun(t *testing.T, seed int64) string {
	t.Helper()
	sc, err := scenario.Load(filepath.Join("..", "..", "examples", "fake-faults.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := fakes.FaultsRunner{}.Run(context.Background(), sc, seed, core.RunOptions{OutRoot: t.TempDir(), Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	return info.Dir
}

func TestShrinkFakeFaultsSeed1(t *testing.T) {
	dir := fakeFaultsRun(t, 1)
	var log bytes.Buffer
	res, err := Shrink(context.Background(), dir, Options{Runner: fakes.FaultsRunner{}, MaxRuns: 60, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(res.Summary())
	if len(res.Original) != fakes.PlanSize || len(res.Minimal) != 2 || !fakes.PlanFails(res.Minimal) {
		t.Fatalf("minimal %+v", res.Minimal)
	}
	kinds := []trace.Kind{res.Minimal[0].Kind, res.Minimal[1].Kind}
	if !reflect.DeepEqual(kinds, []trace.Kind{trace.Partition, trace.Crash}) && !reflect.DeepEqual(kinds, []trace.Kind{trace.Crash, trace.Partition}) {
		t.Fatalf("kinds %v", kinds)
	}
	if res.Runs > 60 || !res.NoMessageFaults {
		t.Fatalf("runs %d noMsg %v", res.Runs, res.NoMessageFaults)
	}
	// Phase 3: the fake runner ignores durations, so every duration halves down to 1.
	for _, f := range res.Minimal {
		if f.DurationSteps != 1 {
			t.Fatalf("duration not shortened: %+v", f)
		}
	}
	if !strings.HasPrefix(res.Summary(), "minimal: 2 faults (from 8) — ") {
		t.Fatalf("summary %q", res.Summary())
	}

	disk, err := core.ReadFaults(filepath.Join(dir, MinimalFile))
	if err != nil || !reflect.DeepEqual(disk, res.Minimal) {
		t.Fatalf("minimal.json: %v %+v", err, disk)
	}
	logText, err := os.ReadFile(filepath.Join(dir, LogFile))
	if err != nil || !strings.Contains(string(logText), "ddmin") || !strings.Contains(string(logText), res.Summary()) {
		t.Fatalf("shrink.log: %v\n%s", err, logText)
	}
	if log.Len() == 0 {
		t.Fatal("no progress log")
	}
	minDir := dir + MinSuffix
	if res.MinDir != minDir {
		t.Fatalf("min dir %q", res.MinDir)
	}
	mres, err := core.ReadResult(minDir)
	if err != nil || mres.Verdict != core.VerdictFail {
		t.Fatalf("min run: %v %+v", err, mres)
	}
	mfaults, _ := core.ReadFaults(filepath.Join(minDir, core.FaultsFile))
	if !reflect.DeepEqual(mfaults, res.Minimal) {
		t.Fatal("min run faults differ")
	}
	if m, _ := core.ReadMeta(minDir); m.FaultsFrom != filepath.Join(dir, MinimalFile) {
		t.Fatalf("faults_from %q", m.FaultsFrom)
	}
	// No temp dirs left behind next to the run.
	entries, _ := os.ReadDir(filepath.Dir(dir))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shrink-") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

func TestShrinkParallelSameResult(t *testing.T) {
	a, err := Shrink(context.Background(), fakeFaultsRun(t, 1), Options{Runner: fakes.FaultsRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Shrink(context.Background(), fakeFaultsRun(t, 1), Options{Runner: fakes.FaultsRunner{}, Parallel: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Minimal, b.Minimal) {
		t.Fatalf("serial %+v vs parallel %+v", a.Minimal, b.Minimal)
	}
}

func TestShrinkBudgetAndErrors(t *testing.T) {
	dir := fakeFaultsRun(t, 1)
	res, err := Shrink(context.Background(), dir, Options{Runner: fakes.FaultsRunner{}, MaxRuns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs > 4 || !fakes.PlanFails(res.Minimal) {
		t.Fatalf("budget: runs %d minimal %+v", res.Runs, res.Minimal)
	}
	skipped := 0
	for _, tr := range res.Trials {
		if strings.HasPrefix(tr.Verdict, "skipped") {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("expected skipped trials")
	}

	// Find a seed whose plan passes: shrinking it must be refused.
	for seed := int64(2); seed < 50; seed++ {
		d := fakeFaultsRun(t, seed)
		if r, _ := core.ReadResult(d); r.Verdict == core.VerdictPass {
			if _, err := Shrink(context.Background(), d, Options{Runner: fakes.FaultsRunner{}}); err == nil || !strings.Contains(err.Error(), "only FAIL") {
				t.Fatalf("want refusal, got %v", err)
			}
			break
		}
	}
	if _, err := Shrink(context.Background(), t.TempDir(), Options{Runner: fakes.FaultsRunner{}}); err == nil {
		t.Fatal("empty dir must error")
	}
	if _, err := Shrink(context.Background(), dir, Options{}); err == nil {
		t.Fatal("nil runner must error")
	}
}

func TestShrinkCancelled(t *testing.T) {
	dir := fakeFaultsRun(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Shrink(ctx, dir, Options{Runner: fakes.FaultsRunner{}}); err == nil {
		t.Fatal("want context error")
	}
}
