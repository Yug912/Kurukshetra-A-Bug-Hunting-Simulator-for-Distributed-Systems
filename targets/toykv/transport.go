package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

// RPC paths under /peer/. Each Raft RPC is exactly one HTTP request so the
// Kurukshetra proxy can observe, delay, drop or duplicate every message.
const (
	pathAppendEntries   = "append_entries"
	pathRequestVote     = "request_vote"
	pathInstallSnapshot = "install_snapshot"
	pathTimeoutNow      = "timeout_now"
)

const (
	rpcTimeout      = 500 * time.Millisecond
	snapshotTimeout = 5 * time.Second
	consumerTimeout = 2 * time.Second
)

// snapshotBody carries an InstallSnapshot request together with the snapshot bytes.
type snapshotBody struct {
	Args *raft.InstallSnapshotRequest `json:"args"`
	Data []byte                       `json:"data"`
}

// HTTPTransport implements raft.Transport over plain HTTP, one request per RPC.
// Server addresses are node IDs ("n2"); the transport maps them to host:port via PEERS.
type HTTPTransport struct {
	id       string
	peers    map[string]string // node ID -> host:port
	consumer chan raft.RPC
	fast     *http.Client
	slow     *http.Client
	done     chan struct{}
	once     sync.Once

	mu     sync.RWMutex
	hb     func(raft.RPC)
	onAck  func() // called after a successful AppendEntries response (lease tracking)
	server *http.Server
}

// NewHTTPTransport returns a transport for node id that reaches peers at the given addresses.
func NewHTTPTransport(id string, peers map[string]string) *HTTPTransport {
	return &HTTPTransport{
		id:       id,
		peers:    peers,
		consumer: make(chan raft.RPC, 16),
		fast:     &http.Client{Timeout: rpcTimeout},
		slow:     &http.Client{Timeout: snapshotTimeout},
		done:     make(chan struct{}),
	}
}

// Serve accepts peer RPCs on ln until Close is called.
func (t *HTTPTransport) Serve(ln net.Listener) error {
	srv := &http.Server{Handler: t, ReadHeaderTimeout: 5 * time.Second}
	t.mu.Lock()
	t.server = srv
	t.mu.Unlock()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// SetAckHook registers fn to be called after every successful AppendEntries response.
func (t *HTTPTransport) SetAckHook(fn func()) {
	t.mu.Lock()
	t.onAck = fn
	t.mu.Unlock()
}

// Consumer implements raft.Transport.
func (t *HTTPTransport) Consumer() <-chan raft.RPC { return t.consumer }

// LocalAddr implements raft.Transport; addresses are node IDs.
func (t *HTTPTransport) LocalAddr() raft.ServerAddress { return raft.ServerAddress(t.id) }

// AppendEntriesPipeline implements raft.Transport; pipelining is not supported.
func (t *HTTPTransport) AppendEntriesPipeline(raft.ServerID, raft.ServerAddress) (raft.AppendPipeline, error) {
	return nil, raft.ErrPipelineReplicationNotSupported
}

// AppendEntries implements raft.Transport.
func (t *HTTPTransport) AppendEntries(_ raft.ServerID, target raft.ServerAddress, args *raft.AppendEntriesRequest, resp *raft.AppendEntriesResponse) error {
	if err := t.call(t.fast, target, pathAppendEntries, args, resp); err != nil {
		return err
	}
	if resp.Success {
		t.mu.RLock()
		fn := t.onAck
		t.mu.RUnlock()
		if fn != nil {
			fn()
		}
	}
	return nil
}

// RequestVote implements raft.Transport.
func (t *HTTPTransport) RequestVote(_ raft.ServerID, target raft.ServerAddress, args *raft.RequestVoteRequest, resp *raft.RequestVoteResponse) error {
	return t.call(t.fast, target, pathRequestVote, args, resp)
}

// TimeoutNow implements raft.Transport.
func (t *HTTPTransport) TimeoutNow(_ raft.ServerID, target raft.ServerAddress, args *raft.TimeoutNowRequest, resp *raft.TimeoutNowResponse) error {
	return t.call(t.fast, target, pathTimeoutNow, args, resp)
}

// InstallSnapshot implements raft.Transport. Snapshots are tiny, so the whole
// snapshot is read into the request body.
func (t *HTTPTransport) InstallSnapshot(_ raft.ServerID, target raft.ServerAddress, args *raft.InstallSnapshotRequest, resp *raft.InstallSnapshotResponse, data io.Reader) error {
	b, err := io.ReadAll(data)
	if err != nil {
		return fmt.Errorf("transport: read snapshot: %w", err)
	}
	return t.call(t.slow, target, pathInstallSnapshot, snapshotBody{Args: args, Data: b}, resp)
}

// EncodePeer implements raft.Transport (identity).
func (t *HTTPTransport) EncodePeer(_ raft.ServerID, addr raft.ServerAddress) []byte {
	return []byte(addr)
}

// DecodePeer implements raft.Transport (identity).
func (t *HTTPTransport) DecodePeer(b []byte) raft.ServerAddress { return raft.ServerAddress(b) }

// SetHeartbeatHandler implements raft.Transport.
func (t *HTTPTransport) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
	t.mu.Lock()
	t.hb = cb
	t.mu.Unlock()
}

