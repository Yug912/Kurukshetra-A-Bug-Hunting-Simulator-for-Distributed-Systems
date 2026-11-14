package main

import (
	"testing"

	"kurukshetra/pkg/core"
)

func TestSummaryLineAndExitCode(t *testing.T) {
	f := 0.97
	info := core.RunInfo{Dir: "runs/x/42", Faults: make([]core.Fault, 7), Result: core.Result{Verdict: "FAIL",
		Checks: []core.CheckResult{{Checker: "linearizable-kv", Verdict: "FAIL"}}, Fidelity: &f}}
	if got := SummaryLine(42, info); got != "seed=42 verdict=FAIL checks=linearizable-kv:FAIL faults=7 dir=runs/x/42 fidelity=97.0%" {
		t.Fatal(got)
	}
	for v, want := range map[string]int{"PASS": 0, "FAIL": 1, "ERROR": 2, "UNKNOWN": 2, "": 2} {
		if ExitCode(v) != want {
			t.Errorf("%s -> %d", v, ExitCode(v))
		}
	}
	if randomSeed() < 0 {
		t.Fatal("negative seed")
	}
}
