package isolation

import (
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/tenancy"
)

const testSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestPlanBuildEvidenceReadyForIsolatedBuildWithProvenanceAndSBOM(t *testing.T) {
	plan, err := PlanBuildEvidence(BuildEvidenceRequest{
		Build: BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskIsolated},
		EvidenceRefs: []EvidenceRef{
			{Kind: EvidenceKindProvenance, Path: "evidence/provenance.json", SHA256: testSHA256, Status: EvidenceStatusReady},
			{Kind: EvidenceKindSBOM, Path: "evidence/sbom.spdx.json", SHA256: "sha256:" + testSHA256, Status: EvidenceStatusReady},
		},
		SecretFingerprints: []string{"sha256:" + testSHA256},
	})
	if err != nil {
		t.Fatalf("PlanBuildEvidence() error = %v", err)
	}
	if plan.SchemaVersion != BuildIsolationEvidenceSchemaVersion || plan.Status != EvidenceStatusReady || plan.Build.Backend != BackendMicroVM || plan.Build.NetworkEgress || len(plan.Issues) != 0 || len(plan.EvidenceRefs) != 2 {
		t.Fatalf("build evidence plan = %#v", plan)
	}
	if err := ValidateBuildEvidencePlan(plan); err != nil {
		t.Fatalf("ValidateBuildEvidencePlan() error = %v", err)
	}
}

func TestPlanBuildEvidenceNeedsReviewWhenUntrustedEvidenceMissing(t *testing.T) {
	plan, err := PlanBuildEvidence(BuildEvidenceRequest{Build: BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskUntrusted}})
	if err != nil {
		t.Fatalf("PlanBuildEvidence() error = %v", err)
	}
	if plan.Status != EvidenceStatusNeedsReview || len(plan.Issues) != 2 {
		t.Fatalf("build evidence plan = %#v, want needs-review with missing evidence issues", plan)
	}
	if plan.Build.Backend != BackendGVisor || plan.Build.NetworkEgress {
		t.Fatalf("build plan = %#v, want gvisor without network egress", plan.Build)
	}
}

func TestPlanBuildEvidenceRejectsUnsafeEvidencePath(t *testing.T) {
	_, err := PlanBuildEvidence(BuildEvidenceRequest{
		Build:        BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskUntrusted},
		EvidenceRefs: []EvidenceRef{{Kind: EvidenceKindProvenance, Path: "../provenance.json", SHA256: testSHA256, Status: EvidenceStatusReady}},
	})
	if err == nil || !strings.Contains(err.Error(), "safe relative") {
		t.Fatalf("PlanBuildEvidence() error = %v, want unsafe path rejection", err)
	}
}

func TestPlanBuildEvidenceRejectsBadSHA256(t *testing.T) {
	_, err := PlanBuildEvidence(BuildEvidenceRequest{
		Build:        BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskUntrusted},
		EvidenceRefs: []EvidenceRef{{Kind: EvidenceKindProvenance, Path: "evidence/provenance.json", SHA256: "bad", Status: EvidenceStatusReady}},
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("PlanBuildEvidence() error = %v, want checksum rejection", err)
	}
}

func TestPlanBuildEvidenceRejectsPlaintextSecretMaterial(t *testing.T) {
	_, err := PlanBuildEvidence(BuildEvidenceRequest{
		Build:              BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskTrusted},
		SecretFingerprints: []string{"secret://build/token"},
	})
	if err == nil || !strings.Contains(err.Error(), "not secret material") {
		t.Fatalf("PlanBuildEvidence() error = %v, want secret material rejection", err)
	}
}

func TestPlanBuildEvidencePreservesTenantLabelValidation(t *testing.T) {
	plan, err := PlanBuildEvidence(BuildEvidenceRequest{Build: BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskTrusted}})
	if err != nil {
		t.Fatalf("PlanBuildEvidence() error = %v", err)
	}
	plan.Build.RequiredLabels[tenancy.LabelProject] = "prj_other"
	if err := ValidateBuildEvidencePlan(plan); err == nil {
		t.Fatal("ValidateBuildEvidencePlan() error = nil, want cross-tenant label rejection")
	}
}
