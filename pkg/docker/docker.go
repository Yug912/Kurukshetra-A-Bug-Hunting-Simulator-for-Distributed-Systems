// Package docker is a thin wrapper over the Docker Go SDK that starts, stops,
// pauses and cleans up the containers of one Kurukshetra run.
//
// Every object it creates carries the labels kuru=1, kuru.run=<runID> and (for
// containers and volumes) kuru.node=<name>, so CleanupRun and CleanupAll can
// find them again even after a crash of the kuru process.
package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

// Label keys put on every object kuru creates.
const (
	LabelKuru = "kuru"
	LabelRun  = "kuru.run"
	LabelNode = "kuru.node"
)

// Controller talks to the Docker daemon.
type Controller struct {
	cli *client.Client
}

// New connects using the standard environment (DOCKER_HOST, DOCKER_CERT_PATH, ...).
// If DOCKER_HOST is unset and the default socket is missing, well-known
// alternative sockets (Colima, Docker Desktop, OrbStack, Rancher) are tried.
func New() (*Controller, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host := discoverHost(); host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Controller{cli: cli}, nil
}

// Close releases the client's connections.
func (c *Controller) Close() error { return c.cli.Close() }

// NewRunID returns "<scenario>-<seed>-<6 random hex>". The randomness only
// affects object names, never run decisions.
func NewRunID(scenario string, seed int64) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%d-%s", scenario, seed, hex.EncodeToString(b))
}

// ContainerName returns kuru-<runID>-<node>.
func ContainerName(runID, node string) string { return "kuru-" + runID + "-" + node }

// Ping checks the daemon is reachable.
func (c *Controller) Ping(ctx context.Context) error {
	if _, err := c.cli.Ping(ctx); err != nil {
		return fmt.Errorf("docker ping (is Docker running?): %w", err)
	}
	return nil
}

// EnsureImage checks the image exists locally. kuru never pulls: images are built
// with `make images`.
func (c *Controller) EnsureImage(ctx context.Context, image string) error {
	if _, err := c.cli.ImageInspect(ctx, image); err != nil {
		if errdefs.IsNotFound(err) {
			return fmt.Errorf("image %s not found locally (run `make images`)", image)
		}
		return fmt.Errorf("inspect image %s: %w", image, err)
	}
	return nil
}

// CreateNetwork creates a bridge network with the given labels and returns its ID.
func (c *Controller) CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error) {
	resp, err := c.cli.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge", Labels: labels})
	if err != nil {
		return "", fmt.Errorf("create network %s: %w", name, err)
	}
	return resp.ID, nil
}

// NodeSpec describes one node container.
type NodeSpec struct {
	RunID, Name, Image, Network string
	Env                         map[string]string
	PeerPort                    int // container port, published to a random 127.0.0.1 host port
	ClientPort                  int // container port, published to a random 127.0.0.1 host port
	Volume                      string
}

// Node is a started container.
type Node struct {
	ID, Name, ClientURL, PeerHostAddr string // PeerHostAddr = 127.0.0.1:<published peer port>
}

// Labels returns the labels for an object of run runID (node may be "").
func Labels(runID, node string) map[string]string {
	l := map[string]string{LabelKuru: "1", LabelRun: runID}
	if node != "" {
		l[LabelNode] = node
	}
	return l
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Start creates the node's volume (if named) and container, starts it and
// reads back its published ports. Host ports are chosen up front (free ports on
// 127.0.0.1) and bound explicitly, so they stay the same across Kill/Restart.
func (c *Controller) Start(ctx context.Context, spec NodeSpec) (Node, error) {
	labels := Labels(spec.RunID, spec.Name)
	if spec.Volume != "" {
		if _, err := c.cli.VolumeCreate(ctx, volume.CreateOptions{Name: spec.Volume, Labels: labels}); err != nil {
			return Node{}, fmt.Errorf("create volume for %s: %w", spec.Name, err)
		}
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		n, err := c.startOnce(ctx, spec, labels)
		if err == nil {
			return n, nil
		}
		lastErr = err
		if !isPortConflict(err) {
			return n, err
		}
	}
	return Node{}, lastErr
}

func isPortConflict(err error) bool {
	m := err.Error()
	return strings.Contains(m, "port is already allocated") || strings.Contains(m, "address already in use")
}

// FreePort returns a currently free TCP port on 127.0.0.1.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (c *Controller) startOnce(ctx context.Context, spec NodeSpec, labels map[string]string) (Node, error) {
	hp, err := FreePort()
	if err != nil {
		return Node{}, err
	}
	hc2, err := FreePort()
	if err != nil {
		return Node{}, err
	}
	peer := nat.Port(fmt.Sprintf("%d/tcp", spec.PeerPort))
	cport := nat.Port(fmt.Sprintf("%d/tcp", spec.ClientPort))
	cfg := &container.Config{
		Image:        spec.Image,
		Env:          envList(spec.Env),
		Labels:       labels,
		Hostname:     spec.Name,
		ExposedPorts: nat.PortSet{peer: {}, cport: {}},
	}
	hc := &container.HostConfig{
		NetworkMode: container.NetworkMode(spec.Network),
		PortBindings: nat.PortMap{
			peer:  {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hp)}},
			cport: {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hc2)}},
		},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}
	if spec.Volume != "" {
		hc.Binds = []string{spec.Volume + ":/data"}
	}
	var nc *network.NetworkingConfig
	if spec.Network != "" {
		nc = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			spec.Network: {Aliases: []string{spec.Name}},
		}}
	}
	name := ContainerName(spec.RunID, spec.Name)
	created, err := c.cli.ContainerCreate(ctx, cfg, hc, nc, nil, name)
	if err != nil {
		return Node{}, fmt.Errorf("create container %s: %w", name, err)
	}
	if err := c.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = c.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
		return Node{}, fmt.Errorf("start container %s: %w", name, err)
	}
	n := Node{ID: created.ID, Name: spec.Name}
	if err := c.readPorts(ctx, &n, peer, cport); err != nil {
		return n, err
	}
	return n, nil
}

