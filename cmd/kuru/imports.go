package main

// Blank imports so each package's init() registrations run.
import (
	_ "kurukshetra/pkg/checker"
	_ "kurukshetra/pkg/engine"
	_ "kurukshetra/pkg/fakes"
	_ "kurukshetra/pkg/shrink"
	_ "kurukshetra/pkg/workload"
)
