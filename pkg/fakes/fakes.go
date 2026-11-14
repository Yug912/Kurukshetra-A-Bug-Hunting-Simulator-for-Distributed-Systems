// Package fakes registers stand-in workloads, checkers and runners so both
// branches can develop and test without each other's code (or Docker).
//
// Registered names: workload "fake-put"; checkers "fake-pass", "fake-fail";
// runners "fake-sample", "fake-faults".
package fakes

import "kurukshetra/pkg/core"

func init() {
	core.RegisterWorkload("fake-put", newFakePut)
	core.RegisterChecker("fake-pass", func() core.Checker { return passChecker{} })
	core.RegisterChecker("fake-fail", func() core.Checker { return failChecker{} })
	core.RegisterRunner("fake-sample", func() (core.Runner, error) { return SampleRunner{}, nil })
	core.RegisterRunner("fake-faults", func() (core.Runner, error) { return FaultsRunner{}, nil })
}
