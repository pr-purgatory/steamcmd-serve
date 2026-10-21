// Package runtime manages game-server containers through the Docker Engine API
// (Docker, Podman and Unraid all serve it; see ADR 0001 §2).
package runtime

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"steamcmd-serve/internal/ports"
)

// Labels on every container the controller creates.
const (
	LabelManaged  = "io.steamcmd-serve.managed"
	LabelInstance = "io.steamcmd-serve.instance"
	LabelRole     = "io.steamcmd-serve.role"
	LabelGame     = "io.steamcmd-serve.game"

	RoleServer = "server"
	RoleJob    = "job"
)

// Docker talks to the container engine at DOCKER_HOST.
type Docker struct{ cli *client.Client }

// New connects using DOCKER_HOST (and related env), negotiating the API version.
func New() (*Docker, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Docker{cli: cli}, nil
}

// Close releases the client.
func (d *Docker) Close() error { return d.cli.Close() }

// Ping checks the engine is reachable.
func (d *Docker) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx, client.PingOptions{})
	return err
}

// MemTotal returns the engine host's memory in bytes.
func (d *Docker) MemTotal(ctx context.Context) (int64, error) {
	res, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return 0, err
	}
	return res.Info.MemTotal, nil
}

// Container is a managed container found on the engine.
type Container struct {
	ID         string
	Name       string
	InstanceID string
	Role       string
	Running    bool
}

// Managed lists all containers carrying the managed label.
func (d *Docker) Managed(ctx context.Context) ([]Container, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", LabelManaged+"=true"),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		out = append(out, Container{
			ID:         c.ID,
			Name:       name,
			InstanceID: c.Labels[LabelInstance],
			Role:       c.Labels[LabelRole],
			Running:    c.State == container.StateRunning,
		})
	}
	return out, nil
}

// PublishedPorts returns host ports published by running containers, except those
// belonging to excludeInstance. Ports bound by non-container host processes are not
// visible here; the engine rejects them when the container starts.
func (d *Docker) PublishedPorts(ctx context.Context, excludeInstance string) (map[ports.Key]bool, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return nil, err
	}
	taken := map[ports.Key]bool{}
	for _, c := range res.Items {
		if excludeInstance != "" && c.Labels[LabelInstance] == excludeInstance {
			continue
		}
		for _, p := range c.Ports {
			if p.PublicPort != 0 {
				taken[ports.Key{Port: int(p.PublicPort), Protocol: p.Type}] = true
			}
		}
	}
	return taken, nil
}

// ServerSpec describes a game-server container.
type ServerSpec struct {
	Name        string
	Image       string
	Cmd         []string
	Env         map[string]string
	Labels      map[string]string
	Binds       []string // host:container[:opts]
	Ports       []ports.Binding
	MemoryBytes int64
	StopSignal  string
	StopTimeout int
}

// Create creates (but does not start) a container, pulling the image if missing.
func (d *Docker) Create(ctx context.Context, s ServerSpec) (string, error) {
	if err := d.ensureImage(ctx, s.Image); err != nil {
		return "", err
	}
	env := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)

	exposed := network.PortSet{}
	bindings := network.PortMap{}
	for _, b := range s.Ports {
		p, err := network.ParsePort(fmt.Sprintf("%d/%s", b.Port, b.Protocol))
		if err != nil {
			return "", err
		}
		exposed[p] = struct{}{}
		bindings[p] = []network.PortBinding{{HostIP: netip.IPv4Unspecified(), HostPort: strconv.Itoa(b.Port)}}
	}
	timeout := s.StopTimeout
	res, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: s.Name,
		Config: &container.Config{
			Image:        s.Image,
			Cmd:          s.Cmd,
			Env:          env,
			Labels:       s.Labels,
			ExposedPorts: exposed,
			StopSignal:   s.StopSignal,
			StopTimeout:  &timeout,
		},
		HostConfig: &container.HostConfig{
			Binds:         s.Binds,
			PortBindings:  bindings,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			Resources:     container.Resources{Memory: s.MemoryBytes},
		},
	})
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

// Start starts a created container.
func (d *Docker) Start(ctx context.Context, id string) error {
	_, err := d.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

// Stop stops a container with its configured signal and timeout.
func (d *Docker) Stop(ctx context.Context, id string) error {
	_, err := d.cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// Remove force-removes a container; a missing container is not an error.
func (d *Docker) Remove(ctx context.Context, id string) error {
	_, err := d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// Running reports whether the container exists and is running.
func (d *Docker) Running(ctx context.Context, id string) (bool, error) {
	res, err := d.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return res.Container.State != nil && res.Container.State.Running, nil
}

func (d *Docker) ensureImage(ctx context.Context, ref string) error {
	_, err := d.cli.ImageInspect(ctx, ref)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return err
	}
	resp, err := d.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer resp.Close()
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}
