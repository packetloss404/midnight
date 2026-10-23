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

// fakeEngine implements EngineClient with a minimal in-memory Docker
// emulation sufficient for adapter unit tests. It is intentionally NOT
// exported; integration tests under //go:build integration use the real
// dockerclient.Client through NewEngineClient.
type fakeEngine struct {
	mu             sync.Mutex
	apiVersion     string
	info           system.Info
	infoErr        error
	pingErr        error
	failCreate     error
	failStart      error
	failStop       error
	failRemove     error
	failInspect    error
	failList       error
	failNetCreate  error
	containers     map[string]*fakeContainer
	containersByID map[string]*fakeContainer
	networks       map[string]*fakeNetwork
	createCalls    int
	startCalls     int
	stopCalls      int
	removeCalls    int
	listCalls      int
	netCreateCalls int
	netRemoveCalls int
	netListCalls   int
	closed         bool
	pulled         []string
	exposedPorts   map[string][]int
	exposedErr     error
	netConnects    []string
	failNetConnect error
}

type fakeContainer struct {
	id         string
	name       string
	cfg        *container.Config
	hc         *container.HostConfig
	netCfg     *network.NetworkingConfig
	state      string
	exitCode   int
	finishedAt string
	created    time.Time
	stopped    bool
	// restarting, restartCount, oomKilled, stateError, and logs let tests
	// drive the liveness evidence the readiness gate reads.
	restarting   bool
	restartCount int
	oomKilled    bool
	stateError   string
	logs         []string
}

type fakeNetwork struct {
	id     string
	name   string
	labels map[string]string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		apiVersion:     MinEngineAPIVersion,
		containers:     make(map[string]*fakeContainer),
		containersByID: make(map[string]*fakeContainer),
		networks:       make(map[string]*fakeNetwork),
		info: system.Info{
			ServerVersion:      "24.0.0",
			Driver:             "overlay2",
			LiveRestoreEnabled: true,
			LoggingDriver:      "json-file",
			SecurityOptions:    []string{"name=no-new-privileges", "name=apparmor"},
			CgroupVersion:      "2",
		},
	}
}

func (f *fakeEngine) NegotiatedAPIVersion() string { return f.apiVersion }

func (f *fakeEngine) Info(ctx context.Context) (system.Info, error) {
	if f.infoErr != nil {
		return system.Info{}, f.infoErr
	}
	return f.info, nil
}

func (f *fakeEngine) Ping(ctx context.Context) error { return f.pingErr }

func (f *fakeEngine) ContainerCreate(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig) (string, error) {
	return f.ContainerCreateWithNetworking(ctx, name, cfg, hc, nil)
}

func (f *fakeEngine) ContainerCreateWithNetworking(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig, netCfg *network.NetworkingConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.failCreate != nil {
		return "", f.failCreate
	}
	if _, exists := f.containers[name]; exists {
		return "", fmt.Errorf("conflict: container name %q is already in use", name)
	}
	id := fmt.Sprintf("id-%s-%d", name, f.createCalls)
	c := &fakeContainer{
		id:      id,
		name:    name,
		cfg:     cfg,
		hc:      hc,
		netCfg:  netCfg,
		state:   "created",
		created: time.Now(),
	}
	f.containers[name] = c
	f.containersByID[id] = c
	return id, nil
}

func (f *fakeEngine) ContainerStart(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.failStart != nil {
		return f.failStart
	}
	c, ok := f.containersByID[id]
	if !ok {
		return fmt.Errorf("no such container: %s", id)
	}
	c.state = "running"
	return nil
}

func (f *fakeEngine) ContainerStop(ctx context.Context, id string, grace time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	if f.failStop != nil {
		return f.failStop
	}
	c, ok := f.containersByID[id]
	if !ok {
		return fmt.Errorf("no such container: %s", id)
	}
	c.state = "exited"
	c.exitCode = 137
	c.finishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	c.stopped = true
	return nil
}

func (f *fakeEngine) ContainerRemove(ctx context.Context, id string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	if f.failRemove != nil {
		return f.failRemove
	}
	c, ok := f.containersByID[id]
	if !ok {
		return fmt.Errorf("no such container: %s", id)
	}
	delete(f.containersByID, id)
	delete(f.containers, c.name)
	return nil
}

