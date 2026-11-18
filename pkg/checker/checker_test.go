package checker

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
)

// spec is one hand-written op: [start, end] in abstract time units; typ is the completion
// type ("" = never completed). For reads, val is the value read.
type spec struct {
	client     int
	f          history.F
	key, val   string
	expect     string
	typ        history.Type
	start, end int64
	final      bool
}

// build turns specs into a history ordered by time with indices assigned.
func build(specs ...spec) []history.Op {
	type ev struct {
		t  int64
		op history.Op
	}
	var evs []ev
	for _, s := range specs {
		inv := history.Op{Client: s.client, Type: history.Invoke, F: s.f, Key: s.key, Expect: s.expect, Node: "n1", Final: s.final}
		if s.f != history.Read {
			inv.Value = s.val
		}
		evs = append(evs, ev{s.start, inv})
		if s.typ != "" {
			c := inv
			c.Type = s.typ
			if s.f == history.Read && s.typ != history.OK {
				c.Value = ""
			} else {
				c.Value = s.val
			}
			evs = append(evs, ev{s.end, c})
		}
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].t < evs[j].t })
	out := make([]history.Op, len(evs))
	for i, e := range evs {
		e.op.Index, e.op.WallNs, e.op.Step = i, e.t*1000+int64(i), e.t
		out[i] = e.op
	}
	return out
}

func w(c int, k, v string, typ history.Type, s, e int64) spec {
	return spec{client: c, f: history.Write, key: k, val: v, typ: typ, start: s, end: e}
}
func r(c int, k, v string, s, e int64) spec {
	return spec{client: c, f: history.Read, key: k, val: v, typ: history.OK, start: s, end: e}
}
func cas(c int, k, exp, v string, typ history.Type, s, e int64) spec {
	return spec{client: c, f: history.CAS, key: k, expect: exp, val: v, typ: typ, start: s, end: e}
}
func final(c int, k, v string, s, e int64) spec {
	x := r(c, k, v, s, e)
	x.final = true
	return x
}

func lin(t *testing.T, ops []history.Op) core.CheckResult {
	t.Helper()
	c, err := core.NewChecker(LinearizableName)
	if err != nil {
		t.Fatal(err)
	}
	return c.Check(ops, "")
}

func dur(t *testing.T, ops []history.Op) core.CheckResult {
	t.Helper()
	c, err := core.NewChecker(DurabilityName)
	if err != nil {
		t.Fatal(err)
	}
	return c.Check(ops, "")
}

func TestLinearizable(t *testing.T) {
	const ok, info, fail = history.OK, history.Info, history.Fail
	cases := []struct {
		name   string
		ops    []history.Op
		want   string
		reason string
	}{
		{"empty", nil, core.VerdictPass, ""},
		{"read missing", build(r(0, "k0", "", 0, 1)), core.VerdictPass, ""},
		{"valid concurrent", build(
			w(0, "k0", "a", ok, 0, 10),
			w(1, "k0", "b", ok, 2, 12), // concurrent: either order
			r(2, "k0", "a", 5, 15),     // a before b
			r(2, "k0", "b", 16, 18),
			w(0, "k1", "x", ok, 11, 13),
			r(1, "k1", "x", 14, 20),
		), core.VerdictPass, ""},
		{"stale read", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", ok, 2, 3),
			r(2, "k0", "a", 4, 5),
		), core.VerdictFail, "stale read of k0 on n1: got a, expected b"},
		{"read of never-written value", build(r(0, "k0", "zz", 0, 1)), core.VerdictFail, "stale read of k0"},
		{"failed write ignored", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", fail, 2, 3),
			r(2, "k0", "a", 4, 5),
		), core.VerdictPass, ""},
		{"info write happened", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", info, 2, 3),
			r(2, "k0", "b", 10, 11),
		), core.VerdictPass, ""},
		{"info write did not happen", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", info, 2, 3),
			r(2, "k0", "a", 10, 11),
		), core.VerdictPass, ""},
		{"info write seen then unseen", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", info, 2, 3),
			r(2, "k0", "b", 10, 11),
			r(2, "k0", "a", 12, 13),
		), core.VerdictFail, "stale read"},
		{"never-completed write may happen", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", "", 2, 0),
			r(2, "k0", "b", 10, 11),
		), core.VerdictPass, ""},
		{"info read dropped", build(
			w(0, "k0", "a", ok, 0, 1),
			spec{client: 1, f: history.Read, key: "k0", typ: info, start: 2, end: 3},
		), core.VerdictPass, ""},
		{"cas ok", build(
			w(0, "k0", "a", ok, 0, 1),
			cas(1, "k0", "a", "b", ok, 2, 3),
			r(2, "k0", "b", 4, 5),
		), core.VerdictPass, ""},
		{"cas on missing key", build(
			cas(1, "k0", "", "b", ok, 0, 1),
			r(2, "k0", "b", 4, 5),
		), core.VerdictPass, ""},
		{"cas ok with wrong expect", build(
			w(0, "k0", "a", ok, 0, 1),
			cas(1, "k0", "zzz", "b", ok, 2, 3),
		), core.VerdictFail, "cas k0 zzz→b on n1 cannot be linearized (state a)"},
		{"cas mismatch (fail) ignored", build(
			w(0, "k0", "a", ok, 0, 1),
			cas(1, "k0", "zzz", "b", fail, 2, 3),
			r(2, "k0", "a", 4, 5),
		), core.VerdictPass, ""},
		{"cas info maybe applied", build(
			w(0, "k0", "a", ok, 0, 1),
			cas(1, "k0", "a", "b", info, 2, 3),
			r(2, "k0", "b", 4, 5),
		), core.VerdictPass, ""},
		{"cas info maybe not applied", build(
			w(0, "k0", "a", ok, 0, 1),
			cas(1, "k0", "a", "b", info, 2, 3),
			r(2, "k0", "a", 4, 5),
		), core.VerdictPass, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := lin(t, tc.ops)
			if res.Verdict != tc.want || !strings.Contains(res.Reason, tc.reason) {
				t.Fatalf("got %s %q, want %s %q", res.Verdict, res.Reason, tc.want, tc.reason)
			}
			if res.Verdict == core.VerdictFail && len(res.BadOps) == 0 {
				t.Fatal("FAIL without bad ops")
			}
		})
	}
}

