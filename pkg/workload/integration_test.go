//go:build integration

package workload

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
)

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func composeCmd(t *testing.T, args ...string) {
	t.Helper()
	file := filepath.Join(repoRoot(), "targets", "toykv", "docker-compose.yml")
	cmd := exec.Command("docker", append([]string{"compose", "-p", "kuru-workload-it", "-f", file}, args...)...)
	cmd.Env = append(os.Environ(), "TOYKV_PORT_N1=18091", "TOYKV_PORT_N2=18092", "TOYKV_PORT_N3=18093")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

func TestWorkloadAgainstCompose(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not available")
	}
	build := exec.Command("docker", "build", "-t", "kuru/toykv", "-f", "targets/toykv/Dockerfile", ".")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	composeCmd(t, "up", "-d", "--force-recreate", "--renew-anon-volumes")
	t.Cleanup(func() { composeCmd(t, "down", "-v") })

	cl := core.Cluster{Nodes: []core.Node{
		{Name: "n1", ClientURL: "http://127.0.0.1:18091"},
		{Name: "n2", ClientURL: "http://127.0.0.1:18092"},
		{Name: "n3", ClientURL: "http://127.0.0.1:18093"},
	}}
	// Wait until some node is leader.
	deadline := time.Now().Add(20 * time.Second)
	for {
		ok := false
		for _, n := range cl.Nodes {
			if resp, err := http.Get(n.ClientURL + "/kv/k0"); err == nil {
				_ = resp.Body.Close()
				ok = ok || resp.StatusCode == 404 || resp.StatusCode == 200
			}
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cluster not ready")
		}
		time.Sleep(200 * time.Millisecond)
	}

	w, err := NewKV(scenario.Workload{Type: "kv", Clients: 4, Keys: 3, OpsPerSec: 100, TimeoutMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var rec history.MemRecorder
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clock := &counterClock{}
	if err := w.Run(ctx, cl, rng.New(1).Stream(rng.StreamWorkload), clock, &rec); err != nil {
		t.Fatal(err)
	}
	if err := w.FinalReads(context.Background(), cl, clock, &rec); err != nil {
		t.Fatal(err)
	}
	ops := rec.Ops()
	pairs, err := history.Pairs(ops)
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{"": true}
	for _, p := range pairs {
		if p.Invoke.F != history.Read {
			written[p.Invoke.Key+"="+p.Invoke.Value] = true
		}
	}
	reads, finalOK := 0, 0
	for _, p := range pairs {
		if p.Invoke.F != history.Read || p.Complete == nil || p.Complete.Type != history.OK {
			continue
		}
		reads++
		if p.Invoke.Final {
			finalOK++
		}
		if v := p.Complete.Value; v != "" && !written[p.Invoke.Key+"="+v] {
			t.Errorf("read %s=%s was never written", p.Invoke.Key, v)
		}
	}
	t.Logf("%d pairs, %d ok reads, %d ok final reads", len(pairs), reads, finalOK)
	if reads < 50 || finalOK < 3 {
		t.Fatalf("too few successful reads: %d (final %d)", reads, finalOK)
	}
}
