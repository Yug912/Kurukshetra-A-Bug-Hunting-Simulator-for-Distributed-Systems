package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/scenario"
)

// copyTree copies src into a new temp dir and returns it (testdata is never modified).
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func sampleCopy(t *testing.T) string {
	return copyTree(t, filepath.Join("..", "..", "testdata", "sample-run"))
}

func TestCheckSampleFails(t *testing.T) {
	dir := sampleCopy(t)
	var out bytes.Buffer
	code, err := runCheck(&out, dir, []string{"linearizable-kv"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if code != 1 || !strings.Contains(s, "FAIL") || !strings.Contains(s, "4,5") || !strings.Contains(s, "linearizability.html") {
		t.Fatalf("code %d\n%s", code, s)
	}
	res, err := core.ReadResult(dir)
	if err != nil || res.Seed != 42 || res.Verdict != core.VerdictFail {
		t.Fatalf("result.json: %v %+v", err, res)
	}
}

func TestCheckCommandExitCodes(t *testing.T) {
	dir := sampleCopy(t)
	var got []int
	exitWith = func(c int) { got = append(got, c) }
	defer func() { exitWith = func(int) {} }()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	for _, chk := range []string{"durability", "linearizable-kv,durability"} {
		rootCmd.SetArgs([]string{"check", dir, "--checkers", chk})
		if err := rootCmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("exit codes %v\n%s", got, out.String())
	}
	for v, want := range map[string]int{core.VerdictPass: 0, core.VerdictFail: 1, core.VerdictUnknown: 2, core.VerdictError: 2} {
		if verdictExitCode(v) != want {
			t.Errorf("%s -> %d", v, verdictExitCode(v))
		}
	}
	if _, err := runCheck(&out, t.TempDir(), nil); err == nil {
		t.Fatal("empty dir must error")
	}
}

func TestToyKVExamplesParse(t *testing.T) {
	want := map[string]string{
		"toykv-correct":     "",
		"toykv-stale-read":  "BUG_STALE_READ",
		"toykv-lost-write":  "BUG_ACK_BEFORE_COMMIT",
		"toykv-lease-clock": "BUG_LEASE_READ",
	}
	for name, flag := range want {
		sc, err := scenario.Load(filepath.Join("..", "..", "examples", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if sc.Name != name || sc.Image != "kuru/toykv" || sc.Workload.Type != "kv" || len(sc.Checkers) != 2 {
			t.Fatalf("%s: %+v", name, sc)
		}
		if flag == "" && len(sc.Env) != 0 || flag != "" && sc.Env[flag] != "1" {
			t.Fatalf("%s env %v", name, sc.Env)
		}
	}
}
