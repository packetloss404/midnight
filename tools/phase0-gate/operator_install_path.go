package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	operatorInstallPathID     = "phase0.beta.b2.operator-install-path"
	operatorInstallPathStatus = "pass"
	operatorInstallPathMode   = "offline-contract"
)

// operatorInstallPathContract records the evidence a real B2 operator install
// run must archive. The phase0 gate validates this contract offline; it never
// installs packages, starts services, probes systemd, or touches host paths.
type operatorInstallPathContract struct {
	ID               string                               `json:"id"`
	Sprint           string                               `json:"sprint"`
	Status           string                               `json:"status"`
	Mode             string                               `json:"mode"`
	OfflineByDefault bool                                 `json:"offlineByDefault"`
	GeneratedAt      generatedTimestampEvidence           `json:"generatedAt"`
	ExpectedEvidence []operatorInstallEvidenceExpectation `json:"expectedEvidence"`
	RequiredChecks   []operatorInstallPathRequiredCheck   `json:"requiredChecks"`
}

type operatorInstallEvidenceExpectation struct {
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	Role           string   `json:"role"`
	Required       bool     `json:"required"`
	RequiredFields []string `json:"requiredFields"`
}

type operatorInstallPathRequiredCheck struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Evidence    []string `json:"evidence"`
}

func buildOperatorInstallPathContract() (operatorInstallPathContract, error) {
	contract := operatorInstallPathContract{
		ID:               operatorInstallPathID,
		Sprint:           "Phase 0 Beta Sprint B2",
		Status:           operatorInstallPathStatus,
		Mode:             operatorInstallPathMode,
		OfflineByDefault: true,
		GeneratedAt:      generatedTimestampEvidence{Value: releaseEvidenceGeneratedAt, Source: "phase0-gate deterministic placeholder"},
		ExpectedEvidence: []operatorInstallEvidenceExpectation{
			{
				Name:     "b2-operator-install-package",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/package-install.log",
				Role:     "package-install",
				Required: true,
				RequiredFields: []string{
					"midnight.deb",
					"apt-install",
					"exit-code-0",
				},
			},
			{
				Name:     "b2-operator-install-env-file",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/midnight-env.stat",
				Role:     "operator-env-file",
				Required: true,
				RequiredFields: []string{
					"/etc/midnight/midnight.env",
					"root:midnight",
					"0640",
					"MIDNIGHT_DB_DSN_FILE",
					"MIDNIGHT_TRAEFIK_DYNAMIC_DIR",
				},
			},
			{
				Name:     "b2-operator-install-systemd-units",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/systemd-units.txt",
				Role:     "systemd-unit-set",
				Required: true,
				RequiredFields: []string{
					"midnight-server.service",
					"midnight-worker.service",
					"midnight-agent.service",
					"midnight-buildkit.service",
					"midnight-github-status-worker.service",
					"midnight-registry.service",
					"no-auto-start-before-doctor",
				},
			},
			{
				Name:     "b2-operator-install-traefik-dynamic-root",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/traefik-dynamic-root.txt",
				Role:     "traefik-dynamic-root",
				Required: true,
				RequiredFields: []string{
					"/etc/midnight/traefik/dynamic",
					"absolute-path",
					"writable-route-group-dir",
					"traefik.dynamic.yaml",
				},
			},
			{
				Name:     "b2-operator-install-doctor-postinst",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/doctor-postinst.json",
				Role:     "postinstall-doctor-output",
				Required: true,
				RequiredFields: []string{
					"exitCode",
					"checks",
					"OR-CHK-DOC-010",
					"OR-CHK-DOC-011",
					"OR-CHK-DOC-012",
					"OR-CHK-DOC-013",
				},
			},
			{
				Name:     "b2-operator-install-service-enable",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/systemctl-enable-now.log",
				Role:     "operator-service-enable",
				Required: true,
				RequiredFields: []string{
					"systemctl enable --now",
					"midnight-server",
					"midnight-worker",
					"midnight-agent",
					"midnight-buildkit",
					"midnight-registry",
					"exit-code-0",
				},
			},
			{
				Name:     "b2-operator-install-doctor-after-enable",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/doctor-after-enable.json",
				Role:     "final-doctor-output",
				Required: true,
				RequiredFields: []string{
					"exitCode",
					"checks",
					"OR-CHK-DOC-014",
					"OR-CHK-DOC-015",
					"OR-CHK-DOC-016",
					"OR-CHK-DOC-017",
				},
			},
			{
				Name:     "b2-operator-install-b1-handoff",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/b1-handoff.json",
				Role:     "b1-live-deploy-handoff",
				Required: true,
				RequiredFields: []string{
					"phase0.beta.b1.live-deploy-proof",
					"b1-run-id",
					"operator-notes",
					"route-config-dir",
					"public-host",
				},
			},
			{
				Name:     "b2-operator-install-operator-notes",
				Path:     "artifacts/phase0/sprint-b2-operator-install/<run-id>/operator-notes.md",
				Role:     "operator-signoff",
				Required: true,
				RequiredFields: []string{
					"run-id",
					"commit",
					"host",
					"install-mode",
					"result",
				},
			},
		},
		RequiredChecks: []operatorInstallPathRequiredCheck{
			{
				ID:          "b2.package-install-offline",
				Description: "operator install evidence is captured without the phase0 gate invoking package managers or host probes",
				Evidence:    []string{"b2-operator-install-package", "b2-operator-install-operator-notes"},
			},
			{
				ID:          "b2.env-file-present",
				Description: "installed environment file evidence records path, ownership, mode, and required route/database hints",
				Evidence:    []string{"b2-operator-install-env-file"},
			},
			{
				ID:          "b2.systemd-unit-set",
				Description: "all five panel-locked Midnight systemd units are present and remain operator-enabled after doctor review",
				Evidence:    []string{"b2-operator-install-systemd-units", "b2-operator-install-service-enable"},
			},
			{
				ID:          "b2.traefik-dynamic-root",
				Description: "Traefik dynamic publication root is absolute, writable for route-group artifacts, and matches file-provider shape",
				Evidence:    []string{"b2-operator-install-traefik-dynamic-root"},
			},
			{
				ID:          "b2.doctor-green",
				Description: "post-install and post-enable doctor JSON is archived with package boot and route readiness checks",
				Evidence:    []string{"b2-operator-install-doctor-postinst", "b2-operator-install-doctor-after-enable"},
			},
			{
				ID:          "b2.b1-handoff",
				Description: "operator install proof carries the B1 live deploy handoff so route and public-host assumptions remain traceable",
				Evidence:    []string{"b2-operator-install-b1-handoff"},
			},
		},
	}
	if err := validateOperatorInstallPathContract(contract); err != nil {
		return operatorInstallPathContract{}, err
	}
	return contract, nil
}

