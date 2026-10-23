package runtime

import (
	"fmt"
	"strings"
)

// BuildArtifact is the runtime-adapter-facing output of a successful build.
// The adapter boundary consumes it by deriving one ScheduleRequest per desired
// instance without exposing build-system internals to runtime adapters.
type BuildArtifact struct {
	DeploymentID  string
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	Image         string
	Digest        string
	Labels        map[string]string
}

// ScheduleRequestFromBuildArtifact derives the adapter ScheduleRequest for one
// instance from a verified build artifact.
func ScheduleRequestFromBuildArtifact(artifact BuildArtifact, instanceID, idempotencyKey string) (ScheduleRequest, error) {
	if artifact.DeploymentID == "" || instanceID == "" {
		return ScheduleRequest{}, fmt.Errorf("runtime boundary: build artifact schedule requires deployment and instance ids")
	}
	if artifact.ProjectID == "" || artifact.EnvironmentID == "" || artifact.ServiceID == "" {
		return ScheduleRequest{}, fmt.Errorf("runtime boundary: build artifact schedule requires project, environment, and service ids")
	}
	image := artifact.Image
	if image != "" && !strings.Contains(image, "@sha256:") && strings.HasPrefix(artifact.Digest, "sha256:") {
		image = image + "@" + artifact.Digest
	}
	if image == "" && strings.Contains(artifact.Digest, "@sha256:") {
		image = artifact.Digest
	}
	if image == "" {
		return ScheduleRequest{}, fmt.Errorf("runtime boundary: build artifact schedule requires an image")
	}
	if !strings.Contains(image, "@sha256:") {
		return ScheduleRequest{}, fmt.Errorf("runtime boundary: build artifact image must be digest-pinned")
	}

	labels := copyArtifactLabels(artifact.Labels)
	labels["midnight.project"] = artifact.ProjectID
	labels["midnight.environment"] = artifact.EnvironmentID
	labels["midnight.service"] = artifact.ServiceID
	labels["midnight.deployment"] = artifact.DeploymentID
	labels["midnight.instance"] = instanceID

	return ScheduleRequest{
		DeploymentID:   artifact.DeploymentID,
		InstanceID:     instanceID,
		ProjectID:      artifact.ProjectID,
		EnvironmentID:  artifact.EnvironmentID,
		ServiceID:      artifact.ServiceID,
		Image:          image,
		Labels:         labels,
		IdempotencyKey: idempotencyKey,
	}, nil
}

func copyArtifactLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+5)
	for k, v := range in {
		out[k] = v
	}
	return out
}
