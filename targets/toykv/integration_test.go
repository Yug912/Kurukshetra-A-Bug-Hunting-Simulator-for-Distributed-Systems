//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

var composePorts = map[string]string{"n1": "18081", "n2": "18082", "n3": "18083"}

func repoRoot(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func compose(t *testing.T, args ...string) {
	t.Helper()
	file := filepath.Join(repoRoot(t), "targets", "toykv", "docker-compose.yml")
	cmd := exec.Command("docker", append([]string{"compose", "-p", "kuru-toykv-it", "-f", file}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

// RequireDocker skips the test when no Docker daemon is reachable.
func requireDocker(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not available: ", err)
	}
}

func composeLeader(t *testing.T, skip string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for id, port := range composePorts {
			if id == skip {
				continue
			}
			resp, err := http.Get("http://127.0.0.1:" + port + "/kuru/health")
			if err != nil {
				continue
			}
			var h struct{ State string }
			err = json.NewDecoder(resp.Body).Decode(&h)
			_ = resp.Body.Close()
			if err != nil {
				continue
			}
			if h.State == "leader" {
				return id
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no leader within %v", timeout)
	return ""
}

func TestComposeClusterFailover(t *testing.T) {
	requireDocker(t)
	root := repoRoot(t)
	build := exec.Command("docker", "build", "-t", "kuru/toykv", "-f", "targets/toykv/Dockerfile", ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	compose(t, "up", "-d", "--force-recreate", "--renew-anon-volumes")
	t.Cleanup(func() { compose(t, "down", "-v") })

	leader := composeLeader(t, "", 15*time.Second)
	base := "http://127.0.0.1:" + composePorts[leader]
	if code, _ := do(t, "PUT", base+"/kv/k0", map[string]string{"value": "v1"}); code != 200 {
		t.Fatalf("put: %d", code)
	}
	if code, b := do(t, "GET", base+"/kv/k0", nil); code != 200 || b["value"] != "v1" {
		t.Fatalf("get: %d %v", code, b)
	}

	kill := exec.Command("docker", "compose", "-p", "kuru-toykv-it", "-f",
		filepath.Join(root, "targets", "toykv", "docker-compose.yml"), "kill", leader)
	if out, err := kill.CombinedOutput(); err != nil {
		t.Fatalf("kill: %v\n%s", err, out)
	}
	start := time.Now()
	newLeader := composeLeader(t, leader, 5*time.Second)
	t.Logf("new leader %s after %v", newLeader, time.Since(start))
	nb := "http://127.0.0.1:" + composePorts[newLeader]
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, b := do(t, "GET", nb+"/kv/k0", nil)
		if code == 200 && b["value"] == "v1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("read after failover: %d %v", code, b)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
