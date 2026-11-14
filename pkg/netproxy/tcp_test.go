package netproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kurukshetra/pkg/trace"
)

// echoServer echoes every byte back until the client half-closes.
func echoServer(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(c, c)
				closeWrite(c)
			}()
		}
	}()
	return l.Addr().String()
}

func tcpSetup(t *testing.T) (*Proxy, *trace.Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	tw, _ := trace.NewWriter(path)
	echo := echoServer(t)
	p, err := New([]string{"n1", "n2"}, map[string]string{"n1": echo, "n2": echo}, &fixedClock{}, tw, WithMode("tcp"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, tw, path
}

// scheduler stand-in: collects messages and lets the test decide.
func collector(p *Proxy) (msgs chan *Message, stop func()) {
	msgs = make(chan *Message, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-p.Incoming():
				msgs <- m
			}
		}
	}()
	return msgs, func() { cancel(); <-done }
}

func dial(t *testing.T, p *Proxy, from, to string) net.Conn {
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port(from, to)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestTCPDataIntactInOrderUnderDelays(t *testing.T) {
	p, tw, path := tcpSetup(t)
	msgs, stop := collector(p)
	defer stop()
	c := dial(t, p, "n1", "n2")
	var want bytes.Buffer
	var held []*Message
	for i := 0; i < 5; i++ {
		chunk := []byte(fmt.Sprintf("chunk-%d;", i))
		want.Write(chunk)
		c.Write(chunk)
		select {
		case m := <-msgs:
			held = append(held, m)
		case <-time.After(2 * time.Second):
			t.Fatal("no message")
		}
	}
	closeWrite(c)
	// Deliver in reverse order: chunk 0 is "delayed" the longest. FIFO must hold.
	var wg sync.WaitGroup
	for i := len(held) - 1; i >= 0; i-- {
		p.DeliverAsync(held[i], &wg, nil)
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want.String() {
		t.Fatalf("got %q want %q", got, want.String())
	}
	p.Close()
	tw.Close()
	evs, _ := trace.ReadFile(path)
	var sends, replies int
	for _, e := range evs {
		if e.Kind == trace.MsgSend && e.Meta["dir"] == "" {
			sends++
			if e.Meta["mode"] != "tcp" || e.Meta["conn"] != "1" || !strings.HasPrefix(e.MsgID, "n1-n2-") {
				t.Fatalf("%+v", e)
			}
		}
		if e.Kind == trace.MsgDeliver && e.Meta["dir"] == "reply" {
			replies++
		}
	}
	if sends != 5 || replies == 0 {
		t.Fatalf("sends %d replies %d", sends, replies)
	}
}

func TestTCPDropResets(t *testing.T) {
	p, tw, path := tcpSetup(t)
	msgs, stop := collector(p)
	defer stop()
	c := dial(t, p, "n2", "n1")
	c.Write([]byte("hello"))
	m := <-msgs
	p.Drop(m, "")
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err := c.Read(make([]byte, 10))
	if err == nil {
		t.Fatalf("want reset, got %v", err)
	}
	p.Close()
	tw.Close()
	evs, _ := trace.ReadFile(path)
	found := false
	for _, e := range evs {
		found = found || e.Kind == trace.MsgDrop && e.Meta["reason"] == "reset"
	}
	if !found {
		t.Fatal("no msg_drop reason=reset")
	}
}

func TestTCPPartitionRefusesAndResets(t *testing.T) {
	p, _, _ := tcpSetup(t)
	msgs, stop := collector(p)
	defer stop()
	existing := dial(t, p, "n1", "n2")
	existing.Write([]byte("a"))
	<-msgs // connection is established and registered
	p.SetBlocked(map[[2]string]bool{{"n1", "n2"}: true})
	existing.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := existing.Read(make([]byte, 1)); err == nil {
		t.Fatal("existing connection should be reset")
	}
	fresh := dial(t, p, "n1", "n2")
	fresh.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := fresh.Read(make([]byte, 1)); err == nil {
		t.Fatal("new connection should be refused")
	}
	// The other direction is not partitioned.
	ok := dial(t, p, "n2", "n1")
	ok.Write([]byte("z"))
	select {
	case m := <-msgs:
		if m.From != "n2" {
			t.Fatal(m.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unblocked link stalled")
	}
}

func TestTCPDuplicateIsDeliver(t *testing.T) {
	p, _, _ := tcpSetup(t)
	msgs, stop := collector(p)
	defer stop()
	c := dial(t, p, "n1", "n2")
	c.Write([]byte("once"))
	closeWrite(c)
	var wg sync.WaitGroup
	p.DuplicateAsync(<-msgs, &wg, nil)
	wg.Wait()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, _ := io.ReadAll(c)
	if string(got) != "once" {
		t.Fatalf("got %q", got)
	}
}
