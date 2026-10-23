package api

import (
	"context"
	"strings"

	openapiv1alpha "github.com/midnight/midnight/api/openapi/v1alpha"
	"github.com/midnight/midnight/internal/db"
	"gopkg.in/yaml.v3"
)

const (
	APIDataReadinessStatusReady       = "ready"
	APIDataReadinessStatusNeedsReview = "needs-review"
	APIDataReadinessStatusBlocked     = "blocked"

	APIDataReadinessIssueReleaseEvidence = "RELEASE_EVIDENCE_NOT_READY"
	APIDataReadinessIssueEvidenceIndex   = "EVIDENCE_INDEX_NOT_READY"
	APIDataReadinessIssueOpenAPIEmpty    = "OPENAPI_UNAVAILABLE"
	APIDataReadinessIssueOpenAPIInvalid  = "OPENAPI_INVALID"
	APIDataReadinessIssueOperationStore  = "OPERATION_STORE_UNHEALTHY"
)

type APIDataReadinessInput struct {
	ReleaseEvidence db.ReleaseEvidencePacket
	EvidenceIndex   db.APISchemaEvidenceIndex
	OpenAPIYAML     []byte
	OperationStore  OperationStore
}

type APIDataReadinessSummary struct {
	Status         string                         `json:"status"`
	IssueCodes     []string                       `json:"issue_codes"`
	Issues         []APIDataReadinessIssue        `json:"issues"`
	Release        APIDataReadinessComponent      `json:"release"`
	EvidenceIndex  APIDataReadinessComponent      `json:"evidence_index"`
	OpenAPI        APIDataReadinessOpenAPI        `json:"openapi"`
	OperationStore APIDataReadinessOperationStore `json:"operation_store"`
}

type APIDataReadinessComponent struct {
	Status string `json:"status"`
}

type APIDataReadinessOpenAPI struct {
	Status    string `json:"status"`
	Available bool   `json:"available"`
	ByteCount int    `json:"byte_count"`
}

type APIDataReadinessOperationStore struct {
	Status         string `json:"status"`
	ListAvailable  bool   `json:"list_available"`
	OperationCount int    `json:"operation_count"`
}

type APIDataReadinessIssue struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func BuildAPIDataReadinessSummary(ctx context.Context, input APIDataReadinessInput) APIDataReadinessSummary {
	if input.OpenAPIYAML == nil {
		input.OpenAPIYAML = openapiv1alpha.MidnightYAML
	}
	summary := APIDataReadinessSummary{
		Status:     APIDataReadinessStatusReady,
		IssueCodes: []string{},
		Issues:     []APIDataReadinessIssue{},
		Release: APIDataReadinessComponent{
			Status: normalizeAPIDataReadinessStatus(input.ReleaseEvidence.Status),
		},
		EvidenceIndex: APIDataReadinessComponent{
			Status: normalizeAPIDataReadinessStatus(input.EvidenceIndex.Status),
		},
		OpenAPI: APIDataReadinessOpenAPI{
			Status:    APIDataReadinessStatusReady,
			Available: true,
			ByteCount: len(input.OpenAPIYAML),
		},
		OperationStore: APIDataReadinessOperationStore{
			Status:        APIDataReadinessStatusReady,
			ListAvailable: true,
		},
	}

	if summary.Release.Status != APIDataReadinessStatusReady {
		summary.addIssue(APIDataReadinessIssueReleaseEvidence, "release evidence is not ready", map[string]any{"status": summary.Release.Status, "issue_codes": input.ReleaseEvidence.IssueCodes})
	}
	if summary.EvidenceIndex.Status != APIDataReadinessStatusReady {
		summary.addIssue(APIDataReadinessIssueEvidenceIndex, "evidence index is not ready", map[string]any{"status": summary.EvidenceIndex.Status})
	}

	if len(input.OpenAPIYAML) == 0 {
		summary.OpenAPI.Status = APIDataReadinessStatusBlocked
		summary.OpenAPI.Available = false
		summary.addIssue(APIDataReadinessIssueOpenAPIEmpty, "OpenAPI contract is unavailable", nil)
	} else if !supportedOpenAPIVersion(input.OpenAPIYAML) {
		summary.OpenAPI.Status = APIDataReadinessStatusNeedsReview
		summary.addIssue(APIDataReadinessIssueOpenAPIInvalid, "OpenAPI contract does not declare the expected version", nil)
	}

	if input.OperationStore == nil {
		summary.OperationStore.Status = APIDataReadinessStatusBlocked
		summary.OperationStore.ListAvailable = false
		summary.addIssue(APIDataReadinessIssueOperationStore, "operation store is unavailable", nil)
	} else {
		operations, err := input.OperationStore.List(ctx, OperationListFilter{})
		if err != nil {
			summary.OperationStore.Status = APIDataReadinessStatusBlocked
			summary.OperationStore.ListAvailable = false
			summary.addIssue(APIDataReadinessIssueOperationStore, "operation store list check failed", map[string]any{"error": err.Error()})
		} else {
			summary.OperationStore.OperationCount = len(operations)
		}
	}

	summary.Status = rollupAPIDataReadinessStatus(summary.Release.Status, summary.EvidenceIndex.Status, summary.OpenAPI.Status, summary.OperationStore.Status)
	return summary
}

func supportedOpenAPIVersion(document []byte) bool {
	var header struct {
		OpenAPI string `yaml:"openapi"`
	}
	return yaml.Unmarshal(document, &header) == nil && strings.HasPrefix(header.OpenAPI, "3.1.")
}

func (summary *APIDataReadinessSummary) addIssue(code, message string, details map[string]any) {
	summary.IssueCodes = append(summary.IssueCodes, code)
	summary.Issues = append(summary.Issues, APIDataReadinessIssue{Code: code, Message: message, Details: details})
}

func rollupAPIDataReadinessStatus(statuses ...string) string {
	status := APIDataReadinessStatusReady
	for _, componentStatus := range statuses {
		componentStatus = normalizeAPIDataReadinessStatus(componentStatus)
		if componentStatus == APIDataReadinessStatusBlocked {
			return APIDataReadinessStatusBlocked
		}
		if componentStatus == APIDataReadinessStatusNeedsReview {
			status = APIDataReadinessStatusNeedsReview
		}
	}
	return status
}

func normalizeAPIDataReadinessStatus(status string) string {
	switch status {
	case APIDataReadinessStatusReady, APIDataReadinessStatusNeedsReview, APIDataReadinessStatusBlocked:
		return status
	default:
		return APIDataReadinessStatusBlocked
	}
}
