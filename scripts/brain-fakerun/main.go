// Command brain-fakerun writes a run folder with any registered runner (default
// fake-faults), so the shrink/serve demo works before Person A's `kuru run` exists.
//
//	go run ./scripts/brain-fakerun -scenario examples/fake-faults.yaml -seed 1 -runs-dir runs
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"kurukshetra/pkg/core"
	_ "kurukshetra/pkg/fakes"
	"kurukshetra/pkg/scenario"
)

func main() {
	path := flag.String("scenario", "examples/fake-faults.yaml", "scenario file")
	seed := flag.Int64("seed", 1, "seed")
	runs := flag.String("runs-dir", "runs", "output root")
	runner := flag.String("runner", "fake-faults", "registered runner")
	flag.Parse()
	if err := run(*path, *seed, *runs, *runner); err != nil {
		fmt.Fprintln(os.Stderr, "brain-fakerun:", err)
		os.Exit(1)
	}
}

func run(path string, seed int64, runs, runner string) error {
	sc, err := scenario.Load(path)
	if err != nil {
		return err
	}
	r, err := core.NewRunner(runner)
	if err != nil {
		return err
	}
	info, err := r.Run(context.Background(), sc, seed, core.RunOptions{OutRoot: runs, Quiet: true})
	if err != nil {
		return err
	}
	fmt.Printf("%s seed %d -> %s (%s, %d faults)\n", sc.Name, seed, info.Result.Verdict, info.Dir, len(info.Faults))
	return nil
}
