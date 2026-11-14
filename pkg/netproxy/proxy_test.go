package netproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kurukshetra/pkg/trace"
)

type fixedClock struct{ s atomic.Int64 }

func (c *fixedClock) Step() int64 { return c.s.Load() }

type recNode struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string // "path|from|body"
}

func newRecNode(t *testing.T) *recNode {
	n := &recNode{}
	n.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n.mu.Lock()
		n.hits = append(n.hits, r.URL.Path+"|"+r.Header.Get("X-Kuru-From")+"|"+string(b)+"|"+r.Header.Get("X-Custom"))
		n.mu.Unlock()
		w.Header().Set("X-Reply", "yes")
		_, _ = fmt.Fprintf(w, "reply:%s", b)
	}))
	t.Cleanup(n.srv.Close)
	return n
}

func (n *recNode) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.hits)
}

func setup(t *testing.T) (*Proxy, map[string]*recNode, *trace.Writer, string) {
	t.Helper()
	nodes := map[string]*recNode{"n1": newRecNode(t), "n2": newRecNode(t), "n3": newRecNode(t)}
	targets := map[string]string{}
	for k, n := range nodes {
		targets[k] = strings.TrimPrefix(n.srv.URL, "http://")
	}
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	tw, err := trace.NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New([]string{"n3", "n1", "n2"}, targets, &fixedClock{}, tw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, nodes, tw, path
}

func send(t *testing.T, p *Proxy, from, to, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", p.Port(from, to), path), strings.NewReader(body))
	req.Header.Set("X-Custom", "c1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestPeerEnv(t *testing.T) {
	p, _, _, _ := setup(t)
	p.SetHost("h")
	env := p.PeerEnv("n1")
	want := fmt.Sprintf("n2=h:%d,n3=h:%d", p.Port("n1", "n2"), p.Port("n1", "n3"))
	if env != want {
		t.Fatalf("got %q want %q", env, want)
	}
	if p.Port("n1", "n1") != 0 {
		t.Fatal("self link")
	}
}

func TestSequentialIDsAndIntactDelivery(t *testing.T) {
	p, nodes, tw, path := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { PassThrough(ctx, p); close(done) }()
	for i := 1; i <= 3; i++ {
		resp, body := send(t, p, "n1", "n2", "/peer/append", fmt.Sprintf("b%d", i))
		if resp.StatusCode != 200 || body != fmt.Sprintf("reply:b%d", i) || resp.Header.Get("X-Reply") != "yes" {
			t.Fatalf("resp %d %q", resp.StatusCode, body)
		}
	}
	send(t, p, "n2", "n1", "/peer/vote", "x")
	p.SetIDPrefix("s")
	send(t, p, "n1", "n2", "/peer/append", "s")
	p.SetIDPrefix("")
	send(t, p, "n1", "n2", "/peer/append", "after")
	cancel()
	<-done
	_ = tw.Close()
	evs, _ := trace.ReadFile(path)
	var ids []string
	for _, e := range evs {
		if e.Kind == trace.MsgSend {
			ids = append(ids, e.MsgID)
			if e.Meta["mode"] != "http" || e.Meta["path"] == "" {
				t.Fatalf("meta %v", e.Meta)
			}
		}
	}
	if strings.Join(ids, ",") != "n1-n2-1,n1-n2-2,n1-n2-3,n2-n1-1,n1-n2-s1,n1-n2-4" {
		t.Fatalf("ids %v", ids)
	}
	if nodes["n2"].hits[0] != "/peer/append|n1|b1|c1" {
		t.Fatalf("hit %q", nodes["n2"].hits[0])
	}
}

// manual runs a scheduler stand-in that applies fn to every message.
func manual(p *Proxy, fn func(m *Message)) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-p.Incoming():
				fn(m)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func TestDropGives504(t *testing.T) {
	p, nodes, tw, path := setup(t)
	stop := manual(p, func(m *Message) { p.Drop(m, "test") })
	resp, _ := send(t, p, "n1", "n3", "/peer/x", "a")
	stop()
	if resp.StatusCode != http.StatusGatewayTimeout || nodes["n3"].count() != 0 {
		t.Fatalf("status %d hits %d", resp.StatusCode, nodes["n3"].count())
	}
	_ = tw.Close()
	evs, _ := trace.ReadFile(path)
	if evs[len(evs)-1].Kind != trace.MsgDrop || evs[len(evs)-1].Meta["reason"] != "test" {
		t.Fatalf("%+v", evs)
	}
}

func TestDuplicateHitsTwice(t *testing.T) {
	p, nodes, tw, path := setup(t)
	stop := manual(p, func(m *Message) { go func() { _ = p.Duplicate(m) }() })
	resp, body := send(t, p, "n2", "n3", "/peer/x", "d")
	if resp.StatusCode != 200 || body != "reply:d" {
		t.Fatal(resp.StatusCode, body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for nodes["n3"].count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if nodes["n3"].count() != 2 {
		t.Fatalf("hits %d", nodes["n3"].count())
	}
	_ = p.Close()
	_ = tw.Close()
	evs, _ := trace.ReadFile(path)
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, string(e.Kind))
	}
	if strings.Join(kinds, ",") != "msg_send,msg_dup,msg_deliver,msg_deliver" {
		t.Fatal(kinds)
	}
}

func TestBlockedLinksDropWithPartition(t *testing.T) {
	p, nodes, tw, path := setup(t)
	p.SetBlocked(map[[2]string]bool{{"n1", "n2"}: true, {"n2", "n1"}: true, {"n3", "n1"}: false})
	if !p.Blocked("n1", "n2") || p.Blocked("n3", "n1") || p.Blocked("n1", "n3") {
		t.Fatal("blocked set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { PassThrough(ctx, p); close(done) }()
	r1, _ := send(t, p, "n1", "n2", "/peer/x", "a")
	r2, _ := send(t, p, "n1", "n3", "/peer/x", "a")
	cancel()
	<-done
	if r1.StatusCode != 504 || r2.StatusCode != 200 || nodes["n2"].count() != 0 {
		t.Fatal(r1.StatusCode, r2.StatusCode)
	}
	_ = tw.Close()
	evs, _ := trace.ReadFile(path)
	found := false
	for _, e := range evs {
		found = found || (e.Kind == trace.MsgDrop && e.MsgID == "n1-n2-1" && e.Meta["reason"] == "partition")
	}
	if !found {
		t.Fatal("no partition drop")
	}
}

func TestSenderGoneStillRecorded(t *testing.T) {
	p, _, tw, path := setup(t)
	got := make(chan *Message, 1)
	stop := manual(p, func(m *Message) { got <- m })
	cl := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := cl.Post(fmt.Sprintf("http://127.0.0.1:%d/peer/x", p.Port("n3", "n2")), "text/plain", strings.NewReader("z"))
	if err == nil {
		t.Fatal("expected client timeout")
	}
	m := <-got
	stop()
	if err := p.Deliver(m); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	evs, _ := trace.ReadFile(path)
	if evs[len(evs)-1].Kind != trace.MsgDeliver {
		t.Fatal("decision not recorded")
	}
}

func TestCloseIdempotentNoLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		p, err := New([]string{"a", "b", "c"}, map[string]string{"a": "127.0.0.1:1", "b": "127.0.0.1:1", "c": "127.0.0.1:1"}, &fixedClock{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		_ = p.Close()
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
}
