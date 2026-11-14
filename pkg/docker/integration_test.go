//go:build integration

package docker

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// NewForTest connects to Docker and makes sure kuru/fake-node exists, or skips.
func newForTest(t *testing.T) *Controller {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	if err := c.EnsureFakeNodeImage(ctx); err != nil {
		t.Skipf("cannot build %s: %v", FakeNodeImage, err)
	}
	return c
}

func TestControllerLifecycle(t *testing.T) {
	c := newForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	runID := NewRunID("itest", 1)
	defer func() {
		if err := c.CleanupRun(context.Background(), runID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		left, err := c.ListRun(context.Background(), runID)
		if err != nil || len(left) != 0 {
			t.Errorf("containers left behind: %v %v", left, err)
		}
	}()
	netID, err := c.CreateNetwork(ctx, "kuru-"+runID, Labels(runID, ""))
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]Node{}
	for _, name := range []string{"n1", "n2", "n3"} {
		n, err := c.Start(ctx, NodeSpec{
			RunID: runID, Name: name, Image: FakeNodeImage, Network: netID,
			Env:      map[string]string{"NODE_ID": name, "PEER_LISTEN": ":7000", "CLIENT_LISTEN": ":8080"},
			PeerPort: 7000, ClientPort: 8080, Volume: fmt.Sprintf("kuru-%s-%s", runID, name),
		})
		if err != nil {
			t.Fatal(err)
		}
		nodes[name] = n
	}
	for name, n := range nodes {
		if err := WaitHealthy(ctx, n.ClientURL, 30*time.Second); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	n2 := nodes["n2"]
	if err := c.Kill(ctx, n2.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Restart(ctx, n2.ID); err != nil {
		t.Fatal(err)
	}
	re, err := c.Inspect(ctx, n2.ID, 7000, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if re.ClientURL != n2.ClientURL {
		t.Logf("note: n2 client port changed on restart: %s -> %s", n2.ClientURL, re.ClientURL)
	}
	if err := WaitHealthy(ctx, re.ClientURL, 30*time.Second); err != nil {
		t.Fatalf("n2 after restart: %v", err)
	}
	n3 := nodes["n3"]
	if err := c.Pause(ctx, n3.ID); err != nil {
		t.Fatal(err)
	}
	if err := WaitHealthy(ctx, n3.ClientURL, 500*time.Millisecond); err == nil {
		t.Fatal("paused node answered health")
	}
	if err := c.Unpause(ctx, n3.ID); err != nil {
		t.Fatal(err)
	}
	if err := WaitHealthy(ctx, n3.ClientURL, 10*time.Second); err != nil {
		t.Fatalf("n3 after unpause: %v", err)
	}
	var logs stringsBuilder
	if err := c.CopyLogs(ctx, nodes["n1"].ID, &logs); err != nil || logs.Len() == 0 {
		t.Fatalf("logs: %v (%d bytes)", err, logs.Len())
	}
}

type stringsBuilder struct{ b []byte }

func (s *stringsBuilder) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }
func (s *stringsBuilder) Len() int                    { return len(s.b) }
