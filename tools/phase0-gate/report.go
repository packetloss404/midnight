package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/observability"
)

type gateReport struct {
	DrillRows                        int                                    `json:"drillRows"`
	DrillStatuses                    map[string]int                         `json:"drillStatuses"`
	AlertRules                       int                                    `json:"alertRules"`
	AlertValidationPassed            bool                                   `json:"alertValidationPassed"`
	LiveDeployProof                  liveDeployProofContract                `json:"liveDeployProof"`
	OperatorInstallPath              operatorInstallPathContract            `json:"operatorInstallPath"`
	Phase3HostDrillMetadata          phase3HostDrillMetadataContract        `json:"phase3HostDrillMetadata"`
	Phase3DrillReservations          *phase3DrillReservationsContract       `json:"phase3DrillReservations,omitempty"`
	Phase3DrillPromotionReadiness    *phase3DrillPromotionReadinessContract `json:"phase3DrillPromotionReadiness,omitempty"`
	Phase0ReleaseEvidenceArchive     *phase0ReleaseEvidenceArchiveContract  `json:"phase0ReleaseEvidenceArchive,omitempty"`
	DrillExecution                   *drillExecutionSummary                 `json:"drillExecution,omitempty"`
	ReleaseEvidenceIndexVerification *releaseEvidenceIndexVerification      `json:"releaseEvidenceIndexVerification,omitempty"`
	EvidencePacket                   evidencePacket                         `json:"evidencePacket"`
}

type evidencePacket struct {
	LastGreen lastGreenEvidence `json:"lastGreen"`
	Items     []evidenceItem    `json:"items"`
}

type lastGreenEvidence struct {
	Status string `json:"status"`
	Source string `json:"source"`
}

type evidenceItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Path   string `json:"path"`
}

func buildGateReport(manifestData, alertData []byte, lastGreen string) (gateReport, error) {
	return buildGateReportWithDrillExecution(manifestData, alertData, lastGreen, nil, false)
}

