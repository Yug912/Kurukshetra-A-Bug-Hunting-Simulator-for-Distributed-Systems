package workload

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"kurukshetra/pkg/checker"
	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
)

// fakeEtcd is an in-memory, linearizable etcd speaking either the v2 or the v3 HTTP API.
type fakeEtcd struct {
	mu      sync.Mutex
	kv      map[string]string
	version string
}

func (f *fakeEtcd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/version" {
		_, _ = w.Write([]byte(`{"etcdserver":"` + f.version + `","etcdcluster":"x"}`))
		return
	}
	if key, ok := strings.CutPrefix(r.URL.Path, "/v2/keys/"); ok && strings.HasPrefix(f.version, "2.") {
		f.v2(w, r, key, body)
		return
	}
	if op, ok := strings.CutPrefix(r.URL.Path, "/v3/kv/"); ok && strings.HasPrefix(f.version, "3.") {
		f.v3(w, op, body)
		return
	}
	w.WriteHeader(404)
}

func (f *fakeEtcd) v2(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	cur, exists := f.kv[key]
	if r.Method == http.MethodGet {
		if !exists {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"errorCode":100,"message":"Key not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"action": "get", "node": map[string]string{"key": "/" + key, "value": cur}})
		return
	}
	form, _ := url.ParseQuery(string(body))
	q := r.URL.Query()
	if q.Get("prevExist") == "false" && exists {
		w.WriteHeader(412)
		_, _ = w.Write([]byte(`{"errorCode":105}`))
		return
	}
	if pv := q.Get("prevValue"); pv != "" {
		if !exists {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"errorCode":100}`))
			return
		}
		if cur != pv {
			w.WriteHeader(412)
			_, _ = w.Write([]byte(`{"errorCode":101}`))
			return
		}
	}
	f.kv[key] = form.Get("value")
	w.WriteHeader(201)
	_, _ = w.Write([]byte(`{"action":"set"}`))
}

func dec(s string) string { b, _ := base64.StdEncoding.DecodeString(s); return string(b) }

func (f *fakeEtcd) v3(w http.ResponseWriter, op string, body []byte) {
	var in struct {
		Key     string `json:"key"`
		Value   string `json:"value"`
		Compare []struct {
			Key, Result, Target, Value string
			CreateRevision             string `json:"create_revision"`
		} `json:"compare"`
		Success []struct {
			RequestPut struct{ Key, Value string } `json:"request_put"`
		} `json:"success"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		w.WriteHeader(400)
		return
	}
	switch op {
	case "range":
		v, ok := f.kv[dec(in.Key)]
		if !ok {
			_, _ = w.Write([]byte(`{"header":{}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"kvs": []map[string]string{{"key": in.Key, "value": base64.StdEncoding.EncodeToString([]byte(v))}}})
	case "put":
		f.kv[dec(in.Key)] = dec(in.Value)
		_, _ = w.Write([]byte(`{"header":{}}`))
	case "txn":
		c := in.Compare[0]
		cur, exists := f.kv[dec(c.Key)]
		ok := (c.Target == "VALUE" && exists && cur == dec(c.Value)) || (c.Target == "CREATE" && c.CreateRevision == "0" && !exists)
		if ok {
			p := in.Success[0].RequestPut
			f.kv[dec(p.Key)] = dec(p.Value)
			_, _ = w.Write([]byte(`{"succeeded":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"header":{}}`)) // succeeded omitted = false
	}
}

func TestEtcdWorkloadBothAPIs(t *testing.T) {
	for _, version := range []string{"2.3.8", "3.5.17"} {
		t.Run(version, func(t *testing.T) {
			srv := httptest.NewServer(&fakeEtcd{kv: map[string]string{}, version: version})
			defer srv.Close()
			w, err := core.NewWorkload(scenario.Workload{Type: "etcd-kv", Clients: 3, Keys: 2, OpsPerSec: 1500, TimeoutMs: 500, ReadFrac: 0.4, WriteFrac: 0.3, CasFrac: 0.3})
			if err != nil {
				t.Fatal(err)
			}
			cl := cluster(srv.URL, srv.URL, srv.URL)
			var rec history.MemRecorder
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			clock := &counterClock{}
			if err := w.Run(ctx, cl, rng.New(3).Stream(rng.StreamWorkload), clock, &rec); err != nil {
				t.Fatal(err)
			}
			if err := w.FinalReads(context.Background(), cl, clock, &rec); err != nil {
				t.Fatal(err)
			}
			ops := rec.Ops()
			pairs, err := history.Pairs(ops)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, p := range pairs {
				got[string(p.Invoke.F)+"/"+string(p.Complete.Type)]++
			}
			t.Logf("%s: %v", version, got)
			for _, k := range []string{"read/ok", "write/ok", "cas/ok", "cas/fail"} {
				if got[k] == 0 {
					t.Errorf("no %s ops", k)
				}
			}
			if got["read/info"]+got["write/info"]+got["cas/info"] > 3 {
				t.Errorf("unexpected info ops: %v", got)
			}
			// A linearizable server must produce a history that checks PASS.
			c, _ := core.NewChecker(checker.LinearizableName)
			if r := c.Check(ops, ""); r.Verdict != core.VerdictPass {
				t.Fatalf("checker: %+v", r)
			}
		})
	}
}

func TestEtcdVersionProbeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	b := &etcdBackend{api: map[string]string{}}
	res := b.call(context.Background(), http.DefaultClient, request{f: history.Write, key: "k", value: "v", node: core.Node{Name: "n1", ClientURL: srv.URL}})
	if res.Type != history.Fail || !strings.Contains(res.Error, "version probe") {
		t.Fatalf("%+v", res)
	}
	if len(b.api) != 0 {
		t.Fatal("failed probe must not be cached")
	}
}
