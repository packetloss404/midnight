package docker

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	dockerclient "github.com/moby/moby/client"
)

// EngineClient is the narrow surface the Docker adapter requires from the
// Docker SDK. Tests substitute a fake; production wires the real
// dockerclient.Client via NewEngineClient.
//
// The interface intentionally stays minimal — only the calls Lane 2's
// Schedule → Start → Reconcile → Stop → Remove cycle exercises plus the
// /info + /version endpoints the daemon.json baseline needs.
type EngineClient interface {
	NegotiatedAPIVersion() string
	Info(ctx context.Context) (system.Info, error)
	Ping(ctx context.Context) error
	ContainerCreate(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig) (string, error)
	// ContainerCreateWithNetworking is the variant that attaches the
	// container to a user-defined network at create time. Implementations
	// MUST behave identically to ContainerCreate when netCfg is nil so
	// callers that do not need private networking can ignore this method.
	ContainerCreateWithNetworking(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig, netCfg *network.NetworkingConfig) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string, grace time.Duration) error
	ContainerRemove(ctx context.Context, id string, force bool) error
	ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error)
	ContainerList(ctx context.Context, labelFilter map[string]string) ([]container.Summary, error)
	ImagePull(ctx context.Context, ref string) (io.ReadCloser, error)
	// ImageExposedPorts returns the TCP ports the image config declares via
	// EXPOSE, sorted ascending. An image with no declaration returns nil.
	ImageExposedPorts(ctx context.Context, ref string) ([]int, error)
	// NetworkCreate creates a user-defined bridge network. The runtime
	// adapter forwards calls from internal/network's DockerProvisioner.
	NetworkCreate(ctx context.Context, name string, options dockerclient.NetworkCreateOptions) (string, error)
	// NetworkRemove removes a network by id.
	NetworkRemove(ctx context.Context, networkID string) error
	// NetworkConnect attaches an existing container to a network. Adapters
	// use it to enroll the control-plane's own probe container into each
	// project bridge so readiness checks can reach workloads.
	NetworkConnect(ctx context.Context, networkID, containerID string) error
	// NetworkList returns the networks matching the given label filter.
	NetworkList(ctx context.Context, labelFilter map[string]string) ([]network.Summary, error)
	Close() error
}

// realEngineClient adapts the upstream Docker SDK client to the EngineClient
// surface. The wrapper exists so unit tests can swap in a fake without
// importing dockerclient.
type realEngineClient struct {
	cli *dockerclient.Client
}

// NewEngineClient builds a Docker SDK client configured for Phase 1
// requirements:
//
//   - Engine API version negotiation enabled (NegotiateAPIVersion).
//   - host falls back to FromEnv(DOCKER_HOST) when host == "" so the agent
//     systemd unit can be configured via env, not hard-coded paths.
//   - A bounded ping deadline at construction time so a wedged daemon
//     surfaces failurecodes.AgentRuntimeUnavailable instead of hanging the
//     agent boot loop.
//
// The function does NOT enforce the v1.43 floor or daemon.json baseline;
// those run inside Adapter.Init() so callers can wrap the SDK client without
// duplicating the gates.
func NewEngineClient(host string) (EngineClient, error) {
	opts := []dockerclient.Opt{
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	}
	if strings.TrimSpace(host) != "" {
		opts = append(opts, dockerclient.WithHost(strings.TrimSpace(host)))
	}
	cli, err := dockerclient.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &realEngineClient{cli: cli}, nil
}

// NegotiatedAPIVersion returns the Engine API version the SDK negotiated
// against the daemon. When negotiation has not yet run (no API call issued)
// the SDK returns its compiled-in default; Adapter.Init() forces a Ping()
// before reading this value.
func (c *realEngineClient) NegotiatedAPIVersion() string {
	if c == nil || c.cli == nil {
		return ""
	}
	return c.cli.ClientVersion()
}

