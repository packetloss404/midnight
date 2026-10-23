package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/strslice"
	dockerclient "github.com/moby/moby/client"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/network"
	"github.com/midnight/midnight/internal/runtime"
)

// AdapterName is reported via Capabilities().Name and recorded on every
// agent registration / heartbeat payload so server-side observability can
// distinguish Phase 1 Docker hosts from the in-memory local adapter.
const AdapterName = "docker"

// AdapterVersion identifies the Phase 1 release shape. Bump when the
// container schema, label set, or naming changes; the conformance suite
// captures the value in its report so cross-runtime drift is visible.
const AdapterVersion = "phase1"

const defaultInternalPort = 8080

var internalPortLabelKeys = []string{
	"midnight.internal_port",
	"midnight.target_port",
	"midnight.port",
}

// Config wires construction-time options for the Docker adapter.
type Config struct {
	// AgentID is recorded in the canonical label set so reconcile can
	// identify orphaned containers.
	AgentID string
	// HostID is recorded in the canonical label set; matches the row in
	// internal/db hosts table.
	HostID string
	// Engine is the EngineClient seam. Tests inject a fake; production
	// wires NewEngineClient(...).
	Engine EngineClient
	// NetworkProvisioner is the per-(project, environment) bridge
	// provisioner. When nil, the adapter constructs the default
	// DockerProvisioner backed by Engine.
	NetworkProvisioner network.Provisioner
	// CGroupMode overrides the cgroup detection (testing only). When the
	// zero value is passed the adapter calls DetectCGroupMode().
	CGroupMode CGroupMode
	// ProbeContainer names the container the control plane itself runs in
	// (compose/dev stacks). When set, the adapter attaches it to every
	// project/environment bridge it converges so readiness probes and
	// routing checks can reach workloads. Installed hosts run the worker on
	// the host network and leave this empty.
	ProbeContainer string
	// BaselineMode selects whether daemon.json baseline conflicts abort boot
	// (BaselineEnforce, the zero-value default) or are downgraded to report
	// warnings (BaselineWarn, for developer engines such as Docker Desktop).
	// cgroup v2 and the Engine API floor are always enforced.
	BaselineMode BaselineMode
	// Now is the clock seam used for ObservedAt timestamps. nil falls back
	// to time.Now().UTC().
	Now func() time.Time
	// StopGrace is the default SIGTERM grace before SIGKILL fallback.
	StopGrace time.Duration
}

// Adapter implements runtime.RuntimeAdapter against the Docker Engine API.
//
// The adapter holds in-memory state for two reasons:
//   - Mapping (deployment_id, instance_id) → container_id without re-listing
//     every reconcile pass.
//   - Recording the IdempotencyKey so Schedule(...) is idempotent on retry
//     even when the underlying Engine API call already created the
//     container.
//
// Persistent state lives on the control plane (Lane 1's resource_store and
// Lane 1's deployment_instances table); this map is a hot-path cache that
// the agent rebuilds from `ContainerList(label_filter)` on boot.
type Adapter struct {
	cfg           Config
	client        EngineClient
	provisioner   network.Provisioner
	probeNetworks map[string]struct{}
	mu            sync.Mutex
	records       map[string]*record
	networks      map[string]runtime.NetworkPolicy
	now           func() time.Time
	// verifier is the optional SignatureVerifier wired through
	// Adapter.EnableSignatureEnforcement when the agent config sets
	// verify_signatures=true. Phase 1 default is nil (existing
	// behavior); see internal/runtime/docker/signature_verify.go.
	verifier SignatureVerifier
}

type record struct {
	deploymentID   string
	instanceID     string
	containerID    string
	containerName  string
	idempotencyKey string
	scheduleReq    runtime.ScheduleRequest
	state          runtime.InstanceState
	observedAt     time.Time
	reason         string
}

