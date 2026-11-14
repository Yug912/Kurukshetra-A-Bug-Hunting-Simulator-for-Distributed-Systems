package core

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kurukshetra/pkg/history"
)

type stubChecker struct {
	name, verdict string
}

func (s stubChecker) Name() string { return s.name }
func (s stubChecker) Check(ops []history.Op, dir string) CheckResult {
	return CheckResult{Checker: s.name, Verdict: s.verdict, BadOps: []int{len(ops) - 1}}
}

func init() {
	RegisterChecker("test-pass", func() Checker { return stubChecker{"test-pass", VerdictPass} })
	RegisterChecker("test-fail", func() Checker { return stubChecker{"test-fail", VerdictFail} })
}

func TestRegistries(t *testing.T) {
	if _, err := NewChecker("nope"); err == nil {
		t.Fatal("want error")
	}
	c, err := NewChecker("test-pass")
	if err != nil || c.Name() != "test-pass" {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(CheckerNames(), []string{"test-fail", "test-pass"}) {
		t.Fatalf("names %v", CheckerNames())
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate should panic")
		}
	}()
	RegisterChecker("test-pass", nil)
}

func TestRunnerAndWorkloadRegistry(t *testing.T) {
	RegisterRunner("test-runner", func() (Runner, error) { return nil, nil })
	if _, err := NewRunner("test-runner"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner("missing"); err == nil {
		t.Fatal("want error")
	}
	found := false
	for _, n := range RunnerNames() {
		found = found || n == "test-runner"
	}
	if !found {
		t.Fatal("runner not listed")
	}
}

func TestCombineVerdict(t *testing.T) {
	c := func(vs ...string) []CheckResult {
		var out []CheckResult
		for _, v := range vs {
			out = append(out, CheckResult{Verdict: v})
		}
		return out
	}
	cases := []struct {
		in   []CheckResult
		want string
	}{
		{nil, VerdictUnknown},
		{c(VerdictPass), VerdictPass},
		{c(VerdictPass, VerdictUnknown), VerdictUnknown},
		{c(VerdictUnknown, VerdictFail), VerdictFail},
		{c(VerdictPass, VerdictPass), VerdictPass},
	}
	for _, tc := range cases {
		if got := CombineVerdict(tc.in); got != tc.want {
			t.Errorf("%v: got %s want %s", tc.in, got, tc.want)
		}
	}
}

func TestFaultsRoundTripSorted(t *testing.T) {
	p := filepath.Join(t.TempDir(), FaultsFile)
	in := []Fault{
		{Step: 700, Kind: "crash", Nodes: []string{"n3"}, DurationSteps: 100},
		{Step: 120, Kind: "pause", Nodes: []string{"n2"}, DurationSteps: 10},
		{Step: 120, Kind: "partition", Groups: [][]string{{"n1"}, {"n2", "n3"}}, DurationSteps: 480},
	}
	if err := WriteFaults(p, in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFaults(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Fault{in[2], in[1], in[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if in[0].Step != 700 {
		t.Fatal("input mutated")
	}
	if d := DescribeFault(in[2]); d != "partition n1 | n2,n3 at step 120 for 480" {
		t.Fatalf("describe: %q", d)
	}
}

func TestRunDir(t *testing.T) {
	if got := RunDir("runs", "x", 42); got != filepath.Join("runs", "x", "42") {
		t.Fatal(got)
	}
}

func TestCheckRun(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, HistoryFile), []byte(`{"index":0,"client":0,"type":"invoke","f":"read","key":"k0","node":"n1","step":1,"wall_ns":1}
{"index":1,"client":0,"type":"ok","f":"read","key":"k0","node":"n1","step":2,"wall_ns":2}
`), 0o644)
	os.WriteFile(filepath.Join(dir, ScenarioFile), []byte("name: s\nimage: img\ncheckers: [test-pass]\n"), 0o644)
	fid := 0.9
	WriteResult(dir, Result{Seed: 9, Scenario: "s", Verdict: VerdictUnknown, Fidelity: &fid})

	r, err := CheckRun(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != VerdictPass || len(r.Checks) != 1 || r.Seed != 9 || r.Fidelity == nil || *r.Fidelity != 0.9 {
		t.Fatalf("got %+v", r)
	}
	r, err = CheckRun(dir, []string{"test-pass", "test-fail"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != VerdictFail || r.Checks[1].BadOps[0] != 1 {
		t.Fatalf("got %+v", r)
	}
	disk, _ := ReadResult(dir)
	if !reflect.DeepEqual(disk, r) {
		t.Fatal("result.json differs")
	}
	if _, err := CheckRun(dir, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown checker") {
		t.Fatal("want unknown checker error")
	}
}

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := Meta{Scenario: "s", Seed: 1, Nodes: []string{"n1"}, Mode: "http", Runner: "docker", Version: Version}
	if err := WriteMeta(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMeta(dir)
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("%v %+v", err, got)
	}
}
