package runtime

import (
	"reflect"
	"sort"
)

// Deployment is the desired runtime state for one managed container.
type Deployment struct {
	ID       string
	Revision string
	Spec     ContainerSpec
	Network  BridgeNetworkSpec
	RouteID  string
}

// ContainerState is the currently observed state for one managed container.
type ContainerState struct {
	DeploymentID string
	Revision     string
	Spec         ContainerSpec
}

// PlanAction describes the pure reconciliation action for one deployment/container.
type PlanAction string

const (
	PlanCreate PlanAction = "create"
	PlanUpdate PlanAction = "update"
	PlanDelete PlanAction = "delete"
	PlanNoop   PlanAction = "noop"
)

// PlanStep is one deterministic desired/current diff result.
type PlanStep struct {
	Action       PlanAction
	DeploymentID string
	Desired      *Deployment
	Current      *ContainerState
}

// ReconcileStepKind describes one side-effect the runtime executor may perform later.
type ReconcileStepKind string

const (
	ReconcileCreateNetwork     ReconcileStepKind = "create_network"
	ReconcilePullImage         ReconcileStepKind = "pull_image"
	ReconcileAttachVolume      ReconcileStepKind = "attach_volume"
	ReconcileCreateContainer   ReconcileStepKind = "create_container"
	ReconcileStartContainer    ReconcileStepKind = "start_container"
	ReconcileWaitHealth        ReconcileStepKind = "wait_health"
	ReconcileUpdateRouteTarget ReconcileStepKind = "update_route_target"
	ReconcileStopContainer     ReconcileStepKind = "stop_container"
	ReconcileRemoveContainer   ReconcileStepKind = "remove_container"
	ReconcileDetachVolume      ReconcileStepKind = "detach_volume"
)

// ReconcileStep is a pure, ordered runtime operation with enough metadata to execute idempotently.
type ReconcileStep struct {
	Kind           ReconcileStepKind
	DeploymentID   string
	Reason         string
	IdempotencyKey string
}

// RuntimeObservations are local runtime facts used to suppress already-satisfied reconcile steps.
type RuntimeObservations struct {
	Capabilities RuntimeCapabilities
	Networks     []ObservedNetwork
	Images       []ObservedImage
	Volumes      []ObservedVolumeAttachment
	Containers   []ObservedContainer
	RouteTargets []ObservedRouteTarget
}

type RuntimeCapabilities struct {
	Volumes bool
}

type ObservedNetwork struct {
	Name string
}

type ObservedImage struct {
	Image string
}

type ObservedVolumeAttachment struct {
	DeploymentID string
	Revision     string
	VolumeID     string
	Target       string
}

type ObservedContainer struct {
	DeploymentID string
	Revision     string
	Created      bool
	Running      bool
	Healthy      bool
}

type ObservedRouteTarget struct {
	RouteID      string
	DeploymentID string
	Revision     string
}

// DiffPlan compares desired deployments with current containers without side effects.
func DiffPlan(desired []Deployment, current []ContainerState) []PlanStep {
	desiredByID := make(map[string]Deployment, len(desired))
	currentByID := make(map[string]ContainerState, len(current))
	ids := make(map[string]struct{}, len(desired)+len(current))

	for _, deployment := range desired {
		desiredByID[deployment.ID] = deployment
		ids[deployment.ID] = struct{}{}
	}
	for _, container := range current {
		currentByID[container.DeploymentID] = container
		ids[container.DeploymentID] = struct{}{}
	}

	orderedIDs := make([]string, 0, len(ids))
	for id := range ids {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)

	steps := make([]PlanStep, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		d, hasDesired := desiredByID[id]
		c, hasCurrent := currentByID[id]
		switch {
		case hasDesired && !hasCurrent:
			desiredCopy := d
			steps = append(steps, PlanStep{Action: PlanCreate, DeploymentID: id, Desired: &desiredCopy})
		case !hasDesired && hasCurrent:
			currentCopy := c
			steps = append(steps, PlanStep{Action: PlanDelete, DeploymentID: id, Current: &currentCopy})
		case desiredMatchesCurrent(d, c):
			desiredCopy := d
			currentCopy := c
			steps = append(steps, PlanStep{Action: PlanNoop, DeploymentID: id, Desired: &desiredCopy, Current: &currentCopy})
		default:
			desiredCopy := d
			currentCopy := c
			steps = append(steps, PlanStep{Action: PlanUpdate, DeploymentID: id, Desired: &desiredCopy, Current: &currentCopy})
		}
	}

	return steps
}

// ReconcilePlan expands desired/current diff actions into deterministic runtime steps without side effects.
func ReconcilePlan(actions []PlanStep, observations RuntimeObservations) []ReconcileStep {
	observed := indexRuntimeObservations(observations)
	steps := make([]ReconcileStep, 0, len(actions)*6)

	for _, action := range actions {
		switch action.Action {
		case PlanCreate:
			steps = appendDesiredDeploymentSteps(steps, action.DeploymentID, action.Desired, observed, "deployment is missing")
		case PlanUpdate:
			steps = appendDesiredDeploymentSteps(steps, action.DeploymentID, action.Desired, observed, "deployment revision or spec changed")
			steps = appendCurrentContainerCleanupSteps(steps, action.DeploymentID, action.Current, observed, "old deployment revision should be removed")
		case PlanDelete:
			steps = appendCurrentContainerCleanupSteps(steps, action.DeploymentID, action.Current, observed, "deployment is no longer desired")
		case PlanNoop:
			steps = appendDesiredDeploymentSteps(steps, action.DeploymentID, action.Desired, observed, "observed runtime state is incomplete")
		}
	}

	return steps
}