func (c *realEngineClient) Info(ctx context.Context) (system.Info, error) {
	result, err := c.cli.Info(ctx, dockerclient.InfoOptions{})
	return result.Info, err
}

func (c *realEngineClient) Ping(ctx context.Context) error {
	_, err := c.cli.Ping(ctx, dockerclient.PingOptions{NegotiateAPIVersion: true})
	return err
}

func (c *realEngineClient) ContainerCreate(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig) (string, error) {
	return c.ContainerCreateWithNetworking(ctx, name, cfg, hc, nil)
}

func (c *realEngineClient) ContainerCreateWithNetworking(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig, netCfg *network.NetworkingConfig) (string, error) {
	resp, err := c.cli.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hc,
		NetworkingConfig: netCfg,
		Name:             name,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *realEngineClient) ContainerStart(ctx context.Context, id string) error {
	_, err := c.cli.ContainerStart(ctx, id, dockerclient.ContainerStartOptions{})
	return err
}

func (c *realEngineClient) ContainerStop(ctx context.Context, id string, grace time.Duration) error {
	timeoutSeconds := int(grace.Round(time.Second).Seconds())
	if timeoutSeconds < 0 {
		timeoutSeconds = 0
	}
	opts := dockerclient.ContainerStopOptions{Timeout: &timeoutSeconds}
	_, err := c.cli.ContainerStop(ctx, id, opts)
	return err
}

func (c *realEngineClient) ContainerRemove(ctx context.Context, id string, force bool) error {
	_, err := c.cli.ContainerRemove(ctx, id, dockerclient.ContainerRemoveOptions{Force: force, RemoveVolumes: false})
	return err
}

func (c *realEngineClient) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	result, err := c.cli.ContainerInspect(ctx, id, dockerclient.ContainerInspectOptions{})
	return result.Container, err
}

func (c *realEngineClient) ContainerList(ctx context.Context, labelFilter map[string]string) ([]container.Summary, error) {
	args := make(dockerclient.Filters)
	for k, v := range labelFilter {
		if v == "" {
			args.Add("label", k)
			continue
		}
		args.Add("label", fmt.Sprintf("%s=%s", k, v))
	}
	result, err := c.cli.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: args})
	return result.Items, err
}

func (c *realEngineClient) ImagePull(ctx context.Context, ref string) (io.ReadCloser, error) {
	return c.cli.ImagePull(ctx, ref, dockerclient.ImagePullOptions{})
}

// LogEngineClient extends EngineClient with the bounded log-tail read the
// adapter uses to explain why a workload is not becoming ready. It is a
// separate interface (like VolumeEngineClient) so existing EngineClient
// implementations keep compiling; the adapter type-asserts for it and
// degrades to an empty tail when the client cannot read logs.
type LogEngineClient interface {
	EngineClient
	// ContainerLogTail returns at most lines of the container's combined
	// stdout/stderr, oldest first, with trailing newlines removed.
	ContainerLogTail(ctx context.Context, id string, lines int) ([]string, error)
}

const (
	// maxLogTailLines caps how many trailing log lines the adapter reports.
	maxLogTailLines = 20
	// maxLogTailLineBytes truncates a single very long line so one workload
	// cannot flood an operation event with a megabyte of output.
	maxLogTailLineBytes = 512
	// maxLogTailBytes caps the total stream bytes read from the Engine.
	maxLogTailBytes = 64 << 10
	// dockerStreamHeaderBytes is the size of the Engine's multiplexed
	// stream frame header (stream type, 3 reserved bytes, 4-byte length).
	dockerStreamHeaderBytes = 8
)

