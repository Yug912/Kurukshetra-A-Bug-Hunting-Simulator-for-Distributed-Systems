package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExportRunsWritesStaticAPI(t *testing.T) {
	runs, _ := serveFixture(t)
	out := t.TempDir()
	got, err := exportRuns(runs, out, []string{"sample/42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "sample/42" {
		t.Fatalf("exported %+v", got)
	}
	var list []RunEntry
	b, err := os.ReadFile(filepath.Join(out, "runs.json"))
	must(t, err)
	must(t, json.Unmarshal(b, &list))
	if len(list) != 1 || list[0].Verdict != "FAIL" {
		t.Fatalf("runs.json %+v", list)
	}
	for _, f := range []string{"trace.jsonl", "history.jsonl", "result.json", "meta.json"} {
		if _, err := os.Stat(filepath.Join(out, "runs", "sample", "42", f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	// The fixture's minimal.json is a symlink to a secret outside the runs dir: never copied.
	if _, err := os.Stat(filepath.Join(out, "runs", "sample", "42", "minimal.json")); !os.IsNotExist(err) {
		t.Fatalf("symlinked minimal.json was exported (err=%v)", err)
	}
	if _, err := exportRuns(runs, out, []string{"nope/1"}); err == nil {
		t.Fatal("unknown run ID must fail")
	}
}
