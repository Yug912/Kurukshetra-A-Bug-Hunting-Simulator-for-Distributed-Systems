package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/hashicorp/raft"
)

type memSink struct {
	bytes.Buffer
	cancelled bool
}

func (s *memSink) ID() string    { return "mem" }
func (s *memSink) Cancel() error { s.cancelled = true; return nil }
func (s *memSink) Close() error  { return nil }

func applyJSON(t *testing.T, f *FSM, c Command) ApplyResult {
	t.Helper()
	b, _ := json.Marshal(c)
	res, ok := f.Apply(&raft.Log{Data: b}).(ApplyResult)
	if !ok {
		t.Fatal("Apply did not return ApplyResult")
	}
	return res
}

func TestFSMApply(t *testing.T) {
	f := NewFSM()
	if r := applyJSON(t, f, Command{Op: opGet, Key: "k"}); !r.OK || r.Found {
		t.Fatalf("get missing: %+v", r)
	}
	if r := applyJSON(t, f, Command{Op: opPut, Key: "k", Value: "a"}); !r.OK {
		t.Fatalf("put: %+v", r)
	}
	if r := applyJSON(t, f, Command{Op: opGet, Key: "k"}); !r.Found || r.Value != "a" {
		t.Fatalf("get: %+v", r)
	}
	if r := applyJSON(t, f, Command{Op: opCAS, Key: "k", Expect: "x", Value: "b"}); r.OK || r.Error != "cas_mismatch" {
		t.Fatalf("cas mismatch: %+v", r)
	}
	if r := applyJSON(t, f, Command{Op: opCAS, Key: "k", Expect: "a", Value: "b"}); !r.OK {
		t.Fatalf("cas: %+v", r)
	}
	if r := applyJSON(t, f, Command{Op: opCAS, Key: "new", Expect: "", Value: "z"}); !r.OK {
		t.Fatalf("cas on missing key with empty expect: %+v", r)
	}
	if v, _ := f.Get("k"); v != "b" {
		t.Fatalf("got %q", v)
	}
	if r := f.Apply(&raft.Log{Data: []byte("{")}).(ApplyResult); r.Error == "" {
		t.Fatal("want error for bad json")
	}
	if r := f.ApplyCommand(Command{Op: "nope"}); r.Error == "" {
		t.Fatal("want error for unknown op")
	}
}

func TestFSMSnapshotRestore(t *testing.T) {
	f := NewFSM()
	f.ApplyCommand(Command{Op: opPut, Key: "a", Value: "1"})
	f.ApplyCommand(Command{Op: opPut, Key: "b", Value: "2"})
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	snap.Release()
	f.ApplyCommand(Command{Op: opPut, Key: "a", Value: "changed"})

	g := NewFSM()
	if err := g.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Get("a"); v != "1" {
		t.Fatalf("a=%q", v)
	}
	if v, _ := g.Get("b"); v != "2" {
		t.Fatalf("b=%q", v)
	}
	if err := g.Restore(io.NopCloser(bytes.NewReader([]byte("nope")))); err == nil {
		t.Fatal("want restore error")
	}
}
