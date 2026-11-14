package explore

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kurukshetra/pkg/core"
	_ "kurukshetra/pkg/fakes"
	"kurukshetra/pkg/scenario"
)

func fakeScenario(t *testing.T) scenario.Scenario {
	sc, err := scenario.Load(filepath.Join("..", "..", "examples", "fake-faults.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func readCSV(t *testing.T, path string) [][]string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestSeeds(t *testing.T) {
	if !reflect.DeepEqual(Seeds(5, 3), []int64{5, 6, 7}) {
		t.Fatal(Seeds(5, 3))
	}
}

func TestExploreFakeFaults(t *testing.T) {
	root := t.TempDir()
	sum, err := Run(context.Background(), fakeScenario(t), Options{Runs: 200, Parallel: 8, Runner: "fake-faults", OutRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Done != 200 || sum.Pass+sum.Fail != 200 || sum.Fail == 0 || sum.Pass == 0 {
		t.Fatalf("%+v", sum)
	}
	rows := readCSV(t, sum.CSVPath)
	if !reflect.DeepEqual(rows[0], CSVHeader) || len(rows) != 201 {
		t.Fatalf("csv rows %d header %v", len(rows), rows[0])
	}
	seen := map[string]bool{}
	for _, r := range rows[1:] {
		seen[r[0]] = true
		_, statErr := os.Stat(filepath.Join(root, "fake-faults", r[0]))
		switch r[1] {
		case "PASS":
			if r[6] != "" || statErr == nil {
				t.Fatalf("passing run %s kept: %v", r[0], r)
			}
		case "FAIL":
			if r[6] == "" || statErr != nil {
				t.Fatalf("failing run %s not kept", r[0])
			}
		}
	}
	if len(seen) != 200 || !seen["1"] || !seen["200"] {
		t.Fatal("seeds missing")
	}
	if sum.Failing[0].Seed != 1 { // seed 1 of fake-faults always FAILs
		t.Fatalf("%+v", sum.Failing[0])
	}
	rep := Report(sum)
	if !strings.Contains(rep, "kuru shrink") || !strings.Contains(rep, "failing seeds: 1(FAIL)") {
		t.Fatal(rep)
	}
	// Appending a second exploration keeps one header.
	if _, err := Run(context.Background(), fakeScenario(t), Options{Runs: 5, StartSeed: 1000, Runner: "fake-faults", OutRoot: root, KeepPassing: true}); err != nil {
		t.Fatal(err)
	}
	rows = readCSV(t, sum.CSVPath)
	if len(rows) != 206 || rows[201][0] == "seed" {
		t.Fatal("append broke the csv")
	}
}

func TestStopAfterFails(t *testing.T) {
	sum, err := Run(context.Background(), fakeScenario(t), Options{Runs: 500, Parallel: 1, Runner: "fake-faults", OutRoot: t.TempDir(), StopAfterFails: 3})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Fail != 3 || !sum.StoppedEarly || sum.Done >= 500 {
		t.Fatalf("%+v", sum)
	}
}

// slowRunner takes 50 ms per run and honours ctx like the engine does.
type slowRunner struct{ started *atomic.Int32 }

func (s slowRunner) Run(ctx context.Context, sc scenario.Scenario, seed int64, o core.RunOptions) (core.RunInfo, error) {
	s.started.Add(1)
	select {
	case <-ctx.Done():
		return core.RunInfo{Result: core.Result{Verdict: core.VerdictError}}, ctx.Err()
	case <-time.After(50 * time.Millisecond):
		return core.RunInfo{Result: core.Result{Verdict: core.VerdictPass}}, nil
	}
}

var started atomic.Int32

func init() {
	core.RegisterRunner("test-slow", func() (core.Runner, error) { return slowRunner{&started}, nil })
}

func TestCancelStopsHandingOutSeeds(t *testing.T) {
	started.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	var progress strings.Builder
	sum, err := Run(ctx, fakeScenario(t), Options{Runs: 1000, Parallel: 2, Runner: "test-slow", OutRoot: t.TempDir(),
		Progress: &progress, ProgressEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.StoppedEarly || sum.Done > 10 || sum.Interrupted == 0 || int(started.Load()) > 12 {
		t.Fatalf("%+v started=%d", sum, started.Load())
	}
	if !strings.Contains(progress.String(), "done ") || !strings.Contains(progress.String(), "rate") {
		t.Fatal(progress.String())
	}
}

func TestUnknownRunner(t *testing.T) {
	if _, err := Run(context.Background(), fakeScenario(t), Options{Runs: 1, Runner: "nope", OutRoot: t.TempDir()}); err == nil {
		t.Fatal("want error")
	}
}
