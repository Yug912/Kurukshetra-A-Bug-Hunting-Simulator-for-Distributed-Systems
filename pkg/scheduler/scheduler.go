package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/netproxy"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

// FaultExecutor applies and heals faults.
type FaultExecutor interface {
	Apply(ctx context.Context, f core.Fault) error
	Heal(ctx context.Context, f core.Fault) error
	HealAll(ctx context.Context) error
}

// Bus is the part of the proxy the scheduler uses (an interface so tests can fake it).
type Bus interface {
	Incoming() <-chan *netproxy.Message
	Blocked(from, to string) bool
	Drop(m *netproxy.Message, reason string)
	DeliverAsync(m *netproxy.Message, wg *sync.WaitGroup, onErr func(error))
	DuplicateAsync(m *netproxy.Message, wg *sync.WaitGroup, onErr func(error))
	Emit(e trace.Event)
}

// Options tunes a Scheduler.
type Options struct {
	NoMessageFaults bool         // always deliver (partitions still apply)
	Replay          *ReplayGuide // force recorded decisions (M6)
	Clock           *Clock       // shared clock; a new one if nil
	Tick            time.Duration
	// OnError receives non-fatal errors (fault execution, forwarding). May be nil.
	OnError func(error)
}

type delayed struct {
	m       *netproxy.Message
	release int64
	fate    Fate // Deliver or Duplicate (replay may record a delayed dup)
}

// Scheduler drives one run.
type Scheduler struct {
	sc    scenario.Scenario
	src   *rng.Source
	bus   Bus
	tw    *trace.Writer
	exec  FaultExecutor
	plan  []core.Fault
	opts  Options
	clock *Clock

	delayed  []delayed
	quiet    bool
	inflight sync.WaitGroup
	stats    Stats
	replay   *replayState
}

// Stats counts decisions taken.
type Stats struct {
	Delivered, Dropped, Duplicated, Delayed, Partitioned int
}

// New builds a scheduler over a real proxy.
func New(sc scenario.Scenario, src *rng.Source, p *netproxy.Proxy, tw *trace.Writer, exec FaultExecutor, plan []core.Fault, opts Options) *Scheduler {
	return NewWithBus(sc, src, p, tw, exec, plan, opts)
}

// NewWithBus builds a scheduler over any Bus.
func NewWithBus(sc scenario.Scenario, src *rng.Source, bus Bus, tw *trace.Writer, exec FaultExecutor, plan []core.Fault, opts Options) *Scheduler {
	if opts.Clock == nil {
		opts.Clock = NewClock()
	}
	if opts.Tick <= 0 {
		opts.Tick = time.Duration(max(sc.StepMs, 1)) * time.Millisecond
	}
	p := append([]core.Fault(nil), plan...)
	s := &Scheduler{sc: sc, src: src, bus: bus, tw: tw, exec: exec, plan: p, opts: opts, clock: opts.Clock}
	if opts.Replay != nil {
		s.replay = newReplayState(opts.Replay)
	}
	return s
}

// Step implements core.StepClock.
func (s *Scheduler) Step() int64 { return s.clock.Step() }

// Stats returns decision counts so far (call after Run/Drain returned).
func (s *Scheduler) Stats() Stats { return s.stats }

// Run ticks until the clock reaches untilStep or ctx is done.
func (s *Scheduler) Run(ctx context.Context, untilStep int64) error {
	return s.loop(ctx, untilStep, false)
}

// Drain runs `steps` more steps delivering every message and releasing delayed
// ones, with no faults and no message faults (the quiet period).
func (s *Scheduler) Drain(ctx context.Context, steps int64) error {
	return s.loop(ctx, s.Step()+steps, true)
}

// Wait blocks until all in-flight deliveries have finished.
func (s *Scheduler) Wait() { s.inflight.Wait() }

func (s *Scheduler) loop(ctx context.Context, until int64, quiet bool) error {
	s.quiet = quiet
	t := time.NewTicker(s.opts.Tick)
	defer t.Stop()
	for s.Step() < until {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		s.tick(ctx)
	}
	return nil
}

func (s *Scheduler) onErr(err error) {
	if err != nil && s.opts.OnError != nil {
		s.opts.OnError(err)
	}
}

// tick advances one step; the order of work inside is part of the determinism contract.
func (s *Scheduler) tick(ctx context.Context) {
	now := s.clock.advance()
	if !s.quiet {
		s.fireFaults(ctx, now)
	}
	var deliver []delayed
	// Release delayed messages (no second draw).
	keep := s.delayed[:0]
	for _, d := range s.delayed {
		if d.release <= now || s.quiet {
			deliver = append(deliver, d)
		} else {
			keep = append(keep, d)
		}
	}
	s.delayed = keep
	// Collect new arrivals, sorted by ID.
	arrived := s.collect()
	if s.replay != nil {
		arrived = s.replay.admit(now, arrived)
	}
	for _, m := range arrived {
		if d, ok := s.decide(now, m); ok {
			deliver = append(deliver, d)
		}
	}
	s.dispatch(now, deliver)
}

