package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/explore"
	"kurukshetra/pkg/scenario"
)

func init() {
	var o explore.Options
	var quiet bool
	cmd := &cobra.Command{
		Use:   "explore <scenario.yaml>",
		Short: "Run a scenario over many seeds in parallel and collect the failing ones",
		Long: `Explore runs seeds S..S+N-1 with a pool of P runners. Passing run folders are
deleted unless --keep-passing; FAIL and ERROR runs are always kept. Every run
is appended to runs/<scenario>/summary.csv
(seed,verdict,checks,faults,steps,duration_ms,dir).

Ctrl-C stops handing out seeds, lets running ones clean up and prints the summary.
Exit code: 0 no failures, 1 at least one FAIL, 2 errors but no FAIL.`,
		Example: `  kuru explore examples/fake-faults.yaml --runner fake-faults --runs 500 --parallel 8
  kuru explore examples/fake-smoke.yaml --runs 12 --parallel 4
  kuru explore examples/toykv-stale-read.yaml --runs 200 --stop-after-fails 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sc, err := scenario.Load(args[0])
			if err != nil {
				return err
			}
			o.Runner, o.OutRoot = runnerName(), runsDir()
			if !quiet {
				o.Progress = cmd.ErrOrStderr()
			}
			ctx, stop := signalContext()
			defer stop()
			sum, err := explore.Run(ctx, sc, o)
			fmt.Fprint(cmd.OutOrStdout(), explore.Report(sum))
			if err != nil {
				return err
			}
			switch {
			case sum.Fail > 0:
				os.Exit(exitFail)
			case sum.Error > 0:
				os.Exit(exitOther)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&o.Runs, "runs", 100, "number of seeds to run")
	f.IntVar(&o.Parallel, "parallel", 4, "concurrent runs")
	f.Int64Var(&o.StartSeed, "start-seed", 1, "first seed")
	f.BoolVar(&o.KeepPassing, "keep-passing", false, "keep run folders of passing runs")
	f.IntVar(&o.StopAfterFails, "stop-after-fails", 0, "stop after this many FAILs (0 = run all)")
	f.BoolVar(&o.NoMessageFaults, "no-message-faults", false, "disable drop/dup/delay")
	f.BoolVarP(&quiet, "quiet", "q", false, "no live progress line")
	rootCmd.AddCommand(cmd)
}
