// Package scenario loads and validates scenario YAML files.
package scenario

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Range is [lo, hi] inclusive; [0,0] means disabled.
type Range [2]int64

// Enabled reports whether the range is not [0,0].
func (r Range) Enabled() bool { return r != Range{} }

// Scenario describes one cluster under test and how to stress it.
type Scenario struct {
	Name          string            `yaml:"name"`
	Nodes         int               `yaml:"nodes"`          // 1..9, default 3
	Image         string            `yaml:"image"`          // docker image of one node
	Mode          string            `yaml:"mode"`           // "http" (default) or "tcp"
	PeerPort      int               `yaml:"peer_port"`      // container port for node-to-node traffic, default 7000
	ClientPort    int               `yaml:"client_port"`    // container port for client API, default 8080
	Env           map[string]string `yaml:"env"`            // extra env for every node (e.g. BUG_STALE_READ: "1")
	DurationSteps int64             `yaml:"duration_steps"` // default 3000
	StepMs        int               `yaml:"step_ms"`        // real ms per logical step, default 2
	QuietSteps    int64             `yaml:"quiet_steps"`    // after healing, before final reads, default 500
	Workload      Workload          `yaml:"workload"`
	Faults        Faults            `yaml:"faults"`
	Checkers      []string          `yaml:"checkers"` // default ["linearizable-kv"]
}

// Workload configures client traffic.
type Workload struct {
	Type      string  `yaml:"type"`        // default "kv"
	Clients   int     `yaml:"clients"`     // default 5
	Keys      int     `yaml:"keys"`        // default 3; keys are "k0","k1",...
	OpsPerSec int     `yaml:"ops_per_sec"` // total across clients, default 50
	TimeoutMs int     `yaml:"timeout_ms"`  // default 1000
	ReadFrac  float64 `yaml:"read_frac"`   // default 0.4
	WriteFrac float64 `yaml:"write_frac"`  // default 0.4
	CasFrac   float64 `yaml:"cas_frac"`    // default 0.2
}

// Faults configures message faults and the fault schedule.
type Faults struct {
	Drop           float64 `yaml:"drop"` // per-message probability
	Dup            float64 `yaml:"dup"`
	DelayProb      float64 `yaml:"delay_prob"`
	DelaySteps     Range   `yaml:"delay_steps"`
	PartitionEvery Range   `yaml:"partition_every"` // steps between partitions
	PartitionLen   Range   `yaml:"partition_len"`
	CrashEvery     Range   `yaml:"crash_every"`
	CrashLen       Range   `yaml:"crash_len"` // steps a crashed node stays down
	PauseEvery     Range   `yaml:"pause_every"`
	PauseLen       Range   `yaml:"pause_len"`
	ClockSkewEvery Range   `yaml:"clock_skew_every"`
	ClockSkewMs    Range   `yaml:"clock_skew_ms"` // offset range, may be negative
}

// Load reads a YAML file, applies defaults and validates it.
func Load(path string) (Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	sc, err := Parse(b)
	if err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", path, err)
	}
	return sc, nil
}

// Parse decodes YAML, applies defaults and validates. Unknown fields are an error.
func Parse(b []byte) (Scenario, error) {
	var sc Scenario
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&sc); err != nil {
		return Scenario{}, fmt.Errorf("scenario: %w", err)
	}
	sc.ApplyDefaults()
	if err := sc.Validate(); err != nil {
		return Scenario{}, err
	}
	return sc, nil
}

// ApplyDefaults fills zero fields with their defaults.
func (s *Scenario) ApplyDefaults() {
	if s.Nodes == 0 {
		s.Nodes = 3
	}
	if s.Mode == "" {
		s.Mode = "http"
	}
	if s.PeerPort == 0 {
		s.PeerPort = 7000
	}
	if s.ClientPort == 0 {
		s.ClientPort = 8080
	}
	if s.DurationSteps == 0 {
		s.DurationSteps = 3000
	}
	if s.StepMs == 0 {
		s.StepMs = 2
	}
	if s.QuietSteps == 0 {
		s.QuietSteps = 500
	}
	if len(s.Checkers) == 0 {
		s.Checkers = []string{"linearizable-kv"}
	}
	w := &s.Workload
	if w.Type == "" {
		w.Type = "kv"
	}
	if w.Clients == 0 {
		w.Clients = 5
	}
	if w.Keys == 0 {
		w.Keys = 3
	}
	if w.OpsPerSec == 0 {
		w.OpsPerSec = 50
	}
	if w.TimeoutMs == 0 {
		w.TimeoutMs = 1000
	}
	if w.ReadFrac == 0 && w.WriteFrac == 0 && w.CasFrac == 0 {
		w.ReadFrac, w.WriteFrac, w.CasFrac = 0.4, 0.4, 0.2
	}
}

var nameRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// Validate checks the scenario (call after ApplyDefaults).
func (s Scenario) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("scenario: name is required")
	}
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("scenario: name %q must match [a-z0-9-]+", s.Name)
	}
	if s.Nodes < 1 || s.Nodes > 9 {
		return fmt.Errorf("scenario: nodes must be 1..9, got %d", s.Nodes)
	}
	if s.Image == "" {
		return fmt.Errorf("scenario: image is required")
	}
	if s.Mode != "http" && s.Mode != "tcp" {
		return fmt.Errorf("scenario: mode must be http or tcp, got %q", s.Mode)
	}
	f := s.Faults
	for _, p := range []struct {
		n string
		v float64
	}{{"drop", f.Drop}, {"dup", f.Dup}, {"delay_prob", f.DelayProb}} {
		if p.v < 0 || p.v > 1 {
			return fmt.Errorf("scenario: faults.%s must be in [0,1], got %v", p.n, p.v)
		}
	}
	w := s.Workload
	for _, p := range []struct {
		n string
		v float64
	}{{"read_frac", w.ReadFrac}, {"write_frac", w.WriteFrac}, {"cas_frac", w.CasFrac}} {
		if p.v < 0 || p.v > 1 {
			return fmt.Errorf("scenario: workload.%s must be in [0,1], got %v", p.n, p.v)
		}
	}
	if sum := w.ReadFrac + w.WriteFrac + w.CasFrac; math.Abs(sum-1) > 0.001 {
		return fmt.Errorf("scenario: workload fractions must sum to 1, got %v", sum)
	}
	ranges := []struct {
		n     string
		r     Range
		every bool
	}{
		{"delay_steps", f.DelaySteps, false},
		{"partition_every", f.PartitionEvery, true},
		{"partition_len", f.PartitionLen, false},
		{"crash_every", f.CrashEvery, true},
		{"crash_len", f.CrashLen, false},
		{"pause_every", f.PauseEvery, true},
		{"pause_len", f.PauseLen, false},
		{"clock_skew_every", f.ClockSkewEvery, true},
		{"clock_skew_ms", f.ClockSkewMs, false},
	}
	for _, r := range ranges {
		if r.r[0] > r.r[1] {
			return fmt.Errorf("scenario: faults.%s lo > hi (%d > %d)", r.n, r.r[0], r.r[1])
		}
		if r.every && r.r.Enabled() && r.r[0] <= 0 {
			return fmt.Errorf("scenario: faults.%s lo must be > 0 when enabled", r.n)
		}
	}
	return nil
}

// NodeNames returns n1..nN.
func NodeNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("n%d", i+1)
	}
	return out
}
