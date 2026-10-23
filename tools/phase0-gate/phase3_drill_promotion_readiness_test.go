package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPhase3DrillPromotionReadinessContractFromData(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}

	contract, err := buildPhase3DrillPromotionReadinessContractFromData(manifest, []byte(validPhase3DrillPromotionReadiness()))
	if err != nil {
		t.Fatalf("buildPhase3DrillPromotionReadinessContractFromData() error = %v", err)
	}
	if contract.ID != phase3DrillPromotionReadinessID || contract.Sprint != "Phase 0 Beta Sprint B8" || contract.Status != "not-ready" {
		t.Fatalf("contract identity = %+v, want B8 not-ready promotion readiness contract", contract)
	}
	if !contract.LocalOnly || contract.Source != phase3DrillPromotionReadinessSourcePath || contract.SourceReservations != phase3DrillReservationsSourcePath {
		t.Fatalf("contract refs = %+v, want local-only refs to B7 reservation index", contract)
	}
	if contract.SourceManifest != phase3HostDrillMetadataSourcePath {
		t.Fatalf("SourceManifest = %q, want %s", contract.SourceManifest, phase3HostDrillMetadataSourcePath)
	}
	if contract.Execution != "not-run" || contract.LiveExecution {
		t.Fatalf("contract execution = %q/%t, want no live execution claim", contract.Execution, contract.LiveExecution)
	}
	if strings.Join(contract.ReservedIDs, ",") != "D-20,D-21,D-22,D-23" {
		t.Fatalf("ReservedIDs = %v, want D-20..D-23", contract.ReservedIDs)
	}
	if strings.Join(contract.RequiredUnblockers, ",") != strings.Join(phase3PromotionReadinessRequiredUnblockers(), ",") {
		t.Fatalf("RequiredUnblockers = %v, want required B8 unblockers", contract.RequiredUnblockers)
	}
	if len(contract.Checks) != 4 {
		t.Fatalf("Checks = %+v, want four D-20..D-23 rows", contract.Checks)
	}
	for _, check := range contract.Checks {
		if check.Status != "not-ready" || check.ReservationID != check.ID {
			t.Fatalf("check = %+v, want not-ready reservation self-reference", check)
		}
		if strings.Join(check.RequiredUnblockers, ",") != strings.Join(phase3PromotionReadinessRequiredUnblockers(), ",") {
			t.Fatalf("RequiredUnblockers = %v, want required B8 unblockers", check.RequiredUnblockers)
		}
		for _, ref := range check.EvidenceRefs {
			if filepath.IsAbs(ref) || strings.Contains(ref, `\`) || strings.Contains(ref, ":") || strings.HasPrefix(ref, "../") {
				t.Fatalf("promotion readiness evidence ref %q is not workspace-relative portable", ref)
			}
		}
	}
}

func TestPhase3DrillPromotionReadinessRejectsUnsafeEvidenceRefs(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	data := strings.Replace(validPhase3DrillPromotionReadiness(), "artifacts/phase3/promotion/D-20/accepted-id-range.json", "../outside/contract.json", 1)

	_, err = buildPhase3DrillPromotionReadinessContractFromData(manifest, []byte(data))
	if err == nil || !strings.Contains(err.Error(), "evidence-ref") || !strings.Contains(err.Error(), "must not escape workspace") {
		t.Fatalf("buildPhase3DrillPromotionReadinessContractFromData() error = %v, want unsafe evidence ref failure", err)
	}
}

func TestPhase3DrillPromotionReadinessRequiresD20ThroughD23(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	data := strings.Replace(validPhase3DrillPromotionReadiness(), "    - id: D-23\n", "    - id: D-24\n", 1)

	_, err = buildPhase3DrillPromotionReadinessContractFromData(manifest, []byte(data))
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("buildPhase3DrillPromotionReadinessContractFromData() error = %v, want unknown reservation id failure", err)
	}
}

func TestPhase3DrillPromotionReadinessRequiresNotReadyLocalOnlyContract(t *testing.T) {
	manifest, err := parseDrillManifest([]byte(validDrillManifest()))
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{
			name: "ready status",
			data: strings.Replace(validPhase3DrillPromotionReadiness(), "      status: not-ready", "      status: ready", 1),
			want: "want not-ready",
		},
		{
			name: "top-level live execution",
			data: strings.Replace(validPhase3DrillPromotionReadiness(), "source-manifest: test/drills/manifest.yaml", "source-manifest: test/drills/manifest.yaml\nlive-execution: true", 1),
			want: "must not declare top-level execution",
		},
		{
			name: "missing unblocker",
			data: strings.Replace(validPhase3DrillPromotionReadiness(), "        - rollback-boundary\n", "", 1),
			want: "missing required unblocker",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildPhase3DrillPromotionReadinessContractFromData(manifest, []byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("buildPhase3DrillPromotionReadinessContractFromData() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBuildGateReportIncludesB8PromotionReadinessWhenFilePresent(t *testing.T) {
	root := t.TempDir()
	drillsDir := filepath.Join(root, "test", "drills")
	if err := os.MkdirAll(drillsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(drillsDir, "phase3-promotion-readiness.yaml"), []byte(validPhase3DrillPromotionReadiness()), 0o644); err != nil {
		t.Fatalf("WriteFile(phase3-promotion-readiness) error = %v", err)
	}
	chdirForTest(t, root)

	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	if report.Phase3DrillPromotionReadiness == nil || len(report.Phase3DrillPromotionReadiness.ReservedIDs) != 4 {
		t.Fatalf("Phase3DrillPromotionReadiness = %+v, want four B8 reserved ids", report.Phase3DrillPromotionReadiness)
	}
	if evidenceItemStatus(report.EvidencePacket, phase3DrillPromotionReadinessEvidenceName) != "not-ready" {
		t.Fatalf("EvidencePacket.Items = %+v, want B8 promotion readiness not-ready evidence", report.EvidencePacket.Items)
	}
	if report.DrillRows != 19 {
		t.Fatalf("DrillRows = %d, want B8 contract without executable D-20+ rows", report.DrillRows)
	}
	for _, want := range []string{"phase3_drill_promotion_readiness=not-ready", "| B8 Phase 3 drill promotion readiness | not-ready | 4 |", "| not-ready | test/drills/phase3-reservations.yaml | not-run | false |"} {
		if !strings.Contains(report.String()+report.Markdown(), want) {
			t.Fatalf("report output missing %q\nString=%q\nMarkdown=%q", want, report.String(), report.Markdown())
		}
	}
}

func validPhase3DrillPromotionReadiness() string {
	return `version: 1
owner: S08
sprint: Phase 0 Beta Sprint B8
local-only: true
reservation-index: test/drills/phase3-reservations.yaml
source-manifest: test/drills/manifest.yaml
checks:
  checklist:
    - id: D-20
      status: not-ready
      domain: networking
      reservation-id: D-20
      carrier-id: D-08
      required-unblockers:
        - accepted-id-range
        - executable-rig
        - harness-assertion
        - live-evidence-ref
        - rollback-boundary
      evidence-refs:
        - artifacts/phase3/promotion/D-20/accepted-id-range.json
        - artifacts/phase3/promotion/D-20/executable-rig.json
        - artifacts/phase3/promotion/D-20/harness-assertion.json
        - artifacts/phase3/promotion/D-20/live-evidence-ref.json
        - artifacts/phase3/promotion/D-20/rollback-boundary.json
    - id: D-21
      status: not-ready
      domain: runtime_adapter
      reservation-id: D-21
      carrier-id: D-06
      required-unblockers:
        - accepted-id-range
        - executable-rig
        - harness-assertion
        - live-evidence-ref
        - rollback-boundary
      evidence-refs:
        - artifacts/phase3/promotion/D-21/accepted-id-range.json
        - artifacts/phase3/promotion/D-21/executable-rig.json
        - artifacts/phase3/promotion/D-21/harness-assertion.json
        - artifacts/phase3/promotion/D-21/live-evidence-ref.json
        - artifacts/phase3/promotion/D-21/rollback-boundary.json
    - id: D-22
      status: not-ready
      domain: registry_cache
      reservation-id: D-22
      carrier-id: D-07
      required-unblockers:
        - accepted-id-range
        - executable-rig
        - harness-assertion
        - live-evidence-ref
        - rollback-boundary
      evidence-refs:
        - artifacts/phase3/promotion/D-22/accepted-id-range.json
        - artifacts/phase3/promotion/D-22/executable-rig.json
        - artifacts/phase3/promotion/D-22/harness-assertion.json
        - artifacts/phase3/promotion/D-22/live-evidence-ref.json
        - artifacts/phase3/promotion/D-22/rollback-boundary.json
    - id: D-23
      status: not-ready
      domain: volume_attach
      reservation-id: D-23
      carrier-id: D-14
      required-unblockers:
        - accepted-id-range
        - executable-rig
        - harness-assertion
        - live-evidence-ref
        - rollback-boundary
      evidence-refs:
        - artifacts/phase3/promotion/D-23/accepted-id-range.json
        - artifacts/phase3/promotion/D-23/executable-rig.json
        - artifacts/phase3/promotion/D-23/harness-assertion.json
        - artifacts/phase3/promotion/D-23/live-evidence-ref.json
        - artifacts/phase3/promotion/D-23/rollback-boundary.json
`
}
