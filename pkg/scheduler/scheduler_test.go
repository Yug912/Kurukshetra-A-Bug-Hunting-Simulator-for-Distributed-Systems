package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/netproxy"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
	"kurukshetra/pkg/trace"
)

type fakeBus struct {
	mu      sync.Mutex
	in      chan *netproxy.Message
	blocked map[[2]string]bool
	ops     []string // "deliver:id", "drop:id:reason", "dup:id", "event:kind:id"
	clock   *Clock
}

func newFakeBus(c *Clock) *fakeBus {
	return &fakeBus{in: make(chan *netproxy.Message, 1000), blocked: map[[2]string]bool{}, clock: c}
}

func (b *fakeBus) Incoming() <-chan *netproxy.Message { return b.in }
func (b *fakeBus) Blocked(f, t string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.blocked[[2]string{f, t}]
}
func (b *fakeBus) log(s string) {
	b.mu.Lock()
	b.ops = append(b.ops, fmt.Sprintf("%d:%s", b.clock.Step(), s))
	b.mu.Unlock()
}
func (b *fakeBus) Drop(m *netproxy.Message, reason string) { b.log("drop:" + m.ID + ":" + reason) }
func (b *fakeBus) DeliverAsync(m *netproxy.Message, wg *sync.WaitGroup, _ func(error)) {
	b.log("deliver:" + m.ID)
}
func (b *fakeBus) DuplicateAsync(m *netproxy.Message, wg *sync.WaitGroup, _ func(error)) {
	b.log("dup:" + m.ID)
}
func (b *fakeBus) Emit(e trace.Event) {
	b.log("event:" + string(e.Kind) + ":" + e.MsgID + ":" + e.Meta["steps"])
}

type fakeExec struct{ ops []string }

func (e *fakeExec) Apply(_ context.Context, f core.Fault) error {
	e.ops = append(e.ops, "apply "+core.DescribeFault(f))
	return nil
}
func (e *fakeExec) Heal(_ context.Context, f core.Fault) error {
	e.ops = append(e.ops, "heal "+core.DescribeFault(f))
	return nil
}
func (e *fakeExec) HealAll(context.Context) error { e.ops = append(e.ops, "healall"); return nil }

func faultyScenario() scenario.Scenario {
	sc := scenario.Scenario{Name: "t", Image: "x"}
	sc.ApplyDefaults()
	sc.Faults.Drop, sc.Faults.Dup, sc.Faults.DelayProb = 0.2, 0.1, 0.2
	sc.Faults.DelaySteps = scenario.Range{1, 5}
	return sc
}

func ids(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("n1-n2-%d", i), fmt.Sprintf("n2-n3-%d", i), fmt.Sprintf("n3-n1-%d", i))
	}
	return out
}

func TestDecideDeterministic(t *testing.T) {
	sc := faultyScenario()
	first := map[string]Decision{}
	counts := map[Fate]int{}
	for _, id := range ids(300) {
		d := Decide(rng.New(5), sc.Faults, id)
		first[id] = d
		counts[d.Fate]++
		if d.Fate == Delay && (d.Steps < 1 || d.Steps > 5) {
			t.Fatalf("delay %d", d.Steps)
		}
	}
	for run := 0; run < 20; run++ {
		for _, id := range ids(300) {
			if Decide(rng.New(5), sc.Faults, id) != first[id] {
				t.Fatalf("run %d: %s differs", run, id)
			}
		}
	}
	n := float64(len(first))
	if f := float64(counts[Drop]) / n; f < 0.15 || f > 0.25 {
		t.Fatalf("drop rate %.2f", f)
	}
	diff := 0
	for _, id := range ids(300) {
		if Decide(rng.New(6), sc.Faults, id) != first[id] {
			diff++
		}
	}
	if diff == 0 {
		t.Fatal("different seed gave identical decisions")
	}
}

func TestLessID(t *testing.T) {
	in := []string{"n1-n2-10", "n1-n2-2", "n1-n3-1", "n1-n2-1"}
	sort.Slice(in, func(i, j int) bool { return LessID(in[i], in[j]) })
	if strings.Join(in, ",") != "n1-n2-1,n1-n2-2,n1-n2-10,n1-n3-1" {
		t.Fatal(in)
	}
}

// runSteps feeds the given messages (arrival order) before step 1 and ticks n steps.
func runSteps(t *testing.T, sc scenario.Scenario, arrivals []string, n int, opts Options, setup func(*fakeBus)) []string {
	t.Helper()
	c := NewClock()
	bus := newFakeBus(c)
	if setup != nil {
		setup(bus)
	}
	opts.Clock = c
	s := NewWithBus(sc, rng.New(9), bus, nil, &fakeExec{}, nil, opts)
	for _, id := range arrivals {
		parts := strings.Split(id, "-")
		bus.in <- &netproxy.Message{ID: id, From: parts[0], To: parts[1]}
	}
	for i := 0; i < n; i++ {
		s.tick(context.Background())
	}
	return bus.ops
}

