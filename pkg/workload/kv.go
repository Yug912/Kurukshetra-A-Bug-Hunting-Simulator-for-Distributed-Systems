// Package workload implements client workloads that drive a cluster and record
// a history. It registers workload type "kv" (docs/PROTOCOL.md client API).
package workload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/scenario"
)

func init() {
	core.RegisterWorkload("kv", NewKV)
}

// Final-read retry policy.
const (
	finalAttempts = 5
	finalWindow   = 2 * time.Second
)

// KV is the "kv" workload: clients issue reads, writes and CAS ops against random nodes.
type KV struct {
	cfg    scenario.Workload
	client *http.Client
}

// NewKV builds a kv workload from config.
func NewKV(cfg scenario.Workload) (core.Workload, error) {
	if cfg.Clients <= 0 || cfg.Keys <= 0 {
		return nil, fmt.Errorf("workload kv: clients and keys must be > 0 (got %d, %d)", cfg.Clients, cfg.Keys)
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = 1000
	}
	if cfg.ReadFrac == 0 && cfg.WriteFrac == 0 && cfg.CasFrac == 0 {
		cfg.ReadFrac, cfg.WriteFrac, cfg.CasFrac = 0.4, 0.4, 0.2
	}
	// No client-side timeout: each request carries its own context deadline.
	return &KV{cfg: cfg, client: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 16}}}, nil
}

// Classify maps an HTTP outcome to a history type per docs/PROTOCOL.md:
// 200 → ok; 404 on read → ok (missing); 409/421/503 → fail; anything else → info.
func Classify(f history.F, code int, err error) history.Type {
	switch {
	case err != nil:
		return history.Info
	case code == http.StatusOK, code == http.StatusNotFound && f == history.Read:
		return history.OK
	case code == http.StatusConflict, code == http.StatusMisdirectedRequest, code == http.StatusServiceUnavailable:
		return history.Fail
	}
	return history.Info
}

// request is one client operation to perform.
type request struct {
	f      history.F
	key    string
	value  string
	expect string
	node   core.Node
}

// outcome is the HTTP result of a request.
type outcome struct {
	code  int
	value string
	err   error
}

// Run implements core.Workload.
func (w *KV) Run(ctx context.Context, cl core.Cluster, r *rand.Rand, clock core.StepClock, rec history.Recorder) error {
	if len(cl.Nodes) == 0 {
		return errors.New("workload kv: empty cluster")
	}
	// One generator per client, drawn in client order before any goroutine starts.
	rs := make([]*rand.Rand, w.cfg.Clients)
	for i := range rs {
		rs[i] = rand.New(rand.NewPCG(r.Uint64(), r.Uint64()))
	}
	lim := newLimiter(w.cfg.OpsPerSec)
	var wg sync.WaitGroup
	for c := range rs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.runClient(ctx, c, rs[c], cl, lim, clock, rec)
		}()
	}
	wg.Wait()
	return nil
}

// clientState is what one client remembers between ops.
type clientState struct {
	id   int
	n    int               // values written so far (c<id>-<n>)
	seen map[string]string // last value this client saw per key
}

