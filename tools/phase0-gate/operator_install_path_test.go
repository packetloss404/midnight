package main

import (
	"strings"
	"testing"
)

func TestBuildOperatorInstallPathContractRecordsOfflineExpectedEvidence(t *testing.T) {
	contract, err := buildOperatorInstallPathContract()
	if err != nil {
		t.Fatalf("buildOperatorInstallPathContract() error = %v", err)
	}

	if contract.ID != operatorInstallPathID || contract.Status != "pass" || contract.Mode != "offline-contract" || !contract.OfflineByDefault {
		t.Fatalf("contract header = %+v, want passing offline B2 contract", contract)
	}
	if len(contract.ExpectedEvidence) != 9 {
		t.Fatalf("ExpectedEvidence length = %d, want exact B2 evidence set", len(contract.ExpectedEvidence))
	}
	wantEvidence := map[string]string{
		"b2-operator-install-env-file":             "operator-env-file",
		"b2-operator-install-systemd-units":        "systemd-unit-set",
		"b2-operator-install-traefik-dynamic-root": "traefik-dynamic-root",
		"b2-operator-install-doctor-postinst":      "postinstall-doctor-output",
		"b2-operator-install-doctor-after-enable":  "final-doctor-output",
		"b2-operator-install-b1-handoff":           "b1-live-deploy-handoff",
	}
	for _, item := range contract.ExpectedEvidence {
		if role, ok := wantEvidence[item.Name]; ok {
			if item.Role != role || !item.Required {
				t.Fatalf("ExpectedEvidence[%s] = %+v, want role %q and required", item.Name, item, role)
			}
			delete(wantEvidence, item.Name)
		}
	}
	if len(wantEvidence) != 0 {
		t.Fatalf("ExpectedEvidence missing required B2 evidence: %+v", wantEvidence)
	}
	if got := contract.ExpectedEvidence[1]; got.Name != "b2-operator-install-env-file" || !containsString(got.RequiredFields, "/etc/midnight/midnight.env") || !containsString(got.RequiredFields, "MIDNIGHT_TRAEFIK_DYNAMIC_DIR") {
		t.Fatalf("ExpectedEvidence[1] = %+v, want env file contract fields", got)
	}
	if got := contract.ExpectedEvidence[2]; got.Name != "b2-operator-install-systemd-units" || !containsString(got.RequiredFields, "midnight-server.service") || !containsString(got.RequiredFields, "midnight-registry.service") {
		t.Fatalf("ExpectedEvidence[2] = %+v, want systemd unit set", got)
	}
	if got := contract.ExpectedEvidence[3]; got.Name != "b2-operator-install-traefik-dynamic-root" || !containsString(got.RequiredFields, "/etc/midnight/traefik/dynamic") || !containsString(got.RequiredFields, "traefik.dynamic.yaml") {
		t.Fatalf("ExpectedEvidence[3] = %+v, want Traefik dynamic root", got)
	}
	if got := contract.ExpectedEvidence[7]; got.Name != "b2-operator-install-b1-handoff" || !containsString(got.RequiredFields, liveDeployProofID) {
		t.Fatalf("ExpectedEvidence[7] = %+v, want B1 handoff marker", got)
	}

	wantChecks := map[string]bool{
		"b2.package-install-offline": false,
		"b2.env-file-present":        false,
		"b2.systemd-unit-set":        false,
		"b2.traefik-dynamic-root":    false,
		"b2.doctor-green":            false,
		"b2.b1-handoff":              false,
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

func TestValidateOperatorInstallPathContractRejectsUnsafeOrIncompleteEvidence(t *testing.T) {
	contract, err := buildOperatorInstallPathContract()
	if err != nil {
		t.Fatalf("buildOperatorInstallPathContract() error = %v", err)
	}
	contract.ExpectedEvidence[0].Path = "../install.log"
	if err := validateOperatorInstallPathContract(contract); err == nil || !strings.Contains(err.Error(), "must not escape workspace") {
		t.Fatalf("validateOperatorInstallPathContract() error = %v, want unsafe path failure", err)
	}

	contract, err = buildOperatorInstallPathContract()
	if err != nil {
		t.Fatalf("buildOperatorInstallPathContract() error = %v", err)
	}
	contract.RequiredChecks[0].Evidence = []string{"unknown-evidence"}
	if err := validateOperatorInstallPathContract(contract); err == nil || !strings.Contains(err.Error(), "unknown evidence") {
		t.Fatalf("validateOperatorInstallPathContract() error = %v, want unknown evidence failure", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
