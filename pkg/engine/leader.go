package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"kurukshetra/pkg/trace"
)

// LeaderPollInterval is how often the leader watcher asks every node for its
// health. It only talks to the nodes' client ports (never through the proxy),
// draws no randomness and never touches the scheduler, so it cannot change
// message decisions, determinism or replay.
var LeaderPollInterval = 100 * time.Millisecond

// healthReport is the body of GET /kuru/health (docs/PROTOCOL.md).
type healthReport struct {
	Node   string `json:"node"`
	State  string `json:"state"`
	Leader string `json:"leader"`
	Term   int64  `json:"term"`
}

// leaderWatcher polls /kuru/health on every node and emits a trace note
// (meta.leader) whenever the cluster's leader changes.
type leaderWatcher struct {
	urls  map[string]string // node → client URL
	step  func() int64
	emit  func(trace.Event)
	hc    *http.Client
	every time.Duration

	mu      sync.Mutex
	current string
	term    int64
}

func newLeaderWatcher(urls map[string]string, step func() int64, emit func(trace.Event)) *leaderWatcher {
	return &leaderWatcher{
		urls:  urls,
		step:  step,
		emit:  emit,
		hc:    &http.Client{Timeout: LeaderPollInterval * 2},
		every: LeaderPollInterval,
	}
}

// Run polls until ctx is done.
func (w *leaderWatcher) Run(ctx context.Context) {
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		w.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// poll asks every node once, in parallel, and records a change of leader.
func (w *leaderWatcher) poll(ctx context.Context) {
	reports := make([]healthReport, 0, len(w.urls))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for node, url := range w.urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := w.fetch(ctx, url)
			if err != nil {
				return // crashed, paused or slow: no opinion this round
			}
			if r.Node == "" {
				r.Node = node
			}
			mu.Lock()
			reports = append(reports, r)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	w.observe(reports)
}

func (w *leaderWatcher) fetch(ctx context.Context, url string) (healthReport, error) {
	var r healthReport
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/kuru/health", nil)
	if err != nil {
		return r, err
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		return r, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return r, fmt.Errorf("status %d", resp.StatusCode)
	}
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// pickLeader returns the node that claims leadership with the highest term
// (an isolated old leader may still claim it with an older term). Ties go to
// the lowest node ID so the choice is stable. "" if nobody claims it.
func pickLeader(reports []healthReport) (string, int64) {
	sort.Slice(reports, func(i, j int) bool { return reports[i].Node < reports[j].Node })
	best, term := "", int64(-1)
	for _, r := range reports {
		if r.State == "leader" && r.Term > term {
			best, term = r.Node, r.Term
		}
	}
	return best, term
}

// observe emits a note when the leader differs from the last one seen.
func (w *leaderWatcher) observe(reports []healthReport) {
	leader, term := pickLeader(reports)
	if leader == "" {
		return // an election in progress is not a change of leader
	}
	w.mu.Lock()
	changed := leader != w.current
	if changed {
		w.current, w.term = leader, term
	}
	w.mu.Unlock()
	if !changed {
		return
	}
	w.emit(trace.Event{
		Step: w.step(),
		Kind: trace.Note,
		Node: leader,
		Meta: map[string]string{"msg": "leader changed", "leader": leader, "term": fmt.Sprint(term)},
	})
}
