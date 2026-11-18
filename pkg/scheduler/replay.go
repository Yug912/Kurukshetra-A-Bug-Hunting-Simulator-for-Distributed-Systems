package scheduler

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"kurukshetra/pkg/netproxy"
)

// ReplayGuide holds the decisions and per-step delivery order recorded by an
// earlier run. pkg/replay builds it from a trace.
type ReplayGuide struct {
	Decisions map[string]Decision // msgID -> recorded decision
	Steps     map[string]int64    // msgID -> step the decision was taken
	Order     map[int64][]string  // step -> recorded delivery order of msg IDs
	WaitSteps int64               // max ticks the clock waits for a recorded message per step
}

// Replay matching windows (in steps).
const (
	// MaxHoldSteps: a message may claim a recorded message up to this many
	// steps before its recorded step; it is then held until that step.
	MaxHoldSteps = 50
	// LateSteps: a message may claim a recorded message up to this many steps
	// after its recorded step (it is then decided late: "shifted").
	LateSteps = 30
)

// ReplayStats counts how the replay followed the guide: decisions taken from
// the guide, extra (unrecorded) messages, recorded messages skipped as missed,
// ticks the clock was frozen waiting for recorded messages, and steps that went
// ahead with an expected message still missing.
type ReplayStats struct {
	Known   int   `json:"known"`
	Extra   int   `json:"extra"`
	Skipped int   `json:"skipped"`
	Waited  int64 `json:"frozen_ticks"`
	Missing int   `json:"missing_steps"`
}

type recorded struct {
	id   string
	step int64
}

// replayState matches live messages to recorded ones and holds them until
// their recorded step.
//
// Matching (per link, in arrival order): a message arriving at step a claims
// the next unclaimed recorded message of its link. Recorded messages whose
// step is older than a-LateSteps are skipped as missed first; if the next one
// is more than MaxHoldSteps in the future the message is an extra and gets an
// "<from>-<to>-x<k>" ID. This keeps the two runs aligned across the
// startup boundary, restarts and timer drift, where a fixed per-link counter
// would slip by one message.
type replayState struct {
	g *ReplayGuide

	mu       sync.Mutex // guards links, claimed, skipped, extras (namer runs on proxy goroutines)
	links    map[string][]recorded
	ptr      map[string]int
	claimed  map[string]bool
	skipped  map[string]bool
	extras   map[string]int
	started  map[string]bool
	startupX map[string]int

	byStep  map[int64][]string
	held    []*netproxy.Message
	orderIx map[string]int
	stats   ReplayStats
	missed  map[int64]bool
	frozen  map[int64]int64
}

func linkOf(id string) (string, int, bool) {
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil {
		return "", 0, false
	}
	return id[:i], n, true
}

func newReplayState(g *ReplayGuide) *replayState {
	if g.WaitSteps <= 0 {
		g.WaitSteps = 30
	}
	r := &replayState{
		g: g, links: map[string][]recorded{}, ptr: map[string]int{}, claimed: map[string]bool{},
		skipped: map[string]bool{}, extras: map[string]int{}, started: map[string]bool{}, startupX: map[string]int{}, byStep: map[int64][]string{},
		orderIx: map[string]int{}, missed: map[int64]bool{}, frozen: map[int64]int64{},
	}
	nums := map[string]int{}
	for id, s := range g.Steps {
		r.byStep[s] = append(r.byStep[s], id)
		if lk, n, ok := linkOf(id); ok {
			r.links[lk] = append(r.links[lk], recorded{id, s})
			nums[id] = n
		}
	}
	for lk := range r.links {
		l := r.links[lk]
		sort.Slice(l, func(i, j int) bool { return nums[l[i].id] < nums[l[j].id] })
	}
	for _, ids := range g.Order {
		for i, id := range ids {
			r.orderIx[id] = i
		}
	}
	return r
}

// name is the proxy Namer: it matches a new message to a recorded one.
func (r *replayState) name(from, to string, a int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lk := from + "-" + to
	l := r.links[lk]
	if r.ptr[lk] == 0 && len(l) >= 2 && !r.started[lk] {
		// First run-phase message of the link: if it is closer to a virtual
		// message one gap before the first recorded one, it is the counterpart
		// of original startup traffic, not of recorded message 1.
		gap := l[1].step - l[0].step
		if gap > 0 && l[0].step-a > gap/2 {
			r.startupX[lk]++
			return lk + "-sx" + strconv.Itoa(r.startupX[lk])
		}
	}
	r.started[lk] = true
	for r.ptr[lk] < len(l) {
		rec := l[r.ptr[lk]]
		if rec.step < a-LateSteps {
			r.skipped[rec.id] = true
			r.stats.Skipped++
			r.ptr[lk]++
			continue
		}
		if rec.step > a+MaxHoldSteps {
			break
		}
		// Late, and the following recorded message is nearer: the late one
		// was not reproduced (e.g. it fell into startup here); skip it.
		if p := r.ptr[lk]; rec.step < a && p+1 < len(l) && l[p+1].step-a < a-rec.step {
			r.skipped[rec.id] = true
			r.stats.Skipped++
			r.ptr[lk]++
			continue
		}
		r.ptr[lk]++
		r.claimed[rec.id] = true
		return rec.id
	}
	r.extras[lk]++
	return lk + "-x" + strconv.Itoa(r.extras[lk])
}

