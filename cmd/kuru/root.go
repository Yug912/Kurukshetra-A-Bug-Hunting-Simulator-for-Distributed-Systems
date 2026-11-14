package main

import "github.com/spf13/cobra"

var (
	flagRunsDir string
	flagRunner  string
	flagVerbose bool
)

// rootCmd is the kuru root command. Subcommands add themselves in init() in their own files.
var rootCmd = &cobra.Command{
	Use:           "kuru",
	Short:         "Kurukshetra: a bug-hunting simulator for distributed systems",
	SilenceUsage:  true,
	SilenceErrors: false,
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&flagRunsDir, "runs-dir", "runs", "root folder for run outputs")
	pf.StringVar(&flagRunner, "runner", "docker", "runner to use (docker, fake-sample, fake-faults, ...)")
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output")
}

// runsDir returns the --runs-dir flag value.
func runsDir() string { return flagRunsDir }

// runnerName returns the --runner flag value.
func runnerName() string { return flagRunner }

// verbose reports whether -v was given.
func verbose() bool { return flagVerbose }