func (s *Scheduler) collect() []*netproxy.Message {
	var out []*netproxy.Message
	for {
		select {
		case m := <-s.bus.Incoming():
			out = append(out, m)
		default:
			sort.Slice(out, func(i, j int) bool { return LessID(out[i].ID, out[j].ID) })
			return out
		}
	}
}

// LessID orders message IDs "<from>-<to>-<n>" by link, then numerically by n.
func LessID(a, b string) bool {
	la, na := splitID(a)
	lb, nb := splitID(b)
	if la != lb {
		return la < lb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

func splitID(id string) (string, int) {
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return id, 0
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil {
		return id, 0
	}
	return id[:i], n
}

// decide handles one new message; it returns a delivery to dispatch this step, if any.
func (s *Scheduler) decide(now int64, m *netproxy.Message) (delayed, bool) {
	if s.bus.Blocked(m.From, m.To) {
		s.stats.Partitioned++
		s.bus.Drop(m, "partition")
		return delayed{}, false
	}
	var d Decision
	switch {
	case s.quiet || s.opts.NoMessageFaults:
		d = Decision{Fate: Deliver}
	case s.replay != nil && s.replay.has(m.ID):
		d = s.replay.decision(m.ID)
	default:
		d = Decide(s.src, s.sc.Faults, m.ID)
		if s.replay != nil {
			s.replay.extra(m.ID)
		}
	}
	switch d.Fate {
	case Drop:
		s.stats.Dropped++
		s.bus.Drop(m, "")
		return delayed{}, false
	case Delay:
		s.stats.Delayed++
		s.bus.Emit(trace.Event{Step: now, Kind: trace.MsgDelay, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size,
			Meta: map[string]string{"path": m.Path, "steps": strconv.FormatInt(d.Steps, 10)}})
		s.delayed = append(s.delayed, delayed{m: m, release: now + d.Steps, fate: Deliver})
		return delayed{}, false
	case Duplicate:
		return delayed{m: m, fate: Duplicate}, true
	}
	return delayed{m: m, fate: Deliver}, true
}

// dispatch delivers in a PRNG-shuffled order (or the recorded order on replay).
func (s *Scheduler) dispatch(now int64, ds []delayed) {
	if len(ds) == 0 {
		return
	}
	sort.Slice(ds, func(i, j int) bool { return LessID(ds[i].m.ID, ds[j].m.ID) })
	if s.replay != nil {
		ds = s.replay.order(now, ds)
	} else {
		r := s.src.Keyed(rng.StreamMessages, fmt.Sprintf("order-%d", now))
		r.Shuffle(len(ds), func(i, j int) { ds[i], ds[j] = ds[j], ds[i] })
	}
	for _, d := range ds {
		// A delayed message released into a partition is dropped.
		if s.bus.Blocked(d.m.From, d.m.To) {
			s.stats.Partitioned++
			s.bus.Drop(d.m, "partition")
			continue
		}
		if d.fate == Duplicate {
			s.stats.Duplicated++
			s.bus.DuplicateAsync(d.m, &s.inflight, s.onErr)
			continue
		}
		s.stats.Delivered++
		s.bus.DeliverAsync(d.m, &s.inflight, s.onErr)
	}
}

// faultKey orders faults by (Step, Kind, Nodes, Groups).
func faultKey(f core.Fault) string {
	var g []string
	for _, grp := range f.Groups {
		g = append(g, strings.Join(grp, ","))
	}
	return fmt.Sprintf("%020d|%s|%s|%s", f.Step, f.Kind, strings.Join(f.Nodes, ","), strings.Join(g, "|"))
}

func (s *Scheduler) fireFaults(ctx context.Context, now int64) {
	var heal, apply []core.Fault
	for _, f := range s.plan {
		if f.DurationSteps > 0 && f.Step+f.DurationSteps == now {
			heal = append(heal, f)
		}
		if f.Step == now {
			apply = append(apply, f)
		}
	}
	byKey := func(fs []core.Fault) {
		sort.Slice(fs, func(i, j int) bool { return faultKey(fs[i]) < faultKey(fs[j]) })
	}
	byKey(heal)
	byKey(apply)
	for _, f := range heal {
		if err := s.exec.Heal(ctx, f); err != nil {
			s.note(now, "heal failed: "+core.DescribeFault(f), err)
		}
	}
	for _, f := range apply {
		if err := s.exec.Apply(ctx, f); err != nil {
			s.note(now, "fault failed: "+core.DescribeFault(f), err)
		}
	}
}

func (s *Scheduler) note(now int64, msg string, err error) {
	s.bus.Emit(trace.Event{Step: now, Kind: trace.Note, Meta: map[string]string{"msg": msg, "error": err.Error()}})
	s.onErr(fmt.Errorf("%s: %w", msg, err))
}