func (c *Controller) readPorts(ctx context.Context, n *Node, peer, cport nat.Port) error {
	// Ports are assigned at start; poll briefly in case the daemon is slow to report them.
	for i := 0; i < 50; i++ {
		info, err := c.cli.ContainerInspect(ctx, n.ID)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", n.Name, err)
		}
		if info.State != nil && !info.State.Running && info.State.Status == "exited" {
			var tail strings.Builder
			_ = c.CopyLogs(ctx, n.ID, &tail)
			return fmt.Errorf("node %s exited with code %d before publishing ports; logs:\n%s", n.Name, info.State.ExitCode, lastLines(tail.String(), 10))
		}
		if info.NetworkSettings != nil {
			p := hostPort(info.NetworkSettings.Ports, peer)
			cp := hostPort(info.NetworkSettings.Ports, cport)
			if p != "" && cp != "" {
				n.PeerHostAddr = net.JoinHostPort("127.0.0.1", p)
				n.ClientURL = "http://" + net.JoinHostPort("127.0.0.1", cp)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("node %s: published ports not reported", n.Name)
}

func hostPort(pm nat.PortMap, p nat.Port) string {
	for _, b := range pm[p] {
		if b.HostPort != "" && b.HostPort != "0" {
			return b.HostPort
		}
	}
	return ""
}

// Kill sends SIGKILL to the container.
func (c *Controller) Kill(ctx context.Context, id string) error {
	if err := c.cli.ContainerKill(ctx, id, "SIGKILL"); err != nil {
		return fmt.Errorf("kill %s: %w", short(id), err)
	}
	return c.waitRunning(ctx, id, false)
}

// Restart starts the same (stopped) container again; its volume keeps the data
// and its explicitly bound host ports stay the same.
func (c *Controller) Restart(ctx context.Context, id string) error {
	if err := c.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("restart %s: %w", short(id), err)
	}
	return c.waitRunning(ctx, id, true)
}

// Inspect returns the node's current published addresses.
func (c *Controller) Inspect(ctx context.Context, id string, peerPort, clientPort int) (Node, error) {
	n := Node{ID: id}
	info, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return n, fmt.Errorf("inspect %s: %w", short(id), err)
	}
	n.Name = info.Config.Labels[LabelNode]
	err = c.readPorts(ctx, &n, nat.Port(fmt.Sprintf("%d/tcp", peerPort)), nat.Port(fmt.Sprintf("%d/tcp", clientPort)))
	return n, err
}

// Pause freezes the container's processes.
func (c *Controller) Pause(ctx context.Context, id string) error {
	if err := c.cli.ContainerPause(ctx, id); err != nil {
		return fmt.Errorf("pause %s: %w", short(id), err)
	}
	return nil
}

// Unpause resumes a paused container.
func (c *Controller) Unpause(ctx context.Context, id string) error {
	if err := c.cli.ContainerUnpause(ctx, id); err != nil {
		return fmt.Errorf("unpause %s: %w", short(id), err)
	}
	return nil
}

