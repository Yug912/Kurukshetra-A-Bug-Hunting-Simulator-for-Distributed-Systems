// Package history defines the client operation log (history.jsonl) that checkers judge.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Type is the kind of a history entry.
type Type string

// Op types.
const (
	Invoke Type = "invoke"
	OK     Type = "ok"
	Fail   Type = "fail" // definitely did not happen
	Info   Type = "info" // unknown (timeout / connection error) — may or may not have happened
)

// F is the client function of an op.
type F string

// Client functions.
const (
	Read  F = "read"
	Write F = "write"
	CAS   F = "cas"
)

// Missing is the Value of a read that found no key. Writes never write "".
const Missing = ""

// Op is one line of history.jsonl.
type Op struct {
	Index  int    `json:"index"` // set by Recorder: 0,1,2,...
	Client int    `json:"client"`
	Type   Type   `json:"type"`
	F      F      `json:"f"`
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`  // write/cas: new value; read ok: value read
	Expect string `json:"expect,omitempty"` // cas only
	Node   string `json:"node"`
	Step   int64  `json:"step"`
	WallNs int64  `json:"wall_ns"`         // used for real-time order in linearizability
	Final  bool   `json:"final,omitempty"` // true for final reads after healing
	Error  string `json:"error,omitempty"`
}

// Recorder records ops. Record assigns Index (and WallNs if 0) and is goroutine-safe.
type Recorder interface{ Record(op Op) Op }

// MemRecorder keeps ops in memory.
type MemRecorder struct {
	mu  sync.Mutex
	ops []Op
}

// Record implements Recorder.
func (m *MemRecorder) Record(op Op) Op {
	m.mu.Lock()
	defer m.mu.Unlock()
	op.Index = len(m.ops)
	if op.WallNs == 0 {
		op.WallNs = time.Now().UnixNano()
	}
	m.ops = append(m.ops, op)
	return op
}

// Ops returns a copy of the recorded ops.
func (m *MemRecorder) Ops() []Op {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Op(nil), m.ops...)
}

// FileRecorder appends ops to a JSONL file, one write per op.
type FileRecorder struct {
	mu   sync.Mutex
	f    *os.File
	next int
	err  error
}

// NewFileRecorder creates (truncating) the history file at path.
func NewFileRecorder(path string) (*FileRecorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &FileRecorder{f: f}, nil
}

// Record implements Recorder. Write errors are returned by Close.
func (r *FileRecorder) Record(op Op) Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	op.Index = r.next
	r.next++
	if op.WallNs == 0 {
		op.WallNs = time.Now().UnixNano()
	}
	b, err := json.Marshal(op)
	if err == nil {
		_, err = r.f.Write(append(b, '\n'))
	}
	if err != nil && r.err == nil {
		r.err = err
	}
	return op
}

// Close closes the file and returns the first write error, if any.
func (r *FileRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cerr := r.f.Close()
	if r.err != nil {
		return r.err
	}
	return cerr
}

// ReadFile reads a history.jsonl file, skipping blank lines.
func ReadFile(path string) ([]Op, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Op
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var op Op
		if err := json.Unmarshal(b, &op); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, op)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s:%d: %w", path, line+1, err)
	}
	return out, nil
}

// Pair is an invocation and its completion.
type Pair struct {
	Invoke   Op
	Complete *Op // Complete nil = never completed (treat as info)
}

// Pairs matches each invoke with the next completion of the same client, in history order.
// Each client has at most one outstanding op; an error is returned if that is violated
// or a completion has no invoke.
func Pairs(ops []Op) ([]Pair, error) {
	var pairs []Pair
	open := map[int]int{} // client -> index into pairs
	for _, op := range ops {
		switch op.Type {
		case Invoke:
			if _, ok := open[op.Client]; ok {
				return nil, fmt.Errorf("history: client %d invoked op %d with another op outstanding", op.Client, op.Index)
			}
			open[op.Client] = len(pairs)
			pairs = append(pairs, Pair{Invoke: op})
		case OK, Fail, Info:
			i, ok := open[op.Client]
			if !ok {
				return nil, fmt.Errorf("history: op %d completes client %d with no outstanding invoke", op.Index, op.Client)
			}
			c := op
			pairs[i].Complete = &c
			delete(open, op.Client)
		default:
			return nil, fmt.Errorf("history: op %d has unknown type %q", op.Index, op.Type)
		}
	}
	return pairs, nil
}
