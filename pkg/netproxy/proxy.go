// Package netproxy sits between every ordered pair of nodes. It parks each
// node-to-node message and executes the scheduler's decision for it (deliver,
// drop, duplicate). The proxy itself never decides anything.
//
// For every ordered pair (i -> j) the proxy opens one listener; node i's PEERS
// entry for j points at that listener, so the port alone identifies sender and
// receiver.
package netproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/trace"
)

// MaxBody is the largest message body accepted (64 MB).
const MaxBody = 64 << 20

// DefaultHost is the address containers use to reach the proxy on the host.
// Override with the KURU_PROXY_HOST environment variable or SetHost.
const DefaultHost = "host.docker.internal"

// Message is one parked node-to-node request.
type Message struct {
	ID          string // "<from>-<to>-<n>"
	From, To    string
	Path        string
	Size        int
	ArrivedStep int64

	method  string
	query   string
	header  http.Header
	body    []byte
	reply   chan reply // buffered(1); first reply wins
	replied atomic.Bool

	// tcp mode (see tcp.go)
	conn *tcpConn
	seq  int
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (m *Message) respond(r reply) {
	if m.reply == nil || !m.replied.CompareAndSwap(false, true) {
		return
	}
	m.reply <- r
}

type link struct{ from, to string }

// Proxy parks messages between nodes.
type Proxy struct {
	clock core.StepClock
	tw    *trace.Writer
	mode  string

	mu       sync.RWMutex
	host     string
	targets  map[string]string // node -> PeerHostAddr
	ports    map[link]int
	counters map[string]int // "<from>-<to>-<prefix>" -> last n
	prefix   string
	namer    Namer
	blocked  map[[2]string]bool

	incoming  chan *Message
	listeners []net.Listener
	servers   []*http.Server
	fwd       *http.Client
	tcp       *tcpState
	wg        sync.WaitGroup
	closeOnce sync.Once
	closed    chan struct{}
}

// Option tunes a Proxy.
type Option func(*Proxy)

// WithMode sets "http" (default) or "tcp".
func WithMode(mode string) Option { return func(p *Proxy) { p.mode = mode } }

// New opens one listener per ordered pair of nodes. targets maps node name to
// the host address of that node's peer port (PeerHostAddr). tw may be nil.
func New(nodes []string, targets map[string]string, clock core.StepClock, tw *trace.Writer, opts ...Option) (*Proxy, error) {
	host := os.Getenv("KURU_PROXY_HOST")
	if host == "" {
		host = DefaultHost
	}
	p := &Proxy{
		clock: clock, tw: tw, mode: "http", host: host,
		targets:  map[string]string{},
		ports:    map[link]int{},
		counters: map[string]int{},
		blocked:  map[[2]string]bool{},
		incoming: make(chan *Message, 1<<16),
		fwd:      &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second}},
		closed:   make(chan struct{}),
	}
	for _, o := range opts {
		o(p)
	}
	for k, v := range targets {
		p.targets[k] = v
	}
	sorted := append([]string(nil), nodes...)
	sort.Strings(sorted)
	if p.mode == "tcp" {
		p.tcp = newTCPState()
	}
	for _, from := range sorted {
		for _, to := range sorted {
			if from == to {
				continue
			}
			l, err := listenUnshadowed()
			if err != nil {
				p.Close()
				return nil, fmt.Errorf("proxy listen %s->%s: %w", from, to, err)
			}
			lk := link{from, to}
			p.ports[lk] = l.Addr().(*net.TCPAddr).Port
			p.listeners = append(p.listeners, l)
			if p.mode == "tcp" {
				p.wg.Add(1)
				go p.acceptTCP(l, lk)
				continue
			}
			srv := &http.Server{Handler: p.handler(lk), ReadHeaderTimeout: 10 * time.Second}
			p.servers = append(p.servers, srv)
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				_ = srv.Serve(l)
			}()
		}
	}
	return p, nil
}

// listenUnshadowed listens on 0.0.0.0:<random> and makes sure nothing else
// holds 127.0.0.1 on the same port: on macOS such a socket (e.g. a Docker port
// forward) would silently take the loopback traffic meant for the proxy.
func listenUnshadowed() (net.Listener, error) {
	var lastErr error
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			return nil, err
		}
		port := l.Addr().(*net.TCPAddr).Port
		probe, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			probe.Close()
			reservePort(port)
			return l, nil
		}
		lastErr = err
		l.Close()
	}
	return nil, fmt.Errorf("no unshadowed port: %w", lastErr)
}

// Ports returns every port the proxy listens on.
func (p *Proxy) Ports() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]int, 0, len(p.ports))
	for _, port := range p.ports {
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}

// SetHost changes the host name nodes use to reach the proxy (default
// host.docker.internal). Call before PeerEnv.
func (p *Proxy) SetHost(h string) {
	p.mu.Lock()
	p.host = h
	p.mu.Unlock()
}

// SetTarget updates the host address of node's peer port (e.g. after a restart).
func (p *Proxy) SetTarget(node, addr string) {
	p.mu.Lock()
	p.targets[node] = addr
	p.mu.Unlock()
}

