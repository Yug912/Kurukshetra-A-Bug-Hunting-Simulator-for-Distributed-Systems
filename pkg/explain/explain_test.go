package explain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/trace"
)

// writeRun builds a run folder from events, ops and checks.
func writeRun(t *testing.T, dir string, evs []trace.Event, ops []history.Op, checks []core.CheckResult, scen string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tw, err := trace.NewWriter(filepath.Join(dir, core.TraceFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		tw.Append(e)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	hr, err := history.NewFileRecorder(filepath.Join(dir, core.HistoryFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		hr.Record(o)
	}
	if err := hr.Close(); err != nil {
		t.Fatal(err)
	}
	verdict := core.VerdictPass
	for _, c := range checks {
		if c.Verdict == core.VerdictFail {
			verdict = core.VerdictFail
		}
	}
	if err := core.WriteResult(dir, core.Result{Seed: 7, Scenario: "lease", Verdict: verdict, Checks: checks}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, core.ScenarioFile), []byte(scen), 0o644); err != nil {
		t.Fatal(err)
	}
}

func leaseRun(t *testing.T, dir string) {
	evs := []trace.Event{
		{Step: 0, Kind: trace.RunStart},
		{Step: 5, Kind: trace.Note, Node: "n1", Meta: map[string]string{"leader": "n1", "term": "2"}},
		{Step: 100, Kind: trace.Partition, Meta: map[string]string{"groups": "n1|n2,n3"}},
		{Step: 140, Kind: trace.Note, Node: "n2", Meta: map[string]string{"leader": "n2", "term": "3"}},
		{Step: 120, Kind: trace.ClockSkew, Node: "n1", Meta: map[string]string{"offset_ms": "500"}},
		{Step: 150, Kind: trace.ClockSkew, Node: "n1", Meta: map[string]string{"offset_ms": "-2500"}},
		{Step: 300, Kind: trace.Heal},
		{Step: 400, Kind: trace.RunEnd},
	}
	ops := []history.Op{
		{Client: 0, Type: history.Invoke, F: history.Write, Key: "k0", Value: "c0-1", Node: "n1", Step: 10},
		{Client: 0, Type: history.OK, F: history.Write, Key: "k0", Value: "c0-1", Node: "n1", Step: 12},
		{Client: 1, Type: history.Invoke, F: history.Write, Key: "k0", Value: "c1-1", Node: "n2", Step: 160},
		{Client: 1, Type: history.OK, F: history.Write, Key: "k0", Value: "c1-1", Node: "n2", Step: 165},
		{Client: 2, Type: history.Invoke, F: history.Read, Key: "k0", Node: "n1", Step: 170},
		{Client: 2, Type: history.OK, F: history.Read, Key: "k0", Value: "c0-1", Node: "n1", Step: 171},
	}
	checks := []core.CheckResult{
		{Checker: "linearizable-kv", Verdict: core.VerdictFail, Reason: "stale read of k0 on n1: got c0-1, expected c1-1", BadOps: []int{4, 5}},
		{Checker: "durability", Verdict: core.VerdictPass},
	}
	writeRun(t, dir, evs, ops, checks, "name: lease\nenv:\n  BUG_LEASE_READ: \"1\"\n")
}

func TestStoryLeaseRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lease", "7")
	leaseRun(t, dir)
	f, err := Gather(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.BadOps) != 1 || f.BadOps[0].Invoke.Step != 170 || f.BadOps[0].Complete.Value != "c0-1" {
		t.Fatalf("bad ops %+v", f.BadOps)
	}
	if l, since := f.LeaderAt(170); l != "n2" || since != 140 {
		t.Fatalf("leader at 170 = %s since %d", l, since)
	}
	s := Story(f)
	for _, want := range []string{
		"`BUG_LEASE_READ=1`",
		"Step 100 → step 300: network partition `n1 | n2,n3`.",
		"Step 120: n1's clock jumped forward by 500 ms (offset +0 → +500 ms).",
		"Step 150: n1's clock jumped backward by 3000 ms (offset +500 → -2500 ms).",
		"client 2 read `k0` from n1 at steps 170–171 and got `c0-1`",
		"client 1's write of `c1-1` (via n2) had already been acknowledged at step 165",
		"The leader at that time was n2 (since step 140), not n1.",
		"its clock jumped back 3000 ms at step 150",
		"Lease reads are only safe if clocks cannot go backwards.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("story is missing %q:\n%s", want, s)
		}
	}
}

func TestStoryLostWriteAfterCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lost", "3")
	evs := []trace.Event{
		{Step: 0, Kind: trace.RunStart},
		{Step: 200, Kind: trace.Crash, Node: "n1"},
		{Step: 320, Kind: trace.Restart, Node: "n1"},
		{Step: 500, Kind: trace.RunEnd},
	}
	ops := []history.Op{
		{Client: 0, Type: history.Invoke, F: history.Write, Key: "k1", Value: "c0-7", Node: "n1", Step: 190},
		{Client: 0, Type: history.OK, F: history.Write, Key: "k1", Value: "c0-7", Node: "n1", Step: 195},
		{Client: 3, Type: history.Invoke, F: history.Read, Key: "k1", Node: "n2", Step: 450, Final: true},
		{Client: 3, Type: history.OK, F: history.Read, Key: "k1", Value: "", Node: "n2", Step: 451, Final: true},
	}
	checks := []core.CheckResult{{Checker: "durability", Verdict: core.VerdictFail, Reason: "acknowledged write k1=c0-7 lost", BadOps: []int{0, 1}}}
	writeRun(t, dir, evs, ops, checks, "name: lost\n")
	f, err := Gather(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := Story(f)
	for _, want := range []string{
		"**durability** failed: acknowledged write k1=c0-7 lost.",
		"client 0 wrote `k1=c0-7` via n1 at steps 190–195 (ok)",
		"n1 acknowledged the write at step 195, then crashed at step 200",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("story is missing %q:\n%s", want, s)
		}
	}
}

func TestResolvePrefersMinimalRun(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "lease", "7")
	leaseRun(t, dir)
	if got := Resolve(dir); got != dir {
		t.Fatalf("no minimal.json: Resolve = %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "minimal.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	leaseRun(t, dir+"-min")
	if got := Resolve(dir); got != dir+"-min" {
		t.Fatalf("Resolve = %s, want the -min run", got)
	}
}

func TestStoryPassHasNothingToExplain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ok", "1")
	writeRun(t, dir, []trace.Event{{Kind: trace.RunStart}}, nil,
		[]core.CheckResult{{Checker: "linearizable-kv", Verdict: core.VerdictPass}}, "name: ok\n")
	f, err := Gather(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s := Story(f); !strings.Contains(s, "Nothing to explain") {
		t.Fatalf("story: %s", s)
	}
}
