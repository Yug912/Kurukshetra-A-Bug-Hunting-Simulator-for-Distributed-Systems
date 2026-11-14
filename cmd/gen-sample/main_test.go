package main

import (
	"os"
	"path/filepath"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

// TestGenerateMatchesCommitted regenerates the sample and compares it byte for byte
// with testdata/sample-run, and checks the story's invariants.
func TestGenerateMatchesCommitted(t *testing.T) {
	dir := t.TempDir()
	if err := run(dir); err != nil {
		t.Fatal(err)
	}
	committed := filepath.Join("..", "..", "testdata", "sample-run")
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		a, _ := os.ReadFile(p)
		b, rerr := os.ReadFile(filepath.Join(committed, rel))
		if rerr != nil || string(a) != string(b) {
			t.Errorf("%s differs from committed sample (run make sample)", rel)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	evs, err := trace.ReadFile(filepath.Join(dir, core.TraceFile))
	if err != nil || len(evs) < 50 || len(evs) > 70 {
		t.Fatalf("trace: %d events, %v", len(evs), err)
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Step < evs[i-1].Step || evs[i].WallNs <= evs[i-1].WallNs {
			t.Fatalf("event %d out of order", i)
		}
	}
	ops, err := history.ReadFile(filepath.Join(dir, core.HistoryFile))
	if err != nil || len(ops) != 14 || ops[5].Value != "c0-1" {
		t.Fatalf("history: %v %+v", err, ops)
	}
	if _, err := history.Pairs(ops); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(ops); i++ {
		if ops[i].WallNs <= ops[i-1].WallNs {
			t.Fatalf("op %d wall order", i)
		}
	}
	if _, err := scenario.Load(filepath.Join(dir, core.ScenarioFile)); err != nil {
		t.Fatal(err)
	}
}