func TestLinearizableBadOpsOverlap(t *testing.T) {
	ops := build(
		w(0, "k0", "a", history.OK, 0, 1),
		w(1, "k0", "b", history.OK, 2, 3),
		r(2, "k0", "a", 4, 8), // culprit
		r(3, "k0", "a", 5, 9), // overlaps the culprit and is also unlinearizable
		r(4, "k0", "b", 20, 21),
	)
	res := lin(t, ops)
	if res.Verdict != core.VerdictFail {
		t.Fatal(res)
	}
	// ops 4/6 (client 2) and 5/7 (client 3) are blamed; earlier writes and the later read are not.
	if !slices.Equal(res.BadOps, []int{4, 5, 6, 7}) {
		t.Fatalf("bad ops %v", res.BadOps)
	}
}

func TestLinearizableBadHistory(t *testing.T) {
	ops := []history.Op{{Index: 0, Client: 0, Type: history.OK}}
	if res := lin(t, ops); res.Verdict != core.VerdictUnknown {
		t.Fatal(res)
	}
}

func copySample(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "sample-run")
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestSampleRun(t *testing.T) {
	dir := copySample(t)
	res, err := core.CheckRun(dir, []string{LinearizableName, DurabilityName})
	if err != nil {
		t.Fatal(err)
	}
	l, d := res.Checks[0], res.Checks[1]
	if res.Verdict != core.VerdictFail || l.Verdict != core.VerdictFail {
		t.Fatalf("%+v", res)
	}
	if !slices.Contains(l.BadOps, 4) || !slices.Contains(l.BadOps, 5) {
		t.Fatalf("bad ops %v", l.BadOps)
	}
	if l.Reason != "stale read of k0 on n1: got c0-1, expected c1-1" {
		t.Fatalf("reason %q", l.Reason)
	}
	if l.Artifacts["linearizability"] != VisualizationFile {
		t.Fatalf("artifacts %v", l.Artifacts)
	}
	if fi, err := os.Stat(filepath.Join(dir, VisualizationFile)); err != nil || fi.Size() < 1000 {
		t.Fatalf("visualization: %v", err)
	}
	if d.Verdict != core.VerdictPass {
		t.Fatalf("durability %+v", d)
	}
}