// New constructs the Phase 1 Docker adapter and runs the Init() boot gates
// (cgroup v2, Engine API floor, daemon.json baseline). On failure the
// returned error is a typed *Error carrying one of:
//
//   - failurecodes.CGroupV2Required
//   - failurecodes.DockerEngineUnsupported
//   - failurecodes.DockerDaemonBaselineConflict
//   - failurecodes.AgentRuntimeUnavailable
//
// Callers (cmd/midnight-agent) MUST surface the typed code without
// rewrapping.
func New(ctx context.Context, cfg Config) (*Adapter, BaselineReport, error) {
	if cfg.Engine == nil {
		return nil, BaselineReport{}, fmt.Errorf("docker adapter: engine client is required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = 30 * time.Second
	}
	provisioner := cfg.NetworkProvisioner
	if provisioner == nil {
		p, err := network.NewDockerProvisioner(engineNetworkClient{client: cfg.Engine})
		if err != nil {
			return nil, BaselineReport{}, fmt.Errorf("docker adapter: build network provisioner: %w", err)
		}
		provisioner = p
	}
	a := &Adapter{
		cfg:         cfg,
		client:      cfg.Engine,
		provisioner: provisioner,
		now:         cfg.Now,
		records:     make(map[string]*record),
		networks:    make(map[string]runtime.NetworkPolicy),
	}
	report, err := a.boot(ctx, cfg.CGroupMode, cfg.BaselineMode)
	if err != nil {
		return nil, report, err
	}
	// Best-effort: warm the provisioner cache from existing daemon state.
	// A failure here is not fatal — EnsureNetwork will rediscover lazily
	// on the first schedule.
	if a.provisioner != nil {
		_, _ = a.provisioner.ListManagedNetworks(ctx)
	}
	return a, report, nil
}

// engineNetworkClient adapts the runtime adapter's EngineClient to the
// narrow DockerNetworkClient surface the network package's provisioner
// needs. This indirection avoids a cycle: the network package cannot
// import this package because this package imports the network package.
type engineNetworkClient struct {
	client EngineClient
}

func (e engineNetworkClient) NetworkCreate(ctx context.Context, name string, options dockerclient.NetworkCreateOptions) (string, error) {
	return e.client.NetworkCreate(ctx, name, options)
}

func (e engineNetworkClient) NetworkRemove(ctx context.Context, networkID string) error {
	return e.client.NetworkRemove(ctx, networkID)
}

func (e engineNetworkClient) NetworkList(ctx context.Context, labelFilter map[string]string) ([]dockernetwork.Summary, error) {
	return e.client.NetworkList(ctx, labelFilter)
}

// boot runs the cgroup v2 / Engine floor / daemon.json baseline checks.
func (a *Adapter) boot(ctx context.Context, mode CGroupMode, baseline BaselineMode) (BaselineReport, error) {
	if err := a.client.Ping(ctx); err != nil {
		return BaselineReport{}, newError(
			failurecodes.AgentRuntimeUnavailable,
			"docker engine ping failed",
			err.Error(),
		)
	}

	if err := EnforceEngineAPIFloor(a.client.NegotiatedAPIVersion()); err != nil {
		return BaselineReport{}, err
	}
	report, err := CheckBaseline(ctx, a.client)
	if err != nil {
		return BaselineReport{}, newError(
			failurecodes.AgentRuntimeUnavailable,
			"docker /info failed",
			err.Error(),
		)
	}
	if err := ensureRuntimeCGroupV2(mode, report); err != nil {
		return report, err
	}
	if err := EnforceBaseline(report); err != nil {
		if baseline != BaselineWarn {
			return report, err
		}
		for _, c := range report.Conflicts {
			report.Warnings = append(report.Warnings, fmt.Sprintf("daemon.json baseline conflict tolerated (mode=warn): %s want=%q got=%q", c.Key, c.Required, c.Observed))
		}
	}
	return report, nil
}

func ensureRuntimeCGroupV2(override CGroupMode, report BaselineReport) error {
	if override != "" {
		return EnsureCGroupV2(override)
	}
	if mode := cgroupModeFromEngineInfo(report.CGroupVersion); mode != CGroupModeUnknown {
		return EnsureCGroupV2(mode)
	}
	detected, err := DetectCGroupMode()
	if err != nil {
		return fmt.Errorf("detect cgroup: %w", err)
	}
	return EnsureCGroupV2(detected)
}

func cgroupModeFromEngineInfo(version string) CGroupMode {
	switch strings.TrimSpace(version) {
	case "2":
		return CGroupModeV2
	case "1":
		return CGroupModeV1
	case "":
		return CGroupModeUnknown
	default:
		return CGroupModeUnknown
	}
}

// Capabilities returns the adapter's declared feature set per ADR-015.
func (a *Adapter) Capabilities() runtime.AdapterCapabilities {
	a.mu.Lock()
	signatureEnforce := a.verifier != nil
	a.mu.Unlock()
	return runtime.AdapterCapabilities{
		Name:                AdapterName,
		Version:             AdapterVersion,
		Major:               runtime.AdapterMajorVersion,
		IdempotentSchedule:  true,
		NetworkPolicy:       true,
		SignatureEnforce:    signatureEnforce,
		ConformanceVerified: false,
	}
}

// Schedule creates the container if it doesn't exist. The call is
// idempotent on the IdempotencyKey: a second call with the same key returns
// the existing record without touching the Docker daemon.
func (a *Adapter) Schedule(ctx context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	if err := ctx.Err(); err != nil {
		return runtime.InstanceStatus{}, err
	}
	if err := validateScheduleRequest(req); err != nil {
		return runtime.InstanceStatus{}, err
	}

	a.mu.Lock()
	key := instanceKey(req.DeploymentID, req.InstanceID)
	rec, ok := a.records[key]
	if ok {
		if rec.idempotencyKey == req.IdempotencyKey {
			status := a.statusOf(rec)
			containerID := rec.containerID
			a.mu.Unlock()
			if err := a.verifyExistingScheduleMounts(ctx, containerID, req.Mounts); err != nil {
				return runtime.InstanceStatus{}, err
			}
			return status, nil
		}
	}
	a.mu.Unlock()

	req.TargetPort = a.resolveTargetPort(ctx, req)
	containerName := canonicalContainerName(req)
	cfg := HardenedContainerConfig(req.Image, mergeLabels(req, a.cfg))
	// Auto-inject MIDNIGHT_* built-ins BEFORE user-supplied env so user
	// vars override (Docker honors last occurrence). PORT is appended
	// after both by applyRoutingPortHint so it cannot be silently
	// overridden by user env, preserving the prior contract.
	cfg.Env = applyEnvWithBuiltins(req, containerName)
	applyStartCommand(cfg, req)
	applyRoutingPortHint(cfg, req)
	hc := HardenedHostConfig()
	applyRestartPolicy(hc, req)
	applyScheduleMounts(hc, req)
	netCfg, err := a.buildNetworkingConfig(ctx, req)
	if err != nil {
		return runtime.InstanceStatus{}, err
	}

	id, err := a.client.ContainerCreateWithNetworking(ctx, containerName, cfg, hc, netCfg)
	if err != nil {
		// If the container already exists with the same name, treat it as
		// the prior schedule being durable and re-attach our record. The
		// reconciler will validate ownership labels.
		if isNameInUse(err) {
			existing, lookupErr := a.client.ContainerInspect(ctx, containerName)
			if lookupErr == nil {
				labels := map[string]string{}
				if existing.Config != nil {
					labels = existing.Config.Labels
				}
				if !ownedByMidnight(labels) {
					return runtime.InstanceStatus{}, newError(
						failurecodes.ContainerNameConflict,
						"container name already in use by an unmanaged process",
						containerName,
					)
				}
				id = existing.ID
				if err := a.verifyExistingScheduleMounts(ctx, id, req.Mounts); err != nil {
					return runtime.InstanceStatus{}, err
				}
			} else {
				return runtime.InstanceStatus{}, newError(
					failurecodes.ContainerNameConflict,
					"container name conflict and inspect failed",
					lookupErr.Error(),
				)
			}
		} else {
			return runtime.InstanceStatus{}, fmt.Errorf("container create: %w", err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	r := &record{
		deploymentID:   req.DeploymentID,
		instanceID:     req.InstanceID,
		containerID:    id,
		containerName:  containerName,
		idempotencyKey: req.IdempotencyKey,
		scheduleReq:    req,
		state:          runtime.InstanceStateAllocated,
		observedAt:     a.now(),
		reason:         req.IdempotencyKey,
	}
	a.records[key] = r
	return a.statusOf(r), nil
}

func (a *Adapter) verifyExistingScheduleMounts(ctx context.Context, containerID string, requested []runtime.ScheduleMount) error {
	inspect, err := a.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return newError(failurecodes.AgentRuntimeUnavailable, "inspect idempotent container mounts failed", err.Error())
	}
	observed := make(map[string]string)
	for _, item := range inspect.Mounts {
		if item.Type == mount.TypeVolume && strings.HasPrefix(item.Name, "midnight-") {
			observed[item.Destination] = item.Name
		}
	}
	if len(observed) != len(requested) {
		return newError(
			failurecodes.ContainerSpecInvalid,
			"idempotent schedule volume mounts differ from existing container",
			containerID,
		)
	}
	for _, item := range requested {
		if observed[item.Target] != item.Source {
			return newError(
				failurecodes.ContainerSpecInvalid,
				"idempotent schedule volume mount differs from existing container",
				item.Target,
			)
		}
	}
	return nil
}

// Start transitions the container to running.
func (a *Adapter) Start(ctx context.Context, deploymentID, instanceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	rec, ok := a.records[instanceKey(deploymentID, instanceID)]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("docker adapter: unknown instance %s/%s", deploymentID, instanceID)
	}
	if err := a.client.ContainerStart(ctx, rec.containerID); err != nil {
		// Idempotent: starting an already-running container is not an
		// error we want to surface to the reconciler.
		if !isNotModified(err) {
			return fmt.Errorf("container start: %w", err)
		}
	}
	a.mu.Lock()
	rec.state = runtime.InstanceStateRunning
	rec.observedAt = a.now()
	a.mu.Unlock()
	return nil
}

// Stop initiates SIGTERM-then-SIGKILL drain on the container.
func (a *Adapter) Stop(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if grace <= 0 {
		grace = a.cfg.StopGrace
	}
	a.mu.Lock()
	rec, ok := a.records[instanceKey(deploymentID, instanceID)]
	a.mu.Unlock()
	if !ok {
		// Stop is idempotent on missing instances — this is the post-Remove
		// reconcile shape.
		return nil
	}
	if err := a.client.ContainerStop(ctx, rec.containerID, grace); err != nil {
		return fmt.Errorf("container stop: %w", err)
	}
	a.mu.Lock()
	rec.state = runtime.InstanceStateStopped
	rec.observedAt = a.now()
	a.mu.Unlock()
	return nil
}

// Remove releases all adapter resources for the instance.
func (a *Adapter) Remove(ctx context.Context, deploymentID, instanceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := instanceKey(deploymentID, instanceID)
	a.mu.Lock()
	rec, ok := a.records[key]
	a.mu.Unlock()
	if !ok {
		return nil
	}
	if err := a.client.ContainerRemove(ctx, rec.containerID, true); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("container remove: %w", err)
		}
	}
	a.mu.Lock()
	delete(a.records, key)
	a.mu.Unlock()
	return nil
}