// Close implements raft.WithClose. It stops the peer HTTP server.
func (t *HTTPTransport) Close() error {
	var err error
	t.once.Do(func() {
		close(t.done)
		t.mu.RLock()
		srv := t.server
		t.mu.RUnlock()
		if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = srv.Shutdown(ctx)
		}
	})
	return err
}

func (t *HTTPTransport) call(c *http.Client, target raft.ServerAddress, path string, args, resp any) error {
	addr, ok := t.peers[string(target)]
	if !ok {
		return fmt.Errorf("transport: unknown peer %q", target)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("transport: encode %s: %w", path, err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/peer/"+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("transport: %s to %s: %w", path, target, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kuru-From", t.id)
	r, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("transport: %s to %s: %w", path, target, err)
	}
	defer func() { _ = r.Body.Close() }()
	rb, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("transport: %s to %s: read: %w", path, target, err)
	}
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("transport: %s to %s: http %d: %s", path, target, r.StatusCode, strings.TrimSpace(string(rb)))
	}
	if err := json.Unmarshal(rb, resp); err != nil {
		return fmt.Errorf("transport: %s to %s: decode: %w", path, target, err)
	}
	return nil
}

// ServeHTTP handles incoming peer RPCs: POST /peer/<rpc>.
func (t *HTTPTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/peer/")
	if !ok || r.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rpc, heartbeat, err := decodeRPC(name, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respCh := make(chan raft.RPCResponse, 1)
	rpc.RespChan = respCh
	if !t.dispatch(rpc, heartbeat) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			http.Error(w, resp.Error.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp.Response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case <-time.After(consumerTimeout):
		http.Error(w, "timeout", http.StatusServiceUnavailable)
	case <-t.done:
		http.Error(w, "shutdown", http.StatusServiceUnavailable)
	}
}

// dispatch hands rpc to the heartbeat handler or the consumer channel; false if it could not.
func (t *HTTPTransport) dispatch(rpc raft.RPC, heartbeat bool) bool {
	if heartbeat {
		t.mu.RLock()
		fn := t.hb
		t.mu.RUnlock()
		if fn != nil {
			fn(rpc)
			return true
		}
	}
	select {
	case t.consumer <- rpc:
		return true
	case <-time.After(consumerTimeout):
		return false
	case <-t.done:
		return false
	}
}

// decodeRPC decodes the body of a peer request into a raft.RPC.
func decodeRPC(name string, body io.Reader) (raft.RPC, bool, error) {
	dec := json.NewDecoder(body)
	var rpc raft.RPC
	switch name {
	case pathAppendEntries:
		var a raft.AppendEntriesRequest
		if err := dec.Decode(&a); err != nil {
			return rpc, false, fmt.Errorf("decode %s: %w", name, err)
		}
		rpc.Command = &a
		hb := a.Term != 0 && a.Addr != nil && a.PrevLogEntry == 0 && a.PrevLogTerm == 0 &&
			len(a.Entries) == 0 && a.LeaderCommitIndex == 0
		return rpc, hb, nil
	case pathRequestVote:
		var a raft.RequestVoteRequest
		if err := dec.Decode(&a); err != nil {
			return rpc, false, fmt.Errorf("decode %s: %w", name, err)
		}
		rpc.Command = &a
	case pathTimeoutNow:
		var a raft.TimeoutNowRequest
		if err := dec.Decode(&a); err != nil {
			return rpc, false, fmt.Errorf("decode %s: %w", name, err)
		}
		rpc.Command = &a
	case pathInstallSnapshot:
		var s snapshotBody
		if err := dec.Decode(&s); err != nil || s.Args == nil {
			return rpc, false, fmt.Errorf("decode %s: %v", name, err)
		}
		rpc.Command = s.Args
		rpc.Reader = bytes.NewReader(s.Data)
	default:
		return rpc, false, fmt.Errorf("unknown rpc %q", name)
	}
	return rpc, false, nil
}
