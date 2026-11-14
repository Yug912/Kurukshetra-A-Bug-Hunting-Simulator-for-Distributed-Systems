package trace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := NewWriter(p)
	if err != nil {
		t.Fatal(err)
	}
	in := []Event{
		{Step: 0, Kind: RunStart},
		{Step: 5, Kind: MsgSend, From: "n1", To: "n2", MsgID: "n1-n2-1", Size: 10, Meta: map[string]string{"path": "/peer/x"}},
		{Step: 6, WallNs: 123, Kind: MsgDeliver, From: "n1", To: "n2", MsgID: "n1-n2-1"},
	}
	var want []Event
	for _, e := range in {
		want = append(want, w.Append(e))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if want[2].WallNs != 123 || want[0].WallNs == 0 {
		t.Fatal("WallNs handling wrong")
	}
}

func TestConcurrentSeq(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trace.jsonl")
	w, _ := NewWriter(p)
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				w.Append(Event{Kind: Note})
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	evs, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1000 {
		t.Fatalf("got %d events", len(evs))
	}
	for i, e := range evs {
		if e.Seq != int64(i) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
}

func TestReadFileErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte("{\"seq\":0,\"kind\":\"note\"}\n\n{bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadFile(p)
	if err == nil || !strings.Contains(err.Error(), ":3:") {
		t.Fatalf("want line 3 error, got %v", err)
	}
}

func TestIsDecision(t *testing.T) {
	cases := map[Kind]bool{
		RunStart: false, RunEnd: false, MsgSend: false, Note: false,
		MsgDeliver: true, MsgDrop: true, MsgDelay: true, MsgDup: true,
		Partition: true, Heal: true, Crash: true, Restart: true,
		Pause: true, Resume: true, ClockSkew: true,
	}
	for k, want := range cases {
		if got := IsDecision(k); got != want {
			t.Errorf("IsDecision(%s)=%v want %v", k, got, want)
		}
	}
}
