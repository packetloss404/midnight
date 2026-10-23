package build

import (
	"testing"
	"time"
)

func TestBuildPlanValidateAcceptsPinnedFroms(t *testing.T) {
	plan := BuildPlan{
		Froms:  ParseDockerfileFroms("FROM alpine@sha256:base AS runtime\n"),
		Worker: WorkerConfig{Timeout: time.Minute, MaxConcurrency: 1, MaxDiskUsagePercent: 80},
		SecretFingerprints: []SecretFingerprintRecord{{
			Name:        "token",
			Fingerprint: SecretFingerprint("token", []byte("secret")),
		}},
		SnapshotDraft: DeploymentSnapshot{
			SchemaVersion: DeploymentSnapshotSchemaVersion,
			Build: BuildSnapshot{
				ProvenanceRef: ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"},
				SBOMRef:       ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom"},
			},
		},
		UntrustedBuild: ClassifyUntrustedBuild(UntrustedBuildRules{
			ExternalContributor: true,
			SecretAccess:        true,
		}),
	}

	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestBuildPlanValidateRejectsMutableSnapshotArtifactRef(t *testing.T) {
	plan := BuildPlan{
		Worker: WorkerConfig{Timeout: time.Minute, MaxConcurrency: 1, MaxDiskUsagePercent: 80},
		SnapshotDraft: DeploymentSnapshot{Build: BuildSnapshot{
			ProvenanceRef: ArtifactRef{Name: "app.intoto.jsonl", Digest: "app:latest"},
		}},
	}

	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want mutable provenance ref rejection")
	}
}

func TestBuildPlanValidateRejectsUnpinnedFrom(t *testing.T) {
	plan := BuildPlan{
		Froms:  ParseDockerfileFroms("FROM alpine:3.20\n"),
		Worker: WorkerConfig{Timeout: time.Minute, MaxConcurrency: 1, MaxDiskUsagePercent: 80},
	}

	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unpinned FROM rejection")
	}
}

func TestDefaultBuildOperationPlanValidate(t *testing.T) {
	plan := DefaultBuildOperationPlan()

	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	got := make([]BuildOperationState, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		got = append(got, step.State)
	}
	want := []BuildOperationState{
		BuildOperationQueued,
		BuildOperationBuilding,
		BuildOperationSigning,
		BuildOperationPushing,
		BuildOperationSnapshot,
		BuildOperationDeploy,
	}
	if len(got) != len(want) {
		t.Fatalf("steps = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps = %#v, want %#v", got, want)
		}
	}
}

func TestBuildOperationPlanValidateRejectsOutOfOrderSteps(t *testing.T) {
	plan := DefaultBuildOperationPlan()
	plan.Steps[1], plan.Steps[2] = plan.Steps[2], plan.Steps[1]

	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want out-of-order rejection")
	}
}

func TestClassifyUntrustedBuild(t *testing.T) {
	if got := ClassifyUntrustedBuild(UntrustedBuildRules{}); got != UntrustedBuildTrusted {
		t.Fatalf("ClassifyUntrustedBuild() = %q, want %q", got, UntrustedBuildTrusted)
	}
	if got := ClassifyUntrustedBuild(UntrustedBuildRules{ExternalContributor: true}); got != UntrustedBuildUntrusted {
		t.Fatalf("ClassifyUntrustedBuild() = %q, want %q", got, UntrustedBuildUntrusted)
	}
	if got := ClassifyUntrustedBuild(UntrustedBuildRules{ExternalContributor: true, SecretAccess: true}); got != UntrustedBuildIsolated {
		t.Fatalf("ClassifyUntrustedBuild() = %q, want %q", got, UntrustedBuildIsolated)
	}
}
