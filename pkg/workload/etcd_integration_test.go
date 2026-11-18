//go:build integration

package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"kurukshetra/pkg/checker"
	"kurukshetra/pkg/core"
	"kurukshetra/pkg/history"
	"kurukshetra/pkg/rng"
	"kurukshetra/pkg/scenario"
)

const etcdProject = "kuru-etcd-it"

var etcdPorts = map[string]string{"n1": "18291", "n2": "18292", "n3": "18293"}

func etcdCompose(t *testing.T, image string, args ...string) {
	t.Helper()
	file := filepath.Join(repoRoot(), "targets", "etcd", "docker-compose.yml")
	cmd := exec.Command("docker", append([]string{"compose", "-p", etcdProject, "-f", file}, args...)...)
	cmd.Env = append(os.Environ(), "ETCD_IMAGE="+image, "ETCD_PORT_N1=18291", "ETCD_PORT_N2=18292", "ETCD_PORT_N3=18293")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
}

// etcdLeader waits for a member to report itself leader and returns (leader, a follower).
func etcdLeader(t *testing.T) (string, string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		leader, follower := "", ""
		for id, port := range etcdPorts {
			resp, err := http.Get("http://127.0.0.1:" + port + "/kuru/health")
			if err != nil {
				continue
			}
			var h struct{ State string }
			_ = json.NewDecoder(resp.Body).Decode(&h)
			_ = resp.Body.Close()
			switch h.State {
			case "leader":
				leader = id
			case "follower":
				follower = id
			}
		}
		if leader != "" && follower != "" {
			return leader, follower
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("no etcd leader")
	return "", ""
}

// runEtcdPartition drives etcd-kv against a 3-member cluster of image while one
// follower is cut off from its peers (but not from clients) for 6 s, then checks.
func runEtcdPartition(t *testing.T, image string) (core.CheckResult, core.CheckResult, []history.Op) {
	etcdCompose(t, image, "up", "-d", "--force-recreate", "--renew-anon-volumes")
	t.Cleanup(func() { etcdCompose(t, image, "down", "-v") })
	leader, victim := etcdLeader(t)
	t.Logf("%s: leader %s, isolating follower %s", image, leader, victim)

	cl := core.Cluster{}
	for _, id := range []string{"n1", "n2", "n3"} {
		cl.Nodes = append(cl.Nodes, core.Node{Name: id, ClientURL: "http://127.0.0.1:" + etcdPorts[id]})
	}
	w, err := NewEtcdKV(scenario.Workload{Type: "etcd-kv", Clients: 4, Keys: 2, OpsPerSec: 60, TimeoutMs: 1000, ReadFrac: 0.5, WriteFrac: 0.3, CasFrac: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	var rec history.MemRecorder
	clock := &counterClock{}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, cl, rng.New(1).Stream(rng.StreamWorkload), clock, &rec) }()

	container := fmt.Sprintf("%s-%s-1", etcdProject, victim)
	network := etcdProject + "_peers"
	time.Sleep(3 * time.Second)
	docker(t, "network", "disconnect", network, container)
	time.Sleep(6 * time.Second)
	docker(t, "network", "connect", "--alias", "p-"+victim, network, container)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // let the member catch up before final reads
	if err := w.FinalReads(context.Background(), cl, clock, &rec); err != nil {
		t.Fatal(err)
	}
	ops := rec.Ops()
	if _, err := history.Pairs(ops); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	lin := (&checker.Linearizable{Timeout: 30 * time.Second}).Check(ops, dir)
	dur := checker.Durability{}.Check(ops, "")
	t.Logf("%s: %d ops; linearizable-kv %s %q; durability %s %q", image, len(ops), lin.Verdict, lin.Reason, dur.Verdict, dur.Reason)
	return lin, dur, ops
}

func requireEtcdImages(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not available")
	}
	for _, img := range []string{"kuru/etcd:buggy", "kuru/etcd:fixed"} {
		if err := exec.Command("docker", "image", "inspect", img).Run(); err != nil {
			t.Skipf("%s not built (scripts/brain-build-images.sh)", img)
		}
	}
}

func TestEtcdBuggyStaleReadsUnderPartition(t *testing.T) {
	requireEtcdImages(t)
	lin, _, _ := runEtcdPartition(t, "kuru/etcd:buggy")
	if lin.Verdict != core.VerdictFail {
		t.Fatalf("etcd 2.3.8 default reads should be stale under a partition, got %+v", lin)
	}
}

func TestEtcdFixedLinearizableUnderPartition(t *testing.T) {
	requireEtcdImages(t)
	lin, dur, _ := runEtcdPartition(t, "kuru/etcd:fixed")
	if lin.Verdict != core.VerdictPass || dur.Verdict != core.VerdictPass {
		t.Fatalf("etcd 3.5.17 must stay linearizable: %+v %+v", lin, dur)
	}
}
