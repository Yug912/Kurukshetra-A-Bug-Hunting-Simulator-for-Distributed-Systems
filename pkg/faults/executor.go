package faults

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"kurukshetra/pkg/core"
	"kurukshetra/pkg/trace"
)

// Links is the part of the proxy the executor uses.
type Links interface {
	SetBlocked(map[[2]string]bool)
}

// Containers is the part of the Docker controller the executor uses.
type Containers interface {
	Kill(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error
	Pause(ctx context.Context, id string) error
	Unpause(ctx context.Context, id string) error
}

// NodeRef identifies one node's container and client API.
type NodeRef struct {
	ContainerID string
	ClientURL   string
}

// Executor applies faults. It implements scheduler.FaultExecutor.
type Executor struct {
	links Links
	ctr   Containers
	nodes map[string]NodeRef
	emit  func(trace.Event)
	http  *http.Client

	mu        sync.Mutex
	parts     map[string][][]string // active partitions by key
	crashed   map[string]bool
	paused    map[string]bool
	skewed    map[string]int64
	onRestart func(node string)
}

// NewExecutor builds an executor. emit writes a trace event stamped with the
// current step (e.g. proxy.Emit); it may be nil.
func NewExecutor(links Links, ctr Containers, nodes map[string]NodeRef, emit func(trace.Event)) *Executor {
	if emit == nil {
		emit = func(trace.Event) {}
	}
	return &Executor{
		links: links, ctr: ctr, nodes: nodes, emit: emit,
		http:  &http.Client{Timeout: time.Second},
		parts: map[string][][]string{}, crashed: map[string]bool{}, paused: map[string]bool{}, skewed: map[string]int64{},
	}
}

// OnRestart registers a callback run after a crashed node is restarted.
func (e *Executor) OnRestart(fn func(node string)) { e.onRestart = fn }

func partKey(f core.Fault) string {
	return strconv.FormatInt(f.Step, 10) + "|" + GroupsString(f.Groups)
}

func (e *Executor) node(name string) (NodeRef, error) {
	n, ok := e.nodes[name]
	if !ok {
		return NodeRef{}, fmt.Errorf("unknown node %q", name)
	}
	return n, nil
}

func (e *Executor) applyBlocked() {
	all := map[[2]string]bool{}
	keys := make([]string, 0, len(e.parts))
	for k := range e.parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for l := range BlockedLinks(e.parts[k]) {
			all[l] = true
		}
	}
	e.links.SetBlocked(all)
}

func partMeta(f core.Fault) map[string]string {
	m := map[string]string{"groups": GroupsString(f.Groups)}
	if len(f.Nodes) > 0 {
		m["bridge"] = f.Nodes[0]
	}
	return m
}

// Apply starts a fault.
func (e *Executor) Apply(ctx context.Context, f core.Fault) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch f.Kind {
	case trace.Partition:
		e.parts[partKey(f)] = f.Groups
		e.applyBlocked()
		e.emit(trace.Event{Kind: trace.Partition, Meta: partMeta(f)})
		return nil
	case trace.Crash:
		return e.forEach(f, func(name string, n NodeRef) error {
			if e.crashed[name] {
				return nil
			}
			if err := e.ctr.Kill(ctx, n.ContainerID); err != nil {
				return fmt.Errorf("crash %s: %w", name, err)
			}
			e.crashed[name] = true
			e.emit(trace.Event{Kind: trace.Crash, Node: name})
			return nil
		})
	case trace.Pause:
		return e.forEach(f, func(name string, n NodeRef) error {
			if e.paused[name] || e.crashed[name] {
				return nil
			}
			if err := e.ctr.Pause(ctx, n.ContainerID); err != nil {
				return fmt.Errorf("pause %s: %w", name, err)
			}
			e.paused[name] = true
			e.emit(trace.Event{Kind: trace.Pause, Node: name})
			return nil
		})
	case trace.ClockSkew:
		return e.forEach(f, func(name string, n NodeRef) error {
			return e.setClock(ctx, name, n, f.OffsetMs)
		})
	}
	return fmt.Errorf("unknown fault kind %q", f.Kind)
}

