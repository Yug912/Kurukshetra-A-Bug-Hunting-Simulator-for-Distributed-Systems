package scheduler

import "kurukshetra/pkg/netproxy"

// ReplayGuide holds the decisions and per-step delivery order recorded by an
// earlier run. It is built by pkg/replay (M6).
type ReplayGuide struct {
	Decisions map[string]Decision // msgID -> recorded decision
	Steps     map[string]int64    // msgID -> step the decision was taken
	Order     map[int64][]string  // step -> recorded delivery order of msg IDs
	WaitSteps int64               // how long to wait for a missing recorded message (default 5)
}

type replayState struct{ g *ReplayGuide }

func newReplayState(g *ReplayGuide) *replayState { return &replayState{g: g} }

func (r *replayState) has(id string) bool { _, ok := r.g.Decisions[id]; return ok }

func (r *replayState) decision(id string) Decision { return r.g.Decisions[id] }

func (r *replayState) extra(id string) {}

func (r *replayState) admit(now int64, ms []*netproxy.Message) []*netproxy.Message { return ms }

func (r *replayState) order(now int64, ds []delayed) []delayed { return ds }
