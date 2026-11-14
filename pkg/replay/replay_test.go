package replay

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kurukshetra/pkg/scheduler"
	"kurukshetra/pkg/trace"
)

func ev(step int64, k trace.Kind, id string, meta ...string) trace.Event {
	e := trace.Event{Step: step, Kind: k, MsgID: id}
	if len(meta) > 0 {
		e.Meta = map[string]string{}
		for i := 0; i+1 < len(meta); i += 2 {
			e.Meta[meta[i]] = meta[i+1]
		}
	}
	return e
}

func sample() []trace.Event {
	return []trace.Event{
		{Step: 0, Kind: trace.RunStart, Meta: map[string]string{"no_message_faults": "true"}},
		ev(0, trace.MsgSend, "a-b-s1"), ev(0, trace.MsgDeliver, "a-b-s1"),
		ev(4, trace.MsgSend, "a-b-1"), ev(5, trace.MsgDeliver, "a-b-1"),
		ev(5, trace.MsgSend, "b-a-1"), ev(6, trace.MsgDelay, "b-a-1", "steps", "3"), ev(9, trace.MsgDeliver, "b-a-1"),
		ev(6, trace.MsgSend, "a-b-2"), ev(7, trace.MsgDup, "a-b-2"), ev(7, trace.MsgDeliver, "a-b-2"), ev(7, trace.MsgDeliver, "a-b-2"),
		ev(8, trace.MsgSend, "a-b-3"), ev(9, trace.MsgDrop, "a-b-3", "reason", "partition"),
		{Step: 9, Kind: trace.Partition, Meta: map[string]string{"groups": "a|b"}},
		ev(9, trace.MsgDeliver, "a-b-4"),
	}
}

func TestBuildGuide(t *testing.T) {
	g := BuildGuide(sample())
	want := map[string]scheduler.Decision{
		"a-b-1": {Fate: scheduler.Deliver},
		"b-a-1": {Fate: scheduler.Delay, Steps: 3},
		"a-b-2": {Fate: scheduler.Duplicate},
		"a-b-3": {Fate: scheduler.Drop, Reason: "partition"},
		"a-b-4": {Fate: scheduler.Deliver},
	}
	if !reflect.DeepEqual(g.Decisions, want) {
		t.Fatalf("%+v", g.Decisions)
	}
	if g.Steps["b-a-1"] != 6 || g.Steps["a-b-1"] != 5 {
		t.Fatal(g.Steps)
	}
	if !reflect.DeepEqual(g.Order[9], []string{"b-a-1", "a-b-4"}) || !reflect.DeepEqual(SortedSteps(g), []int64{5, 7, 9}) {
		t.Fatal(g.Order)
	}
	if !NoMessageFaults(sample()) || NoMessageFaults(nil) {
		t.Fatal("no_message_faults")
	}
	if !IsStartupID("a-b-s3") || !IsStartupID("a-b-sx1") || IsStartupID("a-b-3") || !IsExtraID("a-b-x2") {
		t.Fatal("id kinds")
	}
}

func TestCompare(t *testing.T) {
	orig := sample()
	if f := Compare(orig, orig); f.Score != 1 || f.Total != 5 || f.FaultTotal != 1 {
		t.Fatalf("%+v", f)
	}
	rep := []trace.Event{
		ev(6, trace.MsgDeliver, "a-b-1"),                                            // +1: matched
		ev(20, trace.MsgDelay, "b-a-1", "steps", "3"),                               // shifted
		ev(7, trace.MsgDeliver, "a-b-2"),                                            // changed (was dup)
		ev(9, trace.MsgDrop, "a-b-3", "reason", "partition"),                        // matched
		ev(3, trace.MsgDeliver, "a-b-x1"),                                           // extra
		{Step: 10, Kind: trace.Partition, Meta: map[string]string{"groups": "a|b"}}, // fault at another step
	}
	f := Compare(orig, rep)
	want := Fidelity{Total: 5, Matched: 2, Shifted: 1, Changed: 1, Missed: 1, Extra: 1, FaultTotal: 1, FaultMatched: 0}
	want.Score = 2.0 / 6
	if f != want {
		t.Fatalf("got %+v\nwant %+v", f, want)
	}
	if f.String() == "" || f.MessageScore() != 0.4 || f.FaultScore() != 0 {
		t.Fatal(f.String())
	}
}

func TestNextDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "run", "7")
	os.MkdirAll(d, 0o755)
	if NextDir(d+"/") != d+"-replay-1" {
		t.Fatal(NextDir(d))
	}
	os.MkdirAll(d+"-replay-1", 0o755)
	if NextDir(d) != d+"-replay-2" {
		t.Fatal(NextDir(d))
	}
}
