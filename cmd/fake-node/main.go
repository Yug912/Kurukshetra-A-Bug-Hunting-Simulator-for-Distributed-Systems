// Command fake-node is a stand-in node implementing docs/PROTOCOL.md without
// replication: an in-memory map that always reports state "single".
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type server struct {
	id       string
	mu       sync.Mutex
	kv       map[string]string
	offsetMs int64
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *server) kvHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/kv/")
	if rest == "" {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if key, ok := strings.CutSuffix(rest, "/cas"); ok && r.Method == http.MethodPost {
		var body struct{ Expect, Value string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad_request"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.kv[key] != body.Expect {
			writeJSON(w, 409, map[string]string{"error": "cas_mismatch"})
			return
		}
		s.kv[key] = body.Value
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		v, ok := s.kv[rest]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, 200, map[string]string{"value": v})
	case http.MethodPut:
		var body struct{ Value string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad_request"})
			return
		}
		s.mu.Lock()
		s.kv[rest] = body.Value
		s.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
	}
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"node": s.id, "state": "single", "leader": s.id, "term": 0})
}

func (s *server) clock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OffsetMs int64 `json:"offset_ms"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil {
		writeJSON(w, 400, map[string]string{"error": "bad_request"})
		return
	}
	s.mu.Lock()
	s.offsetMs = body.OffsetMs
	s.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func peerHandler(w http.ResponseWriter, r *http.Request) {
	n, _ := io.Copy(io.Discard, r.Body)
	writeJSON(w, 200, map[string]int64{"echo": n})
}

func pinger(id string, peers []string) {
	c := &http.Client{Timeout: 200 * time.Millisecond}
	for range time.Tick(50 * time.Millisecond) {
		for _, addr := range peers {
			req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/peer/ping", bytes.NewReader([]byte("ping")))
			req.Header.Set("X-Kuru-From", id)
			if resp, err := c.Do(req); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
	}
}

func parsePeers(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if _, addr, ok := strings.Cut(strings.TrimSpace(p), "="); ok && addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

func main() {
	off, _ := strconv.ParseInt(env("KURU_CLOCK_OFFSET_MS", "0"), 10, 64)
	s := &server{id: env("NODE_ID", "n1"), kv: map[string]string{}, offsetMs: off}

	client := http.NewServeMux()
	client.HandleFunc("/kv/", s.kvHandler)
	client.HandleFunc("/kuru/health", s.health)
	client.HandleFunc("/kuru/clock", s.clock)

	peer := http.NewServeMux()
	peer.HandleFunc("/peer/", peerHandler)

	go pinger(s.id, parsePeers(os.Getenv("PEERS")))
	go func() { log.Fatal(http.ListenAndServe(env("PEER_LISTEN", ":7000"), peer)) }()
	log.Printf("fake-node %s listening", s.id)
	log.Fatal(http.ListenAndServe(env("CLIENT_LISTEN", ":8080"), client))
}
