package history

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestFileRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history.jsonl")
	r, err := NewFileRecorder(p)
	if err != nil {
		t.Fatal(err)
	}
	a := r.Record(Op{Client: 0, Type: Invoke, F: Write, Key: "k0", Value: "a", Node: "n1", Step: 1})
	b := r.Record(Op{Client: 0, Type: OK, F: Write, Key: "k0", Value: "a", Node: "n1", Step: 2, WallNs: 7})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Op{a, b}) {
		t.Fatalf("got %+v", got)
	}
	if a.Index != 0 || b.Index != 1 || a.WallNs == 0 || b.WallNs != 7 {
		t.Fatal("index/wall wrong")
	}
}

func TestMemRecorderConcurrent(t *testing.T) {
	var m MemRecorder
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				m.Record(Op{Client: c, Type: Invoke})
			}
		}(i)
	}
	wg.Wait()
	for i, op := range m.Ops() {
		if op.Index != i {
			t.Fatalf("op %d index %d", i, op.Index)
		}
	}
	if len(m.Ops()) != 200 {
		t.Fatal("wrong count")
	}
}

func TestPairs(t *testing.T) {
	var m MemRecorder
	m.Record(Op{Client: 0, Type: Invoke, F: Write, Key: "k", Value: "a"})
	m.Record(Op{Client: 1, Type: Invoke, F: Read, Key: "k"})
	m.Record(Op{Client: 0, Type: OK, F: Write, Key: "k", Value: "a"})
	m.Record(Op{Client: 1, Type: Fail, F: Read, Key: "k"})
	m.Record(Op{Client: 2, Type: Invoke, F: Read, Key: "k"}) // unfinished
	ps, err := Pairs(m.Ops())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 3 {
		t.Fatalf("got %d pairs", len(ps))
	}
	if ps[0].Complete == nil || ps[0].Complete.Index != 2 || ps[1].Complete.Type != Fail {
		t.Fatal("bad pairing")
	}
	if ps[2].Complete != nil {
		t.Fatal("unfinished op should have nil Complete")
	}
}

func TestPairsErrors(t *testing.T) {
	if _, err := Pairs([]Op{{Client: 0, Type: Invoke}, {Client: 0, Type: Invoke}}); err == nil {
		t.Fatal("want error for double invoke")
	}
	if _, err := Pairs([]Op{{Client: 0, Type: OK}}); err == nil {
		t.Fatal("want error for orphan completion")
	}
}
