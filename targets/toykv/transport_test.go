package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/raft"
)

// serveFake runs a receiving transport whose consumer answers every RPC with a fixed response.
func serveFake(t *testing.T) (*HTTPTransport, *httptest.Server, *atomic.Int64) {
	t.Helper()
	recv := NewHTTPTransport("n2", nil)
	srv := httptest.NewServer(recv)
	t.Cleanup(func() { srv.Close(); recv.Close() })
	var count atomic.Int64
	go func() {
		for rpc := range recv.Consumer() {
			count.Add(1)
			switch cmd := rpc.Command.(type) {
			case *raft.AppendEntriesRequest:
				rpc.Respond(&raft.AppendEntriesResponse{Term: cmd.Term, LastLog: 7, Success: true}, nil)
			case *raft.RequestVoteRequest:
				rpc.Respond(&raft.RequestVoteResponse{Term: cmd.Term, Granted: true}, nil)
			case *raft.TimeoutNowRequest:
				rpc.Respond(&raft.TimeoutNowResponse{}, nil)
			case *raft.InstallSnapshotRequest:
				b, _ := io.ReadAll(rpc.Reader)
				rpc.Respond(&raft.InstallSnapshotResponse{Term: cmd.Term, Success: string(b) == "SNAP"}, nil)
			}
		}
	}()
	return recv, srv, &count
}

func sender(srv *httptest.Server) *HTTPTransport {
	return NewHTTPTransport("n1", map[string]string{"n2": strings.TrimPrefix(srv.URL, "http://")})
}

func TestTransportRoundTrip(t *testing.T) {
	_, srv, _ := serveFake(t)
	s := sender(srv)
	acks := 0
	s.SetAckHook(func() { acks++ })

	var ae raft.AppendEntriesResponse
	args := &raft.AppendEntriesRequest{Term: 3, PrevLogEntry: 1, Entries: []*raft.Log{{Index: 2, Term: 3, Data: []byte(`{"op":"put"}`)}}}
	if err := s.AppendEntries("n2", "n2", args, &ae); err != nil {
		t.Fatal(err)
	}
	if !ae.Success || ae.Term != 3 || ae.LastLog != 7 || acks != 1 {
		t.Fatalf("append: %+v acks=%d", ae, acks)
	}
	var rv raft.RequestVoteResponse
	if err := s.RequestVote("n2", "n2", &raft.RequestVoteRequest{Term: 4}, &rv); err != nil || !rv.Granted || rv.Term != 4 {
		t.Fatalf("vote: %v %+v", err, rv)
	}
	var tn raft.TimeoutNowResponse
	if err := s.TimeoutNow("n2", "n2", &raft.TimeoutNowRequest{}, &tn); err != nil {
		t.Fatal(err)
	}
	var is raft.InstallSnapshotResponse
	if err := s.InstallSnapshot("n2", "n2", &raft.InstallSnapshotRequest{Term: 5, Size: 4}, &is, bytes.NewReader([]byte("SNAP"))); err != nil || !is.Success || is.Term != 5 {
		t.Fatalf("snapshot: %v %+v", err, is)
	}
	if _, err := s.AppendEntriesPipeline("n2", "n2"); err != raft.ErrPipelineReplicationNotSupported {
		t.Fatal("pipeline must be unsupported")
	}
	if s.LocalAddr() != "n1" || s.DecodePeer(s.EncodePeer("n2", "n2")) != "n2" {
		t.Fatal("addresses must be node IDs")
	}
}

func TestTransportDuplicateRequestHarmless(t *testing.T) {
	_, srv, count := serveFake(t)
	body := `{"Term":3,"Entries":[{"Index":2,"Term":3}]}`
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/peer/append_entries", strings.NewReader(body))
		req.Header.Set("X-Kuru-From", "n1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("attempt %d: status %d", i, resp.StatusCode)
		}
	}
	if count.Load() != 2 {
		t.Fatalf("consumer saw %d", count.Load())
	}
}

func TestTransportHeartbeatFastPath(t *testing.T) {
	recv := NewHTTPTransport("n2", nil)
	srv := httptest.NewServer(recv)
	defer srv.Close()
	hb := 0
	recv.SetHeartbeatHandler(func(rpc raft.RPC) {
		hb++
		rpc.Respond(&raft.AppendEntriesResponse{Success: true}, nil)
	})
	var resp raft.AppendEntriesResponse
	args := &raft.AppendEntriesRequest{RPCHeader: raft.RPCHeader{Addr: []byte("n1")}, Term: 2}
	if err := sender(srv).AppendEntries("n2", "n2", args, &resp); err != nil || hb != 1 {
		t.Fatalf("err=%v hb=%d", err, hb)
	}
}

func TestTransportErrors(t *testing.T) {
	_, srv, _ := serveFake(t)
	s := sender(srv)
	var rv raft.RequestVoteResponse
	if err := s.RequestVote("n9", "n9", &raft.RequestVoteRequest{}, &rv); err == nil {
		t.Fatal("unknown peer must error")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(504) }))
	defer bad.Close()
	if err := sender(bad).RequestVote("n2", "n2", &raft.RequestVoteRequest{}, &rv); err == nil {
		t.Fatal("non-200 must error")
	}
	resp, _ := http.Post(srv.URL+"/peer/nope", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 400 {
		t.Fatalf("unknown rpc: %d", resp.StatusCode)
	}
}

func TestParsePeers(t *testing.T) {
	p, err := ParsePeers("n2=a:1, n3=b:2,")
	if err != nil || p["n2"] != "a:1" || p["n3"] != "b:2" || len(p) != 2 {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := ParsePeers("n2"); err == nil {
		t.Fatal("want error")
	}
}
