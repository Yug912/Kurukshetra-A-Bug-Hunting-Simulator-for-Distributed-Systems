package fakes

import (
	"bytes"
	"context"
	"encoding/json"
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

// FakePut is the "fake-put" workload: clients PUT k<i>=c<client>-<n> to random nodes.
type FakePut struct {
	cfg  scenario.Workload
	http *http.Client
}

func newFakePut(cfg scenario.Workload) (core.Workload, error) {
	if cfg.Clients <= 0 || cfg.Keys <= 0 {
		return nil, fmt.Errorf("fake-put: clients and keys must be > 0")
	}
	t := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if t <= 0 {
		t = time.Second
	}
	return &FakePut{cfg: cfg, http: &http.Client{Timeout: t}}, nil
}

// classify maps an HTTP outcome to a history type per docs/PROTOCOL.md.
func classify(f history.F, code int, err error) history.Type {
	switch {
	case err != nil:
		return history.Info
	case code == 200, code == 404 && f == history.Read:
		return history.OK
	case code == 409, code == 421, code == 503:
		return history.Fail
	}
	return history.Info
}

func (w *FakePut) do(ctx context.Context, method, url string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// Run implements core.Workload.
func (w *FakePut) Run(ctx context.Context, cl core.Cluster, r *rand.Rand, clock core.StepClock, rec history.Recorder) error {
	if len(cl.Nodes) == 0 {
		return fmt.Errorf("fake-put: empty cluster")
	}
	// Per-client generators drawn up front so the shared r is not used concurrently.
	rs := make([]*rand.Rand, w.cfg.Clients)
	for i := range rs {
		rs[i] = rand.New(rand.NewPCG(r.Uint64(), r.Uint64()))
	}
	var gap time.Duration
	if w.cfg.OpsPerSec > 0 {
		gap = time.Duration(w.cfg.Clients) * time.Second / time.Duration(w.cfg.OpsPerSec)
	}
	var wg sync.WaitGroup
	for c := 0; c < w.cfg.Clients; c++ {
		wg.Add(1)
		go func(c int, cr *rand.Rand) {
			defer wg.Done()
			for n := 1; ctx.Err() == nil; n++ {
				node := cl.Nodes[cr.IntN(len(cl.Nodes))]
				key := fmt.Sprintf("k%d", cr.IntN(w.cfg.Keys))
				val := fmt.Sprintf("c%d-%d", c, n)
				rec.Record(history.Op{Client: c, Type: history.Invoke, F: history.Write, Key: key, Value: val, Node: node.Name, Step: clock.Step()})
				body, _ := json.Marshal(map[string]string{"value": val})
				code, _, err := w.do(ctx, http.MethodPut, node.ClientURL+"/kv/"+key, body)
				op := history.Op{Client: c, Type: classify(history.Write, code, err), F: history.Write, Key: key, Value: val, Node: node.Name, Step: clock.Step()}
				if err != nil {
					op.Error = err.Error()
				} else if op.Type != history.OK {
					op.Error = fmt.Sprintf("http %d", code)
				}
				rec.Record(op)
				if gap > 0 {
					select {
					case <-ctx.Done():
					case <-time.After(gap):
					}
				}
			}
		}(c, rs[c])
	}
	wg.Wait()
	return nil
}

// FinalReads implements core.Workload: GET every key from every node.
func (w *FakePut) FinalReads(ctx context.Context, cl core.Cluster, clock core.StepClock, rec history.Recorder) error {
	for k := 0; k < w.cfg.Keys; k++ {
		key := fmt.Sprintf("k%d", k)
		for _, node := range cl.Nodes {
			rec.Record(history.Op{Client: 0, Type: history.Invoke, F: history.Read, Key: key, Node: node.Name, Step: clock.Step(), Final: true})
			code, b, err := w.do(ctx, http.MethodGet, node.ClientURL+"/kv/"+key, nil)
			op := history.Op{Client: 0, Type: classify(history.Read, code, err), F: history.Read, Key: key, Node: node.Name, Step: clock.Step(), Final: true}
			if err != nil {
				op.Error = err.Error()
			} else if code == 200 {
				var v struct{ Value string }
				if jerr := json.Unmarshal(b, &v); jerr != nil {
					// Unreadable reply: the outcome is unknown.
					op.Type, op.Error = history.Info, "bad reply: "+jerr.Error()
				} else {
					op.Value = v.Value
				}
			} else if op.Type != history.OK {
				op.Error = fmt.Sprintf("http %d", code)
			}
			rec.Record(op)
		}
	}
	return ctx.Err()
}
