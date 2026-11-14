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

func TestRootFlagsAndVersion(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"version", "--runs-dir", "x", "--runner", "fake-faults", "-v"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "kuru ") || runsDir() != "x" || runnerName() != "fake-faults" || !verbose() {
		t.Fatalf("out=%q runs=%q runner=%q", out.String(), runsDir(), runnerName())
	}
}

func TestExamples(t *testing.T) {
	for _, name := range []string{"fake-smoke", "fake-faults"} {
		sc, err := scenario.Load(filepath.Join("..", "..", "examples", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if sc.Name != name {
			t.Fatalf("name %q", sc.Name)
		}
	}
	sc, _ := scenario.Load(filepath.Join("..", "..", "examples", "fake-faults.yaml"))
	r, err := core.NewRunner("fake-faults")
	if err != nil {
		t.Fatal(err)
	}
	info, err := r.Run(context.Background(), sc, 1, core.RunOptions{OutRoot: t.TempDir(), Quiet: true})
	if err != nil || info.Result.Verdict != core.VerdictFail {
		t.Fatalf("%v %+v", err, info.Result)
	}
}
