package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/engine"
	"kurukshetra/pkg/replay"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

func init() {
	var keep, quiet bool
	cmd := &cobra.Command{
		Use:   "replay <run-dir>",
		Short: "Re-run a recorded run, forcing its recorded decisions, and report fidelity",
		Long: `Replay re-runs a run folder with the same seed and fault plan and forces every
message's recorded fate and per-step delivery order. The output goes to
<run-dir>-replay-<n>. Fidelity is the share of recorded decisions reproduced
at the same step (±2) plus fault events reproduced exactly; node-internal
timers and goroutine scheduling are not controlled, so it can be below 100%.

Exit code: 0 PASS, 1 FAIL, 2 ERROR or UNKNOWN (of the replay).`,
		Example: `  kuru replay runs/fake-smoke/1
  kuru replay runs/toykv-stale-read/17 --keep`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := filepath.Clean(args[0])
			orig, err := replay.Load(dir)
			if err != nil {
				return err
			}
			sc, err := scenario.Load(filepath.Join(dir, core.ScenarioFile))
			if err != nil {
				return err
			}
			r, err := core.NewRunner(engine.RunnerName)
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			info, runErr := r.Run(ctx, sc, orig.Meta.Seed, core.RunOptions{ReplayFrom: dir, Keep: keep, Quiet: quiet})
			if info.Dir == "" {
				return runErr
			}
			out := cmd.OutOrStdout()
			if rep, err := trace.ReadFile(filepath.Join(info.Dir, core.TraceFile)); err == nil {
				fmt.Fprintln(out, replay.Compare(orig.Trace, rep))
			}
			same := "reproduced"
			if orig.Result.Verdict != info.Result.Verdict {
				same = "NOT reproduced"
			}
			fmt.Fprintf(out, "verdict %s (original %s): %s\n", info.Result.Verdict, orig.Result.Verdict, same)
			fmt.Fprintln(out, SummaryLine(orig.Meta.Seed, info))
			if runErr != nil && verbose() {
				fmt.Fprintln(cmd.ErrOrStderr(), "error:", runErr)
			}
			os.Exit(ExitCode(info.Result.Verdict))
			return nil
		},
	}
	cmd.Flags().BoolVar(&keep, "keep", false, "keep containers after the replay")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "only print the summary")
	rootCmd.AddCommand(cmd)
}