func (f *fakeEngine) ContainerInspect(ctx context.Context, ref string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failInspect != nil {
		return container.InspectResponse{}, f.failInspect
	}
	c, ok := f.containersByID[ref]
	if !ok {
		c, ok = f.containers[ref]
	}
	if !ok {
		return container.InspectResponse{}, errors.New("no such container")
	}
	resp := container.InspectResponse{
		ID:           c.id,
		Name:         "/" + c.name,
		RestartCount: c.restartCount,
		State: &container.State{
			Running: c.state == "running", Status: container.ContainerState(c.state),
			ExitCode: c.exitCode, FinishedAt: c.finishedAt,
			Restarting: c.restarting, OOMKilled: c.oomKilled, Error: c.stateError,
		},
		Config: c.cfg,
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("10.0.0.1")},
			},
		},
	}
	if c.hc != nil {
		for _, item := range c.hc.Mounts {
			resp.Mounts = append(resp.Mounts, container.MountPoint{
				Type: item.Type, Name: item.Source, Destination: item.Target,
				RW: !item.ReadOnly,
			})
		}
	}
	return resp, nil
}

func (f *fakeEngine) ContainerList(ctx context.Context, labelFilter map[string]string) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.failList != nil {
		return nil, f.failList
	}
	out := make([]container.Summary, 0, len(f.containers))
	for _, c := range f.containers {
		if c.cfg == nil {
			continue
		}
		match := true
		for k, v := range labelFilter {
			if c.cfg.Labels[k] != v && v != "" {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		out = append(out, container.Summary{
			ID:     c.id,
			Names:  []string{"/" + c.name},
			Image:  c.cfg.Image,
			Labels: c.cfg.Labels,
			State:  container.ContainerState(c.state),
			Status: c.state,
		})
	}
	return out, nil
}

func (f *fakeEngine) ImagePull(ctx context.Context, ref string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	return io.NopCloser(strings.NewReader("ok")), nil
}

func (f *fakeEngine) ImageExposedPorts(_ context.Context, ref string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exposedErr != nil {
		return nil, f.exposedErr
	}
	return append([]int(nil), f.exposedPorts[ref]...), nil
}

func (f *fakeEngine) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeEngine) NetworkCreate(_ context.Context, name string, options dockerclient.NetworkCreateOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netCreateCalls++
	if f.failNetCreate != nil {
		return "", f.failNetCreate
	}
	if _, ok := f.networks[name]; ok {
		return "", fmt.Errorf("network %q already exists", name)
	}
	id := fmt.Sprintf("netid-%s-%d", name, f.netCreateCalls)
	f.networks[name] = &fakeNetwork{id: id, name: name, labels: options.Labels}
	return id, nil
}

func (f *fakeEngine) NetworkConnect(_ context.Context, networkID, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNetConnect != nil {
		return f.failNetConnect
	}
	f.netConnects = append(f.netConnects, networkID+"<-"+containerID)
	return nil
}

func (f *fakeEngine) NetworkRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netRemoveCalls++
	for name, n := range f.networks {
		if n.id == id {
			delete(f.networks, name)
			return nil
		}
	}
	return errors.New("no such network")
}

func (f *fakeEngine) NetworkList(_ context.Context, labelFilter map[string]string) ([]network.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netListCalls++
	out := make([]network.Summary, 0, len(f.networks))
	for _, n := range f.networks {
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

// ContainerLogTail makes fakeEngine a LogEngineClient so adapter tests can
// assert that a failed candidate's own output reaches the failure reason.
func (f *fakeEngine) ContainerLogTail(_ context.Context, ref string, lines int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containersByID[ref]
	if !ok {
		c, ok = f.containers[ref]
	}
	if !ok {
		return nil, errors.New("no such container")
	}
	if lines > 0 && len(c.logs) > lines {
		return append([]string(nil), c.logs[len(c.logs)-lines:]...), nil
	}
	return append([]string(nil), c.logs...), nil
}

var _ LogEngineClient = (*fakeEngine)(nil)
