// Command gen-sample deterministically writes testdata/sample-run/: a complete
// run folder (scenario "sample", seed 42, nodes n1,n2,n3) that tells a small story:
// n1 is partitioned away, n2 becomes leader, n1 serves a stale read of k0.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/trace"
)

const (
	seed     = 42
	scName   = "sample"
	stepMs   = 2
	steps    = 1000
	scenYAML = `name: sample
nodes: 3
image: kuru/toykv
mode: http
duration_steps: 1000
step_ms: 2
quiet_steps: 100
workload:
  type: kv
  clients: 3
  keys: 1
faults:
  partition_every: [100, 200]
  partition_len: [300, 500]
  crash_every: [500, 700]
  crash_len: [50, 150]
checkers: [linearizable-kv]
`
)

var startedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// wall maps a step (plus a small tiebreak) to a deterministic wall clock in nanos.
func wall(step int64, tie int) int64 {
	return startedAt.UnixNano() + step*stepMs*int64(time.Millisecond) + int64(tie)*1000
}

type evb struct{ evs []trace.Event }

func (b *evb) add(e trace.Event) { b.evs = append(b.evs, e) }

func (b *evb) msg(step, lat int64, from, to, path string, n *map[string]int) {
	key := from + "-" + to
	(*n)[key]++
	id := fmt.Sprintf("%s-%d", key, (*n)[key])
	m := map[string]string{"path": path}
	b.add(trace.Event{Step: step, Kind: trace.MsgSend, From: from, To: to, MsgID: id, Size: 64, Meta: m})
	b.add(trace.Event{Step: step + lat, Kind: trace.MsgDeliver, From: from, To: to, MsgID: id, Size: 64, Meta: m})
}

func buildTrace() []trace.Event {
	b := &evb{}
	n := map[string]int{}
	next := func(from, to string) string {
		k := from + "-" + to
		n[k]++
		return fmt.Sprintf("%s-%d", k, n[k])
	}
	ae, rv := "/peer/append_entries", "/peer/request_vote"

	b.add(trace.Event{Step: 0, Kind: trace.RunStart, Meta: map[string]string{"mode": "http"}})
	for _, s := range []int64{10, 50, 90} {
		b.msg(s, 2, "n1", "n2", ae, &n)
		b.msg(s, 3, "n1", "n3", ae, &n)
	}
	b.add(trace.Event{Step: 120, Kind: trace.Partition, Meta: map[string]string{"groups": "n1|n2,n3"}})
	for _, s := range []int64{130, 170} {
		id := next("n1", "n2")
		b.add(trace.Event{Step: s, Kind: trace.MsgSend, From: "n1", To: "n2", MsgID: id, Size: 64, Meta: map[string]string{"path": ae}})
		b.add(trace.Event{Step: s + 1, Kind: trace.MsgDrop, From: "n1", To: "n2", MsgID: id, Size: 64, Meta: map[string]string{"path": ae, "reason": "partition"}})
	}
	// Election between n2 and n3.
	b.msg(200, 4, "n2", "n3", rv, &n)
	b.msg(230, 3, "n3", "n2", rv, &n)
	id := next("n2", "n3")
	b.add(trace.Event{Step: 245, Kind: trace.MsgSend, From: "n2", To: "n3", MsgID: id, Size: 48, Meta: map[string]string{"path": rv}})
	b.add(trace.Event{Step: 246, Kind: trace.MsgDelay, From: "n2", To: "n3", MsgID: id, Size: 48, Meta: map[string]string{"path": rv, "steps": "15"}})
	b.add(trace.Event{Step: 261, Kind: trace.MsgDeliver, From: "n2", To: "n3", MsgID: id, Size: 48, Meta: map[string]string{"path": rv}})
	id = next("n3", "n2")
	b.add(trace.Event{Step: 250, Kind: trace.MsgSend, From: "n3", To: "n2", MsgID: id, Size: 48, Meta: map[string]string{"path": rv}})
	b.add(trace.Event{Step: 251, Kind: trace.MsgDup, From: "n3", To: "n2", MsgID: id, Size: 48, Meta: map[string]string{"path": rv}})
	b.add(trace.Event{Step: 253, Kind: trace.MsgDeliver, From: "n3", To: "n2", MsgID: id, Size: 48, Meta: map[string]string{"path": rv}})
	b.add(trace.Event{Step: 255, Kind: trace.MsgDeliver, From: "n3", To: "n2", MsgID: id, Size: 48, Meta: map[string]string{"path": rv}})
	for _, s := range []int64{300, 400, 500} {
		b.msg(s, 2, "n2", "n3", ae, &n)
	}
	b.add(trace.Event{Step: 600, Kind: trace.Heal})
	b.msg(620, 2, "n2", "n1", ae, &n)
	b.msg(620, 2, "n2", "n3", ae, &n)
	b.add(trace.Event{Step: 700, Kind: trace.Crash, Node: "n3"})
	b.msg(720, 2, "n2", "n1", ae, &n)
	id = next("n2", "n3")
	b.add(trace.Event{Step: 720, Kind: trace.MsgSend, From: "n2", To: "n3", MsgID: id, Size: 64, Meta: map[string]string{"path": ae}})
	b.add(trace.Event{Step: 721, Kind: trace.MsgDrop, From: "n2", To: "n3", MsgID: id, Size: 64, Meta: map[string]string{"path": ae, "reason": "crash"}})
	b.add(trace.Event{Step: 800, Kind: trace.Restart, Node: "n3"})
	for _, s := range []int64{850, 950} {
		b.msg(s, 2, "n2", "n1", ae, &n)
		b.msg(s, 3, "n2", "n3", ae, &n)
	}
	b.add(trace.Event{Step: steps, Kind: trace.RunEnd})

	sort.SliceStable(b.evs, func(i, j int) bool { return b.evs[i].Step < b.evs[j].Step })
	for i := range b.evs {
		b.evs[i].WallNs = wall(b.evs[i].Step, i)
	}
	return b.evs
}

