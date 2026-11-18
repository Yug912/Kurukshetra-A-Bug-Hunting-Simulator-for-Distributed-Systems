package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/scenario"
)

func TestShrinkCommandFakeFaults(t *testing.T) {
	sc, err := scenario.Load(filepath.Join("..", "..", "examples", "fake-faults.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := core.NewRunner("fake-faults")
	info, err := r.Run(context.Background(), sc, 1, core.RunOptions{OutRoot: t.TempDir(), Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	// The runner comes from meta.json (fake-faults) since --runner is not given.
	if got := shrinkRunner(false, info.Dir); got != "fake-faults" {
		t.Fatalf("runner %q", got)
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"shrink", info.Dir, "--max-runs", "60"})
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "minimal: 2 faults (from 8)") || !strings.Contains(s, "minimal run: "+info.Dir+"-min") {
		t.Fatal(s)
	}
}
