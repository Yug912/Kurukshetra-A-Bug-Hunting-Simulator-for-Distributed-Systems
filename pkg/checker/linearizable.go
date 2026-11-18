// Package checker judges client histories. It registers the checkers
// "linearizable-kv" (porcupine, per-key register model) and "durability"
// (acknowledged writes must not disappear).
package checker

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/anishathalye/porcupine"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
)

func init() {
	core.RegisterChecker(LinearizableName, func() core.Checker { return &Linearizable{Timeout: 10 * time.Second} })
	core.RegisterChecker(DurabilityName, func() core.Checker { return Durability{} })
}

// Checker names.
const (
	LinearizableName = "linearizable-kv"
	DurabilityName   = "durability"
)

// VisualizationFile is the porcupine HTML written into the run dir on FAIL.
const VisualizationFile = "linearizability.html"

// kvInput is the porcupine input of one op.
type kvInput struct {
	F      history.F
	Key    string
	Value  string // write/cas: new value
	Expect string // cas only
}

// kvOutput is the porcupine output of one op.
type kvOutput struct {
	Type  history.Type // ok or info
	Value string       // read: value read
}

// opMeta links a porcupine operation back to the history.
type opMeta struct {
	Invoke   int // history index of the invoke
	Complete int // history index of the completion, -1 if none
	Node     string
}

// indices returns the history indices of the op.
func (m opMeta) indices() []int {
	if m.Complete < 0 {
		return []int{m.Invoke}
	}
	return []int{m.Invoke, m.Complete}
}

// kvModel is a per-key register. Ops whose outcome is unknown (info) may or may
// not have taken effect, so the model is nondeterministic.
var kvModel = (&porcupine.NondeterministicModel{
	Partition: partitionByKey,
	Init:      func() []any { return []any{history.Missing} },
	Step: func(state, input, output any) []any {
		s, in, out := state.(string), input.(kvInput), output.(kvOutput)
		info := out.Type == history.Info
		switch in.F {
		case history.Read:
			if out.Value == s {
				return []any{s}
			}
			return nil
		case history.Write:
			if info {
				return []any{s, in.Value}
			}
			return []any{in.Value}
		case history.CAS:
			switch {
			case s == in.Expect && info:
				return []any{s, in.Value}
			case s == in.Expect:
				return []any{in.Value}
			case info:
				return []any{s} // did not apply
			}
			return nil
		}
		return nil
	},
	Equal: func(a, b any) bool { return a.(string) == b.(string) },
	DescribeOperation: func(input, output any) string {
		return describeOp(input.(kvInput), output.(kvOutput))
	},
	DescribeState: func(state any) string { return showValue(state.(string)) },
	DescribeOperationMetadata: func(info any) string {
		if m, ok := info.(opMeta); ok {
			return fmt.Sprintf("node %s, history %v", m.Node, m.indices())
		}
		return ""
	},
}).ToModel()

func showValue(v string) string {
	if v == history.Missing {
		return "(missing)"
	}
	return v
}

func describeOp(in kvInput, out kvOutput) string {
	suffix := ""
	if out.Type == history.Info {
		suffix = " (info)"
	}
	switch in.F {
	case history.Read:
		return fmt.Sprintf("R %s→%s%s", in.Key, showValue(out.Value), suffix)
	case history.Write:
		return fmt.Sprintf("W %s=%s%s", in.Key, in.Value, suffix)
	}
	return fmt.Sprintf("CAS %s %s→%s%s", in.Key, showValue(in.Expect), in.Value, suffix)
}

// partitionByKey splits ops per key, keys sorted, op order preserved.
func partitionByKey(ops []porcupine.Operation) [][]porcupine.Operation {
	byKey := map[string][]porcupine.Operation{}
	for _, op := range ops {
		k := op.Input.(kvInput).Key
		byKey[k] = append(byKey[k], op)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]porcupine.Operation, len(keys))
	for i, k := range keys {
		out[i] = byKey[k]
	}
	return out
}

// ToOperations converts a history into porcupine operations: fail completions are
// dropped (the op did not happen); info or missing completions get Return = history
// end + 1 and output type info, except info reads, which constrain nothing and are dropped.
func ToOperations(ops []history.Op) ([]porcupine.Operation, error) {
	pairs, err := history.Pairs(ops)
	if err != nil {
		return nil, err
	}
	var end int64
	for _, op := range ops {
		end = max(end, op.WallNs)
	}
	var out []porcupine.Operation
	for _, p := range pairs {
		in := kvInput{F: p.Invoke.F, Key: p.Invoke.Key, Value: p.Invoke.Value, Expect: p.Invoke.Expect}
		meta := opMeta{Invoke: p.Invoke.Index, Complete: -1, Node: p.Invoke.Node}
		o := porcupine.Operation{ClientId: p.Invoke.Client, Input: in, Call: p.Invoke.WallNs}
		switch {
		case p.Complete == nil || p.Complete.Type == history.Info:
			if p.Invoke.F == history.Read {
				continue
			}
			if p.Complete != nil {
				meta.Complete = p.Complete.Index
			}
			o.Return, o.Output = end+1, kvOutput{Type: history.Info}
		case p.Complete.Type == history.Fail:
			continue
		default:
			meta.Complete = p.Complete.Index
			o.Return = p.Complete.WallNs
			o.Output = kvOutput{Type: history.OK, Value: p.Complete.Value}
		}
		o.Metadata = meta
		out = append(out, o)
	}
	return out, nil
}

