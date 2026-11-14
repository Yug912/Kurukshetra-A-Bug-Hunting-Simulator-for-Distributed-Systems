// Command engine-tracediff compares the per-message decisions of two traces.
//
//	go run ./scripts/engine-tracediff [-min 99] A/trace.jsonl B/trace.jsonl
//
// It prints the share of message IDs (present in both traces) that got the same
// decision, and exits 1 if it is below -min.
package main

import (
	"flag"
	"fmt"
	"os"

	"kurukshetra/pkg/engine"
	"kurukshetra/pkg/trace"
)

func main() {
	min := flag.Float64("min", 99, "minimum percentage of identical decisions")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: engine-tracediff [-min 99] A/trace.jsonl B/trace.jsonl")
		os.Exit(2)
	}
	a, err := trace.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	b, err := trace.ReadFile(flag.Arg(1))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	d := engine.CompareDecisions(a, b)
	fmt.Printf("shared message IDs: %d, identical decisions: %d (%.2f%%), only in A: %d, only in B: %d\n",
		d.Shared, d.Same, d.Percent(), d.OnlyA, d.OnlyB)
	for _, m := range d.Mismatches {
		fmt.Println("  differs:", m)
	}
	if d.Percent() < *min {
		fmt.Printf("FAIL: below %.2f%%\n", *min)
		os.Exit(1)
	}
	fmt.Println("OK")
}