// Port returns the proxy port for the link from -> to (0 if none).
func (p *Proxy) Port(from, to string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ports[link{from, to}]
}

// PeerEnv returns the PEERS value for node: "nj=<host>:<port of node->nj>,...", sorted.
func (p *Proxy) PeerEnv(node string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var parts []string
	for lk, port := range p.ports {
		if lk.from == node {
			parts = append(parts, lk.to+"="+net.JoinHostPort(p.host, strconv.Itoa(port)))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Incoming delivers every parked message to the scheduler.
func (p *Proxy) Incoming() <-chan *Message { return p.incoming }

// SetIDPrefix sets a prefix for the counter part of new message IDs, with its
// own counters: with prefix "s" IDs are "<from>-<to>-s<n>". The engine uses it
// for startup traffic so that the IDs of the run proper ("<from>-<to>-<n>")
// always start at 1 when the logical clock starts, however long startup took.
func (p *Proxy) SetIDPrefix(prefix string) {
	p.mu.Lock()
	p.prefix = prefix
	p.mu.Unlock()
}

// Namer names run-phase messages during a replay. It is called (under the
// proxy's lock; it must not call the proxy) for every message that arrives
// while no ID prefix is set, with the current step, and returns the ID to use;
// "" falls back to the normal "<from>-<to>-<n>" counter.
type Namer func(from, to string, arrivedStep int64) string

// SetNamer installs a Namer (replay only).
func (p *Proxy) SetNamer(n Namer) {
	p.mu.Lock()
	p.namer = n
	p.mu.Unlock()
}

func (p *Proxy) nextID(lk link) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prefix == "" && p.namer != nil {
		if id := p.namer(lk.from, lk.to, p.step()); id != "" {
			return id
		}
	}
	base := lk.from + "-" + lk.to + "-" + p.prefix
	p.counters[base]++
	return base + strconv.Itoa(p.counters[base])
}

func (p *Proxy) emit(e trace.Event) {
	if p.tw == nil {
		return
	}
	if e.Step == 0 && p.clock != nil {
		e.Step = p.clock.Step()
	}
	p.tw.Append(e)
}

// Emit writes a trace event stamped with the current step (used by the scheduler
// for msg_delay so all message events share one writer).
func (p *Proxy) Emit(e trace.Event) { p.emit(e) }

func (p *Proxy) step() int64 {
	if p.clock == nil {
		return 0
	}
	return p.clock.Step()
}

func (p *Proxy) handler(lk link) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > MaxBody {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		m := &Message{
			ID: p.nextID(lk), From: lk.from, To: lk.to, Path: r.URL.Path, Size: len(body),
			ArrivedStep: p.step(), method: r.Method, query: r.URL.RawQuery,
			header: r.Header.Clone(), body: body, reply: make(chan reply, 1),
		}
		m.header.Set("X-Kuru-From", lk.from)
		p.emit(trace.Event{Step: m.ArrivedStep, Kind: trace.MsgSend, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size,
			Meta: map[string]string{"path": m.Path, "mode": "http"}})
		select {
		case p.incoming <- m:
		case <-p.closed:
			http.Error(w, "proxy closed", http.StatusServiceUnavailable)
			return
		}
		select {
		case rp := <-m.reply:
			for k, vs := range rp.header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rp.status)
			_, _ = w.Write(rp.body)
		case <-r.Context().Done():
			// Sender gave up; the scheduler still decides and records the message.
		case <-p.closed:
			http.Error(w, "proxy closed", http.StatusServiceUnavailable)
		}
	})
}

func (p *Proxy) forward(m *Message) (reply, error) {
	p.mu.RLock()
	target := p.targets[m.To]
	p.mu.RUnlock()
	if target == "" {
		return reply{}, fmt.Errorf("no target for node %s", m.To)
	}
	url := "http://" + target + m.Path
	if m.query != "" {
		url += "?" + m.query
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-p.closed:
			cancel()
		case <-ctx.Done():
		}
	}()
	req, err := http.NewRequestWithContext(ctx, m.method, url, bytes.NewReader(m.body))
	if err != nil {
		return reply{}, err
	}
	req.Header = m.header.Clone()
	resp, err := p.fwd.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return reply{}, err
	}
	h := resp.Header.Clone()
	h.Del("Content-Length")
	return reply{status: resp.StatusCode, header: h, body: b}, nil
}

// Deliver emits msg_deliver, forwards the message to its target and writes the
// reply back to the waiting sender. A forwarding error answers 502 to the sender.
func (p *Proxy) Deliver(m *Message) error {
	if m.conn != nil {
		return p.deliverTCP(m)
	}
	p.emit(trace.Event{Kind: trace.MsgDeliver, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: map[string]string{"path": m.Path}})
	rp, err := p.forward(m)
	if err != nil {
		m.respond(reply{status: http.StatusBadGateway, body: []byte(`{"error":"proxy_forward"}`)})
		return fmt.Errorf("deliver %s: %w", m.ID, err)
	}
	m.respond(rp)
	return nil
}

