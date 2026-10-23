package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPhase3DrillReservationsContractFromData(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}

	contract, err := buildPhase3DrillReservationsContractFromData(manifest, []byte(validPhase3DrillReservations()))
	if err != nil {
		t.Fatalf("buildPhase3DrillReservationsContractFromData() error = %v", err)
	}
	if contract.ID != phase3DrillReservationsID || contract.Sprint != "Phase 0 Beta Sprint B7" || contract.Status != "pass" {
		t.Fatalf("contract identity = %+v, want B7 passing reservation contract", contract)
	}
	if !contract.LocalOnly || contract.Source != phase3DrillReservationsSourcePath || contract.SourceManifest != phase3HostDrillMetadataSourcePath {
		t.Fatalf("contract refs = %+v, want local-only workspace-relative reservation refs", contract)
	}
	if strings.Join(contract.RequiredReservations, ",") != "D-20,D-21,D-22,D-23" {
		t.Fatalf("RequiredReservations = %v, want D-20..D-23", contract.RequiredReservations)
	}
	if strings.Join(contract.RequiredDomains, ",") != "networking,runtime_adapter,registry_cache,volume_attach" {
		t.Fatalf("RequiredDomains = %v, want all B7 readiness domains", contract.RequiredDomains)
	}

	covered := make(map[string]string)
	for _, reservation := range contract.Reservations {
		covered[reservation.ID] = reservation.Domain
		if reservation.Execution != "planner-only" || reservation.Rig != "future" {
			t.Fatalf("reservation %+v claims execution, want future planner-only reservation", reservation)
		}
		for _, ref := range reservation.EvidenceRefs {
			if filepath.IsAbs(ref) || strings.Contains(ref, `\`) || strings.Contains(ref, ":") || strings.HasPrefix(ref, "../") {
				t.Fatalf("reservation evidence ref %q is not workspace-relative portable", ref)
			}
		}
	}
	for id, domain := range phase3ReservationRequiredDomainsByID() {
		if covered[id] != domain {
			t.Fatalf("covered reservations = %+v, want %s on %s", covered, id, domain)
		}
	}
}

func TestPhase3DrillReservationsRejectUnsafeEvidenceRefs(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	data := strings.Replace(validPhase3DrillReservations(), "artifacts/phase3/drills/conntrack-flush/plan.json", "../outside/plan.json", 1)

	_, err = buildPhase3DrillReservationsContractFromData(manifest, []byte(data))
	if err == nil || !strings.Contains(err.Error(), "evidence-ref") || !strings.Contains(err.Error(), "must not escape workspace") {
		t.Fatalf("buildPhase3DrillReservationsContractFromData() error = %v, want unsafe evidence ref failure", err)
	}
}

func TestPhase3DrillReservationsRequiresD20ThroughD23(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	data := strings.Replace(validPhase3DrillReservations(), "  - id: D-23\n", "  - id: D-24\n", 1)

	_, err = buildPhase3DrillReservationsContractFromData(manifest, []byte(data))
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("buildPhase3DrillReservationsContractFromData() error = %v, want unknown reservation id failure", err)
	}
}

func TestBuildGateReportIncludesB7ReservationsWhenFilePresent(t *testing.T) {
	root := t.TempDir()
	reservationsDir := filepath.Join(root, "test", "drills")
	if err := os.MkdirAll(reservationsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(reservationsDir, "phase3-reservations.yaml"), []byte(validPhase3DrillReservations()), 0o644); err != nil {
		t.Fatalf("WriteFile(phase3-reservations) error = %v", err)
	}
	chdirForTest(t, root)

	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	if report.Phase3DrillReservations == nil || len(report.Phase3DrillReservations.Reservations) != 4 {
		t.Fatalf("Phase3DrillReservations = %+v, want four B7 reservations", report.Phase3DrillReservations)
	}
	if evidenceItemStatus(report.EvidencePacket, phase3DrillReservationsEvidenceName) != "pass" {
		t.Fatalf("EvidencePacket.Items = %+v, want B7 reservation contract evidence", report.EvidencePacket.Items)
	}
	if strings.Contains(report.String(), "D-20") || report.DrillRows != 19 {
		t.Fatalf("report String()/DrillRows = %q/%d, want reservations without execution drill rows", report.String(), report.DrillRows)
	}
	for _, want := range []string{"| B7 Phase 3 drill reservations | pass | 4 |", "| D-20 | networking | D-08 | conntrack-flush-correctness | planner-only | future |"} {
		if !strings.Contains(report.Markdown(), want) {
			t.Fatalf("Markdown() = %q, want %q", report.Markdown(), want)
		}
	}
}

func validPhase3DrillReservations() string {
	return `version: 1
owner: S08
sprint: Phase 0 Beta Sprint B7
local-only: true
source-manifest: test/drills/manifest.yaml
reservations:
  - id: D-20
    status: reserved
    domain: networking
    carrier-id: D-08
    carrier-reason-code: phase0.alert.conntrack-pressure
    planned-drill: conntrack-flush-correctness
    host-scope: runtime-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill
    evidence-refs:
      - artifacts/phase3/drills/conntrack-flush/plan.json
  - id: D-21
    status: reserved
    domain: runtime_adapter
    carrier-id: D-06
    carrier-reason-code: phase0.alert.deploy-job-stuck
    planned-drill: runtime-adapter-lease-recovery
    host-scope: runtime-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill
    evidence-refs:
      - artifacts/phase3/drills/runtime-adapter-lease/plan.json
  - id: D-22
    status: reserved
    domain: registry_cache
    carrier-id: D-07
    carrier-reason-code: phase0.alert.buildkit-cache-corruption
    planned-drill: registry-cache-integrity
    host-scope: builder-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill
    evidence-refs:
      - artifacts/phase3/drills/registry-cache-integrity/plan.json
  - id: D-23
    status: reserved
    domain: volume_attach
    carrier-id: D-14
    carrier-reason-code: phase0.backup.destination-placeholder
    planned-drill: volume-attach-destination-contract
    host-scope: backup-host
    execution: planner-only
    rig: future
    note: reserved for a future dedicated Phase 3 host drill
    evidence-refs:
      - artifacts/phase3/drills/volume-attach-destination/plan.json
`
}
