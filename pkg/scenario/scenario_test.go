package scenario

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	sc, err := Parse([]byte("name: x\nimage: img\n"))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"nodes", sc.Nodes, 3}, {"mode", sc.Mode, "http"}, {"peer_port", sc.PeerPort, 7000},
		{"client_port", sc.ClientPort, 8080}, {"duration", sc.DurationSteps, int64(3000)},
		{"step_ms", sc.StepMs, 2}, {"quiet", sc.QuietSteps, int64(500)},
		{"checkers", sc.Checkers, []string{"linearizable-kv"}},
		{"wtype", sc.Workload.Type, "kv"}, {"clients", sc.Workload.Clients, 5},
		{"keys", sc.Workload.Keys, 3}, {"ops", sc.Workload.OpsPerSec, 50},
		{"timeout", sc.Workload.TimeoutMs, 1000}, {"read", sc.Workload.ReadFrac, 0.4},
		{"write", sc.Workload.WriteFrac, 0.4}, {"cas", sc.Workload.CasFrac, 0.2},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, c.got, c.want)
		}
	}
}

func TestValidationErrors(t *testing.T) {
	base := "image: img\n"
	cases := []struct{ name, yaml, want string }{
		{"no name", base, "name is required"},
		{"bad name", "name: Bad_Name\n" + base, "must match"},
		{"too many nodes", "name: x\nnodes: 10\n" + base, "nodes must be"},
		{"negative nodes", "name: x\nnodes: -1\n" + base, "nodes must be"},
		{"no image", "name: x\n", "image is required"},
		{"bad mode", "name: x\nmode: udp\n" + base, "mode must be"},
		{"drop prob", "name: x\n" + base + "faults:\n  drop: 1.5\n", "faults.drop"},
		{"dup prob", "name: x\n" + base + "faults:\n  dup: -0.1\n", "faults.dup"},
		{"delay prob", "name: x\n" + base + "faults:\n  delay_prob: 2\n", "faults.delay_prob"},
		{"frac range", "name: x\n" + base + "workload:\n  read_frac: 1.5\n  write_frac: -0.5\n", "read_frac"},
		{"frac sum", "name: x\n" + base + "workload:\n  read_frac: 0.5\n  write_frac: 0.4\n", "sum to 1"},
		{"range lo>hi", "name: x\n" + base + "faults:\n  delay_steps: [5, 1]\n", "delay_steps lo > hi"},
		{"every lo 0", "name: x\n" + base + "faults:\n  crash_every: [0, 10]\n", "crash_every lo must be > 0"},
		{"unknown field", "name: x\n" + base + "bogus: 1\n", "bogus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestLoadAndNodeNames(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(p, []byte("name: ok-1\nimage: img\nnodes: 5\nfaults:\n  clock_skew_ms: [-500, 500]\n  clock_skew_every: [10, 20]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(NodeNames(sc.Nodes), []string{"n1", "n2", "n3", "n4", "n5"}) {
		t.Fatal("bad node names")
	}
	if sc.Faults.ClockSkewMs != (Range{-500, 500}) {
		t.Fatal("range not parsed")
	}
}