func buildGateReportWithDrillExecution(manifestData, alertData []byte, lastGreen string, adapter drillExecutionAdapter, requireExecutedDrills bool) (gateReport, error) {
	manifest, err := parseDrillManifest(manifestData)
	if err != nil {
		return gateReport{}, err
	}
	if err := validateParsedDrillManifest(manifest); err != nil {
		return gateReport{}, err
	}

	alerts, err := observability.ParsePrometheusAlerts(alertData)
	if err != nil {
		alerts = nil
	}
	alertValidationPassed := observability.ValidatePrometheusAlerts(alertData) == nil
	liveDeployProof, err := buildLiveDeployProofContract()
	if err != nil {
		return gateReport{}, err
	}
	operatorInstallPath, err := buildOperatorInstallPathContract()
	if err != nil {
		return gateReport{}, err
	}
	phase3HostDrillMetadata, err := buildPhase3HostDrillMetadataContract(manifest)
	if err != nil {
		return gateReport{}, err
	}
	phase3DrillReservations, err := buildPhase3DrillReservationsContract(manifest)
	if err != nil {
		return gateReport{}, err
	}
	phase3DrillPromotionReadiness, err := buildPhase3DrillPromotionReadinessContract(manifest)
	if err != nil {
		return gateReport{}, err
	}
	phase0ReleaseEvidenceArchive, err := buildPhase0ReleaseEvidenceArchiveContract()
	if err != nil {
		return gateReport{}, err
	}

	if strings.TrimSpace(lastGreen) == "" {
		lastGreen = "unknown"
	}
	report := gateReport{
		DrillRows:               len(manifest.Drills),
		DrillStatuses:           make(map[string]int),
		AlertRules:              len(alerts),
		AlertValidationPassed:   alertValidationPassed,
		LiveDeployProof:         liveDeployProof,
		OperatorInstallPath:     operatorInstallPath,
		Phase3HostDrillMetadata: phase3HostDrillMetadata,
		EvidencePacket: evidencePacket{
			LastGreen: lastGreenEvidence{Status: lastGreen, Source: "flag"},
			Items: []evidenceItem{
				{Name: "drill-manifest", Status: "pass", Path: "test/drills/manifest.yaml"},
				{Name: "prometheus-alerts", Status: validationStatus(alertValidationPassed), Path: "deploy/prometheus/alerts.yaml"},
				{Name: "b1-live-deploy-proof-contract", Status: liveDeployProof.Status, Path: "tools/phase0-gate/live_deploy_proof.go"},
				{Name: "b2-operator-install-path-contract", Status: operatorInstallPath.Status, Path: "tools/phase0-gate/operator_install_path.go"},
				{Name: "b4-canonical-proof-packet-contract", Status: canonicalProofPacketStatus, Path: "tools/phase0-gate/canonical_proof_packet.go"},
				{Name: phase3HostDrillMetadataEvidenceName, Status: phase3HostDrillMetadata.Status, Path: phase3HostDrillMetadata.Source},
			},
		},
	}
	if phase3DrillReservations.ID != "" {
		report.Phase3DrillReservations = &phase3DrillReservations
		report.EvidencePacket.Items = append(report.EvidencePacket.Items, evidenceItem{Name: phase3DrillReservationsEvidenceName, Status: phase3DrillReservations.Status, Path: phase3DrillReservations.Source})
	}
	if phase3DrillPromotionReadiness.ID != "" {
		report.Phase3DrillPromotionReadiness = &phase3DrillPromotionReadiness
		report.EvidencePacket.Items = append(report.EvidencePacket.Items, evidenceItem{Name: phase3DrillPromotionReadinessEvidenceName, Status: phase3DrillPromotionReadiness.Status, Path: phase3DrillPromotionReadiness.Source})
	}
	if phase0ReleaseEvidenceArchive.ID != "" {
		report.Phase0ReleaseEvidenceArchive = &phase0ReleaseEvidenceArchive
		report.EvidencePacket.Items = append(report.EvidencePacket.Items, evidenceItem{Name: phase0ReleaseEvidenceArchiveEvidenceName, Status: phase0ReleaseEvidenceArchive.Status, Path: phase0ReleaseEvidenceArchive.Source})
	}
	for _, drill := range manifest.Drills {
		report.DrillStatuses[drill.Status]++
	}
	if adapter != nil {
		execution, err := adapter.Execute(manifest)
		if err != nil {
			return gateReport{}, err
		}
		report.DrillExecution = &execution
		executionEvidenceStatus := "skipped"
		if requireExecutedDrills {
			executionEvidenceStatus = validationStatus(executedDrillsPassed(execution))
		}
		report.EvidencePacket.Items = append(report.EvidencePacket.Items, evidenceItem{Name: "drill-execution", Status: executionEvidenceStatus, Path: "test/drills/manifest.yaml"})
	}
	return report, nil
}

func executedDrillsPassed(execution drillExecutionSummary) bool {
	return execution.Assertions > 0 && execution.Failed == 0 && execution.Skipped == 0 && execution.DrillStatuses["passed"] == len(execution.Evaluations)
}

