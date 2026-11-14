package workload

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
)

// fakeKV is an in-memory server for docs/PROTOCOL.md. With chaos > 0 it randomly
// hangs (timeouts) or returns 421/503 instead of serving.
type fakeKV struct {
	mu    sync.Mutex
	kv    map[string]string
	chaos float64
	r     *rand.Rand
	hits  atomic.Int64
}

func newFakeKV(chaos float64) *fakeKV {
	return &fakeKV{kv: map[string]string{}, chaos: chaos, r: rand.New(rand.NewPCG(1, 2))}
}

func (f *fakeKV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	// Drain the body first: the server only notices a cancelled client once the body is read.
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	x := f.r.Float64()
	f.mu.Unlock()
	if f.chaos > 0 {
		switch {
		case x < f.chaos/3:
			select { // hang past the client timeout
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		case x < 2*f.chaos/3:
			w.WriteHeader(421)
			w.Write([]byte(`{"error":"not_leader","leader":"n2"}`))
			return
		case x < f.chaos:
			w.WriteHeader(503)
			return
		}
	}
	rest := strings.TrimPrefix(r.URL.Path, "/kv/")
	f.mu.Lock()
	defer f.mu.Unlock()
	var b struct{ Value, Expect string }
	json.Unmarshal(body, &b)
	if key, ok := strings.CutSuffix(rest, "/cas"); ok {
		if f.kv[key] != b.Expect {
			w.WriteHeader(409)
			return
		}
		f.kv[key] = b.Value
		w.Write([]byte(`{"ok":true}`))
		return
	}
	switch r.Method {
	case http.MethodPut:
		f.kv[rest] = b.Value
		w.Write([]byte(`{"ok":true}`))
	case http.MethodGet:
		v, ok := f.kv[rest]
		if !ok {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"value": v})
	}
}

type counterClock struct{ n atomic.Int64 }

func (c *counterClock) Step() int64 { return c.n.Add(1) }

func cluster(urls ...string) core.Cluster {
	var cl core.Cluster
	for i, u := range urls {
		cl.Nodes = append(cl.Nodes, core.Node{Name: []string{"n1", "n2", "n3"}[i], ClientURL: u})
	}
	return cl
}

func newWL(t *testing.T, cfg scenario.Workload) *KV {
	t.Helper()
	cfg.Type = "kv"
	w, err := core.NewWorkload(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w.(*KV)
}

func run(t *testing.T, w *KV, cl core.Cluster, d time.Duration) []history.Op {
	t.Helper()
	var rec history.MemRecorder
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := w.Run(ctx, cl, rng.New(1).Stream(rng.StreamWorkload), &counterClock{}, &rec); err != nil {
		t.Fatal(err)
	}
	return rec.Ops()
}

func TestRunWellFormedAndMix(t *testing.T) {
	srv := httptest.NewServer(newFakeKV(0))
	defer srv.Close()
	w := newWL(t, scenario.Workload{Clients: 4, Keys: 3, OpsPerSec: 2000, TimeoutMs: 500, ReadFrac: 0.5, WriteFrac: 0.3, CasFrac: 0.2})
	ops := run(t, w, cluster(srv.URL, srv.URL, srv.URL), 400*time.Millisecond)
	pairs, err := history.Pairs(ops)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[history.F]int{}
	nodes := map[string]bool{}
	values := map[string]bool{}
	last := map[int]int{} // client -> index of its last pair (may be cut off by cancellation)
	for i, p := range pairs {
		last[p.Invoke.Client] = i
	}
	for i, p := range pairs {
		counts[p.Invoke.F]++
		nodes[p.Invoke.Node] = true
		if p.Complete == nil || (p.Complete.Type == history.Info && last[p.Invoke.Client] != i) {
			t.Fatalf("op %d did not complete cleanly: %+v", p.Invoke.Index, p.Complete)
		}
		if p.Invoke.F != history.Read {
			if values[p.Invoke.Value] {
				t.Fatalf("duplicate value %s", p.Invoke.Value)
			}
			values[p.Invoke.Value] = true
		}
	}
	n := float64(len(pairs))
	if n < 200 {
		t.Fatalf("only %v ops", n)
	}
	for f, want := range map[history.F]float64{history.Read: 0.5, history.Write: 0.3, history.CAS: 0.2} {
		if got := float64(counts[f]) / n; got < want-0.08 || got > want+0.08 {
			t.Errorf("%s fraction %.2f, want ~%.2f", f, got, want)
		}
	}
	if len(nodes) != 3 {
		t.Errorf("nodes used: %v", nodes)
	}
}

func TestRunDeterministicChoices(t *testing.T) {
	// The same seed produces the same per-client op sequence (keys, functions, values).
	seq := func() map[int][]string {
		srv := httptest.NewServer(newFakeKV(0))
		defer srv.Close()
		w := newWL(t, scenario.Workload{Clients: 3, Keys: 2, OpsPerSec: 600, TimeoutMs: 500})
		out := map[int][]string{}
		for _, op := range run(t, w, cluster(srv.URL), 150*time.Millisecond) {
			if op.Type == history.Invoke {
				out[op.Client] = append(out[op.Client], string(op.F)+op.Key+op.Value)
			}
		}
		return out
	}
	a, b := seq(), seq()
	for c := range a {
		n := min(len(a[c]), len(b[c]))
		for i := 0; i < n; i++ {
			if a[c][i] != b[c][i] {
				t.Fatalf("client %d op %d: %s vs %s", c, i, a[c][i], b[c][i])
			}
		}
	}
}

func TestRunChaosClassification(t *testing.T) {
	srv := httptest.NewServer(newFakeKV(0.5))
	defer srv.Close()
	w := newWL(t, scenario.Workload{Clients: 4, Keys: 2, OpsPerSec: 1000, TimeoutMs: 50})
	ops := run(t, w, cluster(srv.URL, srv.URL), 600*time.Millisecond)
	pairs, err := history.Pairs(ops)
	if err != nil {
		t.Fatal(err)
	}
	types := map[history.Type]int{}
	for _, p := range pairs {
		if p.Complete == nil {
			t.Fatal("missing completion")
		}
		c := p.Complete
		types[c.Type]++
		if c.Type != history.OK && c.Error == "" {
			t.Fatalf("no error text on %+v", c)
		}
		if c.Type == history.Fail && !strings.HasPrefix(c.Error, "http ") {
			t.Fatalf("fail without status: %+v", c)
		}
	}
	for _, ty := range []history.Type{history.OK, history.Fail, history.Info} {
		if types[ty] == 0 {
			t.Errorf("no %s completions: %v", ty, types)
		}
	}
}

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(newFakeKV(0))
	defer srv.Close()
	w := newWL(t, scenario.Workload{Clients: 5, Keys: 1, OpsPerSec: 100, TimeoutMs: 500})
	ops := run(t, w, cluster(srv.URL), 500*time.Millisecond)
	invokes := 0
	for _, op := range ops {
		if op.Type == history.Invoke {
			invokes++
		}
	}
	if invokes < 35 || invokes > 60 {
		t.Fatalf("%d ops in 500ms at 100/s", invokes)
	}
}

