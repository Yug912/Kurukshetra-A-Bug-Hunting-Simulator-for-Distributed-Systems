package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kurukshetra/pkg/trace"
)

func TestPickLeaderHighestTermWins(t *testing.T) {
	got, term := pickLeader([]healthReport{
		{Node: "n1", State: "leader", Term: 3}, // isolated old leader
		{Node: "n2", State: "leader", Term: 4},
		{Node: "n3", State: "follower", Leader: "n2", Term: 4},
	})
	if got != "n2" || term != 4 {
		t.Fatalf("pickLeader = %s/%d, want n2/4", got, term)
	}
	if got, _ := pickLeader([]healthReport{{Node: "n1", State: "candidate"}, {Node: "n2", State: "single"}}); got != "" {
		t.Fatalf("no leader claimed: got %q", got)
	}
}

// fakeHealth serves /kuru/health with a state the test can change.
type fakeHealth struct {
	mu sync.Mutex
	r  healthReport
}

func (f *fakeHealth) set(r healthReport) { f.mu.Lock(); f.r = r; f.mu.Unlock() }

func (f *fakeHealth) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(f.r)
}

func TestLeaderWatcherEmitsOnlyOnChange(t *testing.T) {
	h1 := &fakeHealth{r: healthReport{Node: "n1", State: "leader", Term: 1}}
	h2 := &fakeHealth{r: healthReport{Node: "n2", State: "follower", Leader: "n1", Term: 1}}
	s1, s2 := httptest.NewServer(h1), httptest.NewServer(h2)
	defer s1.Close()
	defer s2.Close()

	var step atomic.Int64
	var mu sync.Mutex
	var notes []trace.Event
	w := newLeaderWatcher(map[string]string{"n1": s1.URL, "n2": s2.URL}, step.Load, func(e trace.Event) {
		mu.Lock()
		notes = append(notes, e)
		mu.Unlock()
	})
	ctx := context.Background()
	w.poll(ctx)
	w.poll(ctx) // same leader: no second note
	step.Store(40)
	h1.set(healthReport{Node: "n1", State: "candidate", Term: 2}) // election: no note
	h2.set(healthReport{Node: "n2", State: "candidate", Term: 2})
	w.poll(ctx)
	step.Store(55)
	h2.set(healthReport{Node: "n2", State: "leader", Term: 2})
	w.poll(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 2 {
		t.Fatalf("got %d notes, want 2: %+v", len(notes), notes)
	}
	if notes[0].Meta["leader"] != "n1" || notes[0].Step != 0 || notes[0].Kind != trace.Note {
		t.Fatalf("first note %+v", notes[0])
	}
	if notes[1].Meta["leader"] != "n2" || notes[1].Step != 55 || notes[1].Meta["term"] != "2" {
		t.Fatalf("second note %+v", notes[1])
	}
	if trace.IsDecision(notes[0].Kind) {
		t.Fatal("leader notes must not count as decisions (replay ignores them)")
	}
}

func TestLeaderWatcherIgnoresDeadNodes(t *testing.T) {
	h := &fakeHealth{r: healthReport{Node: "n2", State: "leader", Term: 7}}
	s := httptest.NewServer(h)
	defer s.Close()
	var got []trace.Event
	w := newLeaderWatcher(map[string]string{"n1": "http://127.0.0.1:1", "n2": s.URL},
		func() int64 { return 9 }, func(e trace.Event) { got = append(got, e) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w.poll(ctx)
	if len(got) != 1 || got[0].Meta["leader"] != "n2" {
		t.Fatalf("got %+v", got)
	}
}
