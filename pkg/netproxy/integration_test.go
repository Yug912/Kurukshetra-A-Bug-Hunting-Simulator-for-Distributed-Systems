//go:build integration

package netproxy

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"kurukshetra/pkg/docker"
	"kurukshetra/pkg/trace"
)

func TestPassThroughWithFakeNodes(t *testing.T) {
	c, err := docker.New()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skip(err)
	}
	if err := c.EnsureFakeNodeImage(ctx); err != nil {
		t.Skip(err)
	}
	runID := docker.NewRunID("proxy-itest", 1)
	defer c.CleanupRun(context.Background(), runID)

	path := filepath.Join(t.TempDir(), "trace.jsonl")
	tw, _ := trace.NewWriter(path)
	names := []string{"n1", "n2", "n3"}
	p, err := New(names, nil, &fixedClock{}, tw)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	netID, err := c.CreateNetwork(ctx, "kuru-"+runID, docker.Labels(runID, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		node, err := c.Start(ctx, docker.NodeSpec{RunID: runID, Name: n, Image: docker.FakeNodeImage, Network: netID,
			Env:      map[string]string{"NODE_ID": n, "PEER_LISTEN": ":7000", "CLIENT_LISTEN": ":8080", "PEERS": p.PeerEnv(n)},
			PeerPort: 7000, ClientPort: 8080})
		if err != nil {
			t.Fatal(err)
		}
		p.SetTarget(n, node.PeerHostAddr)
	}
	pctx, pcancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { PassThrough(pctx, p); close(done) }()
	time.Sleep(3 * time.Second)
	// Stop the senders first, then let pass-through drain what is parked.
	if err := c.CleanupRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	pcancel()
	<-done
	p.Close()
	tw.Close()

	evs, err := trace.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sends, delivers := map[string]int{}, map[string]int{}
	for _, e := range evs {
		switch e.Kind {
		case trace.MsgSend:
			sends[e.MsgID]++
		case trace.MsgDeliver:
			delivers[e.MsgID]++
		}
	}
	if len(sends) < 30 {
		t.Fatalf("only %d messages seen; can containers reach the proxy at %s? (set KURU_PROXY_HOST)", len(sends), DefaultHost)
	}
	missing := 0
	for id := range sends {
		if delivers[id] != 1 {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("%d of %d sends without exactly one deliver", missing, len(sends))
	}
	fmt.Printf("proxy integration: %d messages, %d without delivery at shutdown\n", len(sends), missing)
}
