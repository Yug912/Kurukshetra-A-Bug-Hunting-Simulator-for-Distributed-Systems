package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

const (
	applyTimeout  = 2 * time.Second
	leaseDuration = 300 * time.Millisecond
)

// Bugs are the deliberate bug flags (BUG_* env vars).
type Bugs struct {
	StaleRead       bool // answer GET from the local FSM on any node
	AckBeforeCommit bool // reply 200 to PUT/CAS before the entry commits
	LeaseRead       bool // leader serves reads locally while its (skewable) lease is valid
}

// Config configures one toy KV node.
type Config struct {
	ID            string
	PeerListen    string
	ClientListen  string
	Peers         map[string]string // every OTHER node: ID -> host:port
	DataDir       string
	ClockOffsetMs int64
	Bugs          Bugs
	LogLevel      string
	LogOutput     io.Writer
}

// ConfigFromEnv reads the node config from the environment (docs/PROTOCOL.md).
func ConfigFromEnv() (Config, error) {
	c := Config{
		ID:           os.Getenv("NODE_ID"),
		PeerListen:   envOr("PEER_LISTEN", ":7000"),
		ClientListen: envOr("CLIENT_LISTEN", ":8080"),
		DataDir:      envOr("DATA_DIR", "/data"),
		LogLevel:     envOr("LOG_LEVEL", "INFO"),
		Bugs: Bugs{
			StaleRead:       envBool("BUG_STALE_READ"),
			AckBeforeCommit: envBool("BUG_ACK_BEFORE_COMMIT"),
			LeaseRead:       envBool("BUG_LEASE_READ"),
		},
	}
	if c.ID == "" {
		return c, errors.New("config: NODE_ID is required")
	}
	peers, err := ParsePeers(os.Getenv("PEERS"))
	if err != nil {
		return c, err
	}
	c.Peers = peers
	if s := os.Getenv("KURU_CLOCK_OFFSET_MS"); s != "" {
		off, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return c, fmt.Errorf("config: KURU_CLOCK_OFFSET_MS: %w", err)
		}
		c.ClockOffsetMs = off
	}
	return c, nil
}

// ParsePeers parses "n2=host:port,n3=host:port".
func ParsePeers(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, addr, ok := strings.Cut(part, "=")
		if !ok || id == "" || addr == "" {
			return nil, fmt.Errorf("config: bad PEERS entry %q (want id=host:port)", part)
		}
		out[id] = addr
	}
	return out, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string) bool {
	v := strings.ToLower(os.Getenv(k))
	return v == "1" || v == "true" || v == "yes"
}

// Clock is the node's skewable logical clock: Now() = time.Now() + offset.
// It only affects toy KV's own time logic (the lease read), not hashicorp/raft's timers.
type Clock struct{ offsetMs atomic.Int64 }

// Now returns the skewed time.
func (c *Clock) Now() time.Time {
	return time.Now().Add(time.Duration(c.offsetMs.Load()) * time.Millisecond)
}

// SetOffset sets the offset in milliseconds.
func (c *Clock) SetOffset(ms int64) { c.offsetMs.Store(ms) }

// Offset returns the offset in milliseconds.
func (c *Clock) Offset() int64 { return c.offsetMs.Load() }

// Node is one running toy KV node.
type Node struct {
	cfg       Config
	raft      *raft.Raft
	fsm       *FSM
	trans     *HTTPTransport
	store     *raftboltdb.BoltStore
	clock     *Clock
	client    *http.Server
	lastAckNs atomic.Int64 // skewed clock time of the last successful AppendEntries ack
	logger    *log.Logger
	wg        sync.WaitGroup
}

// Start listens on the configured addresses and starts the node.
func Start(cfg Config) (*Node, error) {
	peerLn, err := net.Listen("tcp", cfg.PeerListen)
	if err != nil {
		return nil, fmt.Errorf("toykv: listen peer %s: %w", cfg.PeerListen, err)
	}
	clientLn, err := net.Listen("tcp", cfg.ClientListen)
	if err != nil {
		peerLn.Close()
		return nil, fmt.Errorf("toykv: listen client %s: %w", cfg.ClientListen, err)
	}
	return StartWithListeners(cfg, peerLn, clientLn)
}