func (r gateReport) Render(format string) ([]byte, error) {
	switch format {
	case "text":
		return []byte(fmt.Sprintf("phase0 gate passed: %s\n", r)), nil
	case "markdown":
		return []byte(r.Markdown()), nil
	case "json":
		data, err := r.JSON()
		if err != nil {
			return nil, err
		}
		return append(data, '\n'), nil
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

func (r gateReport) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func (r gateReport) String() string {
	message := fmt.Sprintf("drills=%d drill_statuses=%s alerts=%d alert_validation=%t", r.DrillRows, formatCounts(r.DrillStatuses), r.AlertRules, r.AlertValidationPassed)
	if r.LiveDeployProof.ID != "" {
		message += fmt.Sprintf(" live_deploy_proof=%s", r.LiveDeployProof.Status)
	}
	if r.OperatorInstallPath.ID != "" {
		message += fmt.Sprintf(" operator_install_path=%s", r.OperatorInstallPath.Status)
	}
	if r.Phase3HostDrillMetadata.ID != "" {
		message += fmt.Sprintf(" phase3_host_drill_metadata=%s host_scoped_drills=%d", r.Phase3HostDrillMetadata.Status, len(r.Phase3HostDrillMetadata.HostScopedDrills))
	}
	if r.Phase3DrillReservations != nil && r.Phase3DrillReservations.ID != "" {
		message += fmt.Sprintf(" phase3_drill_reservations=%s reservations=%d", r.Phase3DrillReservations.Status, len(r.Phase3DrillReservations.Reservations))
	}
	if r.Phase3DrillPromotionReadiness != nil && r.Phase3DrillPromotionReadiness.ID != "" {
		message += fmt.Sprintf(" phase3_drill_promotion_readiness=%s reserved_ids=%d checks=%d", r.Phase3DrillPromotionReadiness.Status, len(r.Phase3DrillPromotionReadiness.ReservedIDs), len(r.Phase3DrillPromotionReadiness.Checks))
	}
	if r.Phase0ReleaseEvidenceArchive != nil && r.Phase0ReleaseEvidenceArchive.ID != "" {
		message += fmt.Sprintf(" phase0_release_evidence_archive=%s", r.Phase0ReleaseEvidenceArchive.Status)
	}
	if r.DrillExecution != nil {
		message += fmt.Sprintf(" drill_execution=%s adapter_type=%s assertions=%d skipped=%d", r.DrillExecution.Adapter, r.DrillExecution.AdapterType, r.DrillExecution.Assertions, r.DrillExecution.Skipped)
	}
	if r.ReleaseEvidenceIndexVerification != nil {
		message += fmt.Sprintf(" release_evidence_index=%s", r.ReleaseEvidenceIndexVerification.Status)
	}
	return message
}

func (r gateReport) Markdown() string {
	var builder strings.Builder
	builder.WriteString("# Phase 0 Gate Report\n\n")
	builder.WriteString("| Check | Status | Count |\n")
	builder.WriteString("| --- | --- | ---: |\n")
	builder.WriteString(fmt.Sprintf("| Drill manifest rows | pass | %d |\n", r.DrillRows))
	builder.WriteString(fmt.Sprintf("| Prometheus alert rules | pass | %d |\n", r.AlertRules))
	if r.AlertValidationPassed {
		builder.WriteString("| Alert validation | pass | 1 |\n")
	} else {
		builder.WriteString("| Alert validation | fail | 0 |\n")
	}
	if r.LiveDeployProof.ID != "" {
		builder.WriteString(fmt.Sprintf("| B1 live deploy proof contract | %s | %d |\n", r.LiveDeployProof.Status, len(r.LiveDeployProof.ExpectedEvidence)))
	}
	if r.OperatorInstallPath.ID != "" {
		builder.WriteString(fmt.Sprintf("| B2 operator install path contract | %s | %d |\n", r.OperatorInstallPath.Status, len(r.OperatorInstallPath.ExpectedEvidence)))
	}
	if r.Phase3HostDrillMetadata.ID != "" {
		builder.WriteString(fmt.Sprintf("| B6 Phase 3 host drill candidate catalog | %s | %d |\n", r.Phase3HostDrillMetadata.Status, len(r.Phase3HostDrillMetadata.HostScopedDrills)))
	}
	if r.Phase3DrillReservations != nil && r.Phase3DrillReservations.ID != "" {
		builder.WriteString(fmt.Sprintf("| B7 Phase 3 drill reservations | %s | %d |\n", r.Phase3DrillReservations.Status, len(r.Phase3DrillReservations.Reservations)))
	}
	if r.Phase3DrillPromotionReadiness != nil && r.Phase3DrillPromotionReadiness.ID != "" {
		builder.WriteString(fmt.Sprintf("| B8 Phase 3 drill promotion readiness | %s | %d |\n", r.Phase3DrillPromotionReadiness.Status, len(r.Phase3DrillPromotionReadiness.Checks)))
	}
	if r.Phase0ReleaseEvidenceArchive != nil && r.Phase0ReleaseEvidenceArchive.ID != "" {
		builder.WriteString(fmt.Sprintf("| B9 Phase 0 release evidence archive | %s | %d |\n", r.Phase0ReleaseEvidenceArchive.Status, len(r.Phase0ReleaseEvidenceArchive.RequiredFlags)))
	}

	builder.WriteString("\n## Drill Statuses\n\n")
	builder.WriteString("| Status | Count |\n")
	builder.WriteString("| --- | ---: |\n")
	for _, status := range sortedCountKeys(r.DrillStatuses) {
		builder.WriteString(fmt.Sprintf("| %s | %d |\n", status, r.DrillStatuses[status]))
	}
	if r.Phase3HostDrillMetadata.ID != "" {
		builder.WriteString("\n## Phase 3 Host Drill Candidate Catalog\n\n")
		builder.WriteString("| Drill | Host Scope | Domain | Phase 3 Drill | Execution | Evidence Ref |\n")
		builder.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		for _, drill := range r.Phase3HostDrillMetadata.HostScopedDrills {
			builder.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n", drill.DrillID, drill.HostScope, drill.Domain, drill.Phase3Drill, drill.Execution, drill.EvidenceRef))
		}
	}
	if r.Phase3DrillReservations != nil && r.Phase3DrillReservations.ID != "" {
		builder.WriteString("\n## Phase 3 Drill Reservations\n\n")
		builder.WriteString("| Reservation | Domain | Carrier | Planned Drill | Execution | Rig |\n")
		builder.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		for _, reservation := range r.Phase3DrillReservations.Reservations {
			builder.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n", reservation.ID, reservation.Domain, reservation.CarrierID, reservation.PlannedDrill, reservation.Execution, reservation.Rig))
		}
	}
	if r.Phase3DrillPromotionReadiness != nil && r.Phase3DrillPromotionReadiness.ID != "" {
		builder.WriteString("\n## Phase 3 Drill Promotion Readiness\n\n")
		builder.WriteString("| Status | Source Reservations | Execution | Live Execution |\n")
		builder.WriteString("| --- | --- | --- | --- |\n")
		builder.WriteString(fmt.Sprintf("| %s | %s | %s | %t |\n", r.Phase3DrillPromotionReadiness.Status, r.Phase3DrillPromotionReadiness.SourceReservations, r.Phase3DrillPromotionReadiness.Execution, r.Phase3DrillPromotionReadiness.LiveExecution))
		builder.WriteString("\n| Reserved ID | Domain | Carrier | Unblockers | Evidence Refs |\n")
		builder.WriteString("| --- | --- | --- | --- | ---: |\n")
		for _, check := range r.Phase3DrillPromotionReadiness.Checks {
			builder.WriteString(fmt.Sprintf("| %s | %s | %s | %d | %d |\n", check.ID, check.Domain, check.CarrierID, len(check.RequiredUnblockers), len(check.EvidenceRefs)))
		}
	}
	if r.Phase0ReleaseEvidenceArchive != nil && r.Phase0ReleaseEvidenceArchive.ID != "" {
		builder.WriteString("\n## Phase 0 Release Evidence Archive\n\n")
		builder.WriteString("| Status | Release Evidence | Index | Summary | Verify Files |\n")
		builder.WriteString("| --- | --- | --- | --- | --- |\n")
		builder.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %t |\n", r.Phase0ReleaseEvidenceArchive.Status, r.Phase0ReleaseEvidenceArchive.ReleaseEvidence, r.Phase0ReleaseEvidenceArchive.ReleaseEvidenceIndex, r.Phase0ReleaseEvidenceArchive.ReleaseSummary, r.Phase0ReleaseEvidenceArchive.VerifyEvidenceFiles))
	}
	if r.DrillExecution != nil {
		builder.WriteString("\n## Drill Execution\n\n")
		builder.WriteString("| Adapter | Adapter Type | Assertions | Passed | Failed | Skipped |\n")
		builder.WriteString("| --- | --- | ---: | ---: | ---: | ---: |\n")
		builder.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %d | %d |\n", r.DrillExecution.Adapter, r.DrillExecution.AdapterType, r.DrillExecution.Assertions, r.DrillExecution.Passed, r.DrillExecution.Failed, r.DrillExecution.Skipped))
	}
	if r.ReleaseEvidenceIndexVerification != nil {
		builder.WriteString("\n## Release Evidence Index\n\n")
		builder.WriteString("| Path | Status | Entries |\n")
		builder.WriteString("| --- | --- | ---: |\n")
		builder.WriteString(fmt.Sprintf("| %s | %s | %d |\n", r.ReleaseEvidenceIndexVerification.Path, r.ReleaseEvidenceIndexVerification.Status, r.ReleaseEvidenceIndexVerification.Entries))
	}
	return builder.String()
}

func validationStatus(passed bool) string {
	if passed {
		return "pass"
	}
	return "fail"
}

func formatCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	keys := sortedCountKeys(counts)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

func sortedCountKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
