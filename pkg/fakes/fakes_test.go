package fakes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

func testScenario(t *testing.T, name string) scenario.Scenario {
	sc, err := scenario.Parse([]byte("name: " + name + "\nimage: kuru/fake-node\nworkload:\n  type: fake-put\ncheckers: [fake-pass]\n"))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestCheckers(t *testing.T) {
	ops := []history.Op{{Index: 0}, {Index: 1}, {Index: 2}}
	p, _ := core.NewChecker("fake-pass")
	if r := p.Check(ops, ""); r.Verdict != core.VerdictPass {
		t.Fatal(r)
	}
	f, _ := core.NewChecker("fake-fail")
	r := f.Check(ops, "")
	if r.Verdict != core.VerdictFail || !reflect.DeepEqual(r.BadOps, []int{1, 2}) {
		t.Fatalf("%+v", r)
	}
	if r := f.Check(nil, ""); r.Verdict != core.VerdictFail || len(r.BadOps) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestFaultsRunnerSeed1Fails(t *testing.T) {
	plan := GeneratePlan(1, []string{"n1", "n2", "n3"})
	if len(plan) != PlanSize || !PlanFails(plan) {
		t.Fatalf("seed 1 plan must isolate n1 and crash n3: %+v", plan)
	}
	if !reflect.DeepEqual(plan, GeneratePlan(1, []string{"n1", "n2", "n3"})) {
		t.Fatal("plan not deterministic")
	}
	root := t.TempDir()
	run, _ := core.NewRunner("fake-faults")
	info, err := run.Run(context.Background(), testScenario(t, "fake-faults"), 1, core.RunOptions{OutRoot: root, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.Result.Verdict != core.VerdictFail {
		t.Fatalf("verdict %s", info.Result.Verdict)
	}
	for _, f := range []string{core.FaultsFile, core.MetaFile, core.ScenarioFile, core.TraceFile, core.HistoryFile, core.ResultFile} {
		if _, err := os.Stat(filepath.Join(info.Dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := trace.ReadFile(filepath.Join(info.Dir, core.TraceFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := scenario.Load(filepath.Join(info.Dir, core.ScenarioFile)); err != nil {
		t.Fatal(err)
	}
	disk, _ := core.ReadFaults(filepath.Join(info.Dir, core.FaultsFile))
	if !reflect.DeepEqual(disk, plan) {
		t.Fatal("faults.json differs from plan")
	}
}

func TestFaultsRunnerFromFile(t *testing.T) {
	root := t.TempDir()
	sc := testScenario(t, "ff")
	run := FaultsRunner{}
	write := func(fs []core.Fault) string {
		p := filepath.Join(t.TempDir(), "faults.json")
		core.WriteFaults(p, fs)
		return p
	}
	part := core.Fault{Step: 10, Kind: trace.Partition, Groups: [][]string{{"n1"}, {"n2", "n3"}}, DurationSteps: 50}
	crash := core.Fault{Step: 20, Kind: trace.Crash, Nodes: []string{"n3"}, DurationSteps: 50}
	cases := []struct {
		plan []core.Fault
		want string
	}{
		{[]core.Fault{part, crash}, core.VerdictFail},
		{[]core.Fault{part}, core.VerdictPass},
		{[]core.Fault{crash}, core.VerdictPass},
		{nil, core.VerdictPass},
	}
	for i, c := range cases {
		info, err := run.Run(context.Background(), sc, int64(i), core.RunOptions{OutRoot: root, FaultsFrom: write(c.plan), Quiet: true})
		if err != nil {
			t.Fatal(err)
		}
		if info.Result.Verdict != c.want || info.Meta.FaultsFrom == "" {
			t.Errorf("case %d: got %s want %s", i, info.Result.Verdict, c.want)
		}
	}
}

func TestSampleRunner(t *testing.T) {
	root := t.TempDir()
	run, _ := core.NewRunner("fake-sample")
	info, err := run.Run(context.Background(), testScenario(t, "sample"), 7, core.RunOptions{OutRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if info.Dir != core.RunDir(root, "sample", 7) || info.Result.Seed != 7 || info.Meta.Seed != 7 {
		t.Fatalf("%+v", info)
	}
	if info.Result.Verdict != core.VerdictFail || len(info.Faults) != 2 {
		t.Fatalf("%+v", info)
	}
	evs, err := trace.ReadFile(filepath.Join(info.Dir, core.TraceFile))
	if err != nil || len(evs) == 0 {
		t.Fatal(err)
	}
}

type stepClock struct{}

func (stepClock) Step() int64 { return 1 }

func TestFakePutWorkload(t *testing.T) {
	var mu sync.Mutex
	kv := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/kv/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			var b struct{ Value string }
			json.NewDecoder(r.Body).Decode(&b)
			kv[key] = b.Value
			w.Write([]byte(`{"ok":true}`))
		case http.MethodGet:
			v, ok := kv[key]
			if !ok {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":"not_found"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"value": v})
		}
	}))
	defer srv.Close()
	wl, err := core.NewWorkload(scenario.Workload{Type: "fake-put", Clients: 3, Keys: 2, OpsPerSec: 300, TimeoutMs: 500})
	if err != nil {
		t.Fatal(err)
	}
	cl := core.Cluster{Nodes: []core.Node{{Name: "n1", ClientURL: srv.URL}, {Name: "n2", ClientURL: srv.URL}}}
	var rec history.MemRecorder
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := wl.Run(ctx, cl, rng.New(1).Stream(rng.StreamWorkload), stepClock{}, &rec); err != nil {
		t.Fatal(err)
	}
	n := len(rec.Ops())
	if err := wl.FinalReads(context.Background(), cl, stepClock{}, &rec); err != nil {
		t.Fatal(err)
	}
	ops := rec.Ops()
	if n < 6 || len(ops) != n+2*2*2 {
		t.Fatalf("got %d ops, %d before final reads", len(ops), n)
	}
	pairs, err := history.Pairs(ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		if p.Invoke.F == history.Write && !strings.HasPrefix(p.Invoke.Value, "c") {
			t.Fatalf("bad value %q", p.Invoke.Value)
		}
	}
	for _, op := range ops[n:] {
		if !op.Final || op.F != history.Read {
			t.Fatalf("bad final op %+v", op)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		f    history.F
		code int
		err  error
		want history.Type
	}{
		{history.Write, 200, nil, history.OK},
		{history.Read, 404, nil, history.OK},
		{history.Write, 404, nil, history.Info},
		{history.CAS, 409, nil, history.Fail},
		{history.Write, 421, nil, history.Fail},
		{history.Write, 503, nil, history.Fail},
		{history.Write, 500, nil, history.Info},
		{history.Write, 0, context.DeadlineExceeded, history.Info},
	}
	for _, c := range cases {
		if got := classify(c.f, c.code, c.err); got != c.want {
			t.Errorf("%v %d %v: got %s want %s", c.f, c.code, c.err, got, c.want)
		}
	}
}