func TestSameDecisionsAcrossArrivalOrders(t *testing.T) {
	sc := faultyScenario()
	base := ids(30)
	want := runSteps(t, sc, base, 10, Options{}, nil)
	for i := 0; i < 20; i++ {
		perm := append([]string(nil), base...)
		r := rand.New(rand.NewPCG(uint64(i), 1))
		r.Shuffle(len(perm), func(a, b int) { perm[a], perm[b] = perm[b], perm[a] })
		if got := runSteps(t, sc, perm, 10, Options{}, nil); !reflect.DeepEqual(got, want) {
			t.Fatalf("order %d produced different ops", i)
		}
	}
	kinds := map[string]bool{}
	for _, op := range want {
		kinds[strings.Split(op, ":")[1]] = true
	}
	for _, k := range []string{"deliver", "drop", "dup", "event"} {
		if !kinds[k] {
			t.Fatalf("no %s in %v", k, want)
		}
	}
}

func TestDelayedReleaseStep(t *testing.T) {
	sc := faultyScenario()
	sc.Faults.Drop, sc.Faults.Dup, sc.Faults.DelayProb = 0, 0, 1
	sc.Faults.DelaySteps = scenario.Range{3, 3}
	ops := runSteps(t, sc, []string{"n1-n2-1"}, 6, Options{}, nil)
	want := []string{"1:event:msg_delay:n1-n2-1:3", "4:deliver:n1-n2-1"}
	if !reflect.DeepEqual(ops, want) {
		t.Fatal(ops)
	}
}

func TestBlockedDrops(t *testing.T) {
	sc := faultyScenario()
	ops := runSteps(t, sc, []string{"n1-n2-1", "n2-n3-1"}, 1, Options{NoMessageFaults: true}, func(b *fakeBus) {
		b.blocked[[2]string{"n1", "n2"}] = true
	})
	if !reflect.DeepEqual(ops, []string{"1:drop:n1-n2-1:partition", "1:deliver:n2-n3-1"}) {
		t.Fatal(ops)
	}
}

func TestNoMessageFaultsDeliversAll(t *testing.T) {
	sc := faultyScenario()
	sc.Faults.Drop = 1
	ops := runSteps(t, sc, ids(10), 1, Options{NoMessageFaults: true}, nil)
	if len(ops) != 30 {
		t.Fatal(len(ops))
	}
	for _, op := range ops {
		if !strings.Contains(op, ":deliver:") {
			t.Fatal(op)
		}
	}
}

func TestFaultsFireAndHealInOrder(t *testing.T) {
	sc := faultyScenario()
	exec := &fakeExec{}
	plan := []core.Fault{
		{Step: 2, Kind: trace.Crash, Nodes: []string{"n3"}, DurationSteps: 2},
		{Step: 2, Kind: trace.Partition, Groups: [][]string{{"n1"}, {"n2", "n3"}}, DurationSteps: 3},
		{Step: 3, Kind: trace.ClockSkew, Nodes: []string{"n1"}, OffsetMs: 5},
	}
	c := NewClock()
	s := NewWithBus(sc, rng.New(1), newFakeBus(c), nil, exec, plan, Options{Clock: c})
	for i := 0; i < 6; i++ {
		s.tick(context.Background())
	}
	want := []string{
		"apply crash n3 at step 2 for 2",
		"apply partition n1 | n2,n3 at step 2 for 3",
		"apply clock_skew n1 by 5ms at step 3 for 0",
		"heal crash n3 at step 2 for 2",
		"heal partition n1 | n2,n3 at step 2 for 3",
	}
	if !reflect.DeepEqual(exec.ops, want) {
		t.Fatalf("%q", exec.ops)
	}
}

func TestRunAndDrainAdvanceClock(t *testing.T) {
	sc := faultyScenario()
	c := NewClock()
	bus := newFakeBus(c)
	s := NewWithBus(sc, rng.New(1), bus, nil, &fakeExec{}, nil, Options{Clock: c, Tick: time.Millisecond})
	if err := s.Run(context.Background(), 20); err != nil || s.Step() != 20 {
		t.Fatal(err, s.Step())
	}
	bus.in <- &netproxy.Message{ID: "n1-n2-1", From: "n1", To: "n2"}
	sc.Faults.Drop = 1 // Drain ignores message faults
	if err := s.Drain(context.Background(), 5); err != nil || s.Step() != 25 {
		t.Fatal(err, s.Step())
	}
	if last := bus.ops[len(bus.ops)-1]; !strings.Contains(last, "deliver:n1-n2-1") {
		t.Fatal(bus.ops)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx, 1000); err == nil {
		t.Fatal("want ctx error")
	}
}