func TestCancellationPrompt(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		<-r.Context().Done()
	}))
	defer hang.Close()
	w := newWL(t, scenario.Workload{Clients: 3, Keys: 1, TimeoutMs: 10000})
	var rec history.MemRecorder
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx, cluster(hang.URL), rand.New(rand.NewPCG(1, 1)), &counterClock{}, &rec)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop within 1s of cancel")
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("stopped after %v", d)
	}
	pairs, err := history.Pairs(rec.Ops())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		if p.Complete == nil || p.Complete.Type != history.Info {
			t.Fatalf("interrupted op must complete as info: %+v", p)
		}
	}
	if len(pairs) != 3 {
		t.Fatalf("want one op per client, got %d", len(pairs))
	}
}

func TestFinalReadsRetry(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(421)
			return
		}
		w.Write([]byte(`{"value":"c0-1"}`))
	}))
	defer srv.Close()
	w := newWL(t, scenario.Workload{Clients: 2, Keys: 2, TimeoutMs: 500})
	var rec history.MemRecorder
	if err := w.FinalReads(context.Background(), cluster(srv.URL, srv.URL), &counterClock{}, &rec); err != nil {
		t.Fatal(err)
	}
	ops := rec.Ops()
	pairs, err := history.Pairs(ops)
	if err != nil {
		t.Fatal(err)
	}
	// 2 keys x 2 nodes = 4 reads, plus 2 retried attempts on the first one.
	if len(pairs) != 6 {
		t.Fatalf("got %d attempts", len(pairs))
	}
	for _, p := range pairs {
		if !p.Invoke.Final || p.Invoke.Client != 2 || p.Invoke.F != history.Read {
			t.Fatalf("bad final op %+v", p.Invoke)
		}
	}
	if pairs[0].Complete.Type != history.Fail || pairs[2].Complete.Value != "c0-1" {
		t.Fatalf("%+v / %+v", pairs[0].Complete, pairs[2].Complete)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		f    history.F
		code int
		err  error
		want history.Type
	}{
		{history.Write, 200, nil, history.OK},
		{history.Read, 404, nil, history.OK},
		{history.Write, 404, nil, history.Info},
		{history.CAS, 409, nil, history.Fail},
		{history.Read, 421, nil, history.Fail},
		{history.Write, 503, nil, history.Fail},
		{history.Write, 500, nil, history.Info},
		{history.Write, 504, nil, history.Info},
		{history.Write, 0, errors.New("timeout"), history.Info},
	}
	for _, c := range cases {
		if got := Classify(c.f, c.code, c.err); got != c.want {
			t.Errorf("%s %d %v: got %s want %s", c.f, c.code, c.err, got, c.want)
		}
	}
	if _, err := NewKV(scenario.Workload{}); err == nil {
		t.Fatal("zero clients must be rejected")
	}
}
