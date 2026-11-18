package scheduler

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"kurukshetra/pkg/netproxy"
	"kurukshetra/pkg/rng"
)

// guide with link a-b recorded at steps 25, 50, 75 and link c-b at 10, 35.
func testGuide() *ReplayGuide {
	g := &ReplayGuide{Decisions: map[string]Decision{}, Steps: map[string]int64{}, Order: map[int64][]string{}, WaitSteps: 3}
	add := func(id string, step int64, d Decision) {
		g.Decisions[id], g.Steps[id] = d, step
		if d.Fate == Deliver || d.Fate == Duplicate {
			g.Order[step] = append(g.Order[step], id)
		}
	}
	add("a-b-1", 25, Decision{Fate: Deliver})
	add("a-b-2", 50, Decision{Fate: Drop, Reason: "partition"})
	add("a-b-3", 75, Decision{Fate: Delay, Steps: 4})
	add("c-b-1", 10, Decision{Fate: Deliver})
	add("c-b-2", 35, Decision{Fate: Duplicate})
	return g
}

func TestNamerMatching(t *testing.T) {
	cases := []struct {
		name     string
		arrivals []int64 // arrival steps on link a-b
		want     []string
	}{
		{"on time", []int64{24, 49, 74}, []string{"a-b-1", "a-b-2", "a-b-3"}},
		{"early is held, not extra", []int64{20, 45}, []string{"a-b-1", "a-b-2"}},
		{"startup counterpart", []int64{0, 24}, []string{"a-b-sx1", "a-b-1"}},
		{"late but nearest", []int64{30}, []string{"a-b-1"}},
		{"one cycle late skips", []int64{48, 73}, []string{"a-b-2", "a-b-3"}},
		{"too far ahead is extra", []int64{24, 49, 74, 200}, []string{"a-b-1", "a-b-2", "a-b-3", "a-b-x1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReplayState(testGuide())
			var got []string
			for _, a := range c.arrivals {
				got = append(got, r.name("a", "b", a))
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestFreezeWaitsBoundedAndSkipsBlockedSenders(t *testing.T) {
	r := newReplayState(testGuide())
	for i := 0; i < 3; i++ {
		if !r.freeze(10) {
			t.Fatalf("tick %d: should wait for c-b-1", i)
		}
	}
	if r.freeze(10) || r.stats.Missing != 1 || r.stats.Waited != 3 {
		t.Fatalf("budget not respected: %+v", r.stats)
	}
	r2 := newReplayState(testGuide())
	r2.held = []*netproxy.Message{{ID: "c-b-x1", From: "c", To: "b"}}
	if r2.freeze(10) {
		t.Fatal("must not wait for a sender that has a held message")
	}
	r3 := newReplayState(testGuide())
	r3.name("c", "b", 9)
	if r3.freeze(10) {
		t.Fatal("arrived message must not freeze the clock")
	}
}

func TestReplayForcesRecordedDecisionsAndTiming(t *testing.T) {
	sc := faultyScenario()
	sc.Faults.Drop, sc.Faults.Dup, sc.Faults.DelayProb = 0, 0, 0
	c := NewClock()
	bus := newFakeBus(c)
	s := NewWithBus(sc, rng.New(1), bus, nil, &fakeExec{}, nil, Options{Clock: c, Replay: testGuide()})
	namer := s.ReplayNamer()
	send := func(from, to string) {
		bus.in <- &netproxy.Message{ID: namer(from, to, c.Step()), From: from, To: to, ArrivedStep: c.Step()}
	}
	send("c", "b") // arrives at 0, recorded at 10: held until 10
	send("a", "b") // at 0, 25 steps before a-b-1: a startup counterpart
	for c.Step() < 40 {
		if c.Step() == 22 {
			send("a", "b") // a-b-1 recorded at 25: held until 25
		}
		if c.Step() == 30 {
			send("a", "b") // a-b-2 recorded at 50 (drop:partition): held, then dropped
			send("c", "b") // c-b-2 recorded at 35 (dup)
		}
		s.tick(context.Background())
	}
	for c.Step() < 60 {
		s.tick(context.Background())
	}
	got := strings.Join(bus.ops, " ")
	for _, want := range []string{"1:deliver:a-b-sx1", "10:deliver:c-b-1", "25:deliver:a-b-1", "35:dup:c-b-2", "50:drop:a-b-2:partition"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestStartupCounterpartDeliveredWithoutFaults(t *testing.T) {
	sc := faultyScenario()
	sc.Faults.Drop = 1 // every PRNG decision would drop
	c := NewClock()
	bus := newFakeBus(c)
	s := NewWithBus(sc, rng.New(1), bus, nil, &fakeExec{}, nil, Options{Clock: c, Replay: testGuide()})
	bus.in <- &netproxy.Message{ID: "a-b-sx1", From: "a", To: "b"}
	bus.in <- &netproxy.Message{ID: "a-b-x1", From: "a", To: "b"}
	s.tick(context.Background())
	got := strings.Join(bus.ops, " ")
	if !strings.Contains(got, "deliver:a-b-sx1") || !strings.Contains(got, "drop:a-b-x1") {
		t.Fatal(got)
	}
}