func (c *Controller) waitRunning(ctx context.Context, id string, want bool) error {
	for i := 0; i < 100; i++ {
		info, err := c.cli.ContainerInspect(ctx, id)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", short(id), err)
		}
		if info.State != nil && info.State.Running == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("container %s did not reach running=%v", short(id), want)
}

// CopyLogs writes the container's stdout and stderr (demultiplexed) to w.
func (c *Controller) CopyLogs(ctx context.Context, id string, w io.Writer) error {
	rc, err := c.cli.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true})
	if err != nil {
		return fmt.Errorf("logs %s: %w", short(id), err)
	}
	defer rc.Close()
	info, err := c.cli.ContainerInspect(ctx, id)
	if err == nil && info.Config != nil && info.Config.Tty {
		_, err = io.Copy(w, rc)
	} else {
		_, err = stdcopy.StdCopy(w, w, rc)
	}
	if err != nil {
		return fmt.Errorf("copy logs %s: %w", short(id), err)
	}
	return nil
}

// Removed lists what a cleanup removed.
type Removed struct {
	Containers, Networks, Volumes []string
}

// Empty reports whether nothing was removed.
func (r Removed) Empty() bool { return len(r.Containers)+len(r.Networks)+len(r.Volumes) == 0 }

func (r Removed) String() string {
	return fmt.Sprintf("%d containers, %d networks, %d volumes", len(r.Containers), len(r.Networks), len(r.Volumes))
}

// CleanupRun removes containers, the network and volumes labelled kuru.run=<runID>.
func (c *Controller) CleanupRun(ctx context.Context, runID string) error {
	_, err := c.cleanup(ctx, LabelRun+"="+runID)
	return err
}

// CleanupAll removes everything labelled kuru=1 and reports what it removed.
func (c *Controller) CleanupAll(ctx context.Context) (Removed, error) {
	return c.cleanup(ctx, LabelKuru+"=1")
}

func (c *Controller) cleanup(ctx context.Context, label string) (Removed, error) {
	var rm Removed
	var errs []error
	f := filters.NewArgs(filters.Arg("label", label))
	cs, err := c.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return rm, fmt.Errorf("list containers: %w", err)
	}
	for _, ct := range cs {
		err := c.cli.ContainerRemove(ctx, ct.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove container %s: %w", short(ct.ID), err))
			continue
		}
		rm.Containers = append(rm.Containers, strings.TrimPrefix(firstName(ct.Names), "/"))
	}
	ns, err := c.cli.NetworkList(ctx, network.ListOptions{Filters: f})
	if err != nil {
		errs = append(errs, fmt.Errorf("list networks: %w", err))
	}
	for _, n := range ns {
		if err := c.cli.NetworkRemove(ctx, n.ID); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove network %s: %w", n.Name, err))
			continue
		}
		rm.Networks = append(rm.Networks, n.Name)
	}
	vs, err := c.cli.VolumeList(ctx, volume.ListOptions{Filters: f})
	if err != nil {
		errs = append(errs, fmt.Errorf("list volumes: %w", err))
	}
	for _, v := range vs.Volumes {
		if err := c.cli.VolumeRemove(ctx, v.Name, true); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", v.Name, err))
			continue
		}
		rm.Volumes = append(rm.Volumes, v.Name)
	}
	return rm, errors.Join(errs...)
}

// ListRun returns the names of containers labelled kuru.run=<runID>.
func (c *Controller) ListRun(ctx context.Context, runID string) ([]string, error) {
	cs, err := c.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelRun+"="+runID))})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ct := range cs {
		out = append(out, strings.TrimPrefix(firstName(ct.Names), "/"))
	}
	return out, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ListRunPrefix returns containers, networks and volumes whose kuru.run label
// starts with prefix (e.g. "<scenario>-<seed>-"), without removing anything.
func (c *Controller) ListRunPrefix(ctx context.Context, prefix string) ([]string, error) {
	f := filters.NewArgs(filters.Arg("label", LabelKuru+"=1"))
	var out []string
	cs, err := c.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	for _, ct := range cs {
		if strings.HasPrefix(ct.Labels[LabelRun], prefix) {
			out = append(out, "container "+strings.TrimPrefix(firstName(ct.Names), "/"))
		}
	}
	ns, err := c.cli.NetworkList(ctx, network.ListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	for _, n := range ns {
		if strings.HasPrefix(n.Labels[LabelRun], prefix) {
			out = append(out, "network "+n.Name)
		}
	}
	vs, err := c.cli.VolumeList(ctx, volume.ListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	for _, v := range vs.Volumes {
		if strings.HasPrefix(v.Labels[LabelRun], prefix) {
			out = append(out, "volume "+v.Name)
		}
	}
	return out, nil
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