func buildHistory() []history.Op {
	w, r := history.Write, history.Read
	ops := []history.Op{
		{Client: 0, Type: history.Invoke, F: w, Key: "k0", Value: "c0-1", Node: "n1", Step: 10},
		{Client: 0, Type: history.OK, F: w, Key: "k0", Value: "c0-1", Node: "n1", Step: 20},
		{Client: 1, Type: history.Invoke, F: w, Key: "k0", Value: "c1-1", Node: "n2", Step: 300},
		{Client: 1, Type: history.OK, F: w, Key: "k0", Value: "c1-1", Node: "n2", Step: 320},
		{Client: 2, Type: history.Invoke, F: r, Key: "k0", Node: "n1", Step: 400},
		{Client: 2, Type: history.OK, F: r, Key: "k0", Value: "c0-1", Node: "n1", Step: 410}, // stale read
		{Client: 0, Type: history.Invoke, F: r, Key: "k0", Node: "n2", Step: 650},
		{Client: 0, Type: history.OK, F: r, Key: "k0", Value: "c1-1", Node: "n2", Step: 660},
	}
	for i, node := range []string{"n1", "n2", "n3"} {
		s := int64(900 + 20*i)
		ops = append(ops,
			history.Op{Client: 0, Type: history.Invoke, F: r, Key: "k0", Node: node, Step: s, Final: true},
			history.Op{Client: 0, Type: history.OK, F: r, Key: "k0", Value: "c1-1", Node: node, Step: s + 5, Final: true},
		)
	}
	for i := range ops {
		ops[i].WallNs = wall(ops[i].Step, i)
	}
	return ops
}

var logs = map[string]string{
	"n1": "step 0 node n1 starting as follower\nstep 60 n1 became leader term 1\nstep 120 n1 lost contact with peers\nstep 410 n1 served read k0=c0-1\nstep 620 n1 stepped down, leader n2 term 2\n",
	"n2": "step 0 node n2 starting as follower\nstep 200 n2 starting election term 2\nstep 255 n2 became leader term 2\nstep 320 n2 committed k0=c1-1\n",
	"n3": "step 0 node n3 starting as follower\nstep 204 n3 voted for n2 term 2\nstep 700 n3 killed\nstep 800 n3 restarted, recovering from /data\n",
}

func run(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, core.LogsDir), 0o755); err != nil {
		return err
	}
	tw, err := trace.NewWriter(filepath.Join(dir, core.TraceFile))
	if err != nil {
		return err
	}
	for _, e := range buildTrace() {
		tw.Append(e)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	hr, err := history.NewFileRecorder(filepath.Join(dir, core.HistoryFile))
	if err != nil {
		return err
	}
	for _, op := range buildHistory() {
		hr.Record(op)
	}
	if err := hr.Close(); err != nil {
		return err
	}
	faults := []core.Fault{
		{Step: 120, Kind: trace.Partition, Groups: [][]string{{"n1"}, {"n2", "n3"}}, DurationSteps: 480},
		{Step: 700, Kind: trace.Crash, Nodes: []string{"n3"}, DurationSteps: 100},
	}
	if err := core.WriteFaults(filepath.Join(dir, core.FaultsFile), faults); err != nil {
		return err
	}
	res := core.Result{Seed: seed, Scenario: scName, Verdict: core.VerdictFail, Checks: []core.CheckResult{{
		Checker: "linearizable-kv", Verdict: core.VerdictFail, Reason: "stale read of k0 on n1", BadOps: []int{4, 5},
	}}}
	if err := core.WriteResult(dir, res); err != nil {
		return err
	}
	meta := core.Meta{
		Scenario: scName, Seed: seed, Nodes: []string{"n1", "n2", "n3"}, Mode: "http", Runner: "docker",
		StartedAt: startedAt, DurationMs: steps * stepMs, Steps: steps, Version: "sample",
	}
	if err := core.WriteMeta(dir, meta); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, core.ScenarioFile), []byte(scenYAML), 0o644); err != nil {
		return err
	}
	for node, text := range logs {
		if err := os.WriteFile(filepath.Join(dir, core.LogsDir, node+".log"), []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	out := flag.String("out", filepath.Join("testdata", "sample-run"), "output run folder")
	flag.Parse()
	if err := run(*out); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", *out)
}
