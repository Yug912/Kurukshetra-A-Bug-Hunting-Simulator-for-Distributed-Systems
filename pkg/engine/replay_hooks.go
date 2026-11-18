package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/replay"
	"kurukshetra/pkg/trace"
)

// prepareReplay loads the original run named by opts.ReplayFrom: it builds the
// replay guide, reuses the original fault plan and message-fault setting, and
// points the output at <run-dir>-replay-<n>.
func (x *run) prepareReplay() error {
	orig, err := replay.Load(x.opts.ReplayFrom)
	if err != nil {
		return err
	}
	x.guide = replay.BuildGuide(orig.Trace)
	if x.opts.FaultsFrom == "" {
		x.opts.FaultsFrom = filepath.Join(orig.Dir, core.FaultsFile)
	}
	if replay.NoMessageFaults(orig.Trace) {
		x.opts.NoMessageFaults = true
	}
	x.opts.ReplayFrom = orig.Dir
	x.dir = replay.NextDir(orig.Dir)
	return nil
}

// scoreReplay compares the new trace with the original, writes
// logs/replay-fidelity.json and returns the fidelity score.
func (x *run) scoreReplay() (float64, error) {
	orig, err := trace.ReadFile(filepath.Join(x.opts.ReplayFrom, core.TraceFile))
	if err != nil {
		return 0, err
	}
	rep, err := trace.ReadFile(filepath.Join(x.dir, core.TraceFile))
	if err != nil {
		return 0, err
	}
	f := replay.Compare(orig, rep)
	st := x.sched.ReplayStats()
	out := struct {
		replay.Fidelity
		Scheduler any `json:"scheduler"`
	}{f, st}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(filepath.Join(x.dir, core.LogsDir, "replay-fidelity.json"), append(b, '\n'), 0o644); err != nil {
		return 0, err
	}
	x.logln("%s", f)
	x.logln("replay scheduler: %d recorded decisions used, %d extra messages, %d waits, %d steps with missing messages",
		st.Known, st.Extra, st.Waited, st.Missing)
	if f.Total+f.FaultTotal == 0 {
		return 0, fmt.Errorf("original trace has no decisions to compare")
	}
	return f.Score, nil
}
