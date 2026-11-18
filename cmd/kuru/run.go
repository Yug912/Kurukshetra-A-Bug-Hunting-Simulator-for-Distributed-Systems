package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/scenario"
)

// Exit codes of run, replay and explore.
const (
	exitPass  = 0
	exitFail  = 1
	exitOther = 2
)

func init() {
	var (
		seed            int64
		keep, quiet     bool
		faultsFrom      string
		noMessageFaults bool
	)
	cmd := &cobra.Command{
		Use:   "run <scenario.yaml>",
		Short: "Run one scenario with one seed",
		Long: `Run one scenario with one seed and write a run folder
(runs/<scenario>/<seed>/ by default).

Exit code: 0 PASS, 1 FAIL, 2 ERROR or UNKNOWN.`,
		Example: `  kuru run examples/fake-smoke.yaml --seed 1
  kuru run examples/fake-faults.yaml --runner fake-faults --seed 1
  kuru run examples/fake-smoke.yaml --faults-from runs/fake-smoke/1/faults.json --no-message-faults`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sc, err := scenario.Load(args[0])
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("seed") {
				seed = randomSeed()
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "using random seed %d\n", seed)
			}
			r, err := core.NewRunner(runnerName())
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			info, runErr := r.Run(ctx, sc, seed, core.RunOptions{
				OutRoot: runsDir(), FaultsFrom: faultsFrom, NoMessageFaults: noMessageFaults,
				Keep: keep, Quiet: quiet,
			})
			if info.Dir == "" {
				return runErr
			}
			if runErr != nil && info.Result.Verdict == "" {
				info.Result.Verdict = core.VerdictError
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), SummaryLine(seed, info))
			if runErr != nil && verbose() {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "error:", runErr)
			}
			os.Exit(ExitCode(info.Result.Verdict))
			return nil
		},
	}
	f := cmd.Flags()
	f.Int64Var(&seed, "seed", 0, "seed (default: random, printed)")
	f.BoolVar(&keep, "keep", false, "keep containers after the run")
	f.StringVar(&faultsFrom, "faults-from", "", "use this faults.json instead of generating a plan")
	f.BoolVar(&noMessageFaults, "no-message-faults", false, "disable drop/dup/delay (partitions still apply)")
	f.BoolVarP(&quiet, "quiet", "q", false, "only print the summary line")
	rootCmd.AddCommand(cmd)
}

// SummaryLine renders "seed=42 verdict=FAIL checks=linearizable-kv:FAIL faults=7 dir=runs/x/42".
func SummaryLine(seed int64, info core.RunInfo) string {
	var checks []string
	for _, c := range info.Result.Checks {
		checks = append(checks, c.Checker+":"+c.Verdict)
	}
	cs := strings.Join(checks, ",")
	if cs == "" {
		cs = "-"
	}
	line := fmt.Sprintf("seed=%d verdict=%s checks=%s faults=%d dir=%s", seed, info.Result.Verdict, cs, len(info.Faults), info.Dir)
	if info.Result.Fidelity != nil {
		line += fmt.Sprintf(" fidelity=%.1f%%", *info.Result.Fidelity*100)
	}
	if info.Result.Error != "" {
		line += " error=" + strconvQuote(info.Result.Error)
	}
	return line
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// ExitCode maps a verdict to the process exit code.
func ExitCode(verdict string) int {
	switch verdict {
	case core.VerdictPass:
		return exitPass
	case core.VerdictFail:
		return exitFail
	}
	return exitOther
}

func randomSeed() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 1
	}
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1) // non-negative
}

// signalContext is cancelled on Ctrl-C / SIGTERM; a second signal exits at once.
func signalContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			fmt.Fprintln(os.Stderr, "\ninterrupted: cleaning up (press Ctrl-C again to force quit)")
			cancel()
		case <-ctx.Done():
			return
		}
		select {
		case <-ch:
			os.Exit(130)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { signal.Stop(ch); cancel() }
}
