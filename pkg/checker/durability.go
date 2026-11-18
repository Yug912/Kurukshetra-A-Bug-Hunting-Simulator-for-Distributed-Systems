package checker

import (
	"fmt"
	"math"
	"slices"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
)

// Durability is the "durability" checker: an acknowledged write must not vanish.
//
// A write or CAS W that completed ok is lost when all of these hold:
//   - no ok read (including final reads) ever returned W's value;
//   - no other write/CAS on the key (ok or info) could be ordered after W, i.e.
//     none returned (or, for info, might still return) after W was invoked;
//   - the key has at least one ok final read, and none returned W's value.
//
// Values are unique per write, so a read of W's value proves W took effect.
// Keys without an ok final read are not judged; no ok final reads at all → UNKNOWN.
type Durability struct{}

// Name implements core.Checker.
func (Durability) Name() string { return DurabilityName }

type writeOp struct {
	invoke, complete int   // history indices (complete -1 if none)
	call, ret        int64 // wall ns; ret = +inf for info
	key, value       string
	ok               bool
}

// Check implements core.Checker.
func (Durability) Check(ops []history.Op, _ string) core.CheckResult {
	res := core.CheckResult{Checker: DurabilityName}
	pairs, err := history.Pairs(ops)
	if err != nil {
		res.Verdict, res.Reason = core.VerdictUnknown, err.Error()
		return res
	}
	readValues := map[string]map[string]bool{} // key -> values returned by ok reads
	finalOK := map[string]bool{}
	writes := map[string][]writeOp{}
	for _, p := range pairs {
		in, c := p.Invoke, p.Complete
		if in.F == history.Read {
			if c != nil && c.Type == history.OK {
				if readValues[in.Key] == nil {
					readValues[in.Key] = map[string]bool{}
				}
				readValues[in.Key][c.Value] = true
				finalOK[in.Key] = finalOK[in.Key] || in.Final
			}
			continue
		}
		if c != nil && c.Type == history.Fail {
			continue
		}
		w := writeOp{invoke: in.Index, complete: -1, call: in.WallNs, ret: math.MaxInt64, key: in.Key, value: in.Value}
		if c != nil {
			w.complete = c.Index
			if c.Type == history.OK {
				w.ok, w.ret = true, c.WallNs
			}
		}
		writes[in.Key] = append(writes[in.Key], w)
	}
	if len(finalOK) == 0 {
		res.Verdict, res.Reason = core.VerdictUnknown, "no successful final reads"
		return res
	}
	var lost []writeOp
	for key, ws := range writes {
		if !finalOK[key] {
			continue
		}
		for i, w := range ws {
			if !w.ok || readValues[key][w.value] {
				continue
			}
			overwritten := false
			for j, o := range ws {
				if j != i && o.ret > w.call {
					overwritten = true
					break
				}
			}
			if !overwritten {
				lost = append(lost, w)
			}
		}
	}
	if len(lost) == 0 {
		res.Verdict = core.VerdictPass
		return res
	}
	slices.SortFunc(lost, func(a, b writeOp) int { return a.invoke - b.invoke })
	res.Verdict = core.VerdictFail
	res.Reason = fmt.Sprintf("acknowledged write %s=%s lost", lost[0].key, lost[0].value)
	if len(lost) > 1 {
		res.Reason += fmt.Sprintf(" (and %d more)", len(lost)-1)
	}
	for _, w := range lost {
		res.BadOps = append(res.BadOps, w.invoke)
		if w.complete >= 0 {
			res.BadOps = append(res.BadOps, w.complete)
		}
	}
	return res
}
