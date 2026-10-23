package main

import (
	"strings"
	"testing"
)

func TestBuildLiveDeployProofContractRecordsOfflineExpectedEvidence(t *testing.T) {
	contract, err := buildLiveDeployProofContract()
	if err != nil {
		t.Fatalf("buildLiveDeployProofContract() error = %v", err)
	}

	if contract.ID != liveDeployProofID || contract.Status != "pass" || contract.Mode != "offline-contract" || !contract.OfflineByDefault {
		t.Fatalf("contract header = %+v, want passing offline B1 contract", contract)
	}
	if len(contract.ExpectedEvidence) != 21 {
		t.Fatalf("ExpectedEvidence length = %d, want exact B1 evidence set", len(contract.ExpectedEvidence))
	}
	if contract.ExpectedEvidence[0].Name != "b1-live-deploy-commit" || contract.ExpectedEvidence[0].Path != "artifacts/phase0/sprint-b1-live-deploy/<run-id>/commit.txt" {
		t.Fatalf("ExpectedEvidence[0] = %+v, want live deploy run artifact", contract.ExpectedEvidence[0])
	}
	if got := contract.ExpectedEvidence[19]; got.Name != "b1-live-deploy-certificate-summary" || got.Required {
		t.Fatalf("ExpectedEvidence[19] = %+v, want optional HTTPS certificate summary", got)
	}
	wantChecks := map[string]bool{
		"b1.local-profile":             false,
		"b1.release-gate-prerequisite": false,
		"b1.live-doctor-ready":         false,
		"b1.deploy-active":             false,
		"b1.timeline-complete":         false,
		"b1.archive-integrity":         false,
	}
	for _, check := range contract.RequiredChecks {
		if _, ok := wantChecks[check.ID]; ok {
			wantChecks[check.ID] = true
		}
	}
	for id, found := range wantChecks {
		if !found {
			t.Fatalf("RequiredChecks missing %q: %+v", id, contract.RequiredChecks)
		}
	}
}

func TestValidateLiveDeployProofContractRejectsUnsafeOrIncompleteEvidence(t *testing.T) {
	contract, err := buildLiveDeployProofContract()
	if err != nil {
		t.Fatalf("buildLiveDeployProofContract() error = %v", err)
	}
	contract.ExpectedEvidence[0].Path = "../run.json"
	if err := validateLiveDeployProofContract(contract); err == nil || !strings.Contains(err.Error(), "must not escape workspace") {
		t.Fatalf("validateLiveDeployProofContract() error = %v, want unsafe path failure", err)
	}

	contract, err = buildLiveDeployProofContract()
	if err != nil {
		t.Fatalf("buildLiveDeployProofContract() error = %v", err)
	}
	contract.RequiredChecks[0].Evidence = []string{"unknown-evidence"}
	if err := validateLiveDeployProofContract(contract); err == nil || !strings.Contains(err.Error(), "unknown evidence") {
		t.Fatalf("validateLiveDeployProofContract() error = %v, want unknown evidence failure", err)
	}
}
