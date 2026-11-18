package main

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/shrink"
)

func init() {
	var maxRuns, parallel int
	cmd := &cobra.Command{
		Use:   "shrink <run-dir>",
		Short: "Minimize the fault schedule of a failing run",
		Long: "Find a small fault schedule that still makes the run FAIL: first try without message faults, " +
			"then delta-debug the fault list, then shorten each fault. Writes minimal.json and shrink.log into " +
			"<run-dir> and keeps a run of the minimal schedule in <run-dir>-min.\n\n" +
			"The runner is --runner if given, else the runner recorded in meta.json, else docker.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := shrinkRunner(cmd.Flags().Changed("runner"), args[0])
			var progress io.Writer
			if verbose() {
				progress = cmd.ErrOrStderr()
			}
			return runShrink(cmd, args[0], name, maxRuns, parallel, progress)
		},
	}
	cmd.Flags().IntVar(&maxRuns, "max-runs", 60, "maximum number of trial runs")
	cmd.Flags().IntVar(&parallel, "parallel", 1, "trial runs at once")
	rootCmd.AddCommand(cmd)
}

// shrinkRunner picks the runner: the explicit flag, else meta.json's runner if registered, else the flag default.
func shrinkRunner(flagSet bool, dir string) string {
	if flagSet {
		return runnerName()
	}
	if m, err := core.ReadMeta(dir); err == nil && slices.Contains(core.RunnerNames(), m.Runner) {
		return m.Runner
	}
	return runnerName()
}

func runShrink(cmd *cobra.Command, dir, runnerName string, maxRuns, parallel int, progress io.Writer) error {
	runner, err := core.NewRunner(runnerName)
	if err != nil {
		return fmt.Errorf("shrink: %w", err)
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "shrinking %s with runner %s (max %d runs, %d parallel)\n", dir, runnerName, maxRuns, parallel)
	res, err := shrink.Shrink(cmd.Context(), dir, shrink.Options{Runner: runner, MaxRuns: maxRuns, Parallel: parallel, Log: progress})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, res.Summary())
	_, _ = fmt.Fprintf(out, "wrote %s/%s and %s/%s\n", dir, shrink.MinimalFile, dir, shrink.LogFile)
	if res.MinDir != "" {
		_, _ = fmt.Fprintf(out, "minimal run: %s\n", res.MinDir)
	} else {
		_, _ = fmt.Fprintln(os.Stderr, "warning: no run of the minimal schedule was kept")
	}
	return nil
}
