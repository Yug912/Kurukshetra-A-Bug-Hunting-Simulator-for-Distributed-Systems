package faults

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/trace"
)

type fakeLinks struct{ blocked map[[2]string]bool }

func (f *fakeLinks) SetBlocked(b map[[2]string]bool) { f.blocked = b }

type fakeCtr struct{ ops []string }

func (f *fakeCtr) Kill(_ context.Context, id string) error {
	f.ops = append(f.ops, "kill "+id)
	return nil
}
func (f *fakeCtr) Restart(_ context.Context, id string) error {
	f.ops = append(f.ops, "restart "+id)
	return nil
}
func (f *fakeCtr) Pause(_ context.Context, id string) error {
	f.ops = append(f.ops, "pause "+id)
	return nil
}
func (f *fakeCtr) Unpause(_ context.Context, id string) error {
	f.ops = append(f.ops, "unpause "+id)
	return nil
}

type clockSrv struct {
	mu      sync.Mutex
	offsets []int64
	status  int
}

func newClockSrv(t *testing.T, status int) (*clockSrv, string) {
	c := &clockSrv{status: status}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			OffsetMs int64 `json:"offset_ms"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		c.mu.Lock()
		c.offsets = append(c.offsets, b.OffsetMs)
		c.mu.Unlock()
		w.WriteHeader(c.status)
	}))
	t.Cleanup(s.Close)
	return c, s.URL
}

func TestExecutor(t *testing.T) {
	links, ctr := &fakeLinks{}, &fakeCtr{}
	c1, u1 := newClockSrv(t, 200)
	_, u2 := newClockSrv(t, 404)
	nodes := map[string]NodeRef{"n1": {"c1", u1}, "n2": {"c2", u2}, "n3": {"c3", u1}}
	var evs []trace.Event
	e := NewExecutor(links, ctr, nodes, func(ev trace.Event) { evs = append(evs, ev) })
	restarted := ""
	e.OnRestart(func(n string) { restarted = n })
	ctx := context.Background()

	part := core.Fault{Step: 1, Kind: trace.Partition, Groups: [][]string{{"n1"}, {"n2", "n3"}}, DurationSteps: 5}
	crash := core.Fault{Step: 2, Kind: trace.Crash, Nodes: []string{"n3"}, DurationSteps: 5}
	pause := core.Fault{Step: 3, Kind: trace.Pause, Nodes: []string{"n2"}, DurationSteps: 5}
	skew1 := core.Fault{Step: 4, Kind: trace.ClockSkew, Nodes: []string{"n1"}, OffsetMs: 250}
	skew2 := core.Fault{Step: 4, Kind: trace.ClockSkew, Nodes: []string{"n2"}, OffsetMs: -100}
	for _, f := range []core.Fault{part, crash, pause, skew1, skew2} {
		if err := e.Apply(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if len(links.blocked) != 4 || !links.blocked[[2]string{"n1", "n3"}] {
		t.Fatal(links.blocked)
	}
	if !reflect.DeepEqual(e.Down(), []string{"n2", "n3"}) {
		t.Fatal(e.Down())
	}
	if err := e.Heal(ctx, part); err != nil || len(links.blocked) != 0 {
		t.Fatal(err, links.blocked)
	}
	if err := e.Heal(ctx, crash); err != nil || restarted != "n3" {
		t.Fatal(err)
	}
	if err := e.HealAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.HealAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ctr.ops, []string{"kill c3", "pause c2", "restart c3", "unpause c2"}) {
		t.Fatal(ctr.ops)
	}
	if !reflect.DeepEqual(c1.offsets, []int64{250, 0}) {
		t.Fatal("clock offsets", c1.offsets)
	}
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, string(ev.Kind))
		if ev.Kind == trace.ClockSkew && ev.Node == "n2" && ev.Meta["unsupported"] != "true" {
			t.Fatal("404 should be unsupported", ev.Meta)
		}
		if ev.Kind == trace.Partition && ev.Meta["groups"] != "n1|n2,n3" {
			t.Fatal(ev.Meta)
		}
	}
	want := "partition,crash,pause,clock_skew,clock_skew,heal,restart,resume,clock_skew,clock_skew"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("%s\n%s", strings.Join(kinds, ","), want)
	}
}

func TestExecutorUnknownNodeAndKind(t *testing.T) {
	e := NewExecutor(&fakeLinks{}, &fakeCtr{}, map[string]NodeRef{}, nil)
	if e.Apply(context.Background(), core.Fault{Kind: trace.Crash, Nodes: []string{"zz"}}) == nil {
		t.Fatal("unknown node")
	}
	if e.Apply(context.Background(), core.Fault{Kind: "boom"}) == nil {
		t.Fatal("unknown kind")
	}
}
