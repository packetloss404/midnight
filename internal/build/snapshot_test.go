package build

import "testing"

func TestDeploymentSnapshotRollbackByDigestDoesNotRequireBuild(t *testing.T) {
	snapshot := DeploymentSnapshot{
		SchemaVersion: DeploymentSnapshotSchemaVersion,
		Images: []ImageSnapshot{{
			Name:        "app",
			Tag:         "stable",
			Digest:      "sha256:1234",
			RollbackRef: "sha256:1234",
		}},
	}

	if snapshot.RequiresBuild() {
		t.Fatal("rollback snapshot with digest pin required a build")
	}

	pins := snapshot.ImagePins()
	if len(pins) != 1 || pins[0].Digest != "sha256:1234" || pins[0].RollbackRef != "sha256:1234" {
		t.Fatalf("ImagePins() = %#v", pins)
	}
}

func TestSecretFingerprintDoesNotExposePlaintext(t *testing.T) {
	fingerprint := SecretFingerprint("token", []byte("super-secret"))

	if fingerprint == "" || fingerprint == "super-secret" {
		t.Fatalf("SecretFingerprint() = %q", fingerprint)
	}
	if got := SecretFingerprint("token", []byte("super-secret")); got != fingerprint {
		t.Fatalf("SecretFingerprint() = %q, want stable %q", got, fingerprint)
	}
}

func TestProvenanceStatementUsesInTotoSLSAShape(t *testing.T) {
	statement := NewProvenanceStatement("app", "sha256:abcd", "buildkit://worker", "https://midnight.dev/buildkit")

	if statement.Type != InTotoStatementType || statement.PredicateType != SLSAProvenancePredicateV1 {
		t.Fatalf("statement type = %q predicate = %q", statement.Type, statement.PredicateType)
	}
	if len(statement.Subject) != 1 || statement.Subject[0].Digest["sha256"] != "abcd" {
		t.Fatalf("statement subject = %#v", statement.Subject)
	}
	if statement.Predicate.RunDetails.Builder.ID != "buildkit://worker" {
		t.Fatalf("builder = %#v", statement.Predicate.RunDetails.Builder)
	}
}

func TestDeploymentSnapshotValidateDraftAcceptsSBOMAndProvenanceRefs(t *testing.T) {
	snapshot := DeploymentSnapshot{
		SchemaVersion: DeploymentSnapshotSchemaVersion,
		Build: BuildSnapshot{
			ProvenanceRef: ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov", MediaType: "application/vnd.in-toto+json"},
			SBOMRef:       ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom", MediaType: "application/spdx+json"},
		},
	}

	if err := snapshot.ValidateDraft(); err != nil {
		t.Fatalf("ValidateDraft() error = %v", err)
	}
}

func TestDeploymentSnapshotValidateDraftRejectsMutableArtifactRefs(t *testing.T) {
	snapshot := DeploymentSnapshot{Build: BuildSnapshot{SBOMRef: ArtifactRef{Name: "app.spdx.json", Digest: "app:latest"}}}

	if err := snapshot.ValidateDraft(); err == nil {
		t.Fatal("ValidateDraft() error = nil, want mutable artifact ref rejection")
	}
}

func TestSelectRollbackSnapshotPrevious(t *testing.T) {
	history := []DeploymentSnapshotRecord{
		{Deployment: "prod", Revision: "r1", Snapshot: DeploymentSnapshot{Build: BuildSnapshot{SourceRef: "r1"}}},
		{Deployment: "prod", Revision: "r2", Snapshot: DeploymentSnapshot{Build: BuildSnapshot{SourceRef: "r2"}}},
		{Deployment: "staging", Revision: "s1", Snapshot: DeploymentSnapshot{Build: BuildSnapshot{SourceRef: "s1"}}},
		{Deployment: "prod", Revision: "r3", Snapshot: DeploymentSnapshot{Build: BuildSnapshot{SourceRef: "r3"}}},
	}

	selected, ok := SelectRollbackSnapshot(history, RollbackTarget{Kind: RollbackPrevious, Deployment: "prod"})
	if !ok || selected.Revision != "r2" {
		t.Fatalf("SelectRollbackSnapshot(previous) = %#v, %v", selected, ok)
	}
}

func TestSelectRollbackSnapshotByDeploymentAndRevision(t *testing.T) {
	history := []DeploymentSnapshotRecord{
		{Deployment: "prod", Revision: "r1"},
		{Deployment: "staging", Revision: "s1"},
		{Deployment: "prod", Revision: "r2"},
	}

	selected, ok := SelectRollbackSnapshot(history, RollbackTarget{Kind: RollbackToDeployment, Deployment: "prod"})
	if !ok || selected.Revision != "r2" {
		t.Fatalf("SelectRollbackSnapshot(to-deployment) = %#v, %v", selected, ok)
	}

	selected, ok = SelectRollbackSnapshot(history, RollbackTarget{Kind: RollbackToRevision, Revision: "s1"})
	if !ok || selected.Deployment != "staging" {
		t.Fatalf("SelectRollbackSnapshot(to-revision) = %#v, %v", selected, ok)
	}
}
