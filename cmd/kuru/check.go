package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/core"
)

func init() {
	var checkers []string
	cmd := &cobra.Command{
		Use:   "check <run-dir>",
		Short: "Re-run checkers on a run folder and print the verdicts",
		Long: "Re-run checkers on a run folder (default: the scenario's checkers), rewrite result.json " +
			"and print a table. Exit code: 0 PASS, 1 FAIL, 2 anything else.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runCheck(cmd.OutOrStdout(), args[0], checkers)
			if err != nil {
				return err
			}
			exitWith(code)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&checkers, "checkers", nil, "comma-separated checkers to run (default: from scenario.yaml)")
	rootCmd.AddCommand(cmd)
}

// exitWith ends the process with code unless it is 0. Tests replace it.
var exitWith = func(code int) {
	if code != 0 {
		os.Exit(code)
	}
}

// verdictExitCode maps a verdict to kuru check's exit code.
func verdictExitCode(v string) int {
	switch v {
	case core.VerdictPass:
		return 0
	case core.VerdictFail:
		return 1
	}
	return 2
}

// runCheck runs the checkers on dir, prints the table to w and returns the exit code.
func runCheck(w io.Writer, dir string, names []string) (int, error) {
	res, err := core.CheckRun(dir, names)
	if err != nil {
		return 2, fmt.Errorf("check %s: %w", dir, err)
	}
	printChecks(w, res)
	return verdictExitCode(res.Verdict), nil
}

// printChecks prints a result as a table.
func printChecks(w io.Writer, res core.Result) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CHECKER\tVERDICT\tREASON\tBAD OPS")
	for _, c := range res.Checks {
		ops := make([]string, len(c.BadOps))
		for i, o := range c.BadOps {
			ops[i] = fmt.Sprint(o)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Checker, c.Verdict, dash(c.Reason), dash(strings.Join(ops, ",")))
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(w, "verdict: %s\n", res.Verdict)
	for _, c := range res.Checks {
		for name, file := range c.Artifacts {
			_, _ = fmt.Fprintf(w, "%s: %s\n", name, file)
		}
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
