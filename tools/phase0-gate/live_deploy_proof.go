package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	liveDeployProofID     = "phase0.beta.b1.live-deploy-proof"
	liveDeployProofStatus = "pass"
	liveDeployProofMode   = "offline-contract"
)

// liveDeployProofContract records the evidence a real B1 live deploy must
// archive. The phase0 gate validates this contract offline; it never attempts
// to install, deploy, call DNS, or probe a network endpoint by default.
type liveDeployProofContract struct {
	ID               string                          `json:"id"`
	Sprint           string                          `json:"sprint"`
	Status           string                          `json:"status"`
	Mode             string                          `json:"mode"`
	OfflineByDefault bool                            `json:"offlineByDefault"`
	GeneratedAt      generatedTimestampEvidence      `json:"generatedAt"`
	ExpectedEvidence []liveDeployEvidenceExpectation `json:"expectedEvidence"`
	RequiredChecks   []liveDeployProofRequiredCheck  `json:"requiredChecks"`
}

type liveDeployEvidenceExpectation struct {
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	Role           string   `json:"role"`
	Required       bool     `json:"required"`
	RequiredFields []string `json:"requiredFields"`
}

type liveDeployProofRequiredCheck struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Evidence    []string `json:"evidence"`
}

func buildLiveDeployProofContract() (liveDeployProofContract, error) {
	contract := liveDeployProofContract{
		ID:               liveDeployProofID,
		Sprint:           "Phase 0 Beta Sprint B1",
		Status:           liveDeployProofStatus,
		Mode:             liveDeployProofMode,
		OfflineByDefault: true,
		GeneratedAt:      generatedTimestampEvidence{Value: releaseEvidenceGeneratedAt, Source: "phase0-gate deterministic placeholder"},
		ExpectedEvidence: []liveDeployEvidenceExpectation{
			{
				Name:     "b1-live-deploy-commit",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/commit.txt",
				Role:     "source-revision",
				Required: true,
				RequiredFields: []string{
					"git-sha",
				},
			},
			{
				Name:     "b1-live-deploy-git-status",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/git-status.txt",
				Role:     "workspace-state",
				Required: true,
				RequiredFields: []string{
					"status-lines",
				},
			},
			{
				Name:     "b1-live-deploy-docker-server",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/docker-server.json",
				Role:     "docker-runtime",
				Required: true,
				RequiredFields: []string{
					"Server",
					"Version",
					"ApiVersion",
				},
			},
			{
				Name:     "b1-live-deploy-release-check",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/release-check.log",
				Role:     "local-gate-prerequisite",
				Required: true,
				RequiredFields: []string{
					"exit-code-0",
					"phase0-gate",
				},
			},
			{
				Name:     "b1-live-deploy-doctor-live",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/doctor-live.json",
				Role:     "live-doctor-report",
				Required: true,
				RequiredFields: []string{
					"exitCode",
					"checks",
					"OR-CHK-DOC-001",
					"OR-CHK-DOC-009",
				},
			},
			{
				Name:     "b1-live-deploy-systemd-status",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/systemd-status.txt",
				Role:     "service-status",
				Required: true,
				RequiredFields: []string{
					"midnight-server",
					"midnight-worker",
					"traefik",
				},
			},
			{
				Name:     "b1-live-deploy-service-startup",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/service-startup.log",
				Role:     "service-startup-logs",
				Required: true,
				RequiredFields: []string{
					"storage=postgres",
					"runtime=docker",
					"route_configured=true",
				},
			},
			{
				Name:     "b1-live-deploy-config",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/midnight-b1.json",
				Role:     "proof-project-config",
				Required: true,
				RequiredFields: []string{
					"project-slug",
					"service-slug",
				},
			},
			{
				Name:     "b1-live-deploy-config-apply",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/config-apply.json",
				Role:     "config-apply-operation",
				Required: true,
				RequiredFields: []string{
					"operationId",
					"status",
				},
			},
			{
				Name:     "b1-live-deploy-services",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/services.json",
				Role:     "service-lookup",
				Required: true,
				RequiredFields: []string{
					"serviceId",
					"serviceSlug",
				},
			},
			{
				Name:     "b1-live-deploy-request",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/deploy-request.json",
				Role:     "deploy-intake-request",
				Required: true,
				RequiredFields: []string{
					"environment_id",
					"image",
					"deployment_id",
					"instance_id",
					"route",
				},
			},
			{
				Name:     "b1-live-deploy-response-headers",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/deploy-response.headers",
				Role:     "deploy-intake-response-headers",
				Required: true,
				RequiredFields: []string{
					"http-status-202",
					"idempotency-key",
				},
			},
			{
				Name:     "b1-live-deploy-response",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/deploy-response.json",
				Role:     "deploy-intake-response",
				Required: true,
				RequiredFields: []string{
					"operationId",
					"kind=deploy",
					"state=queued",
				},
			},
			{
				Name:     "b1-live-deploy-operation-events",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/operation-events-<n>.sse",
				Role:     "operation-event-stream",
				Required: true,
				RequiredFields: []string{
					"queued",
					"RUNNING",
					"pull-verify",
					"schedule",
					"start",
					"readiness-routing-check",
					"promote",
					"SUCCEEDED",
				},
			},
			{
				Name:     "b1-live-deploy-docker-ps",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/docker-ps.jsonl",
				Role:     "candidate-container",
				Required: true,
				RequiredFields: []string{
					"deployment-id",
					"running-container",
				},
			},
			{
				Name:     "b1-live-deploy-traefik-stat",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/traefik-dynamic-stat.txt",
				Role:     "route-artifact-stat",
				Required: true,
				RequiredFields: []string{
					"traefik.dynamic.yaml",
					"route-group",
				},
			},
			{
				Name:     "b1-live-deploy-traefik-log",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/traefik-after-deploy.log",
				Role:     "route-reload-log",
				Required: true,
				RequiredFields: []string{
					"no-reload-errors",
				},
			},
			{
				Name:     "b1-live-deploy-route-response-headers",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/route-response.headers",
				Role:     "public-route-response-headers",
				Required: true,
				RequiredFields: []string{
					"http-status-2xx",
					"host",
				},
			},
			{
				Name:     "b1-live-deploy-route-response-body",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/route-response.body",
				Role:     "candidate-app-response",
				Required: true,
				RequiredFields: []string{
					"candidate-app-marker",
					"no-secrets",
				},
			},
			{
				Name:     "b1-live-deploy-certificate-summary",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/certificate-summary.txt",
				Role:     "https-certificate-summary",
				Required: false,
				RequiredFields: []string{
					"subject",
					"issuer",
					"notBefore",
					"notAfter",
				},
			},
			{
				Name:     "b1-live-deploy-operator-notes",
				Path:     "artifacts/phase0/sprint-b1-live-deploy/<run-id>/operator-notes.md",
				Role:     "operator-signoff",
				Required: true,
				RequiredFields: []string{
					"run-id",
					"commit",
					"public-host",
					"image",
					"result",
				},
			},
		},
		RequiredChecks: []liveDeployProofRequiredCheck{
			{
				ID:          "b1.local-profile",
				Description: "live proof records local profile or documented public-route exception and never hides DNS/TLS assumptions",
				Evidence:    []string{"b1-live-deploy-operator-notes", "b1-live-deploy-route-response-headers", "b1-live-deploy-certificate-summary"},
			},
			{
				ID:          "b1.release-gate-prerequisite",
				Description: "local phase0 release check passed before live proof execution",
				Evidence:    []string{"b1-live-deploy-release-check"},
			},
			{
				ID:          "b1.live-doctor-ready",
				Description: "live doctor checks captured host and package readiness before deploy intake",
				Evidence:    []string{"b1-live-deploy-doctor-live"},
			},
			{
				ID:          "b1.deploy-active",
				Description: "deploy operation reaches ACTIVE and records the deployment id, instance id, and image digest",
				Evidence:    []string{"b1-live-deploy-request", "b1-live-deploy-response", "b1-live-deploy-operation-events", "b1-live-deploy-docker-ps"},
			},
			{
				ID:          "b1.timeline-complete",
				Description: "timeline answers source, builder, digest, host, start, health, route, TLS, and failure fields",
				Evidence:    []string{"b1-live-deploy-operation-events", "b1-live-deploy-traefik-stat", "b1-live-deploy-traefik-log"},
			},
			{
				ID:          "b1.archive-integrity",
				Description: "every live proof artifact path is workspace-relative and support-safe",
				Evidence:    []string{"b1-live-deploy-git-status", "b1-live-deploy-operator-notes"},
			},
		},
	}
	if err := validateLiveDeployProofContract(contract); err != nil {
		return liveDeployProofContract{}, err
	}
	return contract, nil
}