// Heal ends a fault.
func (e *Executor) Heal(ctx context.Context, f core.Fault) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch f.Kind {
	case trace.Partition:
		if _, ok := e.parts[partKey(f)]; !ok {
			return nil
		}
		delete(e.parts, partKey(f))
		e.applyBlocked()
		e.emit(trace.Event{Kind: trace.Heal, Meta: partMeta(f)})
		return nil
	case trace.Crash:
		return e.forEach(f, func(name string, n NodeRef) error { return e.restart(ctx, name, n) })
	case trace.Pause:
		return e.forEach(f, func(name string, n NodeRef) error { return e.resume(ctx, name, n) })
	case trace.ClockSkew:
		return e.forEach(f, func(name string, n NodeRef) error {
			if _, ok := e.skewed[name]; !ok {
				return nil
			}
			return e.setClock(ctx, name, n, 0)
		})
	}
	return fmt.Errorf("unknown fault kind %q", f.Kind)
}

func (e *Executor) restart(ctx context.Context, name string, n NodeRef) error {
	if !e.crashed[name] {
		return nil
	}
	if err := e.ctr.Restart(ctx, n.ContainerID); err != nil {
		return fmt.Errorf("restart %s: %w", name, err)
	}
	delete(e.crashed, name)
	delete(e.skewed, name) // a restarted node starts from KURU_CLOCK_OFFSET_MS again
	e.emit(trace.Event{Kind: trace.Restart, Node: name})
	if e.onRestart != nil {
		e.onRestart(name)
	}
	return nil
}

func (e *Executor) resume(ctx context.Context, name string, n NodeRef) error {
	if !e.paused[name] {
		return nil
	}
	if err := e.ctr.Unpause(ctx, n.ContainerID); err != nil {
		return fmt.Errorf("unpause %s: %w", name, err)
	}
	delete(e.paused, name)
	e.emit(trace.Event{Kind: trace.Resume, Node: name})
	return nil
}

func (e *Executor) forEach(f core.Fault, fn func(string, NodeRef) error) error {
	var errs []error
	for _, name := range f.Nodes {
		n, err := e.node(name)
		if err == nil {
			err = fn(name, n)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// setClock posts the offset to the node. A 404 is recorded as unsupported and
// is not an error. Must hold e.mu.
func (e *Executor) setClock(ctx context.Context, name string, n NodeRef, offset int64) error {
	meta := map[string]string{"offset_ms": strconv.FormatInt(offset, 10)}
	body, _ := json.Marshal(map[string]int64{"offset_ms": offset})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.ClientURL+"/kuru/clock", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	switch {
	case err != nil:
		meta["error"] = err.Error()
		e.emit(trace.Event{Kind: trace.ClockSkew, Node: name, Meta: meta})
		return fmt.Errorf("clock skew %s: %w", name, err)
	case resp.StatusCode == http.StatusNotFound:
		meta["unsupported"] = "true"
	case resp.StatusCode != http.StatusOK:
		meta["error"] = "status " + strconv.Itoa(resp.StatusCode)
	}
	if resp != nil {
		resp.Body.Close()
	}
	if offset == 0 {
		delete(e.skewed, name)
	} else {
		e.skewed[name] = offset
	}
	e.emit(trace.Event{Kind: trace.ClockSkew, Node: name, Meta: meta})
	return nil
}

// HealAll unblocks all links, restarts crashed nodes, unpauses paused ones and
// resets clock offsets to 0. It is safe to call more than once.
func (e *Executor) HealAll(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var errs []error
	if len(e.parts) > 0 {
		keys := make([]string, 0, len(e.parts))
		for k := range e.parts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e.emit(trace.Event{Kind: trace.Heal, Meta: map[string]string{"groups": GroupsString(e.parts[k])}})
		}
		e.parts = map[string][][]string{}
		e.links.SetBlocked(nil)
	}
	for _, name := range sortedKeys(e.paused) {
		errs = append(errs, e.resume(ctx, name, e.nodes[name]))
	}
	for _, name := range sortedKeys(e.crashed) {
		errs = append(errs, e.restart(ctx, name, e.nodes[name]))
	}
	skewed := make(map[string]bool, len(e.skewed))
	for k := range e.skewed {
		skewed[k] = true
	}
	for _, name := range sortedKeys(skewed) {
		errs = append(errs, e.setClock(ctx, name, e.nodes[name], 0))
	}
	return errors.Join(errs...)
}

// Down reports the nodes currently crashed or paused, sorted.
func (e *Executor) Down() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	all := map[string]bool{}
	for k := range e.crashed {
		all[k] = true
	}
	for k := range e.paused {
		all[k] = true
	}
	return sortedKeys(all)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