func desiredMatchesCurrent(desired Deployment, current ContainerState) bool {
	return desired.Revision == current.Revision && reflect.DeepEqual(normalizedSpec(desired.Spec), normalizedSpec(current.Spec))
}

type indexedRuntimeObservations struct {
	capabilities RuntimeCapabilities
	networks     map[string]struct{}
	images       map[string]struct{}
	volumes      map[string]ObservedVolumeAttachment
	containers   map[string]ObservedContainer
	routeTargets map[string]ObservedRouteTarget
}

func indexRuntimeObservations(observations RuntimeObservations) indexedRuntimeObservations {
	indexed := indexedRuntimeObservations{
		capabilities: observations.Capabilities,
		networks:     make(map[string]struct{}, len(observations.Networks)),
		images:       make(map[string]struct{}, len(observations.Images)),
		volumes:      make(map[string]ObservedVolumeAttachment, len(observations.Volumes)),
		containers:   make(map[string]ObservedContainer, len(observations.Containers)),
		routeTargets: make(map[string]ObservedRouteTarget, len(observations.RouteTargets)),
	}
	for _, network := range observations.Networks {
		indexed.networks[network.Name] = struct{}{}
	}
	for _, image := range observations.Images {
		indexed.images[image.Image] = struct{}{}
	}
	for _, volume := range observations.Volumes {
		indexed.volumes[volumeAttachmentKey(volume.DeploymentID, volume.Revision, volume.VolumeID, volume.Target)] = volume
	}
	for _, container := range observations.Containers {
		indexed.containers[deploymentRevisionKey(container.DeploymentID, container.Revision)] = container
	}
	for _, target := range observations.RouteTargets {
		indexed.routeTargets[target.RouteID] = target
	}
	return indexed
}

func appendDesiredDeploymentSteps(steps []ReconcileStep, deploymentID string, desired *Deployment, observed indexedRuntimeObservations, reason string) []ReconcileStep {
	if desired == nil {
		return steps
	}
	if desired.Network.Name != "" {
		if _, ok := observed.networks[desired.Network.Name]; !ok {
			steps = append(steps, reconcileStep(ReconcileCreateNetwork, deploymentID, reason, desired.Network.Name))
		}
	}
	if desired.Spec.Image != "" {
		if _, ok := observed.images[desired.Spec.Image]; !ok {
			steps = append(steps, reconcileStep(ReconcilePullImage, deploymentID, reason, desired.Spec.Image))
		}
	}
	if observed.capabilities.Volumes {
		for _, volume := range normalizeVolumeMounts(desired.Spec.Volumes) {
			key := volumeAttachmentKey(deploymentID, desired.Revision, volume.VolumeID, volume.Target)
			if _, ok := observed.volumes[key]; !ok {
				steps = append(steps, reconcileStep(ReconcileAttachVolume, deploymentID, reason, desired.Revision+":"+volume.VolumeID+":"+volume.Target))
			}
		}
	}

	container, hasContainer := observed.containers[deploymentRevisionKey(deploymentID, desired.Revision)]
	if !hasContainer || !container.Created {
		steps = append(steps, reconcileStep(ReconcileCreateContainer, deploymentID, reason, desired.Revision))
	}
	if !hasContainer || !container.Running {
		steps = append(steps, reconcileStep(ReconcileStartContainer, deploymentID, reason, desired.Revision))
	}
	if !hasContainer || !container.Healthy {
		steps = append(steps, reconcileStep(ReconcileWaitHealth, deploymentID, reason, desired.Revision))
	}
	if desired.RouteID != "" {
		target, ok := observed.routeTargets[desired.RouteID]
		if !ok || target.DeploymentID != deploymentID || target.Revision != desired.Revision {
			steps = append(steps, reconcileStep(ReconcileUpdateRouteTarget, deploymentID, reason, desired.RouteID+":"+desired.Revision))
		}
	}
	return steps
}

func appendCurrentContainerCleanupSteps(steps []ReconcileStep, deploymentID string, current *ContainerState, observed indexedRuntimeObservations, reason string) []ReconcileStep {
	if current == nil {
		return steps
	}
	container, hasContainer := observed.containers[deploymentRevisionKey(deploymentID, current.Revision)]
	if !hasContainer || container.Running {
		steps = append(steps, reconcileStep(ReconcileStopContainer, deploymentID, reason, current.Revision))
	}
	if !hasContainer || container.Created {
		steps = append(steps, reconcileStep(ReconcileRemoveContainer, deploymentID, reason, current.Revision))
	}
	if observed.capabilities.Volumes {
		for _, volume := range normalizeVolumeMounts(current.Spec.Volumes) {
			steps = append(steps, reconcileStep(ReconcileDetachVolume, deploymentID, reason, current.Revision+":"+volume.VolumeID+":"+volume.Target))
		}
	}
	return steps
}

func reconcileStep(kind ReconcileStepKind, deploymentID, reason, keyPart string) ReconcileStep {
	return ReconcileStep{
		Kind:           kind,
		DeploymentID:   deploymentID,
		Reason:         reason,
		IdempotencyKey: string(kind) + ":" + deploymentID + ":" + keyPart,
	}
}

func deploymentRevisionKey(deploymentID, revision string) string {
	return deploymentID + ":" + revision
}

func volumeAttachmentKey(deploymentID, revision, volumeID, target string) string {
	return deploymentID + ":" + revision + ":" + volumeID + ":" + target
}
