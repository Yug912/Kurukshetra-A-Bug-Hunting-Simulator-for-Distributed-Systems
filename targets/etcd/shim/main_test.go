package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{ID: "n2", Peers: map[string]string{"n1": "proxy-a:9001", "n3": "proxy-b:9003"}, PeerListen: ":7000", ClientListen: ":8080", DataDir: "/data"}
}

func TestEtcdArgsAreTheSameClusterOnEveryNode(t *testing.T) {
	c := testConfig()
	args := strings.Join(c.EtcdArgs(), " ")
	for _, want := range []string{
		"--name n2",
		"--listen-peer-urls http://0.0.0.0:7000",
		"--initial-advertise-peer-urls http://n2:7101",
		"--initial-cluster n1=http://n1:7100,n2=http://n2:7101,n3=http://n3:7102",
		"--data-dir /data/etcd",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	// Another node sees different proxy addresses but must build the identical --initial-cluster.
	other := Config{ID: "n1", Peers: map[string]string{"n2": "x:1", "n3": "y:2"}, PeerListen: ":7000"}
	idx := slices.Index(other.EtcdArgs(), "--initial-cluster")
	if other.EtcdArgs()[idx+1] != "n1=http://n1:7100,n2=http://n2:7101,n3=http://n3:7102" {
		t.Fatal(other.EtcdArgs())
	}
}

func TestForwards(t *testing.T) {
	f := testConfig().Forwards()
	want := map[string]string{"127.0.0.1:7100": "proxy-a:9001", "127.0.0.1:7101": "127.0.0.1:7000", "127.0.0.1:7102": "proxy-b:9003"}
	if len(f) != 3 {
		t.Fatal(f)
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s -> %s, want %s", k, f[k], v)
		}
	}
}

func TestAddHostsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte("127.0.0.1 localhost"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := addHosts(p, []string{"n1", "n2"}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(p)
	if strings.Count(string(b), "127.0.0.1 n1 n2") != 1 {
		t.Fatalf("%q", b)
	}
}

func TestForwardPipesBothWays(t *testing.T) {
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	defer func() { _ = echo.Close() }()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forward(ctx, ln, echo.Addr().String())
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	_ = c.(*net.TCPConn).CloseWrite()
	b, _ := io.ReadAll(c)
	if string(b) != "ping" {
		t.Fatalf("%q", b)
	}
}

func TestHandler(t *testing.T) {
	etcd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/maintenance/status":
			_, _ = w.Write([]byte(`{"header":{"member_id":"11"},"leader":"22","raftTerm":"4"}`))
		case "/v3/cluster/member/list":
			_, _ = w.Write([]byte(`{"members":[{"ID":"11","name":"n2"},{"ID":"22","name":"n1"}]}`))
		default:
			_, _ = w.Write([]byte("proxied " + r.URL.Path))
		}
	}))
	defer etcd.Close()
	h := handler(testConfig(), strings.TrimPrefix(etcd.URL, "http://"))
	get := func(method, path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := get("GET", "/kuru/health"); code != 200 || body != `{"node":"n2","state":"follower","leader":"n1","term":4}` {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := get("POST", "/kuru/clock"); code != 404 {
		t.Fatalf("clock %d", code)
	}
	if code, body := get("GET", "/v2/keys/k0"); code != 200 || body != "proxied /v2/keys/k0" {
		t.Fatalf("proxy %d %s", code, body)
	}
	// etcd down: health still answers 200 with state "starting"; proxied calls get 502.
	down := handler(testConfig(), "127.0.0.1:1")
	rec := httptest.NewRecorder()
	down.ServeHTTP(rec, httptest.NewRequest("GET", "/kuru/health", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"starting"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	down.ServeHTTP(rec, httptest.NewRequest("GET", "/v2/keys/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("down proxy %d", rec.Code)
	}
}

func TestParsePeers(t *testing.T) {
	if p, err := parsePeers("n1=a:1,n3=b:2"); err != nil || len(p) != 2 {
		t.Fatal(p, err)
	}
	if _, err := parsePeers("bad"); err == nil {
		t.Fatal("want error")
	}
}