func validateLiveDeployProofContract(contract liveDeployProofContract) error {
	if contract.ID != liveDeployProofID {
		return fmt.Errorf("live deploy proof contract id = %q, want %q", contract.ID, liveDeployProofID)
	}
	if contract.Sprint != "Phase 0 Beta Sprint B1" {
		return fmt.Errorf("live deploy proof contract sprint = %q, want Phase 0 Beta Sprint B1", contract.Sprint)
	}
	if contract.Status != liveDeployProofStatus {
		return fmt.Errorf("live deploy proof contract status = %q, want %q", contract.Status, liveDeployProofStatus)
	}
	if contract.Mode != liveDeployProofMode || !contract.OfflineByDefault {
		return errors.New("live deploy proof contract must remain offline by default")
	}
	if contract.GeneratedAt.Value != releaseEvidenceGeneratedAt || strings.TrimSpace(contract.GeneratedAt.Source) == "" {
		return errors.New("live deploy proof contract must use deterministic generatedAt evidence")
	}
	if len(contract.ExpectedEvidence) == 0 {
		return errors.New("live deploy proof contract requires expected evidence")
	}
	evidenceNames := make(map[string]struct{}, len(contract.ExpectedEvidence))
	for i, item := range contract.ExpectedEvidence {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return fmt.Errorf("live deploy proof expectedEvidence[%d] name is required", i)
		}
		if _, ok := evidenceNames[name]; ok {
			return fmt.Errorf("live deploy proof expectedEvidence[%d] duplicates name %q", i, name)
		}
		evidenceNames[name] = struct{}{}
		if strings.TrimSpace(item.Role) == "" {
			return fmt.Errorf("live deploy proof expectedEvidence[%d] role is required", i)
		}
		if !item.Required && item.Name != "b1-live-deploy-certificate-summary" {
			return fmt.Errorf("live deploy proof expectedEvidence[%d] optional evidence is only allowed for HTTPS certificate summary", i)
		}
		if err := validateRelativeSafeEvidenceRefPath(item.Path); err != nil {
			return fmt.Errorf("live deploy proof expectedEvidence[%d] path: %w", i, err)
		}
		if filepath.ToSlash(item.Path) != item.Path {
			return fmt.Errorf("live deploy proof expectedEvidence[%d] path must use slash separators: %s", i, item.Path)
		}
		if len(item.RequiredFields) == 0 {
			return fmt.Errorf("live deploy proof expectedEvidence[%d] requires fields", i)
		}
	}
	if len(contract.RequiredChecks) == 0 {
		return errors.New("live deploy proof contract requires checks")
	}
	checkIDs := make(map[string]struct{}, len(contract.RequiredChecks))
	for i, check := range contract.RequiredChecks {
		id := strings.TrimSpace(check.ID)
		if id == "" {
			return fmt.Errorf("live deploy proof requiredChecks[%d] id is required", i)
		}
		if _, ok := checkIDs[id]; ok {
			return fmt.Errorf("live deploy proof requiredChecks[%d] duplicates id %q", i, id)
		}
		checkIDs[id] = struct{}{}
		if strings.TrimSpace(check.Description) == "" {
			return fmt.Errorf("live deploy proof requiredChecks[%d] description is required", i)
		}
		if len(check.Evidence) == 0 {
			return fmt.Errorf("live deploy proof requiredChecks[%d] evidence is required", i)
		}
		for _, name := range check.Evidence {
			if _, ok := evidenceNames[name]; !ok {
				return fmt.Errorf("live deploy proof requiredChecks[%d] references unknown evidence %q", i, name)
			}
		}
	}
	return nil
}
