// Command kuru-etcd-shim runs one etcd member under Kurukshetra's node protocol.
//
// etcd derives member IDs from the peer URLs in --initial-cluster, so every
// member must use the same URL for each peer. Kurukshetra, however, hands each
// node its own address for every peer (the engine's proxy). The shim bridges
// the two: every member advertises http://<id>:<7100+i>, all IDs resolve to
// 127.0.0.1 inside the container (/etc/hosts), and the shim forwards
// 127.0.0.1:<7100+i> to PEERS[<id>] (or to this node's own peer port). Each
// forwarded connection is a plain TCP stream, so scenarios use mode: tcp.
//
// On CLIENT_LISTEN the shim serves GET /kuru/health, answers POST /kuru/clock
// with 404 (etcd has no logical clock to skew) and reverse-proxies everything
// else to etcd's client port.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	advertiseBase = 7100
	etcdClient    = "127.0.0.1:2379"
)

// Config is the node configuration from the environment (docs/PROTOCOL.md).
type Config struct {
	ID           string
	Peers        map[string]string
	PeerListen   string
	ClientListen string
	DataDir      string
	EtcdBin      string
	HostsFile    string
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// parsePeers parses "n2=host:port,n3=host:port".
func parsePeers(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, addr, ok := strings.Cut(part, "=")
		if !ok || id == "" || addr == "" {
			return nil, fmt.Errorf("bad PEERS entry %q", part)
		}
		out[id] = addr
	}
	return out, nil
}

// IDs returns every member ID (this node and its peers), sorted.
func (c Config) IDs() []string {
	ids := []string{c.ID}
	for id := range c.Peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// advertisePort is the stable per-member port every node uses in --initial-cluster.
func (c Config) advertisePort(id string) int {
	for i, x := range c.IDs() {
		if x == id {
			return advertiseBase + i
		}
	}
	return -1
}

// peerPort is the port of PEER_LISTEN (etcd listens there for proxied peer traffic).
func (c Config) peerPort() string {
	_, port, err := net.SplitHostPort(c.PeerListen)
	if err != nil || port == "" {
		return "7000"
	}
	return port
}

// EtcdArgs returns the etcd command line for this member.
func (c Config) EtcdArgs() []string {
	var cluster []string
	for _, id := range c.IDs() {
		cluster = append(cluster, fmt.Sprintf("%s=http://%s:%d", id, id, c.advertisePort(id)))
	}
	return []string{
		"--name", c.ID,
		"--data-dir", filepath.Join(c.DataDir, "etcd"),
		"--listen-peer-urls", "http://0.0.0.0:" + c.peerPort(),
		"--initial-advertise-peer-urls", fmt.Sprintf("http://%s:%d", c.ID, c.advertisePort(c.ID)),
		"--listen-client-urls", "http://" + etcdClient,
		"--advertise-client-urls", "http://" + etcdClient,
		"--initial-cluster", strings.Join(cluster, ","),
		"--initial-cluster-state", "new",
		"--initial-cluster-token", "kurukshetra",
	}
}

// Forwards maps each local listen address to its target: peers go to PEERS, self to the own peer port.
func (c Config) Forwards() map[string]string {
	out := map[string]string{}
	for _, id := range c.IDs() {
		local := fmt.Sprintf("127.0.0.1:%d", c.advertisePort(id))
		if id == c.ID {
			out[local] = "127.0.0.1:" + c.peerPort()
		} else {
			out[local] = c.Peers[id]
		}
	}
	return out
}

// addHosts makes every member ID resolve to 127.0.0.1.
func addHosts(path string, ids []string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	line := "127.0.0.1 " + strings.Join(ids, " ")
	if strings.Contains(string(b), line) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "\n%s\n", line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// forward accepts on ln and pipes each connection to target until ctx ends.
func forward(ctx context.Context, ln net.Listener, target string) {
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go pipe(conn, target)
	}
}

func pipe(in net.Conn, target string) {
	defer func() { _ = in.Close() }()
	out, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = out.Close() }()
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}
	go cp(out, in)
	go cp(in, out)
	wg.Wait()
}

// handler serves /kuru/* and proxies the rest to etcd.
func handler(c Config, etcd string) http.Handler {
	target, _ := url.Parse("http://" + etcd)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, `{"error":%q}`, "etcd unavailable: "+err.Error())
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /kuru/health", func(w http.ResponseWriter, r *http.Request) {
		h := health(r.Context(), c, etcd)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"node":%q,"state":%q,"leader":%q,"term":%d}`, c.ID, h.state, h.leader, h.term)
	})
	mux.HandleFunc("/kuru/clock", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"clock skew not supported by etcd"}`))
	})
	mux.Handle("/", proxy)
	return mux
}

type healthInfo struct {
	state, leader string
	term          uint64
}

func main() {
	log.SetPrefix("kuru-etcd-shim ")
	peers, err := parsePeers(os.Getenv("PEERS"))
	if err != nil {
		log.Fatal(err)
	}
	c := Config{
		ID:           os.Getenv("NODE_ID"),
		Peers:        peers,
		PeerListen:   envOr("PEER_LISTEN", ":7000"),
		ClientListen: envOr("CLIENT_LISTEN", ":8080"),
		DataDir:      envOr("DATA_DIR", "/data"),
		EtcdBin:      envOr("ETCD_BIN", "/usr/local/bin/etcd"),
		HostsFile:    envOr("HOSTS_FILE", "/etc/hosts"),
	}
	if c.ID == "" {
		log.Fatal("NODE_ID is required")
	}
	if err := run(c); err != nil {
		log.Fatal(err)
	}
}

func run(c Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := addHosts(c.HostsFile, c.IDs()); err != nil {
		return fmt.Errorf("hosts: %w", err)
	}
	for local, target := range c.Forwards() {
		ln, err := net.Listen("tcp", local)
		if err != nil {
			return fmt.Errorf("forward %s: %w", local, err)
		}
		go forward(ctx, ln, target)
	}
	srv := &http.Server{Addr: c.ClientListen, Handler: handler(c, etcdClient), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("client server: %v", err)
		}
	}()
	cmd := exec.Command(c.EtcdBin, c.EtcdArgs()...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	log.Printf("starting %s %s", c.EtcdBin, strings.Join(c.EtcdArgs(), " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start etcd: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return fmt.Errorf("etcd exited: %v", err)
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		if err := <-done; err != nil {
			log.Printf("etcd stopped: %v", err)
		}
		return nil
	}
}
