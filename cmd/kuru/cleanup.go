package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"kurukshetra/pkg/docker"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "cleanup",
		Short: "Remove every container, network and volume kuru created (label kuru=1)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := docker.New()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := c.Ping(ctx); err != nil {
				return err
			}
			rm, err := c.CleanupAll(ctx)
			out := cmd.OutOrStdout()
			for _, n := range rm.Containers {
				_, _ = fmt.Fprintln(out, "container", n)
			}
			for _, n := range rm.Networks {
				_, _ = fmt.Fprintln(out, "network  ", n)
			}
			for _, n := range rm.Volumes {
				_, _ = fmt.Fprintln(out, "volume   ", n)
			}
			_, _ = fmt.Fprintf(out, "removed %s\n", rm)
			return err
		},
	})
}
