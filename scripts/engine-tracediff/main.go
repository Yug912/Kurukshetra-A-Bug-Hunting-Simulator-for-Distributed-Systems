// Command engine-tracediff compares the per-message decisions of two traces.
//
//	go run ./scripts/engine-tracediff [-min 99] A/trace.jsonl B/trace.jsonl
//
// It prints how many message IDs (present in both traces) got the same
// decision, overall and for seed-driven decisions only (excluding messages cut
// by a partition or decided in the quiet period, whose fate depends on node
// timing). It exits 1 if the seed-driven share is below -min.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/replay"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

func main() {
	min := flag.Float64("min", 99, "minimum percentage of identical seed-driven decisions")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: engine-tracediff [-min 99] A/trace.jsonl B/trace.jsonl")
		os.Exit(2)
	}
	a, err := trace.ReadFile(flag.Arg(0))
	check(err)
	b, err := trace.ReadFile(flag.Arg(1))
	check(err)
	var quietFrom int64
	if sc, err := scenario.Load(filepath.Join(filepath.Dir(flag.Arg(0)), core.ScenarioFile)); err == nil {
		quietFrom = sc.DurationSteps
	}
	d := replay.CompareDecisions(a, b, quietFrom)
	fmt.Printf("shared message IDs: %d (only in A: %d, only in B: %d)\n", d.Shared, d.OnlyA, d.OnlyB)
	fmt.Printf("seed-driven decisions identical: %d/%d (%.2f%%)\n", d.SeededSame, d.Seeded, d.SeededPercent())
	fmt.Printf("all decisions identical:         %d/%d (%.2f%%; the rest sit on partition/quiet boundaries)\n", d.Same, d.Shared, d.Percent())
	for _, m := range d.Mismatches {
		fmt.Println("  differs:", m)
	}
	if d.SeededPercent() < *min {
		fmt.Printf("FAIL: seed-driven decisions below %.2f%%\n", *min)
		os.Exit(1)
	}
	fmt.Println("OK")
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
