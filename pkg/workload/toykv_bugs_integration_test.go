//go:build integration

package workload

import (
	"bytes"
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

// These tests stand in for Person A's engine until it is on main: a 3-node toy KV
// cluster from targets/toykv/docker-compose.yml, the kv workload, and one fault
// injected by hand (the leader is cut off from its peers for 6 s while clients
// can still reach it). They show that the correct build passes and that each
// bug flag produces a real checker FAIL.

const bugsProject = "kuru-toykv-bugs"

var bugsPorts = map[string]string{"n1": "18191", "n2": "18192", "n3": "18193"}

func bugsCompose(t *testing.T, env []string, args ...string) {
	t.Helper()
	file := filepath.Join(repoRoot(), "targets", "toykv", "docker-compose.yml")
	cmd := exec.Command("docker", append([]string{"compose", "-p", bugsProject, "-f", file}, args...)...)
	cmd.Env = append(append(os.Environ(), "TOYKV_PORT_N1=18191", "TOYKV_PORT_N2=18192", "TOYKV_PORT_N3=18193"), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

func toykvLeader(t *testing.T) string {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for id, port := range bugsPorts {
			resp, err := http.Get("http://127.0.0.1:" + port + "/kuru/health")
			if err != nil {
				continue
			}
			var h struct{ State string }
			_ = json.NewDecoder(resp.Body).Decode(&h)
			_ = resp.Body.Close()
			if h.State == "leader" {
				return id
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no toy KV leader")
	return ""
}

type bugCase struct {
	name       string
	env        []string
	keys       int
	skewMs     int64 // clock offset applied to the isolated leader (0 = none)
	stopAtHeal bool  // stop traffic before healing so lost writes are not overwritten
	wantFail   bool
}

func runToyKVCase(t *testing.T, bc bugCase) (core.CheckResult, core.CheckResult) {
	bugsCompose(t, bc.env, "up", "-d", "--force-recreate", "--renew-anon-volumes")
	t.Cleanup(func() { bugsCompose(t, bc.env, "down", "-v") })
	leader := toykvLeader(t)

	cl := core.Cluster{}
	for _, id := range []string{"n1", "n2", "n3"} {
		cl.Nodes = append(cl.Nodes, core.Node{Name: id, ClientURL: "http://127.0.0.1:" + bugsPorts[id]})
	}
	w, err := NewKV(scenario.Workload{Type: "kv", Clients: 6, Keys: bc.keys, OpsPerSec: 80, TimeoutMs: 1000, ReadFrac: 0.5, WriteFrac: 0.3, CasFrac: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	var rec history.MemRecorder
	clock := &counterClock{}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, cl, rng.New(1).Stream(rng.StreamWorkload), clock, &rec) }()

	container := fmt.Sprintf("%s-%s-1", bugsProject, leader)
	network := bugsProject + "_peers"
	time.Sleep(3 * time.Second)
	docker(t, "network", "disconnect", network, container)
	if bc.skewMs != 0 {
		body, _ := json.Marshal(map[string]int64{"offset_ms": bc.skewMs})
		resp, err := http.Post("http://127.0.0.1:"+bugsPorts[leader]+"/kuru/clock", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	time.Sleep(6 * time.Second)
	if bc.stopAtHeal {
		cancel()
		if err := <-done; err != nil {
			t.Logf("workload stopped by cancel: %v", err)
		}
	}
	docker(t, "network", "connect", "--alias", "p-"+leader, network, container)
	if !bc.stopAtHeal {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * time.Second)
	if err := w.FinalReads(context.Background(), cl, clock, &rec); err != nil {
		t.Fatal(err)
	}
	ops := rec.Ops()
	lin := (&checker.Linearizable{Timeout: 30 * time.Second}).Check(ops, "")
	dur := checker.Durability{}.Check(ops, "")
	t.Logf("%s (isolated leader %s): %d ops; linearizable-kv %s %q; durability %s %q", bc.name, leader, len(ops), lin.Verdict, lin.Reason, dur.Verdict, dur.Reason)
	return lin, dur
}

func TestToyKVBugFlagsUnderPartition(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", "kuru/toykv").Run(); err != nil {
		t.Skip("kuru/toykv not built")
	}
	cases := []bugCase{
		{name: "correct", keys: 2},
		{name: "stale-read", env: []string{"BUG_STALE_READ=1"}, keys: 2, wantFail: true},
		{name: "ack-before-commit", env: []string{"BUG_ACK_BEFORE_COMMIT=1"}, keys: 30, stopAtHeal: true, wantFail: true},
		{name: "lease-read", env: []string{"BUG_LEASE_READ=1"}, keys: 2, skewMs: -1500, wantFail: true},
	}
	for _, bc := range cases {
		t.Run(bc.name, func(t *testing.T) {
			lin, dur := runToyKVCase(t, bc)
			failed := lin.Verdict == core.VerdictFail || dur.Verdict == core.VerdictFail
			if bc.wantFail && !failed {
				t.Fatalf("bug %s produced no FAIL", bc.name)
			}
			if !bc.wantFail && (lin.Verdict != core.VerdictPass || dur.Verdict != core.VerdictPass) {
				t.Fatalf("correct build must PASS: %+v %+v", lin, dur)
			}
		})
	}
}