// Drain marks the instance as draining and removes it from active routing.
func (a *Adapter) Drain(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	rec, ok := a.records[instanceKey(deploymentID, instanceID)]
	if ok {
		rec.state = runtime.InstanceStateDraining
		rec.observedAt = a.now()
	}
	a.mu.Unlock()
	if !ok {
		return nil
	}
	return a.Stop(ctx, deploymentID, instanceID, grace)
}

// DeploymentInstanceIDs returns every locally observed instance for a
// deployment. Reconcile rebuilds records from labels after worker restart, so
// runtime actions can fan out across stateless replicas without trusting an
// in-memory-only allocation.
func (a *Adapter) DeploymentInstanceIDs(_ context.Context, deploymentID string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0)
	for _, record := range a.records {
		if record.deploymentID == deploymentID {
			ids = append(ids, record.instanceID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// StreamInstanceStatus returns the latest observed status for every adapter-
// managed instance. Status is sorted by (deployment_id, instance_id) so
// downstream consumers see a deterministic order.
func (a *Adapter) StreamInstanceStatus(ctx context.Context) ([]runtime.InstanceStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	keys := make([]string, 0, len(a.records))
	for k := range a.records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]runtime.InstanceStatus, 0, len(keys))
	for _, k := range keys {
		out = append(out, a.statusOf(a.records[k]))
	}
	a.mu.Unlock()
	return out, nil
}

// Reconcile compares the adapter's recorded desired state against the live
// Docker container set, fixing observed state and detecting drift. The pass
// also re-seeds the network provisioner cache so per-(project, environment)
// networks discovered after a process restart are honored without re-create.
func (a *Adapter) Reconcile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.provisioner != nil {
		// Re-seed the cache from the daemon's view. A failure here is not
		// fatal — EnsureNetwork rediscovers lazily on the next schedule.
		_, _ = a.provisioner.ListManagedNetworks(ctx)
	}
	live, err := a.client.ContainerList(ctx, map[string]string{
		LabelManagedBy: LabelManagedByV,
	})
	if err != nil {
		return newError(
			failurecodes.AgentRuntimeUnavailable,
			"docker container list failed",
			err.Error(),
		)
	}

	observedByKey := make(map[string]container.Summary, len(live))
	for _, c := range live {
		if c.Labels[LabelWorkloadKind] == WorkloadKindPredeploy ||
			c.Labels[LabelWorkloadKind] == WorkloadKindCron ||
			c.Labels[LabelWorkloadKind] == WorkloadKindTemplateSmoke {
			continue
		}
		key, ok := instanceKeyFromLabels(c.Labels)
		if !ok {
			continue
		}
		observedByKey[key] = c
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for key, live := range observedByKey {
		if _, ok := a.records[key]; ok {
			continue
		}
		a.records[key] = recordFromContainerSummary(live, now)
	}
	for key, rec := range a.records {
		live, ok := observedByKey[key]
		if !ok {
			rec.state = runtime.InstanceStateLost
			rec.observedAt = now
			rec.reason = "container missing from docker engine"
			continue
		}
		rec.containerID = live.ID
		rec.state = mapDockerSummaryToInstanceState(string(live.State), live.Status)
		rec.observedAt = now
		rec.reason = live.Status
	}
	// Ensure a per-(project, environment) network exists for every active
	// record. This is the "ensure on first deploy after process restart"
	// behavior promised in the deliverable: ListManagedNetworks above
	// seeds the cache; this loop catches any tenant whose network was
	// removed out-of-band.
	if a.provisioner != nil {
		seen := make(map[string]struct{}, len(a.records))
		for _, rec := range a.records {
			if rec == nil {
				continue
			}
			projectID := rec.scheduleReq.ProjectID
			envID := rec.scheduleReq.EnvironmentID
			if projectID == "" || envID == "" {
				continue
			}
			key := projectID + "|" + envID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			_, _ = a.provisioner.EnsureNetwork(ctx, projectID, envID)
		}
	}
	return nil
}

// ObserveVolumeAttachments returns only mounts proven on running,
// Midnight-managed containers. Stopped/missing containers do not own a writer
// lease and are omitted so the durable reconciler can mark them detached.
func (a *Adapter) ObserveVolumeAttachments(ctx context.Context) ([]runtime.ObservedVolumeAttachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	containers, err := a.client.ContainerList(ctx, map[string]string{
		LabelManagedBy: LabelManagedByV,
	})
	if err != nil {
		return nil, newError(failurecodes.AgentRuntimeUnavailable, "docker container list failed", err.Error())
	}
	out := make([]runtime.ObservedVolumeAttachment, 0)
	for _, summary := range containers {
		inspect, err := a.client.ContainerInspect(ctx, summary.ID)
		if err != nil {
			return nil, newError(failurecodes.AgentRuntimeUnavailable, "docker container inspect failed", err.Error())
		}
		if inspect.State == nil || !inspect.State.Running || inspect.Config == nil {
			continue
		}
		labels := inspect.Config.Labels
		for _, item := range inspect.Mounts {
			if item.Type != mount.TypeVolume || !strings.HasPrefix(item.Name, "midnight-vol_") {
				continue
			}
			out = append(out, runtime.ObservedVolumeAttachment{
				VolumeID:      strings.TrimPrefix(item.Name, "midnight-"),
				DeploymentID:  labels[LabelDeployment],
				InstanceID:    labels[LabelInstance],
				ProjectID:     labels[LabelProject],
				ServiceID:     labels[LabelService],
				EnvironmentID: labels[LabelEnvironment],
				Source:        item.Name,
				MountPath:     item.Destination,
				ReadOnly:      !item.RW,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VolumeID != out[j].VolumeID {
			return out[i].VolumeID < out[j].VolumeID
		}
		if out[i].DeploymentID != out[j].DeploymentID {
			return out[i].DeploymentID < out[j].DeploymentID
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out, nil
}

// ApplyNetworkPolicy records tenant ownership. Schedule converges the
// corresponding Docker bridge before creating a container, so the adapter can
// truthfully advertise the network-policy capability.
func (a *Adapter) ApplyNetworkPolicy(ctx context.Context, policy runtime.NetworkPolicy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(policy.BridgeName) == "" {
		return fmt.Errorf("docker adapter: network policy bridge name is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// We track tenancy in-memory so cross-tenant collisions are caught
	// without a Docker round-trip; the bridge is created during the host
	// agent's network-manager pass which is not in this lane's slice.
	if a.networks == nil {
		a.networks = make(map[string]runtime.NetworkPolicy)
	}
	if existing, ok := a.networks[policy.BridgeName]; ok {
		if existing.ProjectID != policy.ProjectID || existing.EnvironmentID != policy.EnvironmentID {
			return fmt.Errorf("docker adapter: bridge %q already bound to (%s,%s)", policy.BridgeName, existing.ProjectID, existing.EnvironmentID)
		}
	}
	a.networks[policy.BridgeName] = policy
	return nil
}

// ResolveRoutingTarget returns the routable target for one instance.
func (a *Adapter) ResolveRoutingTarget(ctx context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	if err := ctx.Err(); err != nil {
		return runtime.RoutingTarget{}, err
	}
	key := instanceKey(deploymentID, instanceID)
	a.mu.Lock()
	rec, ok := a.records[key]
	a.mu.Unlock()
	if !ok {
		return runtime.RoutingTarget{}, fmt.Errorf("docker adapter: no instance %s/%s", deploymentID, instanceID)
	}
	insp, err := a.client.ContainerInspect(ctx, rec.containerID)
	if err != nil {
		if isNotFound(err) {
			return runtime.RoutingTarget{}, fmt.Errorf("docker adapter: instance gone")
		}
		return runtime.RoutingTarget{}, fmt.Errorf("container inspect: %w", err)
	}
	labels := map[string]string{}
	if insp.Config != nil && insp.Config.Labels != nil {
		labels = insp.Config.Labels
	}
	hostID := firstNonEmpty(
		labels[LabelHostID],
		labels["midnight.host"],
		rec.scheduleReq.Labels[LabelHostID],
		rec.scheduleReq.Labels["midnight.host"],
		a.cfg.HostID,
	)
	containerID := firstNonEmpty(insp.ID, rec.containerID)
	readiness := dockerInspectReadiness(insp)
	observedAt := a.now()
	target := runtime.RoutingTarget{
		DeploymentID:    deploymentID,
		InstanceID:      instanceID,
		HostID:          hostID,
		RoutingTargetID: containerID,
		ContainerID:     containerID,
		ContainerIP:     primaryIP(insp),
		InternalPort:    internalPort(insp, rec.scheduleReq),
		ProjectID:       rec.scheduleReq.ProjectID,
		EnvironmentID:   rec.scheduleReq.EnvironmentID,
		Ready:           readiness.ready,
		ObservedAt:      observedAt,
	}
	a.mu.Lock()
	if current, ok := a.records[key]; ok && current.containerID == rec.containerID {
		current.state = readiness.state
		current.observedAt = observedAt
		current.reason = readiness.reason
	}
	a.mu.Unlock()
	return target, nil
}

// AttachVolume reports unsupported until Docker volume attach/detach is wired.
func (a *Adapter) AttachVolume(ctx context.Context, attach runtime.VolumeAttachment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return runtime.UnsupportedOperationError{
		Adapter:   AdapterName,
		Operation: "AttachVolume",
		Reason:    "Docker volume attach/detach is not implemented",
	}
}

// DetachVolume reports unsupported until Docker volume attach/detach is wired.
func (a *Adapter) DetachVolume(ctx context.Context, deploymentID, instanceID, volumeName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return runtime.UnsupportedOperationError{
		Adapter:   AdapterName,
		Operation: "DetachVolume",
		Reason:    "Docker volume attach/detach is not implemented",
	}
}

// PullVerify verifies a candidate image before pulling it. Without an
// injected verifier, the adapter refuses before ImagePull with
// IMAGE_SIGNATURE_REQUIRED so the default worker path fails closed. With a
// verifier configured, signature failure also returns before ImagePull,
// keeping the existing running version untouched by the failed candidate.
func (a *Adapter) PullVerify(ctx context.Context, image string) (runtime.PullVerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.PullVerifyResult{}, err
	}
	if a.client == nil {
		return runtime.PullVerifyResult{}, fmt.Errorf("docker adapter: no engine client")
	}
	image = strings.TrimSpace(image)
	a.mu.Lock()
	verifier := a.verifier
	a.mu.Unlock()
	verified := false
	trusted := false
	if verifier == nil {
		err := newError(
			failurecodes.ImageSignatureRequired,
			"no image trust policy configured",
			"configure cosign verification or a trusted-image allowlist before deploy",
		)
		return runtime.PullVerifyResult{
			Image:    image,
			Verified: false,
			Trusted:  false,
			Reason:   err.Error(),
		}, err
	}
	if verifier != nil {
		if err := verifier.Verify(ctx, image); err != nil {
			return runtime.PullVerifyResult{
				Image:    image,
				Verified: false,
				Trusted:  false,
				Reason:   err.Error(),
			}, err
		}
		verified = true
		trusted = true
	}
	rc, err := a.client.ImagePull(ctx, image)
	if err != nil {
		err = newError(failurecodes.AgentRuntimeUnavailable, "docker image pull failed", err.Error())
		return runtime.PullVerifyResult{
			Image:    image,
			Verified: verified,
			Trusted:  trusted,
			Reason:   err.Error(),
		}, err
	}
	if rc != nil {
		// Drain the pull progress so the daemon completes the layer
		// download before we report success.
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
	return runtime.PullVerifyResult{
		Image:    image,
		Verified: verified,
		Trusted:  trusted,
		Reason:   "signature verified before pull",
	}, nil
}

func (a *Adapter) statusOf(rec *record) runtime.InstanceStatus {
	return runtime.InstanceStatus{
		DeploymentID: rec.deploymentID,
		InstanceID:   rec.instanceID,
		State:        rec.state,
		ObservedAt:   rec.observedAt,
		Reason:       rec.reason,
	}
}

// container/instance bookkeeping helpers --------------------------------

func instanceKey(deploymentID, instanceID string) string {
	return deploymentID + "/" + instanceID
}

func canonicalContainerName(req runtime.ScheduleRequest) string {
	depSeq := SequenceFromInstanceID(req.DeploymentID)
	instSeq := SequenceFromInstanceID(req.InstanceID)
	return ContainerName(req.ServiceID, depSeq, instSeq)
}

func instanceKeyFromLabels(labels map[string]string) (string, bool) {
	deploymentID := strings.TrimSpace(labels[LabelDeployment])
	instanceID := strings.TrimSpace(labels[LabelInstance])
	if deploymentID == "" || instanceID == "" {
		return "", false
	}
	return instanceKey(deploymentID, instanceID), true
}

func recordFromContainerSummary(c container.Summary, observedAt time.Time) *record {
	labels := copyStringMap(c.Labels)
	req := runtime.ScheduleRequest{
		DeploymentID:   labels[LabelDeployment],
		InstanceID:     labels[LabelInstance],
		ProjectID:      labels[LabelProject],
		EnvironmentID:  labels[LabelEnvironment],
		ServiceID:      labels[LabelService],
		Image:          c.Image,
		Labels:         labels,
		IdempotencyKey: labels[LabelIdempotency],
	}
	return &record{
		deploymentID:   req.DeploymentID,
		instanceID:     req.InstanceID,
		containerID:    c.ID,
		containerName:  summaryContainerName(c, req),
		idempotencyKey: req.IdempotencyKey,
		scheduleReq:    req,
		state:          mapDockerSummaryToInstanceState(string(c.State), c.Status),
		observedAt:     observedAt,
		reason:         c.Status,
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func summaryContainerName(c container.Summary, req runtime.ScheduleRequest) string {
	for _, name := range c.Names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		if name != "" {
			return name
		}
	}
	return canonicalContainerName(req)
}

func mergeLabels(req runtime.ScheduleRequest, cfg Config) map[string]string {
	set := LabelSet{
		ProjectID:      req.ProjectID,
		EnvironmentID:  req.EnvironmentID,
		ServiceID:      req.ServiceID,
		DeploymentID:   req.DeploymentID,
		InstanceID:     req.InstanceID,
		AgentID:        cfg.AgentID,
		HostID:         cfg.HostID,
		IdempotencyKey: req.IdempotencyKey,
		Extra:          req.Labels,
	}
	return set.AsMap()
}

func validateScheduleRequest(req runtime.ScheduleRequest) error {
	if strings.TrimSpace(req.DeploymentID) == "" || strings.TrimSpace(req.InstanceID) == "" {
		return fmt.Errorf("docker adapter: deployment and instance ids are required")
	}
	if strings.TrimSpace(req.ServiceID) == "" {
		return fmt.Errorf("docker adapter: service id is required for deterministic container naming")
	}
	if strings.TrimSpace(req.Image) == "" {
		return newError(failurecodes.ContainerSpecInvalid, "image reference is required", req.DeploymentID)
	}
	if req.TargetPort < 0 || req.TargetPort > 65535 {
		return newError(failurecodes.ContainerSpecInvalid, "target port is out of range", req.DeploymentID)
	}
	switch strings.TrimSpace(req.RestartPolicy) {
	case "", "no", "always", "unless-stopped", "on-failure":
	default:
		return newError(failurecodes.ContainerSpecInvalid, "restart policy is unsupported", req.DeploymentID)
	}
	if req.RestartMaxRetries < 0 || (req.RestartMaxRetries > 0 && req.RestartPolicy != "on-failure") {
		return newError(failurecodes.ContainerSpecInvalid, "restart retries require on-failure policy", req.DeploymentID)
	}
	if len(req.Mounts) > 0 && req.Replicas > 1 {
		return newError(
			failurecodes.VolumeReplicasUnsupported,
			"single-writer volume cannot be attached to multiple replicas",
			req.ServiceID,
		)
	}
	if err := runtime.ValidateSingleReplica(req.Replicas); err != nil {
		return newError(failurecodes.ContainerSpecInvalid, err.Error(), req.DeploymentID)
	}
	targets := make(map[string]struct{}, len(req.Mounts))
	for _, item := range req.Mounts {
		volumeID := strings.TrimSpace(item.VolumeID)
		source := strings.TrimSpace(item.Source)
		target := strings.TrimSpace(item.Target)
		if !item.IsVolume || volumeID == "" || source != "midnight-"+volumeID {
			return newError(
				failurecodes.ContainerSpecInvalid,
				"volume mount source does not match durable volume id",
				source,
			)
		}
		if target == "" || target == "/" || !strings.HasPrefix(target, "/") || path.Clean(target) != target || containsControl(target) {
			return newError(
				failurecodes.ContainerSpecInvalid,
				"volume mount target must be an absolute clean path",
				target,
			)
		}
		if _, duplicate := targets[target]; duplicate {
			return newError(
				failurecodes.ContainerSpecInvalid,
				"duplicate volume mount target",
				target,
			)
		}
		targets[target] = struct{}{}
	}
	for _, key := range RequiredLabelKeys() {
		if key == LabelManagedBy {
			continue // synthesized by the adapter
		}
		if _, ok := req.Labels[key]; !ok {
			// Labels are reasserted by mergeLabels; the validator only
			// rejects when the caller actively claims a wrong tenancy.
			continue
		}
	}
	return nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// applyEnvWithBuiltins returns the canonical env-var slice for a schedule
// request: MIDNIGHT_* built-ins first, then user-supplied entries. The
// Docker daemon honors the LAST occurrence of any duplicate key, so
// user entries override the built-ins (Railway precedence rule).
//
// The container name is used as the MIDNIGHT_REPLICA_ID default when the
// caller does not supply one explicitly. Today's single-replica
// scheduler is content with this; the multi-host scheduler will set
// it from the agent status payload directly.
func applyEnvWithBuiltins(req runtime.ScheduleRequest, containerName string) []string {
	builtins := network.BuildBuiltins(network.BuiltinsInput{
		ServiceSlug:     firstNonEmpty(req.ServiceSlug, req.ServiceID),
		ProjectID:       req.ProjectID,
		ProjectSlug:     firstNonEmpty(req.ProjectSlug, req.ProjectID),
		EnvironmentID:   req.EnvironmentID,
		EnvironmentName: firstNonEmpty(req.EnvironmentName, req.EnvironmentID),
		PublicDomain:    req.PublicDomain,
		ReplicaID:       firstNonEmpty(req.InstanceID, containerName),
		DeploymentID:    req.DeploymentID,
	})
	return network.MergeUserOverridesAfter(builtins, req.Env)
}

// buildNetworkingConfig returns the NetworkingConfig the adapter passes
// to ContainerCreate. The returned value attaches the container to the
// per-(project, environment) Docker bridge with two DNS aliases: the
// bare service slug and the {slug}.midnight.internal FQDN. Any inability to
// prove that boundary is a typed error; Midnight never falls back to Docker's
// default bridge.
func (a *Adapter) buildNetworkingConfig(ctx context.Context, req runtime.ScheduleRequest) (*dockernetwork.NetworkingConfig, error) {
	if a == nil || a.provisioner == nil {
		return nil, newError(
			failurecodes.PrivateNetworkUnavailable,
			"private network provisioner is unavailable",
			"project/environment isolation cannot be guaranteed",
		)
	}
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.EnvironmentID) == "" {
		return nil, newError(
			failurecodes.PrivateNetworkUnavailable,
			"project_id and environment_id are required for private networking",
			req.DeploymentID,
		)
	}
	networkID, err := a.provisioner.EnsureNetwork(ctx, req.ProjectID, req.EnvironmentID)
	if err != nil {
		return nil, newError(
			failurecodes.PrivateNetworkUnavailable,
			"private network convergence failed",
			err.Error(),
		)
	}
	if err := a.enrollProbeContainer(ctx, string(networkID)); err != nil {
		return nil, newError(
			failurecodes.PrivateNetworkUnavailable,
			"probe container could not join the environment network",
			err.Error(),
		)
	}
	pn := network.ProjectNetwork{ProjectID: req.ProjectID, EnvironmentID: req.EnvironmentID}
	slug := firstNonEmpty(req.ServiceSlug, req.ServiceID)
	aliases := network.ServiceAliases(slug)
	dnsNames := make([]string, 0, len(aliases)+1)
	dnsNames = append(dnsNames, aliases...)
	netName := pn.Name()
	return &dockernetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
			netName: {
				Aliases:  aliases,
				DNSNames: dnsNames,
			},
		},
	}, nil
}

// enrollProbeContainer attaches the configured probe container to the
// environment bridge. Docker reports an already-attached container as an
// error, which is treated as success so the call stays idempotent.
func (a *Adapter) enrollProbeContainer(ctx context.Context, networkID string) error {
	probe := strings.TrimSpace(a.cfg.ProbeContainer)
	if probe == "" || strings.TrimSpace(networkID) == "" {
		return nil
	}
	a.mu.Lock()
	if a.probeNetworks == nil {
		a.probeNetworks = map[string]struct{}{}
	}
	if _, done := a.probeNetworks[networkID]; done {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	if err := a.client.NetworkConnect(ctx, networkID, probe); err != nil && !isAlreadyConnected(err) {
		return err
	}
	a.mu.Lock()
	a.probeNetworks[networkID] = struct{}{}
	a.mu.Unlock()
	return nil
}

func isAlreadyConnected(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "already connected") || strings.Contains(msg, "already attached")
}

// applyScheduleMounts folds the (already materialized) volume mounts the
// ScheduleRequest carries into HostConfig.Mounts. Each entry is rendered
// as a Docker named-volume mount (Type=volume); bind-mounts are not
// emitted because Midnight only supports named volumes for persistence.
func applyScheduleMounts(hc *container.HostConfig, req runtime.ScheduleRequest) {
	if hc == nil || len(req.Mounts) == 0 {
		return
	}
	for _, m := range req.Mounts {
		source := strings.TrimSpace(m.Source)
		target := strings.TrimSpace(m.Target)
		hc.Mounts = append(hc.Mounts, mount.Mount{
			Type:     mount.TypeVolume,
			Source:   source,
			Target:   target,
			ReadOnly: m.ReadOnly,
		})
	}
}

// resolveTargetPort keeps an operator-chosen port untouched. When the port
// is only the platform default and the image declares exactly one TCP port
// that differs, the image wins: routing, readiness, and the injected PORT
// variable then agree with what the process actually listens on. Any
// inspect failure falls back to the default so scheduling never depends on
// this hint.
func (a *Adapter) resolveTargetPort(ctx context.Context, req runtime.ScheduleRequest) int {
	if !req.TargetPortInferred || a.client == nil {
		return req.TargetPort
	}
	ports, err := a.client.ImageExposedPorts(ctx, req.Image)
	if err != nil || len(ports) != 1 {
		return req.TargetPort
	}
	return ports[0]
}

func applyRoutingPortHint(cfg *container.Config, req runtime.ScheduleRequest) {
	if cfg == nil {
		return
	}
	port := req.TargetPort
	if port == 0 {
		port = internalPortFromLabels(req.Labels)
	}
	if port == 0 {
		port = defaultInternalPort
	}
	if cfg.ExposedPorts == nil {
		cfg.ExposedPorts = dockernetwork.PortSet{}
	}
	if p, err := dockernetwork.ParsePort(strconv.Itoa(port) + "/tcp"); err == nil {
		cfg.ExposedPorts[p] = struct{}{}
	}
	if !hasEnvKey(cfg.Env, "PORT") {
		cfg.Env = append(cfg.Env, "PORT="+strconv.Itoa(port))
	}
}

func internalPort(insp container.InspectResponse, req runtime.ScheduleRequest) int {
	if req.TargetPort > 0 {
		return req.TargetPort
	}
	if port := internalPortFromLabels(labelsFromInspect(insp)); port > 0 {
		return port
	}
	if port := internalPortFromLabels(req.Labels); port > 0 {
		return port
	}
	if port := firstExposedPort(insp); port > 0 {
		return port
	}
	if port := portFromEnv(insp); port > 0 {
		return port
	}
	return defaultInternalPort
}

func applyStartCommand(cfg *container.Config, req runtime.ScheduleRequest) {
	if cfg == nil || strings.TrimSpace(req.StartCommand) == "" {
		return
	}
	// Override the image entrypoint so the operator's configured command has
	// the same semantics for Dockerfile and Railpack images.
	cfg.Entrypoint = strslice.StrSlice{"/bin/sh", "-lc"}
	cfg.Cmd = strslice.StrSlice{req.StartCommand}
}

func applyRestartPolicy(hc *container.HostConfig, req runtime.ScheduleRequest) {
	if hc == nil || strings.TrimSpace(req.RestartPolicy) == "" {
		return
	}
	hc.RestartPolicy = container.RestartPolicy{
		Name:              container.RestartPolicyMode(req.RestartPolicy),
		MaximumRetryCount: req.RestartMaxRetries,
	}
}

func labelsFromInspect(insp container.InspectResponse) map[string]string {
	if insp.Config == nil {
		return nil
	}
	return insp.Config.Labels
}

func internalPortFromLabels(labels map[string]string) int {
	for _, key := range internalPortLabelKeys {
		if port := parsePort(labels[key]); port > 0 {
			return port
		}
	}
	return 0
}

func firstExposedPort(insp container.InspectResponse) int {
	if insp.Config == nil || len(insp.Config.ExposedPorts) == 0 {
		return 0
	}
	ports := make([]int, 0, len(insp.Config.ExposedPorts))
	for p := range insp.Config.ExposedPorts {
		if port := parsePort(p.Port()); port > 0 {
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	if len(ports) == 0 {
		return 0
	}
	return ports[0]
}

func portFromEnv(insp container.InspectResponse) int {
	if insp.Config == nil {
		return 0
	}
	for _, env := range insp.Config.Env {
		key, value, ok := strings.Cut(env, "=")
		if !ok || key != "PORT" {
			continue
		}
		if port := parsePort(value); port > 0 {
			return port
		}
	}
	return 0
}

func parsePort(value string) int {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

func hasEnvKey(env []string, want string) bool {
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok && key == want {
			return true
		}
	}
	return false
}

// ownedByMidnight reports whether the labels carry the canonical managed-by
// signature (Sprint 4 S4-04). Used to decide whether a stale container is
// safe to garbage-collect or must surface CONTAINER_NAME_CONFLICT.
func ownedByMidnight(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	return labels[LabelManagedBy] == LabelManagedByV
}

// Docker SDK error helpers ----------------------------------------------

func isNameInUse(err error) bool {
	if err == nil {
		return false
	}
	if dockerclient.IsErrConnectionFailed(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "is already in use") || strings.Contains(msg, "name already in use")
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if cerrdefs.IsNotFound(err) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such container")
}

func isNotModified(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already started")
}

// mapDockerStateToInstanceState turns the Engine's container.Summary.State
// string into the panel-locked instance state vocabulary.
func mapDockerStateToInstanceState(s string) runtime.InstanceState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running":
		return runtime.InstanceStateRunning
	case "healthy", "ready":
		return runtime.InstanceStateReady
	case "created":
		return runtime.InstanceStateAllocated
	case "restarting":
		return runtime.InstanceStateRestarting
	case "paused":
		return runtime.InstanceStateUnhealthy
	case "exited":
		return runtime.InstanceStateStopped
	case "dead":
		return runtime.InstanceStateCrashed
	case "removing":
		return runtime.InstanceStateDraining
	default:
		return runtime.InstanceStateUnhealthy
	}
}

func mapDockerSummaryToInstanceState(state, status string) runtime.InstanceState {
	base := mapDockerStateToInstanceState(state)
	if base != runtime.InstanceStateRunning {
		return base
	}
	switch dockerStatusReadiness(status) {
	case runtime.InstanceStateReady:
		return runtime.InstanceStateReady
	case runtime.InstanceStateStarting:
		return runtime.InstanceStateStarting
	case runtime.InstanceStateUnhealthy:
		return runtime.InstanceStateUnhealthy
	default:
		return runtime.InstanceStateRunning
	}
}

type inspectReadiness struct {
	ready  bool
	state  runtime.InstanceState
	reason string
}

func dockerInspectReadiness(insp container.InspectResponse) inspectReadiness {
	if insp.State == nil {
		return inspectReadiness{
			ready:  false,
			state:  runtime.InstanceStateUnhealthy,
			reason: "docker inspect did not include container state",
		}
	}
	status := strings.TrimSpace(string(insp.State.Status))
	if !insp.State.Running {
		if status == "" {
			status = "not running"
		}
		return inspectReadiness{
			ready:  false,
			state:  mapDockerStateToInstanceState(status),
			reason: "docker container is " + status,
		}
	}

	health := insp.State.Health
	if health == nil || health.Status == container.NoHealthcheck {
		return inspectReadiness{
			ready:  true,
			state:  runtime.InstanceStateReady,
			reason: "docker container is running with no healthcheck",
		}
	}

	switch strings.ToLower(strings.TrimSpace(string(health.Status))) {
	case string(container.Healthy):
		return inspectReadiness{
			ready:  true,
			state:  runtime.InstanceStateReady,
			reason: "docker healthcheck is healthy",
		}
	case string(container.Starting):
		return inspectReadiness{
			ready:  false,
			state:  runtime.InstanceStateStarting,
			reason: "docker healthcheck is starting",
		}
	case string(container.Unhealthy):
		return inspectReadiness{
			ready:  false,
			state:  runtime.InstanceStateUnhealthy,
			reason: dockerHealthReason("docker healthcheck is unhealthy", health),
		}
	default:
		if dockerStatusReadiness(status) == runtime.InstanceStateReady {
			return inspectReadiness{
				ready:  true,
				state:  runtime.InstanceStateReady,
				reason: "docker status reports ready",
			}
		}
		healthStatus := strings.TrimSpace(string(health.Status))
		if healthStatus == "" {
			healthStatus = "unknown"
		}
		return inspectReadiness{
			ready:  false,
			state:  runtime.InstanceStateUnhealthy,
			reason: "docker healthcheck status is " + healthStatus,
		}
	}
}

func dockerStatusReadiness(status string) runtime.InstanceState {
	lower := strings.ToLower(strings.TrimSpace(status))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "unhealthy"):
		return runtime.InstanceStateUnhealthy
	case strings.Contains(lower, "health: starting") || strings.Contains(lower, "(starting)"):
		return runtime.InstanceStateStarting
	case strings.Contains(lower, "(healthy)") || lower == "healthy" || lower == "ready" || strings.Contains(lower, "(ready)"):
		return runtime.InstanceStateReady
	default:
		return ""
	}
}

func dockerHealthReason(prefix string, health *container.Health) string {
	if health == nil {
		return prefix
	}
	if len(health.Log) == 0 {
		if health.FailingStreak > 0 {
			return fmt.Sprintf("%s (failing streak %d)", prefix, health.FailingStreak)
		}
		return prefix
	}
	last := health.Log[len(health.Log)-1]
	if last == nil {
		return prefix
	}
	output := strings.TrimSpace(last.Output)
	if output == "" {
		output = fmt.Sprintf("exit code %d", last.ExitCode)
	}
	if health.FailingStreak > 0 {
		return fmt.Sprintf("%s: %s (failing streak %d)", prefix, output, health.FailingStreak)
	}
	return prefix + ": " + output
}

// primaryIP picks the first non-empty IPv4 address attached to the
// container's networks.
func primaryIP(insp container.InspectResponse) string {
	if insp.NetworkSettings == nil {
		return ""
	}
	keys := make([]string, 0, len(insp.NetworkSettings.Networks))
	for k := range insp.NetworkSettings.Networks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		net := insp.NetworkSettings.Networks[k]
		if net == nil {
			continue
		}
		if net.IPAddress.IsValid() {
			return net.IPAddress.String()
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// guard against unused import of errors when no helpers reference it.
var _ = errors.New

// restartLoopThreshold is how many runtime restarts make a candidate a
// restart loop rather than a slow start. Docker's default restart policy
// resurrects a crashing container immediately, so a workload that cannot run
// looks "running" for a fraction of every cycle; counting restarts is the
// only stable signal that waiting will not help.
const restartLoopThreshold = 3

// ObserveInstanceLiveness reports whether the instance has stopped for good
// or is restart-looping, with the exit code and a bounded tail of the
// workload's own output. It is the evidence a readiness wait needs to fail
// fast instead of dialing a dead port until its timeout elapses.
//
// Any inspect failure returns an unobserved liveness (Observed false) rather
// than an error, because "we could not look" must never be mistaken for
// "the workload is dead".
func (a *Adapter) ObserveInstanceLiveness(ctx context.Context, deploymentID, instanceID string) (runtime.InstanceLiveness, error) {
	live := runtime.InstanceLiveness{DeploymentID: deploymentID, InstanceID: instanceID}
	if err := ctx.Err(); err != nil {
		return live, err
	}
	if a == nil || a.client == nil {
		return live, nil
	}
	key := instanceKey(deploymentID, instanceID)
	a.mu.Lock()
	rec, ok := a.records[key]
	a.mu.Unlock()
	if !ok {
		return live, nil
	}
	insp, err := a.client.ContainerInspect(ctx, rec.containerID)
	if err != nil {
		if isNotFound(err) {
			live.Observed = true
			live.State = runtime.InstanceStateRemoved
			live.Exited = true
			live.Reason = "container no longer exists"
			live.ObservedAt = a.now()
			return live, nil
		}
		return live, nil
	}
	live.Observed = true
	live.ObservedAt = a.now()
	live.Restarts = insp.RestartCount
	if insp.State == nil {
		live.State = runtime.InstanceStateUnhealthy
		return live, nil
	}
	live.State = mapDockerStateToInstanceState(string(insp.State.Status))
	live.ExitCode = insp.State.ExitCode
	live.OOMKilled = insp.State.OOMKilled
	live.Reason = terminationReason(insp)
	switch {
	case !insp.State.Running && !insp.State.Restarting:
		live.Exited = true
	case insp.RestartCount >= restartLoopThreshold:
		live.RestartLooping = true
	}
	if live.Exited || live.RestartLooping {
		live.LogTail = a.containerLogTail(ctx, rec.containerID)
	}
	return live, nil
}

// containerLogTail is best-effort: an engine client without log support, or
// a log read that fails, yields no tail rather than an error.
func (a *Adapter) containerLogTail(ctx context.Context, containerID string) []string {
	reader, ok := a.client.(LogEngineClient)
	if !ok {
		return nil
	}
	lines, err := reader.ContainerLogTail(ctx, containerID, maxLogTailLines)
	if err != nil {
		return nil
	}
	return lines
}

// terminationReason describes the last stop in the operator's vocabulary:
// out of memory, a signal, a non-zero exit, or a clean exit that is still
// wrong for a service expected to keep running.
func terminationReason(insp container.InspectResponse) string {
	state := insp.State
	if state == nil {
		return ""
	}
	if detail := strings.TrimSpace(state.Error); detail != "" {
		return "container error: " + detail
	}
	if state.OOMKilled {
		return "container was killed for exceeding its memory limit"
	}
	status := strings.TrimSpace(string(state.Status))
	if state.Running || state.Restarting {
		if insp.RestartCount > 0 {
			return fmt.Sprintf("container restarted %d times (last exit code %d)", insp.RestartCount, state.ExitCode)
		}
		return "container is " + status
	}
	if state.ExitCode == 0 {
		return "container exited with code 0 before it was ready"
	}
	return fmt.Sprintf("container exited with code %d", state.ExitCode)
}

// ImageDeclaredPorts reports the TCP ports the image declares via EXPOSE.
// An image that declares none returns an empty slice, which tells callers a
// port-shaped readiness probe has nothing to aim at.
func (a *Adapter) ImageDeclaredPorts(ctx context.Context, image string) ([]int, error) {
	if a == nil || a.client == nil {
		return nil, nil
	}
	return a.client.ImageExposedPorts(ctx, image)
}
