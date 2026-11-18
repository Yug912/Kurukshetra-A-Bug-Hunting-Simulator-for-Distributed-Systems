package replay

import (
	"testing"

	"kurukshetra/pkg/trace"
)

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
	diff := CompareDecisions(a, b, 0)
	if diff.Shared != 4 || diff.Same != 3 || diff.OnlyB != 1 || diff.Percent() != 75 || len(diff.Mismatches) != 1 {
		t.Fatalf("%+v", diff)
	}
	// m4 differs only by partition timing: seeded decisions all agree.
	if diff.Seeded != 3 || diff.SeededPercent() != 100 {
		t.Fatalf("%+v", diff)
	}
	b[0] = trace.Event{Kind: trace.MsgDrop, MsgID: "m1"}
	if d := CompareDecisions(a, b, 0); d.SeededSame != 2 {
		t.Fatalf("%+v", d)
	}
}
