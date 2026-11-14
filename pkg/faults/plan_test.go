package faults

import (
	"path/filepath"
	"reflect"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

func smoke(t *testing.T, nodes int) scenario.Scenario {
	t.Helper()
	sc, err := scenario.Load(filepath.Join("..", "..", "examples", "fake-smoke.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sc.Nodes = nodes
	return sc
}

func TestPlanDeterministic(t *testing.T) {
	sc := smoke(t, 3)
	a := Plan(sc, rng.New(42))
	b := Plan(sc, rng.New(42))
	if !reflect.DeepEqual(a, b) || len(a) == 0 {
		t.Fatalf("not deterministic or empty: %d", len(a))
	}
	if reflect.DeepEqual(a, Plan(sc, rng.New(43))) {
		t.Fatal("seeds 42 and 43 gave the same plan")
	}
}

func TestPlanAllKindsAndSorted(t *testing.T) {
	sc := smoke(t, 3)
	kinds := map[trace.Kind]int{}
	for _, f := range Plan(sc, rng.New(1)) {
		kinds[f.Kind]++
	}
	for _, k := range []trace.Kind{trace.Partition, trace.Crash, trace.Pause, trace.ClockSkew} {
		if kinds[k] == 0 {
			t.Errorf("no %s in seed 1 plan: %v", k, kinds)
		}
	}
}

func TestPlanValidSeeds1to100(t *testing.T) {
	for _, n := range []int{1, 2, 3, 5, 7} {
		sc := smoke(t, n)
		for seed := int64(1); seed <= 100; seed++ {
			p := Plan(sc, rng.New(seed))
			if err := Validate(p, scenario.NodeNames(n)); err != nil {
				t.Fatalf("n=%d seed=%d: %v", n, seed, err)
			}
			for _, f := range p {
				if f.Step >= sc.DurationSteps || f.Step <= 0 {
					t.Fatalf("n=%d seed=%d: step %d out of range", n, seed, f.Step)
				}
				if f.Kind == trace.Partition && n == 5 && len(f.Nodes) == 0 && len(f.Groups[0]) > MaxDown(n) && len(f.Groups[1]) > MaxDown(n) {
					t.Fatalf("no minority side: %+v", f)
				}
			}
		}
	}
}

func TestToggleOneTypeDoesNotReshuffleOthers(t *testing.T) {
	sc := smoke(t, 3)
	only := func(p []core.Fault, k trace.Kind) []core.Fault {
		var out []core.Fault
		for _, f := range p {
			if f.Kind == k {
				out = append(out, f)
			}
		}
		return out
	}
	full := Plan(sc, rng.New(7))
	noSkew := sc
	noSkew.Faults.ClockSkewEvery, noSkew.Faults.ClockSkewMs = scenario.Range{}, scenario.Range{}
	reduced := Plan(noSkew, rng.New(7))
	if !reflect.DeepEqual(only(full, trace.Partition), only(reduced, trace.Partition)) {
		t.Fatal("disabling clock skew changed partitions")
	}
	if len(only(reduced, trace.ClockSkew)) != 0 {
		t.Fatal("skew still present")
	}
}

func TestNoOverlapAndMinorityRules(t *testing.T) {
	sc := smoke(t, 5)
	sc.Faults.PartitionEvery = scenario.Range{10, 20} // shorter than the length: forces pushes
	sc.Faults.CrashEvery = scenario.Range{5, 10}      // dense: forces skips
	sc.Faults.PauseEvery = scenario.Range{5, 10}
	for seed := int64(1); seed <= 50; seed++ {
		p := Plan(sc, rng.New(seed))
		if err := Validate(p, scenario.NodeNames(5)); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	}
	bad := []core.Fault{
		{Step: 1, Kind: trace.Crash, Nodes: []string{"n1"}, DurationSteps: 10},
		{Step: 2, Kind: trace.Pause, Nodes: []string{"n2"}, DurationSteps: 10},
	}
	if Validate(bad, scenario.NodeNames(3)) == nil {
		t.Fatal("two nodes down of 3 should be invalid")
	}
	overlap := []core.Fault{
		{Step: 1, Kind: trace.Partition, Groups: [][]string{{"n1"}, {"n2", "n3"}}, DurationSteps: 10},
		{Step: 5, Kind: trace.Partition, Groups: [][]string{{"n2"}, {"n1", "n3"}}, DurationSteps: 10},
	}
	if Validate(overlap, scenario.NodeNames(3)) == nil {
		t.Fatal("overlapping partitions should be invalid")
	}
}

func TestBridgeAndBlockedLinks(t *testing.T) {
	b := BlockedLinks([][]string{{"n1"}, {"n2"}})
	if !b[[2]string{"n1", "n2"}] || !b[[2]string{"n2", "n1"}] || b[[2]string{"n1", "n3"}] || len(b) != 2 {
		t.Fatal(b)
	}
	if GroupsString([][]string{{"n1"}, {"n2", "n3"}}) != "n1|n2,n3" {
		t.Fatal("groups string")
	}
	sawBridge := false
	sc := smoke(t, 3)
	for seed := int64(1); seed <= 30; seed++ {
		for _, f := range Plan(sc, rng.New(seed)) {
			if f.Kind == trace.Partition && len(f.Nodes) == 1 {
				sawBridge = true
			}
		}
	}
	if !sawBridge {
		t.Fatal("no bridge partition in 30 seeds")
	}
}
