package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/hashicorp/raft"
)

// Command ops.
const (
	opPut = "put"
	opCAS = "cas"
	opGet = "get"
)

// Command is one replicated KV command (JSON in the Raft log).
type Command struct {
	Op     string `json:"op"`
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Expect string `json:"expect,omitempty"`
}

// ApplyResult is what FSM.Apply returns for a command.
type ApplyResult struct {
	OK    bool   `json:"ok"`
	Value string `json:"value,omitempty"`
	Found bool   `json:"found"`
	Error string `json:"error,omitempty"`
}

// FSM is a map[string]string state machine.
type FSM struct {
	mu sync.RWMutex
	kv map[string]string
}

// NewFSM returns an empty FSM.
func NewFSM() *FSM { return &FSM{kv: map[string]string{}} }

// Apply implements raft.FSM.
func (f *FSM) Apply(l *raft.Log) any {
	var c Command
	if err := json.Unmarshal(l.Data, &c); err != nil {
		return ApplyResult{Error: fmt.Sprintf("bad command: %v", err)}
	}
	return f.ApplyCommand(c)
}

// ApplyCommand applies one command directly (used by Apply and tests).
func (f *FSM) ApplyCommand(c Command) ApplyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch c.Op {
	case opPut:
		f.kv[c.Key] = c.Value
		return ApplyResult{OK: true}
	case opCAS:
		if cur := f.kv[c.Key]; cur != c.Expect {
			return ApplyResult{Value: cur, Error: "cas_mismatch"}
		}
		f.kv[c.Key] = c.Value
		return ApplyResult{OK: true}
	case opGet:
		v, ok := f.kv[c.Key]
		return ApplyResult{OK: true, Value: v, Found: ok}
	}
	return ApplyResult{Error: fmt.Sprintf("unknown op %q", c.Op)}
}

// Get reads the local state without going through Raft.
func (f *FSM) Get(key string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	v, ok := f.kv[key]
	return v, ok
}

// Snapshot implements raft.FSM.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	b, err := json.Marshal(f.kv)
	if err != nil {
		return nil, fmt.Errorf("fsm: snapshot: %w", err)
	}
	return fsmSnapshot(b), nil
}

// Restore implements raft.FSM.
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()
	kv := map[string]string{}
	if err := json.NewDecoder(rc).Decode(&kv); err != nil {
		return fmt.Errorf("fsm: restore: %w", err)
	}
	f.mu.Lock()
	f.kv = kv
	f.mu.Unlock()
	return nil
}

type fsmSnapshot []byte

// Persist implements raft.FSMSnapshot.
func (s fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s); err != nil {
		_ = sink.Cancel()
		return fmt.Errorf("fsm: persist: %w", err)
	}
	return sink.Close()
}

// Release implements raft.FSMSnapshot.
func (s fsmSnapshot) Release() {}