func (w *KV) runClient(ctx context.Context, id int, r *rand.Rand, cl core.Cluster, lim *limiter, clock core.StepClock, rec history.Recorder) {
	st := &clientState{id: id, seen: map[string]string{}}
	for {
		if err := lim.wait(ctx); err != nil {
			return
		}
		req := w.next(r, st, cl)
		out := w.execute(ctx, req, id, false, clock, rec)
		if out.err == nil && (out.code == http.StatusOK || out.code == http.StatusNotFound && req.f == history.Read) {
			if req.f == history.Read {
				st.seen[req.key] = out.value
			} else {
				st.seen[req.key] = req.value
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// next picks the client's next operation.
func (w *KV) next(r *rand.Rand, st *clientState, cl core.Cluster) request {
	req := request{
		key:  fmt.Sprintf("k%d", r.IntN(w.cfg.Keys)),
		node: cl.Nodes[r.IntN(len(cl.Nodes))],
	}
	x := r.Float64()
	switch {
	case x < w.cfg.ReadFrac:
		req.f = history.Read
		return req
	case x < w.cfg.ReadFrac+w.cfg.WriteFrac:
		req.f = history.Write
	default:
		req.f = history.CAS
		req.expect = st.seen[req.key]
	}
	st.n++
	req.value = fmt.Sprintf("c%d-%d", st.id, st.n)
	return req
}

// execute records the invoke, performs the request and records the completion.
func (w *KV) execute(ctx context.Context, req request, client int, final bool, clock core.StepClock, rec history.Recorder) outcome {
	op := history.Op{Client: client, F: req.f, Key: req.key, Value: req.value, Expect: req.expect, Node: req.node.Name, Final: final}
	op.Type, op.Step = history.Invoke, clock.Step()
	rec.Record(op)

	out := w.send(ctx, req)
	op.Type, op.Step, op.WallNs = Classify(req.f, out.code, out.err), clock.Step(), 0
	switch {
	case out.err != nil:
		op.Error = out.err.Error()
	case op.Type != history.OK:
		op.Error = fmt.Sprintf("http %d", out.code)
	}
	if req.f == history.Read {
		op.Value = ""
		if op.Type == history.OK {
			op.Value = out.value
		}
	}
	rec.Record(op)
	return out
}

// send performs one HTTP request with the configured timeout.
func (w *KV) send(ctx context.Context, req request) outcome {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(w.cfg.TimeoutMs)*time.Millisecond)
	defer cancel()
	base := req.node.ClientURL + "/kv/" + req.key
	var method, url string
	var body any
	switch req.f {
	case history.Read:
		method, url = http.MethodGet, base
	case history.Write:
		method, url, body = http.MethodPut, base, map[string]string{"value": req.value}
	case history.CAS:
		method, url, body = http.MethodPost, base+"/cas", map[string]string{"expect": req.expect, "value": req.value}
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	hr, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return outcome{err: err}
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(hr)
	if err != nil {
		return outcome{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return outcome{code: resp.StatusCode, err: fmt.Errorf("read body: %w", err)}
	}
	out := outcome{code: resp.StatusCode}
	if req.f == history.Read && resp.StatusCode == http.StatusOK {
		var v struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return outcome{code: resp.StatusCode, err: fmt.Errorf("decode read: %w", err)}
		}
		out.value = v.Value
	}
	return out
}

// FinalReads implements core.Workload: every key from every node, retrying
// 421/503 up to 5 times over 2 s so the leader's view is recorded. Every attempt
// is recorded. Final reads use client id cfg.Clients (a client of their own).
func (w *KV) FinalReads(ctx context.Context, cl core.Cluster, clock core.StepClock, rec history.Recorder) error {
	client := w.cfg.Clients
	gap := finalWindow / finalAttempts
	for k := 0; k < w.cfg.Keys; k++ {
		key := fmt.Sprintf("k%d", k)
		for _, node := range cl.Nodes {
			for attempt := 0; attempt < finalAttempts; attempt++ {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				out := w.execute(ctx, request{f: history.Read, key: key, node: node}, client, true, clock, rec)
				if out.err != nil || (out.code != http.StatusMisdirectedRequest && out.code != http.StatusServiceUnavailable) {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(gap):
				}
			}
		}
	}
	return nil
}

// limiter is a shared token bucket (burst 1) that spaces ops evenly to opsPerSec in total.
type limiter struct {
	mu   sync.Mutex
	gap  time.Duration
	next time.Time
}

func newLimiter(opsPerSec int) *limiter {
	l := &limiter{}
	if opsPerSec > 0 {
		l.gap = time.Second / time.Duration(opsPerSec)
	}
	return l
}

// wait blocks until the caller may start an op, or ctx is done.
func (l *limiter) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.gap == 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	at := l.next
	l.next = l.next.Add(l.gap)
	l.mu.Unlock()
	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