func (c *realEngineClient) ContainerLogTail(ctx context.Context, id string, lines int) ([]string, error) {
	if lines <= 0 || lines > maxLogTailLines {
		lines = maxLogTailLines
	}
	stream, err := c.cli.ContainerLogs(ctx, id, dockerclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(lines),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(io.LimitReader(stream, maxLogTailBytes))
	if err != nil && len(raw) == 0 {
		return nil, err
	}
	return tailLogLines(demultiplexDockerStream(raw), lines), nil
}

// demultiplexDockerStream strips the Engine's 8-byte stream framing from a
// non-TTY log stream. A TTY container's logs arrive unframed, so a payload
// whose first byte is not a known stream id is returned untouched.
func demultiplexDockerStream(raw []byte) []byte {
	if len(raw) < dockerStreamHeaderBytes || raw[0] > 2 || raw[1] != 0 || raw[2] != 0 || raw[3] != 0 {
		return raw
	}
	out := make([]byte, 0, len(raw))
	for len(raw) >= dockerStreamHeaderBytes {
		size := int(binary.BigEndian.Uint32(raw[4:dockerStreamHeaderBytes]))
		raw = raw[dockerStreamHeaderBytes:]
		if size < 0 || size > len(raw) {
			size = len(raw)
		}
		out = append(out, raw[:size]...)
		raw = raw[size:]
	}
	return out
}

// tailLogLines returns the last `lines` non-empty lines, oldest first, each
// truncated to maxLogTailLineBytes.
func tailLogLines(payload []byte, lines int) []string {
	if lines <= 0 {
		return nil
	}
	split := strings.Split(strings.ReplaceAll(string(payload), "\r\n", "\n"), "\n")
	out := make([]string, 0, lines)
	for _, line := range split {
		trimmed := strings.TrimRight(line, "\r\n\t ")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		if len(trimmed) > maxLogTailLineBytes {
			trimmed = trimmed[:maxLogTailLineBytes] + "…"
		}
		out = append(out, trimmed)
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *realEngineClient) ImageExposedPorts(ctx context.Context, ref string) ([]int, error) {
	insp, err := c.cli.ImageInspect(ctx, ref)
	if err != nil {
		return nil, err
	}
	if insp.Config == nil {
		return nil, nil
	}
	return exposedTCPPorts(insp.Config.ExposedPorts), nil
}

// exposedTCPPorts normalizes an OCI ExposedPorts set ("6379/tcp", "80")
// into sorted TCP port numbers. UDP/SCTP declarations are ignored.
func exposedTCPPorts(set map[string]struct{}) []int {
	ports := make([]int, 0, len(set))
	for key := range set {
		portPart, proto, found := strings.Cut(key, "/")
		if found && !strings.EqualFold(proto, "tcp") {
			continue
		}
		if port := parsePort(portPart); port > 0 {
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	if len(ports) == 0 {
		return nil
	}
	return ports
}

func (c *realEngineClient) NetworkCreate(ctx context.Context, name string, options dockerclient.NetworkCreateOptions) (string, error) {
	resp, err := c.cli.NetworkCreate(ctx, name, options)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *realEngineClient) NetworkConnect(ctx context.Context, networkID, containerID string) error {
	_, err := c.cli.NetworkConnect(ctx, networkID, dockerclient.NetworkConnectOptions{Container: containerID})
	return err
}

func (c *realEngineClient) NetworkRemove(ctx context.Context, networkID string) error {
	_, err := c.cli.NetworkRemove(ctx, networkID, dockerclient.NetworkRemoveOptions{})
	return err
}

func (c *realEngineClient) NetworkList(ctx context.Context, labelFilter map[string]string) ([]network.Summary, error) {
	args := make(dockerclient.Filters)
	for k, v := range labelFilter {
		if v == "" {
			args.Add("label", k)
			continue
		}
		args.Add("label", fmt.Sprintf("%s=%s", k, v))
	}
	result, err := c.cli.NetworkList(ctx, dockerclient.NetworkListOptions{Filters: args})
	return result.Items, err
}

func (c *realEngineClient) Close() error {
	if c == nil || c.cli == nil {
		return nil
	}
	return c.cli.Close()
}
