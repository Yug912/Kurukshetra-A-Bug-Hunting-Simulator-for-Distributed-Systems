package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

func TestRegistered(t *testing.T) {
	r, err := core.NewRunner(RunnerName)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*Runner); !ok {
		t.Fatalf("%T", r)
	}
}

func TestNodeEnv(t *testing.T) {
	sc := scenario.Scenario{Name: "x", Image: "i", Env: map[string]string{"BUG_X": "1", "NODE_ID": "evil"}}
	sc.ApplyDefaults()
	env := NodeEnv(sc, "n2", "n1=h:1,n3=h:2")
	want := map[string]string{"BUG_X": "1", "NODE_ID": "n2", "PEER_LISTEN": ":7000", "CLIENT_LISTEN": ":8080",
		"PEERS": "n1=h:1,n3=h:2", "DATA_DIR": "/data", "KURU_CLOCK_OFFSET_MS": "0"}
	if len(env) != len(want) {
		t.Fatal(env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Fatalf("%s=%q want %q", k, env[k], v)
		}
	}
}

func TestDecisionsAndCompare(t *testing.T) {
	a := []trace.Event{
		{Kind: trace.MsgSend, MsgID: "m1"}, {Kind: trace.MsgDeliver, MsgID: "m1"},
		{Kind: trace.MsgDup, MsgID: "m2"}, {Kind: trace.MsgDeliver, MsgID: "m2"}, {Kind: trace.MsgDeliver, MsgID: "m2"},
		{Kind: trace.MsgDelay, MsgID: "m3", Meta: map[string]string{"steps": "4"}}, {Kind: trace.MsgDeliver, MsgID: "m3"},
		{Kind: trace.MsgDrop, MsgID: "m4", Meta: map[string]string{"reason": "partition"}},
	}
	d := Decisions(a)
	want := map[string]string{"m1": "deliver", "m2": "dup", "m3": "delay:4", "m4": "drop:partition"}
	for id, k := range want {
		if d[id].Kind != k {
			t.Fatalf("%s: %s want %s", id, d[id].Kind, k)
		}
	}
	b := append([]trace.Event{}, a[:7]...)
	b = append(b, trace.Event{Kind: trace.MsgDeliver, MsgID: "m4"}, trace.Event{Kind: trace.MsgDeliver, MsgID: "m5"})
	diff := CompareDecisions(a, b)
	if diff.Shared != 4 || diff.Same != 3 || diff.OnlyB != 1 || diff.Percent() != 75 || len(diff.Mismatches) != 1 {
		t.Fatalf("%+v", diff)
	}
}

func TestTimings(t *testing.T) {
	tm := newTimings()
	tm.begin("a")
	tm.begin("b")
	tm.begin("a")
	tm.end()
	p := filepath.Join(t.TempDir(), "t.json")
	if err := tm.write(p); err != nil {
		t.Fatal(err)
	}
	var out []struct{ Phase string }
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &out)
	if len(out) != 2 || out[0].Phase != "a" || out[1].Phase != "b" {
		t.Fatal(string(b))
	}
}

func TestRunRejectsInvalidScenario(t *testing.T) {
	_, err := (&Runner{}).Run(t.Context(), scenario.Scenario{Name: "Bad Name", Image: "x"}, 1, core.RunOptions{OutRoot: t.TempDir()})
	if err == nil {
		t.Fatal("want validation error")
	}
}
