package fakes

import (
	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
)

type passChecker struct{}

// Name implements core.Checker.
func (passChecker) Name() string { return "fake-pass" }

// Check always passes.
func (passChecker) Check(ops []history.Op, runDir string) core.CheckResult {
	return core.CheckResult{Checker: "fake-pass", Verdict: core.VerdictPass}
}

type failChecker struct{}

// Name implements core.Checker.
func (failChecker) Name() string { return "fake-fail" }

// Check always fails, blaming the last two ops.
func (failChecker) Check(ops []history.Op, runDir string) core.CheckResult {
	var bad []int
	for i := max(0, len(ops)-2); i < len(ops); i++ {
		bad = append(bad, ops[i].Index)
	}
	return core.CheckResult{Checker: "fake-fail", Verdict: core.VerdictFail, Reason: "fake-fail always fails", BadOps: bad}
}
