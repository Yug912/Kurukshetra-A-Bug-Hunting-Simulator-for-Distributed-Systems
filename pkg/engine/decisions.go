package engine

import (
	"sort"

	"kurukshetra/pkg/trace"
)

// MessageDecision summarizes what happened to one message in a trace:
// "dup", "delay:<steps>", "drop:<reason>" or "deliver", and the step of the decision.
type MessageDecision struct {
	Kind string
	Step int64
}

// Decisions extracts the decision of every message ID in a trace.
// Priority when a message has several events: dup > delay > drop > deliver.
func Decisions(evs []trace.Event) map[string]MessageDecision {
	rank := func(k string) int {
		switch {
		case k == "dup":
			return 4
		case len(k) >= 5 && k[:5] == "delay":
			return 3
		case len(k) >= 4 && k[:4] == "drop":
			return 2
		}
		return 1
	}
	out := map[string]MessageDecision{}
	for _, e := range evs {
		var k string
		switch e.Kind {
		case trace.MsgDup:
			k = "dup"
		case trace.MsgDelay:
			k = "delay:" + e.Meta["steps"]
		case trace.MsgDrop:
			k = "drop:" + e.Meta["reason"]
		case trace.MsgDeliver:
			k = "deliver"
		default:
			continue
		}
		if cur, ok := out[e.MsgID]; !ok || rank(k) > rank(cur.Kind) {
			out[e.MsgID] = MessageDecision{Kind: k, Step: e.Step}
		}
	}
	return out
}

// DecisionDiff compares the decisions of two traces on the message IDs present in both.
type DecisionDiff struct {
	Shared, Same int
	OnlyA, OnlyB int
	Mismatches   []string // first few "id: a vs b"
}

// Percent returns Same/Shared as a percentage (100 if nothing is shared).
func (d DecisionDiff) Percent() float64 {
	if d.Shared == 0 {
		return 100
	}
	return 100 * float64(d.Same) / float64(d.Shared)
}

// CompareDecisions diffs the per-message decisions of two traces.
func CompareDecisions(a, b []trace.Event) DecisionDiff {
	da, db := Decisions(a), Decisions(b)
	var d DecisionDiff
	ids := make([]string, 0, len(da))
	for id := range da {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		y, ok := db[id]
		if !ok {
			d.OnlyA++
			continue
		}
		d.Shared++
		if da[id].Kind == y.Kind {
			d.Same++
		} else if len(d.Mismatches) < 10 {
			d.Mismatches = append(d.Mismatches, id+": "+da[id].Kind+" vs "+y.Kind)
		}
	}
	for id := range db {
		if _, ok := da[id]; !ok {
			d.OnlyB++
		}
	}
	return d
}
