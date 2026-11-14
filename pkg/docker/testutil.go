package docker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// FakeNodeImage is the image built from cmd/fake-node.
const FakeNodeImage = "kuru/fake-node"

// RepoRoot walks up from the working directory to the folder containing go.mod.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// EnsureFakeNodeImage builds kuru/fake-node with the docker CLI if it is missing.
// Used by integration tests.
func (c *Controller) EnsureFakeNodeImage(ctx context.Context) error {
	if c.EnsureImage(ctx, FakeNodeImage) == nil {
		return nil
	}
	root, err := RepoRoot()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", FakeNodeImage, "-f", filepath.Join(root, "cmd", "fake-node", "Dockerfile"), root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build %s: %w\n%s", FakeNodeImage, err, out)
	}
	return nil
}

// WaitHealthy polls GET <clientURL>/kuru/health until it returns 200, with backoff.
func WaitHealthy(ctx context.Context, clientURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hc := &http.Client{Timeout: time.Second}
	delay := 20 * time.Millisecond
	var last error
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, clientURL+"/kuru/health", nil)
		resp, err := hc.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("health %s: %w (last: %v)", clientURL, ctx.Err(), last)
		case <-time.After(delay):
		}
		if delay < 500*time.Millisecond {
			delay *= 2
		}
	}
}