func validateOperatorInstallPathContract(contract operatorInstallPathContract) error {
	if contract.ID != operatorInstallPathID {
		return fmt.Errorf("operator install path contract id = %q, want %q", contract.ID, operatorInstallPathID)
	}
	if contract.Sprint != "Phase 0 Beta Sprint B2" {
		return fmt.Errorf("operator install path contract sprint = %q, want Phase 0 Beta Sprint B2", contract.Sprint)
	}
	if contract.Status != operatorInstallPathStatus {
		return fmt.Errorf("operator install path contract status = %q, want %q", contract.Status, operatorInstallPathStatus)
	}
	if contract.Mode != operatorInstallPathMode || !contract.OfflineByDefault {
		return errors.New("operator install path contract must remain offline by default")
	}
	if contract.GeneratedAt.Value != releaseEvidenceGeneratedAt || strings.TrimSpace(contract.GeneratedAt.Source) == "" {
		return errors.New("operator install path contract must use deterministic generatedAt evidence")
	}
	if len(contract.ExpectedEvidence) == 0 {
		return errors.New("operator install path contract requires expected evidence")
	}
	evidenceNames := make(map[string]struct{}, len(contract.ExpectedEvidence))
	for i, item := range contract.ExpectedEvidence {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return fmt.Errorf("operator install path expectedEvidence[%d] name is required", i)
		}
		if _, ok := evidenceNames[name]; ok {
			return fmt.Errorf("operator install path expectedEvidence[%d] duplicates name %q", i, name)
		}
		evidenceNames[name] = struct{}{}
		if strings.TrimSpace(item.Role) == "" {
			return fmt.Errorf("operator install path expectedEvidence[%d] role is required", i)
		}
		if !item.Required {
			return fmt.Errorf("operator install path expectedEvidence[%d] must be required", i)
		}
		if err := validateRelativeSafeEvidenceRefPath(item.Path); err != nil {
			return fmt.Errorf("operator install path expectedEvidence[%d] path: %w", i, err)
		}
		if filepath.ToSlash(item.Path) != item.Path {
			return fmt.Errorf("operator install path expectedEvidence[%d] path must use slash separators: %s", i, item.Path)
		}
		if len(item.RequiredFields) == 0 {
			return fmt.Errorf("operator install path expectedEvidence[%d] requires fields", i)
		}
	}
	for _, requiredName := range []string{
		"b2-operator-install-env-file",
		"b2-operator-install-systemd-units",
		"b2-operator-install-traefik-dynamic-root",
		"b2-operator-install-doctor-postinst",
		"b2-operator-install-doctor-after-enable",
		"b2-operator-install-b1-handoff",
	} {
		if _, ok := evidenceNames[requiredName]; !ok {
			return fmt.Errorf("operator install path contract missing required evidence %q", requiredName)
		}
	}
	if len(contract.RequiredChecks) == 0 {
		return errors.New("operator install path contract requires checks")
	}
	checkIDs := make(map[string]struct{}, len(contract.RequiredChecks))
	for i, check := range contract.RequiredChecks {
		id := strings.TrimSpace(check.ID)
		if id == "" {
			return fmt.Errorf("operator install path requiredChecks[%d] id is required", i)
		}
		if _, ok := checkIDs[id]; ok {
			return fmt.Errorf("operator install path requiredChecks[%d] duplicates id %q", i, id)
		}
		checkIDs[id] = struct{}{}
		if strings.TrimSpace(check.Description) == "" {
			return fmt.Errorf("operator install path requiredChecks[%d] description is required", i)
		}
		if len(check.Evidence) == 0 {
			return fmt.Errorf("operator install path requiredChecks[%d] evidence is required", i)
		}
		for _, name := range check.Evidence {
			if _, ok := evidenceNames[name]; !ok {
				return fmt.Errorf("operator install path requiredChecks[%d] references unknown evidence %q", i, name)
			}
		}
	}
	return nil
}