// Drop emits msg_drop (meta reason) and answers 504 to the sender.
func (p *Proxy) Drop(m *Message, reason string) {
	meta := map[string]string{"path": m.Path}
	if reason != "" {
		meta["reason"] = reason
	}
	if m.conn != nil {
		p.dropTCP(m, meta)
		return
	}
	p.emit(trace.Event{Kind: trace.MsgDrop, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: meta})
	m.respond(reply{status: http.StatusGatewayTimeout, body: []byte(`{"error":"dropped"}`)})
}

// Duplicate emits msg_dup and delivers the message twice; the sender receives
// the first reply and the second is discarded.
func (p *Proxy) Duplicate(m *Message) error {
	if m.conn != nil { // duplicate is not allowed in tcp mode
		return p.deliverTCP(m)
	}
	p.emit(trace.Event{Kind: trace.MsgDup, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: map[string]string{"path": m.Path}})
	err1 := p.Deliver(m)
	p.emit(trace.Event{Kind: trace.MsgDeliver, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: map[string]string{"path": m.Path, "dup": "true"}})
	_, err2 := p.forward(m)
	return errors.Join(err1, err2)
}

// SetBlocked replaces the set of partitioned links (keys are [from, to]).
func (p *Proxy) SetBlocked(blocked map[[2]string]bool) {
	c := make(map[[2]string]bool, len(blocked))
	for k, v := range blocked {
		if v {
			c[k] = true
		}
	}
	p.mu.Lock()
	p.blocked = c
	p.mu.Unlock()
	if p.tcp != nil {
		p.resetBlockedTCP()
	}
}

// Blocked reports whether the link from -> to is partitioned.
func (p *Proxy) Blocked(from, to string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.blocked[[2]string{from, to}]
}

// Close stops all listeners, fails parked senders and waits for goroutines. Idempotent.
func (p *Proxy) Close() error {
	var err error
	p.closeOnce.Do(func() {
		close(p.closed)
		for _, s := range p.servers {
			_ = s.Close()
		}
		for _, l := range p.listeners {
			_ = l.Close()
		}
		if p.tcp != nil {
			p.closeTCP()
		}
		p.wg.Wait()
		if t, ok := p.fwd.Transport.(*http.Transport); ok {
			t.CloseIdleConnections()
		}
	})
	return err
}

// PassThrough delivers every message immediately (dropping those on blocked
// links with reason "partition") until ctx is done. It is a stand-in for the
// scheduler in tests.
func PassThrough(ctx context.Context, p *Proxy) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.closed:
			return
		case m := <-p.incoming:
			if p.Blocked(m.From, m.To) {
				p.Drop(m, "partition")
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = p.Deliver(m)
			}()
		}
	}
}

// DeliverAsync emits msg_deliver now (so trace order follows call order) and
// forwards in a goroutine tracked by wg. Errors are reported to onErr (may be nil).
func (p *Proxy) DeliverAsync(m *Message, wg *sync.WaitGroup, onErr func(error)) {
	if m.conn != nil {
		p.deliverTCPAsync(m, wg, onErr)
		return
	}
	p.emit(trace.Event{Kind: trace.MsgDeliver, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: map[string]string{"path": m.Path}})
	wg.Add(1)
	go func() {
		defer wg.Done()
		rp, err := p.forward(m)
		if err != nil {
			m.respond(reply{status: http.StatusBadGateway, body: []byte(`{"error":"proxy_forward"}`)})
			if onErr != nil {
				onErr(fmt.Errorf("deliver %s: %w", m.ID, err))
			}
			return
		}
		m.respond(rp)
	}()
}

// DuplicateAsync emits msg_dup and both msg_deliver events now and performs the
// two forwards in a goroutine tracked by wg (sender gets the first reply).
// In tcp mode it is a plain delivery.
func (p *Proxy) DuplicateAsync(m *Message, wg *sync.WaitGroup, onErr func(error)) {
	if m.conn != nil {
		p.deliverTCPAsync(m, wg, onErr)
		return
	}
	meta := map[string]string{"path": m.Path}
	p.emit(trace.Event{Kind: trace.MsgDup, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: meta})
	p.emit(trace.Event{Kind: trace.MsgDeliver, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: meta})
	p.emit(trace.Event{Kind: trace.MsgDeliver, From: m.From, To: m.To, MsgID: m.ID, Size: m.Size, Meta: map[string]string{"path": m.Path, "dup": "true"}})
	wg.Add(1)
	go func() {
		defer wg.Done()
		rp, err := p.forward(m)
		if err != nil {
			m.respond(reply{status: http.StatusBadGateway, body: []byte(`{"error":"proxy_forward"}`)})
		} else {
			m.respond(rp)
		}
		_, err2 := p.forward(m)
		if e := errors.Join(err, err2); e != nil && onErr != nil {
			onErr(fmt.Errorf("duplicate %s: %w", m.ID, e))
		}
	}()
}
