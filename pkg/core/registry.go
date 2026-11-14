package core

import (
	"sort"
	"sync"

	"kurukshetra/pkg/scenario"
)

var (
	regMu     sync.RWMutex
	workloads = map[string]WorkloadFactory{}
	checkers  = map[string]CheckerFactory{}
	runners   = map[string]RunnerFactory{}
)

func register[F any](m map[string]F, kind, name string, f F) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, ok := m[name]; ok {
		panic("core: duplicate " + kind + " " + name)
	}
	m[name] = f
}

func lookup[F any](m map[string]F, name string) (F, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	f, ok := m[name]
	return f, ok
}

func names[F any](m map[string]F) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RegisterWorkload registers a workload type. It panics on duplicates.
func RegisterWorkload(typ string, f WorkloadFactory) { register(workloads, "workload", typ, f) }

// NewWorkload builds the workload named by cfg.Type.
func NewWorkload(cfg scenario.Workload) (Workload, error) {
	f, ok := lookup(workloads, cfg.Type)
	if !ok {
		return nil, errorf("unknown workload %q (have %v)", cfg.Type, names(workloads))
	}
	return f(cfg)
}

// RegisterChecker registers a checker. It panics on duplicates.
func RegisterChecker(name string, f CheckerFactory) { register(checkers, "checker", name, f) }

// NewChecker builds the named checker.
func NewChecker(name string) (Checker, error) {
	f, ok := lookup(checkers, name)
	if !ok {
		return nil, errorf("unknown checker %q (have %v)", name, names(checkers))
	}
	return f(), nil
}

// CheckerNames lists registered checkers, sorted.
func CheckerNames() []string { return names(checkers) }

// RegisterRunner registers a runner. It panics on duplicates.
func RegisterRunner(name string, f RunnerFactory) { register(runners, "runner", name, f) }

// NewRunner builds the named runner.
func NewRunner(name string) (Runner, error) {
	f, ok := lookup(runners, name)
	if !ok {
		return nil, errorf("unknown runner %q (have %v)", name, names(runners))
	}
	return f()
}

// RunnerNames lists registered runners, sorted.
func RunnerNames() []string { return names(runners) }