// isStartupCounterpart reports whether id ("<link>-sx<k>") was matched to the
// original's startup traffic; such messages pass straight through, as they did.
func isStartupCounterpart(id string) bool {
	i := strings.LastIndexByte(id, '-')
	return i >= 0 && strings.HasPrefix(id[i+1:], "sx")
}

func (r *replayState) has(id string) bool {
	_, ok := r.g.Decisions[id]
	return ok
}

func (r *replayState) decision(id string) Decision {
	r.stats.Known++
	return r.g.Decisions[id]
}

func (r *replayState) extra(string) { r.stats.Extra++ }

// stage parks new arrivals until admit.
func (r *replayState) stage(arrivals []*netproxy.Message) {
	r.held = append(r.held, arrivals...)
}

// pending reports whether recorded message id is still expected: not claimed,
// not skipped, and the next unclaimed message of its link.
func (r *replayState) pending(id string) bool {
	if r.claimed[id] || r.skipped[id] {
		return false
	}
	lk, _, ok := linkOf(id)
	if !ok {
		return false
	}
	l := r.links[lk]
	p := r.ptr[lk]
	return p < len(l) && l[p].id == id
}

// freeze reports whether the clock should stay put for one more tick because
// a message recorded for step next is the next one expected on its link and has
// not arrived. It waits at most WaitSteps ticks per step.
//
// It never waits for a message whose sender has a message held by us: nodes
// usually send synchronously, so that sender is blocked until the clock moves.
func (r *replayState) freeze(next int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	blocked := map[string]bool{}
	for _, m := range r.held {
		blocked[m.From] = true
	}
	for _, id := range r.byStep[next] {
		if !r.pending(id) {
			continue
		}
		if lk, _, ok := linkOf(id); ok {
			if from, _, _ := strings.Cut(lk, "-"); blocked[from] {
				continue
			}
		}
		if r.frozen[next] < r.g.WaitSteps {
			r.frozen[next]++
			r.stats.Waited++
			return true
		}
		if !r.missed[next] {
			r.missed[next] = true
			r.stats.Missing++
		}
		return false
	}
	return false
}

// admit returns the staged messages to decide at step now. A recorded message
// that arrived before its recorded step is held until that step.
func (r *replayState) admit(now int64) []*netproxy.Message {
	all := r.held
	r.held = nil
	var ready []*netproxy.Message
	for _, m := range all {
		if rs, known := r.g.Steps[m.ID]; known && rs > now {
			r.held = append(r.held, m)
			continue
		}
		ready = append(ready, m)
	}
	sort.SliceStable(ready, func(i, j int) bool {
		si, iok := r.g.Steps[ready[i].ID]
		sj, jok := r.g.Steps[ready[j].ID]
		if iok != jok {
			return iok
		}
		if si != sj {
			return si < sj
		}
		return LessID(ready[i].ID, ready[j].ID)
	})
	return ready
}

// order sorts this step's deliveries by their recorded position; messages
// without a recorded position keep their ID order after the recorded ones.
func (r *replayState) order(now int64, ds []delayed) []delayed {
	sort.SliceStable(ds, func(i, j int) bool {
		ii, iok := r.orderIx[ds[i].m.ID]
		jj, jok := r.orderIx[ds[j].m.ID]
		if iok != jok {
			return iok
		}
		return iok && ii < jj
	})
	return ds
}

// ReplayNamer returns the proxy Namer that matches live messages to the
// recorded ones (nil when not replaying).
func (s *Scheduler) ReplayNamer() netproxy.Namer {
	if s.replay == nil {
		return nil
	}
	return s.replay.name
}

// ReplayStats returns how the replay followed its guide (zero if not replaying).
func (s *Scheduler) ReplayStats() ReplayStats {
	if s.replay == nil {
		return ReplayStats{}
	}
	s.replay.mu.Lock()
	defer s.replay.mu.Unlock()
	return s.replay.stats
}
