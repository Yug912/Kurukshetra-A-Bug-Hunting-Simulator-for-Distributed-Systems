package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/core"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the kuru version",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "kuru %s (%s)\n", core.Version, runtime.Version())
		},
	})
}
