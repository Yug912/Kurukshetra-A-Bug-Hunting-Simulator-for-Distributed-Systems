// Package trace defines the event log written by every run (trace.jsonl).
//
// Meta key conventions:
//   - path:        HTTP path of a message (e.g. /peer/append_entries)
//   - steps:       delay length in steps (msg_delay)
//   - reason:      why something happened (e.g. "partition" on a msg_drop)
//   - groups:      partition groups, e.g. "n1|n2,n3"
//   - offset_ms:   clock skew offset in milliseconds
//   - mode:        "http" or "tcp"
//   - unsupported: "true" if a node did not support a fault
package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Kind is the type of a trace event.
type Kind string

// Event kinds.
const (
	RunStart   Kind = "run_start"
	RunEnd     Kind = "run_end"
	MsgSend    Kind = "msg_send"
	MsgDeliver Kind = "msg_deliver"
	MsgDrop    Kind = "msg_drop"
	MsgDelay   Kind = "msg_delay"
	MsgDup     Kind = "msg_dup"
	Partition  Kind = "partition"
	Heal       Kind = "heal"
	Crash      Kind = "crash"
	Restart    Kind = "restart"
	Pause      Kind = "pause"
	Resume     Kind = "resume"
	ClockSkew  Kind = "clock_skew"
	Note       Kind = "note"
)

// Event is one line of trace.jsonl.
type Event struct {
	Seq    int64             `json:"seq"`     // set by Writer: 0,1,2,...
	Step   int64             `json:"step"`    // logical clock
	WallNs int64             `json:"wall_ns"` // unix nanos; display only, never used for decisions
	Kind   Kind              `json:"kind"`
	Node   string            `json:"node,omitempty"`
	From   string            `json:"from,omitempty"`
	To     string            `json:"to,omitempty"`
	MsgID  string            `json:"msg_id,omitempty"`
	Size   int               `json:"size,omitempty"`
	Meta   map[string]string `json:"meta,omitempty"`
}

// IsDecision reports whether k is a decision the engine made (message fate or fault),
// i.e. something a replay must reproduce.
func IsDecision(k Kind) bool {
	switch k {
	case MsgDeliver, MsgDrop, MsgDelay, MsgDup,
		Partition, Heal, Crash, Restart, Pause, Resume, ClockSkew:
		return true
	}
	return false
}

const flushEvery = 100

// Writer appends events to a JSONL file. It is safe for concurrent use.
type Writer struct {
	mu      sync.Mutex
	f       *os.File
	bw      *bufio.Writer
	next    int64
	pending int
	err     error
}

// NewWriter creates (truncating) the trace file at path.
func NewWriter(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, bw: bufio.NewWriter(f)}, nil
}

// Append assigns the next Seq (and WallNs if zero), writes the event and returns it.
// Write errors are kept and returned by Flush/Close.
func (w *Writer) Append(e Event) Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	e.Seq = w.next
	w.next++
	if e.WallNs == 0 {
		e.WallNs = time.Now().UnixNano()
	}
	b, err := json.Marshal(e)
	if err == nil {
		b = append(b, '\n')
		_, err = w.bw.Write(b)
	}
	if err != nil && w.err == nil {
		w.err = err
	}
	w.pending++
	if w.pending >= flushEvery {
		w.pending = 0
		if err := w.bw.Flush(); err != nil && w.err == nil {
			w.err = err
		}
	}
	return e
}

// Flush writes buffered events to disk.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = 0
	if err := w.bw.Flush(); err != nil && w.err == nil {
		w.err = err
	}
	return w.err
}

// Close flushes and closes the file.
func (w *Writer) Close() error {
	ferr := w.Flush()
	cerr := w.f.Close()
	if ferr != nil {
		return ferr
	}
	return cerr
}

// ReadFile reads a trace.jsonl file, skipping blank lines.
func ReadFile(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s:%d: %w", path, line+1, err)
	}
	return out, nil
}
