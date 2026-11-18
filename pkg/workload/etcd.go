package workload

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/scenario"
)

func init() {
	core.RegisterWorkload("etcd-kv", NewEtcdKV)
}

// NewEtcdKV builds the "etcd-kv" workload: the same register workload as "kv",
// speaking etcd's HTTP API. The API is chosen per node from GET /version:
// etcd 2.x → the v2 keys API with default reads (served by whichever member is
// asked, which Jepsen showed can be stale); etcd 3.x → the v3 JSON gateway,
// whose reads are linearizable by default.
func NewEtcdKV(cfg scenario.Workload) (core.Workload, error) {
	return newRegisterWorkload("etcd-kv", cfg, &etcdBackend{api: map[string]string{}})
}

// etcdBackend speaks etcd v2 or v3 depending on each node's version.
type etcdBackend struct {
	mu  sync.Mutex
	api map[string]string // node client URL -> "v2" | "v3"
}

// call implements backend.
func (e *etcdBackend) call(ctx context.Context, c *http.Client, req request) result {
	api, err := e.apiFor(ctx, c, req.node.ClientURL)
	if err != nil {
		// Nothing was sent: a read is harmless, a write definitely did not happen.
		return result{Type: history.Fail, Error: "version probe: " + err.Error()}
	}
	if api == "v2" {
		return etcdV2(ctx, c, req)
	}
	return etcdV3(ctx, c, req)
}

// apiFor returns (and caches) the API version of the node at base.
func (e *etcdBackend) apiFor(ctx context.Context, c *http.Client, base string) (string, error) {
	e.mu.Lock()
	api, ok := e.api[base]
	e.mu.Unlock()
	if ok {
		return api, nil
	}
	code, b, err := doHTTP(ctx, c, http.MethodGet, base+"/version", "", nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("http %d", code)
	}
	var v struct {
		Server string `json:"etcdserver"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Server == "" {
		return "", fmt.Errorf("bad /version %q", strings.TrimSpace(string(b)))
	}
	api = "v3"
	if strings.HasPrefix(v.Server, "2.") || strings.HasPrefix(v.Server, "0.") {
		api = "v2"
	}
	e.mu.Lock()
	e.api[base] = api
	e.mu.Unlock()
	return api, nil
}

// unknown is the result for a request whose effect is unknown.
func unknown(f history.F, code int, err error, body []byte) result {
	msg := fmt.Sprintf("http %d: %s", code, strings.TrimSpace(string(body)))
	if err != nil {
		msg = err.Error()
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return result{Type: history.Info, Error: msg}
}

// etcdV2 performs one op with the v2 keys API (/v2/keys/<key>).
func etcdV2(ctx context.Context, c *http.Client, req request) result {
	u := req.node.ClientURL + "/v2/keys/" + url.PathEscape(req.key)
	if req.f == history.Read {
		code, b, err := doHTTP(ctx, c, http.MethodGet, u, "", nil)
		switch {
		case err != nil:
			return unknown(req.f, code, err, nil)
		case code == http.StatusOK:
			var v struct {
				Node struct {
					Value string `json:"value"`
				} `json:"node"`
			}
			if err := json.Unmarshal(b, &v); err != nil {
				return unknown(req.f, code, err, nil)
			}
			return result{Type: history.OK, Value: v.Node.Value}
		case code == http.StatusNotFound && v2ErrorCode(b) == 100:
			return result{Type: history.OK, Value: history.Missing}
		}
		return unknown(req.f, code, nil, b)
	}
	q := url.Values{}
	if req.f == history.CAS {
		if req.expect == history.Missing {
			q.Set("prevExist", "false")
		} else {
			q.Set("prevValue", req.expect)
		}
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	form := url.Values{"value": {req.value}}.Encode()
	code, b, err := doHTTP(ctx, c, http.MethodPut, u, "application/x-www-form-urlencoded", strings.NewReader(form))
	switch {
	case err != nil:
		return unknown(req.f, code, err, nil)
	case code == http.StatusOK || code == http.StatusCreated:
		return result{Type: history.OK}
	case req.f == history.CAS && (code == http.StatusPreconditionFailed || code == http.StatusNotFound):
		// 101 compare failed, 105 node exists, 100 key not found: definitely not applied.
		return result{Type: history.Fail, Error: fmt.Sprintf("http %d: cas mismatch (etcd error %d)", code, v2ErrorCode(b))}
	case code >= 400 && code < 500:
		return result{Type: history.Fail, Error: fmt.Sprintf("http %d: %s", code, strings.TrimSpace(string(b)))}
	}
	return unknown(req.f, code, nil, b)
}

func v2ErrorCode(b []byte) int {
	var e struct {
		ErrorCode int `json:"errorCode"`
	}
	_ = json.Unmarshal(b, &e)
	return e.ErrorCode
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// etcdV3 performs one op with the v3 JSON gateway (/v3/kv/range|put|txn).
func etcdV3(ctx context.Context, c *http.Client, req request) result {
	base := req.node.ClientURL + "/v3/kv/"
	var path string
	var body any
	switch req.f {
	case history.Read:
		path, body = "range", map[string]any{"key": b64(req.key)}
	case history.Write:
		path, body = "put", map[string]any{"key": b64(req.key), "value": b64(req.value)}
	default:
		cmp := map[string]any{"key": b64(req.key), "result": "EQUAL", "target": "VALUE", "value": b64(req.expect)}
		if req.expect == history.Missing {
			cmp = map[string]any{"key": b64(req.key), "result": "EQUAL", "target": "CREATE", "create_revision": "0"}
		}
		path, body = "txn", map[string]any{
			"compare": []any{cmp},
			"success": []any{map[string]any{"request_put": map[string]any{"key": b64(req.key), "value": b64(req.value)}}},
		}
	}
	bs, _ := json.Marshal(body)
	code, b, err := doHTTP(ctx, c, http.MethodPost, base+path, "application/json", bytes.NewReader(bs))
	if err != nil {
		return unknown(req.f, code, err, nil)
	}
	if code != http.StatusOK {
		if code >= 400 && code < 500 {
			return result{Type: history.Fail, Error: fmt.Sprintf("http %d: %s", code, strings.TrimSpace(string(b)))}
		}
		return unknown(req.f, code, nil, b)
	}
	switch req.f {
	case history.Read:
		var v struct {
			Kvs []struct {
				Value string `json:"value"`
			} `json:"kvs"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return unknown(req.f, code, err, nil)
		}
		if len(v.Kvs) == 0 {
			return result{Type: history.OK, Value: history.Missing}
		}
		raw, err := base64.StdEncoding.DecodeString(v.Kvs[0].Value)
		if err != nil {
			return unknown(req.f, code, err, nil)
		}
		return result{Type: history.OK, Value: string(raw)}
	case history.CAS:
		var v struct {
			Succeeded bool `json:"succeeded"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return unknown(req.f, code, err, nil)
		}
		if !v.Succeeded {
			return result{Type: history.Fail, Error: "cas mismatch (txn not succeeded)"}
		}
	}
	return result{Type: history.OK}
}
