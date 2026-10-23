package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	dockerclient "github.com/moby/moby/client"
)

// NewFakeEngineForTests returns an in-memory EngineClient suitable for
// integration tests in OTHER packages (e.g. the agent ↔ server end-to-end
// test in internal/api). Production code must NOT call this constructor —
// use NewEngineClient against the real Docker daemon. The fake mirrors the
// Phase 1 baseline expectations (overlay2, live-restore, json-file,
// no-new-privileges, cgroup v2) so adapter Init() succeeds without touching
// a real host.
func NewFakeEngineForTests() EngineClient {
	return &inMemEngine{
		apiVersion:     MinEngineAPIVersion,
		containers:     make(map[string]*inMemContainer),
		containersByID: make(map[string]*inMemContainer),
		networks:       make(map[string]*inMemNetwork),
		info: system.Info{
			ServerVersion:      "24.0.0",
			Driver:             "overlay2",
			LiveRestoreEnabled: true,
			LoggingDriver:      "json-file",
			SecurityOptions:    []string{"name=no-new-privileges"},
			CgroupVersion:      "2",
		},
	}
}

// inMemEngine is the production-visible variant of fakeEngine used by the
// _test files inside this package. The implementation is duplicated so the
// _test files can keep their own assertion-friendly counters without
// exposing them to non-test imports.
type inMemEngine struct {
	mu             sync.Mutex
	apiVersion     string
	info           system.Info
	containers     map[string]*inMemContainer
	containersByID map[string]*inMemContainer
	networks       map[string]*inMemNetwork
	createCount    int
	netCreateCount int
}

type inMemContainer struct {
	id     string
	name   string
	cfg    *container.Config
	hc     *container.HostConfig
	netCfg *network.NetworkingConfig
	state  string
}

type inMemNetwork struct {
	id     string
	name   string
	labels map[string]string
}

func (i *inMemEngine) NegotiatedAPIVersion() string { return i.apiVersion }

func (i *inMemEngine) Info(ctx context.Context) (system.Info, error) { return i.info, nil }

func (i *inMemEngine) Ping(ctx context.Context) error { return nil }

func (i *inMemEngine) ContainerCreate(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig) (string, error) {
	return i.ContainerCreateWithNetworking(ctx, name, cfg, hc, nil)
}

func (i *inMemEngine) ContainerCreateWithNetworking(_ context.Context, name string, cfg *container.Config, hc *container.HostConfig, netCfg *network.NetworkingConfig) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.containers[name]; exists {
		return "", fmt.Errorf("conflict: container name %q already in use", name)
	}
	i.createCount++
	id := fmt.Sprintf("id-%s-%d", name, i.createCount)
	c := &inMemContainer{id: id, name: name, cfg: cfg, hc: hc, netCfg: netCfg, state: "created"}
	i.containers[name] = c
	i.containersByID[id] = c
	return id, nil
}

func (i *inMemEngine) ContainerStart(ctx context.Context, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, ok := i.containersByID[id]
	if !ok {
		return fmt.Errorf("no such container: %s", id)
	}
	c.state = "running"
	return nil
}

func (i *inMemEngine) ContainerStop(ctx context.Context, id string, grace time.Duration) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, ok := i.containersByID[id]
	if !ok {
		return fmt.Errorf("no such container: %s", id)
	}
	c.state = "exited"
	return nil
}

func (i *inMemEngine) ContainerRemove(ctx context.Context, id string, force bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, ok := i.containersByID[id]
	if !ok {
		return nil
	}
	delete(i.containersByID, id)
	delete(i.containers, c.name)
	return nil
}

func (i *inMemEngine) ContainerInspect(ctx context.Context, ref string) (container.InspectResponse, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, ok := i.containersByID[ref]
	if !ok {
		c, ok = i.containers[ref]
	}
	if !ok {
		return container.InspectResponse{}, fmt.Errorf("no such container: %s", ref)
	}
	return container.InspectResponse{
		ID:     c.id,
		Name:   "/" + c.name,
		State:  &container.State{Running: c.state == "running", Status: container.ContainerState(c.state)},
		Config: c.cfg,
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("10.0.0.1")},
			},
		},
	}, nil
}

func (i *inMemEngine) ContainerList(ctx context.Context, labelFilter map[string]string) ([]container.Summary, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]container.Summary, 0, len(i.containers))
	for _, c := range i.containers {
		if c.cfg == nil {
			continue
		}
		ok := true
		for k, v := range labelFilter {
			if c.cfg.Labels[k] != v && v != "" {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		out = append(out, container.Summary{
			ID:     c.id,
			Names:  []string{"/" + c.name},
			Labels: c.cfg.Labels,
			State:  container.ContainerState(c.state),
			Status: c.state,
		})
	}
	return out, nil
}

func (i *inMemEngine) ImagePull(ctx context.Context, ref string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("ok")), nil
}

func (i *inMemEngine) ImageExposedPorts(context.Context, string) ([]int, error) { return nil, nil }

func (i *inMemEngine) Close() error { return nil }

func (i *inMemEngine) NetworkCreate(_ context.Context, name string, options dockerclient.NetworkCreateOptions) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.networks[name]; ok {
		return "", fmt.Errorf("network %q already exists", name)
	}
	i.netCreateCount++
	id := fmt.Sprintf("netid-%s-%d", name, i.netCreateCount)
	i.networks[name] = &inMemNetwork{id: id, name: name, labels: options.Labels}
	return id, nil
}

func (i *inMemEngine) NetworkConnect(context.Context, string, string) error { return nil }

func (i *inMemEngine) NetworkRemove(_ context.Context, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	for name, n := range i.networks {
		if n.id == id {
			delete(i.networks, name)
			return nil
		}
	}
	return errors.New("no such network")
}

func (i *inMemEngine) NetworkList(_ context.Context, labelFilter map[string]string) ([]network.Summary, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]network.Summary, 0, len(i.networks))
	for _, n := range i.networks {
		match := true
		for k, v := range labelFilter {
			if n.labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, network.Summary{Network: network.Network{ID: n.id, Name: n.name, Labels: n.labels}})
		}
	}
	return out, nil
}
