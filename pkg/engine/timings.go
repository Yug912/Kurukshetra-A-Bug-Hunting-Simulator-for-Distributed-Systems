package engine

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// timings records how long each phase of a run took (written to
// logs/engine-timings.json; meta.json has no field for it).
type timings struct {
	mu    sync.Mutex
	cur   string
	since time.Time
	ms    map[string]int64
	order []string
}

func newTimings() *timings { return &timings{ms: map[string]int64{}} }

func (t *timings) begin(phase string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
	t.cur, t.since = phase, time.Now()
}

func (t *timings) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
}

func (t *timings) stopLocked() {
	if t.cur == "" {
		return
	}
	if _, ok := t.ms[t.cur]; !ok {
		t.order = append(t.order, t.cur)
	}
	t.ms[t.cur] += time.Since(t.since).Milliseconds()
	t.cur = ""
}

func (t *timings) write(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	type phase struct {
		Phase string `json:"phase"`
		Ms    int64  `json:"ms"`
	}
	var out []phase
	for _, p := range t.order {
		out = append(out, phase{p, t.ms[p]})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