// StartWithListeners starts the node on already-open listeners (used by tests).
func StartWithListeners(cfg Config, peerLn, clientLn net.Listener) (*Node, error) {
	if cfg.LogOutput == nil {
		cfg.LogOutput = os.Stderr
	}
	n := &Node{cfg: cfg, fsm: NewFSM(), clock: &Clock{}, logger: log.New(cfg.LogOutput, cfg.ID+" ", log.LstdFlags|log.Lmicroseconds)}
	n.clock.SetOffset(cfg.ClockOffsetMs)
	n.trans = NewHTTPTransport(cfg.ID, cfg.Peers)
	n.trans.SetAckHook(func() { n.lastAckNs.Store(n.clock.Now().UnixNano()) })

	n.client = &http.Server{Handler: n.routes(), ReadHeaderTimeout: 5 * time.Second}
	// NewRaft returns without waiting for a leader, so the HTTP servers (and
	// /kuru/health) are up right after it, before any election.
	if err := n.startRaft(); err != nil {
		peerLn.Close()
		clientLn.Close()
		n.Shutdown()
		return nil, err
	}
	n.serve(func() error { return n.trans.Serve(peerLn) })
	n.serve(func() error {
		if err := n.client.Serve(clientLn); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	n.logger.Printf("started peer=%s client=%s peers=%v bugs=%+v", peerLn.Addr(), clientLn.Addr(), cfg.Peers, cfg.Bugs)
	return n, nil
}

func (n *Node) serve(fn func() error) {
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := fn(); err != nil {
			n.logger.Printf("server error: %v", err)
		}
	}()
}

func (n *Node) startRaft() error {
	if err := os.MkdirAll(n.cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("toykv: data dir: %w", err)
	}
	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(n.cfg.ID)
	rc.HeartbeatTimeout = 400 * time.Millisecond
	rc.ElectionTimeout = 400 * time.Millisecond
	rc.LeaderLeaseTimeout = 200 * time.Millisecond
	rc.CommitTimeout = 20 * time.Millisecond
	rc.SnapshotThreshold = 1024
	rc.LogOutput = n.cfg.LogOutput
	rc.LogLevel = n.cfg.LogLevel

	store, err := raftboltdb.NewBoltStore(filepath.Join(n.cfg.DataDir, "raft.db"))
	if err != nil {
		return fmt.Errorf("toykv: bolt store: %w", err)
	}
	n.store = store
	snaps, err := raft.NewFileSnapshotStore(n.cfg.DataDir, 2, n.cfg.LogOutput)
	if err != nil {
		return fmt.Errorf("toykv: snapshot store: %w", err)
	}
	r, err := raft.NewRaft(rc, n.fsm, store, store, snaps, n.trans)
	if err != nil {
		return fmt.Errorf("toykv: new raft: %w", err)
	}
	n.raft = r
	err = r.BootstrapCluster(raft.Configuration{Servers: n.servers()}).Error()
	if err != nil && !errors.Is(err, raft.ErrCantBootstrap) {
		return fmt.Errorf("toykv: bootstrap: %w", err)
	}
	return nil
}

// servers is the static cluster configuration: this node plus every peer, sorted by ID.
func (n *Node) servers() []raft.Server {
	ids := []string{n.cfg.ID}
	for id := range n.cfg.Peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]raft.Server, len(ids))
	for i, id := range ids {
		out[i] = raft.Server{Suffrage: raft.Voter, ID: raft.ServerID(id), Address: raft.ServerAddress(id)}
	}
	return out
}

// Shutdown stops Raft and the HTTP servers and closes the store.
func (n *Node) Shutdown() {
	if n.raft != nil {
		if err := n.raft.Shutdown().Error(); err != nil {
			n.logger.Printf("raft shutdown: %v", err)
		}
	}
	n.trans.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	n.client.Shutdown(ctx)
	n.wg.Wait()
	if n.store != nil {
		n.store.Close()
	}
}

// State returns the Raft state ("leader", "follower", "candidate", "shutdown").
func (n *Node) State() string { return strings.ToLower(n.raft.State().String()) }

// Leader returns the ID of the known leader, or "".
func (n *Node) Leader() string {
	_, id := n.raft.LeaderWithID()
	return string(id)
}