// Linearizable is the "linearizable-kv" checker.
type Linearizable struct {
	Timeout time.Duration // porcupine budget; 0 means no limit
}

// Name implements core.Checker.
func (l *Linearizable) Name() string { return LinearizableName }

// Check implements core.Checker.
func (l *Linearizable) Check(ops []history.Op, runDir string) core.CheckResult {
	res := core.CheckResult{Checker: LinearizableName}
	pops, err := ToOperations(ops)
	if err != nil {
		res.Verdict, res.Reason = core.VerdictUnknown, err.Error()
		return res
	}
	verdict, info := porcupine.CheckOperationsVerbose(kvModel, pops, l.Timeout)
	switch verdict {
	case porcupine.Ok:
		res.Verdict = core.VerdictPass
		return res
	case porcupine.Unknown:
		res.Verdict, res.Reason = core.VerdictUnknown, "timeout"
		return res
	}
	res.Verdict = core.VerdictFail
	parts := partitionByKey(pops)
	var reasons []string
	for i, part := range parts {
		bad, reason, ok := explain(part, info.PartialLinearizations()[i])
		if !ok {
			continue
		}
		reasons = append(reasons, reason)
		res.BadOps = append(res.BadOps, bad...)
	}
	if len(reasons) == 0 {
		reasons = []string{"history is not linearizable"}
	}
	res.Reason = reasons[0]
	if len(reasons) > 1 {
		res.Reason += fmt.Sprintf(" (and %d more key(s))", len(reasons)-1)
	}
	slices.Sort(res.BadOps)
	res.BadOps = slices.Compact(res.BadOps)
	if runDir != "" {
		if err := porcupine.VisualizePath(kvModel, info, filepath.Join(runDir, VisualizationFile)); err == nil {
			res.Artifacts = map[string]string{"linearizability": VisualizationFile}
		}
	}
	return res
}

// explain finds why one key's ops are not linearizable. The longest partial
// linearization is the longest linearizable prefix; of the ops left out, the one
// that must return first is the op that cannot be linearized. It is blamed
// together with every op overlapping it in time. ok is false if the key is fine.
func explain(part []porcupine.Operation, partials [][]int) (bad []int, reason string, ok bool) {
	longest := []int{}
	for _, p := range partials {
		if len(p) > len(longest) {
			longest = p
		}
	}
	if len(longest) == len(part) {
		return nil, "", false
	}
	in := map[int]bool{}
	for _, id := range longest {
		in[id] = true
	}
	culprit := -1
	for id, op := range part {
		if !in[id] && (culprit < 0 || op.Return < part[culprit].Return) {
			culprit = id
		}
	}
	c := part[culprit]
	for id, op := range part {
		if id == culprit || (!in[id] && op.Call <= c.Return && c.Call <= op.Return) {
			bad = append(bad, op.Metadata.(opMeta).indices()...)
		}
	}
	expected := statesAfter(part, longest)
	cin, cout, meta := c.Input.(kvInput), c.Output.(kvOutput), c.Metadata.(opMeta)
	exp := make([]string, len(expected))
	for i, s := range expected {
		exp[i] = showValue(s)
	}
	switch cin.F {
	case history.Read:
		reason = fmt.Sprintf("stale read of %s on %s: got %s, expected %s", cin.Key, meta.Node, showValue(cout.Value), strings.Join(exp, " or "))
	case history.Write:
		reason = fmt.Sprintf("write %s=%s on %s cannot be linearized (state %s)", cin.Key, cin.Value, meta.Node, strings.Join(exp, " or "))
	default:
		reason = fmt.Sprintf("cas %s %s→%s on %s cannot be linearized (state %s)", cin.Key, showValue(cin.Expect), cin.Value, meta.Node, strings.Join(exp, " or "))
	}
	return bad, reason, true
}

// statesAfter replays a linearization and returns the possible register states.
func statesAfter(part []porcupine.Operation, order []int) []string {
	state := kvModel.Init()
	for _, id := range order {
		_, state = kvModel.Step(state, part[id].Input, part[id].Output)
	}
	var out []string
	for _, s := range state.([]any) {
		out = append(out, s.(string))
	}
	sort.Strings(out)
	return out
}
