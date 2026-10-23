package runtime

import "testing"

func TestScheduleRequestFromBuildArtifactAddsBoundaryLabels(t *testing.T) {
	artifact := BuildArtifact{
		DeploymentID:  "dep_01",
		ProjectID:     "prj_01",
		EnvironmentID: "env_01",
		ServiceID:     "svc_01",
		Image:         "registry.example.com/app@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Labels:        map[string]string{"custom": "kept"},
	}
	req, err := ScheduleRequestFromBuildArtifact(artifact, "inst_01", "key-01")
	if err != nil {
		t.Fatalf("ScheduleRequestFromBuildArtifact: %v", err)
	}
	if req.Image != artifact.Image {
		t.Fatalf("image = %q, want %q", req.Image, artifact.Image)
	}
	if req.IdempotencyKey != "key-01" {
		t.Fatalf("idempotency key = %q", req.IdempotencyKey)
	}
	want := map[string]string{
		"midnight.project":     artifact.ProjectID,
		"midnight.environment": artifact.EnvironmentID,
		"midnight.service":     artifact.ServiceID,
		"midnight.deployment":  artifact.DeploymentID,
		"midnight.instance":    "inst_01",
		"custom":               "kept",
	}
	for key, value := range want {
		if got := req.Labels[key]; got != value {
			t.Fatalf("label %s = %q, want %q", key, got, value)
		}
	}
	if artifact.Labels["midnight.instance"] != "" {
		t.Fatal("artifact labels were mutated")
	}
}

func TestScheduleRequestFromBuildArtifactRequiresDigestPinnedImage(t *testing.T) {
	artifact := BuildArtifact{
		DeploymentID:  "dep_01",
		ProjectID:     "prj_01",
		EnvironmentID: "env_01",
		ServiceID:     "svc_01",
		Image:         "registry.example.com/app:latest",
	}
	if _, err := ScheduleRequestFromBuildArtifact(artifact, "inst_01", "key-01"); err == nil {
		t.Fatal("expected tag-only image to be rejected")
	}
}

func TestScheduleRequestFromBuildArtifactCombinesImageAndDigest(t *testing.T) {
	artifact := BuildArtifact{
		DeploymentID:  "dep_01",
		ProjectID:     "prj_01",
		EnvironmentID: "env_01",
		ServiceID:     "svc_01",
		Image:         "registry.example.com/app:v1",
		Digest:        "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	req, err := ScheduleRequestFromBuildArtifact(artifact, "inst_01", "key-01")
	if err != nil {
		t.Fatalf("ScheduleRequestFromBuildArtifact: %v", err)
	}
	want := "registry.example.com/app:v1@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if req.Image != want {
		t.Fatalf("image = %q, want %q", req.Image, want)
	}
}