func (n *Node) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /kv/{key}", n.handleGet)
	mux.HandleFunc("PUT /kv/{key}", n.handlePut)
	mux.HandleFunc("POST /kv/{key}/cas", n.handleCAS)
	mux.HandleFunc("GET /kuru/health", n.handleHealth)
	mux.HandleFunc("POST /kuru/clock", n.handleClock)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (n *Node) handleHealth(w http.ResponseWriter, _ *http.Request) {
	term, _ := strconv.ParseUint(n.raft.Stats()["term"], 10, 64)
	writeJSON(w, http.StatusOK, map[string]any{"node": n.cfg.ID, "state": n.State(), "leader": n.Leader(), "term": term})
}

func (n *Node) handleClock(w http.ResponseWriter, r *http.Request) {
	var b struct {
		OffsetMs int64 `json:"offset_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	n.clock.SetOffset(b.OffsetMs)
	n.logger.Printf("clock offset set to %dms", b.OffsetMs)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// leaseValid reports whether the (buggy) lease is still valid on the skewable clock.
func (n *Node) leaseValid() bool {
	last := n.lastAckNs.Load()
	return last != 0 && n.clock.Now().UnixNano() < last+leaseDuration.Nanoseconds()
}

func (n *Node) handleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if n.cfg.Bugs.StaleRead || (n.cfg.Bugs.LeaseRead && n.leaseValid()) {
		v, ok := n.fsm.Get(key)
		writeRead(w, v, ok)
		return
	}
	res, code := n.apply(Command{Op: opGet, Key: key}, true)
	if code != 0 {
		n.writeErr(w, code)
		return
	}
	writeRead(w, res.Value, res.Found)
}

func writeRead(w http.ResponseWriter, v string, found bool) {
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (n *Node) handlePut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	n.write(w, Command{Op: opPut, Key: r.PathValue("key"), Value: b.Value})
}

func (n *Node) handleCAS(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Expect string `json:"expect"`
		Value  string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	n.write(w, Command{Op: opCAS, Key: r.PathValue("key"), Value: b.Value, Expect: b.Expect})
}

func (n *Node) write(w http.ResponseWriter, c Command) {
	res, code := n.apply(c, !n.cfg.Bugs.AckBeforeCommit)
	if code != 0 {
		n.writeErr(w, code)
		return
	}
	if res.Error == "cas_mismatch" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "cas_mismatch"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// apply runs c through the Raft log on the leader. With wait=false it returns as
// soon as the entry is handed to Raft (the BUG_ACK_BEFORE_COMMIT behaviour).
// The returned code is 0 on success, or an HTTP status for writeErr.
func (n *Node) apply(c Command, wait bool) (ApplyResult, int) {
	if n.raft.State() != raft.Leader {
		if n.Leader() == "" {
			return ApplyResult{}, http.StatusServiceUnavailable
		}
		return ApplyResult{}, http.StatusMisdirectedRequest
	}
	data, _ := json.Marshal(c)
	f := n.raft.Apply(data, applyTimeout)
	if !wait {
		return ApplyResult{OK: true}, 0
	}
	done := make(chan error, 1)
	go func() { done <- f.Error() }()
	select {
	case err := <-done:
		switch {
		case err == nil:
		case errors.Is(err, raft.ErrNotLeader):
			// Rejected before it was appended to the log: definitely not applied.
			return ApplyResult{}, http.StatusMisdirectedRequest
		case errors.Is(err, raft.ErrEnqueueTimeout), errors.Is(err, raft.ErrRaftShutdown):
			return ApplyResult{}, http.StatusServiceUnavailable
		default:
			// ErrLeadershipLost etc.: the entry may still commit — outcome unknown.
			n.logger.Printf("apply %s %s: %v", c.Op, c.Key, err)
			return ApplyResult{}, http.StatusInternalServerError
		}
	case <-time.After(applyTimeout):
		// Appended but not known to be committed: outcome unknown.
		return ApplyResult{}, http.StatusGatewayTimeout
	}
	res, ok := f.Response().(ApplyResult)
	if !ok {
		return ApplyResult{}, http.StatusInternalServerError
	}
	return res, 0
}

func (n *Node) writeErr(w http.ResponseWriter, code int) {
	switch code {
	case http.StatusMisdirectedRequest:
		writeJSON(w, code, map[string]string{"error": "not_leader", "leader": n.Leader()})
	case http.StatusServiceUnavailable:
		writeJSON(w, code, map[string]string{"error": "unavailable"})
	default:
		// 500/504: outcome unknown; clients record these as info.
		writeJSON(w, code, map[string]string{"error": "unknown"})
	}
}