func TestDurability(t *testing.T) {
	const ok, info = history.OK, history.Info
	cases := []struct {
		name   string
		ops    []history.Op
		want   string
		reason string
		bad    []int
	}{
		{"no final reads", build(w(0, "k0", "a", ok, 0, 1)), core.VerdictUnknown, "no successful final reads", nil},
		{"write survives", build(
			w(0, "k0", "a", ok, 0, 1),
			final(9, "k0", "a", 10, 11),
		), core.VerdictPass, "", nil},
		{"lost acknowledged write", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k1", "x", ok, 2, 3),
			final(9, "k0", "", 10, 11),
			final(9, "k1", "x", 12, 13),
		), core.VerdictFail, "acknowledged write k0=a lost", []int{0, 1}},
		{"overwritten later", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", ok, 2, 3),
			final(9, "k0", "b", 10, 11),
		), core.VerdictPass, "", nil},
		{"concurrent overwrite", build(
			w(0, "k0", "a", ok, 0, 10),
			w(1, "k0", "b", ok, 2, 12),
			final(9, "k0", "a", 20, 21),
		), core.VerdictPass, "", nil},
		{"info write may overwrite", build(
			w(0, "k0", "a", ok, 0, 1),
			w(1, "k0", "b", info, 2, 3),
			final(9, "k0", "b", 10, 11),
		), core.VerdictPass, "", nil},
		{"seen by a read then lost", build(
			w(0, "k0", "a", ok, 0, 1),
			r(1, "k0", "a", 2, 3),
			final(9, "k0", "", 10, 11),
		), core.VerdictPass, "", nil},
		{"lost cas", build(
			cas(0, "k0", "", "c", ok, 0, 1),
			final(9, "k0", "", 10, 11),
		), core.VerdictFail, "acknowledged write k0=c lost", []int{0, 1}},
		{"key without final read not judged", build(
			w(0, "k0", "a", ok, 0, 1),
			final(9, "k1", "", 10, 11),
		), core.VerdictPass, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := dur(t, tc.ops)
			if res.Verdict != tc.want || !strings.Contains(res.Reason, tc.reason) {
				t.Fatalf("got %s %q, want %s %q", res.Verdict, res.Reason, tc.want, tc.reason)
			}
			if tc.bad != nil && !slices.Equal(res.BadOps, tc.bad) {
				t.Fatalf("bad ops %v want %v", res.BadOps, tc.bad)
			}
		})
	}
}

// simulate produces a random linearizable history: each op takes effect at a
// linearization point inside its [call, return] interval, applied in that order.
func simulate(n int, seed uint64) []history.Op {
	rnd := rand.New(rand.NewPCG(seed, seed))
	state := map[string]string{}
	var specs []spec
	clientFree := []int64{} // per client: time its last op returned
	written := 0
	for i := 0; i < n; i++ {
		lp := int64(i) * 10
		call, ret := lp-rnd.Int64N(40), lp+rnd.Int64N(40)
		c := -1
		for id, free := range clientFree {
			if free < call {
				c = id
				break
			}
		}
		if c < 0 {
			c = len(clientFree)
			clientFree = append(clientFree, 0)
		}
		clientFree[c] = ret
		key := []string{"k0", "k1", "k2"}[rnd.IntN(3)]
		switch x := rnd.Float64(); {
		case x < 0.5:
			specs = append(specs, r(c, key, state[key], call, ret))
		case x < 0.8:
			written++
			v := "v" + string(rune('a'+written%26)) + time.Duration(written).String()
			state[key] = v
			specs = append(specs, w(c, key, v, history.OK, call, ret))
		default:
			written++
			v := "c" + time.Duration(written).String()
			exp := state[key]
			if rnd.IntN(4) == 0 {
				exp = "nope"
			}
			if exp == state[key] {
				state[key] = v
				specs = append(specs, cas(c, key, exp, v, history.OK, call, ret))
			} else {
				specs = append(specs, cas(c, key, exp, v, history.Fail, call, ret))
			}
		}
	}
	return build(specs...)
}

func TestLinearizableLargeRandomHistory(t *testing.T) {
	ops := simulate(5000, 7)
	if _, err := history.Pairs(ops); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res := lin(t, ops)
	d := time.Since(start)
	t.Logf("5000 ops checked in %v", d)
	if res.Verdict != core.VerdictPass {
		t.Fatalf("%+v", res)
	}
	if d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	// Corrupt one read: the checker must now fail.
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i].F == history.Read && ops[i].Type == history.OK {
			ops[i].Value = "corrupt"
			break
		}
	}
	if res := lin(t, ops); res.Verdict != core.VerdictFail {
		t.Fatalf("corrupted history: %+v", res)
	}
}
