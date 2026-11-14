package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

type testCluster struct {
	t       *testing.T
	nodes   map[string]*Node
	clients map[string]string // id -> base URL
}

// startCluster starts n in-process nodes on random localhost ports.
func startCluster(t *testing.T, n int, bugs Bugs) *testCluster {
	t.Helper()
	ids := make([]string, n)
	peerLns := map[string]net.Listener{}
	clientLns := map[string]net.Listener{}
	for i := range ids {
		ids[i] = fmt.Sprintf("n%d", i+1)
		pl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		cl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		peerLns[ids[i]], clientLns[ids[i]] = pl, cl
	}
	tc := &testCluster{t: t, nodes: map[string]*Node{}, clients: map[string]string{}}
	for _, id := range ids {
		peers := map[string]string{}
		for _, other := range ids {
			if other != id {
				peers[other] = peerLns[other].Addr().String()
			}
		}
		cfg := Config{ID: id, Peers: peers, DataDir: t.TempDir(), Bugs: bugs, LogLevel: "ERROR", LogOutput: io.Discard}
		node, err := StartWithListeners(cfg, peerLns[id], clientLns[id])
		if err != nil {
			t.Fatal(err)
		}
		tc.nodes[id] = node
		tc.clients[id] = "http://" + clientLns[id].Addr().String()
	}
	t.Cleanup(func() {
		for _, n := range tc.nodes {
			n.Shutdown()
		}
	})
	return tc
}

func (tc *testCluster) waitLeader(timeout time.Duration) string {
	tc.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for id, n := range tc.nodes {
			if n.State() == "leader" {
				return id
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	tc.t.Fatal("no leader elected")
	return ""
}

func do(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestClusterFailover(t *testing.T) {
	tc := startCluster(t, 3, Bugs{})
	for id, url := range tc.clients {
		if code, h := do(t, "GET", url+"/kuru/health", nil); code != 200 || h["node"] != id {
			t.Fatalf("health %s: %d %v", id, code, h)
		}
	}
	leader := tc.waitLeader(5 * time.Second)
	url := tc.clients[leader]

	if code, _ := do(t, "GET", url+"/kv/k0", nil); code != 404 {
		t.Fatalf("missing key: %d", code)
	}
	if code, _ := do(t, "PUT", url+"/kv/k0", map[string]string{"value": "v1"}); code != 200 {
		t.Fatalf("put: %d", code)
	}
	if code, b := do(t, "GET", url+"/kv/k0", nil); code != 200 || b["value"] != "v1" {
		t.Fatalf("get: %d %v", code, b)
	}
	if code, _ := do(t, "POST", url+"/kv/k0/cas", map[string]string{"expect": "nope", "value": "v2"}); code != 409 {
		t.Fatalf("cas mismatch: %d", code)
	}
	if code, _ := do(t, "POST", url+"/kv/k0/cas", map[string]string{"expect": "v1", "value": "v2"}); code != 200 {
		t.Fatalf("cas: %d", code)
	}
	for id, u := range tc.clients {
		if id == leader {
			continue
		}
		if code, b := do(t, "GET", u+"/kv/k0", nil); code != 421 || b["leader"] != leader {
			t.Fatalf("follower read: %d %v", code, b)
		}
	}
	if code, _ := do(t, "POST", url+"/kuru/clock", map[string]int{"offset_ms": 250}); code != 200 || tc.nodes[leader].clock.Offset() != 250 {
		t.Fatal("clock offset not set")
	}

	tc.nodes[leader].Shutdown()
	delete(tc.nodes, leader)
	newLeader := tc.waitLeader(5 * time.Second)
	if newLeader == leader {
		t.Fatal("same leader")
	}
	// The new leader may need a moment to commit its first entry.
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, b := do(t, "GET", tc.clients[newLeader]+"/kv/k0", nil)
		if code == 200 && b["value"] == "v2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read after failover: %d %v", code, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStaleReadBugServesFollowerReads(t *testing.T) {
	tc := startCluster(t, 3, Bugs{StaleRead: true})
	leader := tc.waitLeader(5 * time.Second)
	if code, _ := do(t, "PUT", tc.clients[leader]+"/kv/k", map[string]string{"value": "x"}); code != 200 {
		t.Fatal("put failed")
	}
	for id, u := range tc.clients {
		if id == leader {
			continue
		}
		// A follower answers locally instead of redirecting with 421.
		if code, _ := do(t, "GET", u+"/kv/k", nil); code == 421 {
			t.Fatalf("follower %s redirected despite BUG_STALE_READ", id)
		}
	}
}

func TestLeaseExpiresWithClock(t *testing.T) {
	n := &Node{clock: &Clock{}}
	if n.leaseValid() {
		t.Fatal("no ack yet")
	}
	n.lastAckNs.Store(n.clock.Now().UnixNano())
	if !n.leaseValid() {
		t.Fatal("fresh ack must be valid")
	}
	n.clock.SetOffset(500)
	if n.leaseValid() {
		t.Fatal("clock moved forward past lease")
	}
	n.clock.SetOffset(-800)
	n.lastAckNs.Store(time.Now().UnixNano()) // ack measured before the clock jumped back
	if !n.leaseValid() {
		t.Fatal("backward skew must extend the lease (the bug)")
	}
}
