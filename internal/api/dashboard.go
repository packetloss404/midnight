package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/notifications"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/pkg/agentapi"
	"github.com/midnight/midnight/pkg/orailtypes"
)

var dashboardSampleTime = time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

const (
	defaultPhase0ProofPacketDir             = "artifacts/phase0"
	defaultPhase0DrillManifestPath          = "test/drills/manifest.yaml"
	defaultPhase0B7ReservationIndexPath     = "test/drills/phase3-reservations.yaml"
	defaultPhase0B8PromotionReadinessPath   = "test/drills/phase3-promotion-readiness.yaml"
	defaultPhase0B9ReleaseArchivePath       = "test/drills/phase0-release-evidence-archive.yaml"
	phase0ProofPacketSummarySchemaVersion   = "midnight.phase0.proof-packet-summary.v1"
	phase0B5HostDrillSummarySchemaVersion   = "midnight.phase0.beta-b5.host-drill-metadata-summary.v1"
	phase0B6CandidateCatalogSchemaVersion   = "midnight.phase0.beta-b6.host-drill-candidate-catalog.v1"
	phase0B7ReservationIndexSchemaVersion   = "midnight.phase0.beta-b7.host-drill-reservation-index.v1"
	phase0B8PromotionReadinessSchemaVersion = "midnight.phase0.beta-b8.drill-promotion-readiness.v1"
	phase0B9ReleaseArchiveSchemaVersion     = "midnight.phase0.beta-b9.release-evidence-archive.v1"
	phase0ProofStatusReady                  = "ready"
	phase0ProofStatusNeedsReview            = "needs-review"
	phase0ProofStatusBlocked                = "blocked"
	phase0ProofIssueReleaseEvidenceMissing  = "PHASE0_RELEASE_EVIDENCE_MISSING"   // not-a-failure-code
	phase0ProofIssueReleaseEvidenceInvalid  = "PHASE0_RELEASE_EVIDENCE_INVALID"   // not-a-failure-code
	phase0ProofIssueReleaseEvidenceNotReady = "PHASE0_RELEASE_EVIDENCE_NOT_READY" // not-a-failure-code
	phase0ProofIssueReleaseSummaryMissing   = "PHASE0_RELEASE_SUMMARY_MISSING"    // not-a-failure-code
	phase0ProofIssueReleaseSummaryInvalid   = "PHASE0_RELEASE_SUMMARY_INVALID"    // not-a-failure-code
	phase0ProofIssueReleaseSummaryNotReady  = "PHASE0_RELEASE_SUMMARY_NOT_READY"  // not-a-failure-code
	phase0ProofIssueEvidenceIndexMissing    = "PHASE0_EVIDENCE_INDEX_MISSING"     // not-a-failure-code
	phase0ProofIssueEvidenceIndexInvalid    = "PHASE0_EVIDENCE_INDEX_INVALID"     // not-a-failure-code
	phase0ProofIssueEvidenceIndexNotPass    = "PHASE0_EVIDENCE_INDEX_NOT_PASS"    // not-a-failure-code
	phase0B5IssueDrillManifestMissing       = "PHASE0_B5_DRILL_MANIFEST_MISSING"  // not-a-failure-code
	phase0B5IssueDrillManifestInvalid       = "PHASE0_B5_DRILL_MANIFEST_INVALID"  // not-a-failure-code
	phase0B5IssueAbsoluteArtifactRef        = "PHASE0_B5_ABSOLUTE_ARTIFACT_REF"   // not-a-failure-code
	phase0B8IssuePromotionReadinessInvalid  = "PHASE0_B8_PROMOTION_READINESS_INVALID"
	phase0B9IssueReleaseArchiveInvalid      = "PHASE0_B9_RELEASE_EVIDENCE_ARCHIVE_INVALID"
	dashboardRouteNotPromoted               = "not-promoted"
	dashboardRouteNotReported               = "not-reported"
)

type dashboardSummary struct {
	Status              string `json:"status"`
	APIVersion          string `json:"api_version"`
	ServerVersion       string `json:"server_version"`
	ProjectCount        int    `json:"project_count"`
	ServiceCount        int    `json:"service_count"`
	OperationCount      int    `json:"operation_count"`
	AuditStatus         string `json:"audit_status"`
	AuditEventCount     int    `json:"audit_event_count"`
	TemplateCardCount   int    `json:"template_card_count"`
	NotificationPreview string `json:"notification_preview"`
}

type dashboardRuntimeInventory struct {
	SchemaVersion string                    `json:"schema_version"`
	GeneratedAt   time.Time                 `json:"generated_at"`
	Projects      []Project                 `json:"projects"`
	Services      []dashboardRuntimeService `json:"services"`
	Operations    []orailtypes.Operation    `json:"operations"`
	Agents        []AgentSnapshot           `json:"agents"`
	Hosts         []dashboardRuntimeHost    `json:"hosts"`
	Routes        []dashboardRuntimeRoute   `json:"routes"`
}

type dashboardRuntimeService struct {
	ID             string                         `json:"id"`
	ProjectID      string                         `json:"project_id,omitempty"`
	Slug           string                         `json:"slug,omitempty"`
	Name           string                         `json:"name"`
	Kind           string                         `json:"kind"`
	Engine         string                         `json:"engine,omitempty"`
	EngineVersion  string                         `json:"engine_version,omitempty"`
	TemplateID     string                         `json:"template_id,omitempty"`
	DeploymentID   string                         `json:"deployment_id,omitempty"`
	InstanceID     string                         `json:"instance_id,omitempty"`
	OperationID    string                         `json:"operation_id,omitempty"`
	OperationState string                         `json:"operation_state,omitempty"`
	Queue          string                         `json:"queue,omitempty"`
	JobID          string                         `json:"job_id,omitempty"`
	JobState       string                         `json:"job_state,omitempty"`
	RouteIntent    string                         `json:"route_intent,omitempty"`
	LastEvent      string                         `json:"last_event,omitempty"`
	LastEventAt    *time.Time                     `json:"last_event_at,omitempty"`
	RuntimeState   string                         `json:"runtime_state"`
	Version        string                         `json:"version"`
	Replicas       string                         `json:"replicas"`
	Route          string                         `json:"route"`
	Health         string                         `json:"health"`
	Updated        string                         `json:"updated"`
	CreatedAt      time.Time                      `json:"created_at"`
	UpdatedAt      time.Time                      `json:"updated_at"`
	Connection     *templates.ConnectionGuidance  `json:"connection,omitempty"`
	CredentialRefs []templates.CredentialRef      `json:"credential_refs,omitempty"`
	Volume         *templates.VolumePlan          `json:"volume,omitempty"`
	Backup         *templates.BackupPlan          `json:"backup,omitempty"`
	RestorePlan    *templates.RestorePlanGuidance `json:"restore_plan_guidance,omitempty"`
	Networking     *templates.NetworkPlan         `json:"networking,omitempty"`
}

type dashboardRuntimeHost struct {
	Name          string    `json:"name"`
	Profile       string    `json:"profile"`
	Docker        string    `json:"docker"`
	Cgroup        string    `json:"cgroup"`
	Doctor        string    `json:"doctor"`
	AgentID       string    `json:"agent_id"`
	Version       string    `json:"version,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
}

type dashboardRuntimeRoute struct {
	Target        string `json:"target"`
	Route         string `json:"route"`
	Source        string `json:"source"`
	State         string `json:"state"`
	Detail        string `json:"detail"`
	ServiceID     string `json:"service_id,omitempty"`
	Service       string `json:"service,omitempty"`
	Host          string `json:"host,omitempty"`
	RouteName     string `json:"route_name,omitempty"`
	DeploymentID  string `json:"deployment_id,omitempty"`
	InstanceID    string `json:"instance_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
}

type dashboardTemplateDeployRequest struct {
	TemplateID   string `json:"template_id"`
	ServiceName  string `json:"service_name,omitempty"`
	DatabaseName string `json:"database_name,omitempty"`
}

type dashboardTemplateDeployResponse struct {
	SchemaVersion string                      `json:"schema_version"`
	Service       dashboardRuntimeService     `json:"service"`
	Operation     orailtypes.Operation        `json:"operation"`
	Plan          templates.InstantiationPlan `json:"plan"`
	Queue         string                      `json:"queue,omitempty"`
	JobID         string                      `json:"job_id,omitempty"`
	JobState      string                      `json:"job_state,omitempty"`
	Message       string                      `json:"message"`
}

type dashboardRuntimeServiceMetadata struct {
	TemplateID     string
	DatabaseName   string
	DeploymentID   string
	InstanceID     string
	OperationID    string
	OperationState orailtypes.OperationState
	Queue          queue.Name
	JobID          string
	JobState       queue.JobState
	RouteIntent    string
	Route          string
	InstanceState  string
	InstanceReason string
	InstanceAt     time.Time
	LastEvent      string
	LastEventAt    time.Time
}

type phase0ProofPacketSummary struct {
	SchemaVersion             string                                 `json:"schema_version"`
	Status                    string                                 `json:"status"`
	IssueCodes                []string                               `json:"issue_codes"`
	ReleaseEvidence           phase0ProofArtifactStatus              `json:"release_evidence"`
	EvidenceIndex             phase0ProofArtifactStatus              `json:"evidence_index"`
	EvidenceIndexVerification phase0EvidenceIndexVerificationSummary `json:"evidence_index_verification"`
	ReleaseSummary            phase0ReleaseSummarySnapshot           `json:"release_summary"`
	Command                   phase0OptionalValue                    `json:"command"`
	RunnerOS                  phase0OptionalValue                    `json:"runner_os"`
	CommitSHA                 phase0OptionalValue                    `json:"commit_sha"`
	OperatorNotes             phase0OperatorNotesSummary             `json:"operator_notes"`
}

type phase0ProofArtifactStatus struct {
	Path      string `json:"path"`
	Present   bool   `json:"present"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Generated string `json:"generated_at,omitempty"`
}

type phase0EvidenceIndexVerificationSummary struct {
	Status       string   `json:"status"`
	Path         string   `json:"path,omitempty"`
	Entries      int      `json:"entries,omitempty"`
	RequiredRefs []string `json:"required_refs,omitempty"`
	FilesChecked bool     `json:"files_checked,omitempty"`
	Error        string   `json:"error,omitempty"`
}

func (summary *phase0EvidenceIndexVerificationSummary) UnmarshalJSON(data []byte) error {
	var raw struct {
		Status            string   `json:"status"`
		Path              string   `json:"path"`
		Entries           int      `json:"entries"`
		RequiredRefs      []string `json:"requiredRefs"`
		RequiredRefsSnake []string `json:"required_refs"`
		FilesChecked      bool     `json:"filesChecked"`
		FilesCheckedSnake bool     `json:"files_checked"`
		Error             string   `json:"error"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	summary.Status = raw.Status
	summary.Path = raw.Path
	summary.Entries = raw.Entries
	summary.RequiredRefs = raw.RequiredRefs
	if len(summary.RequiredRefs) == 0 {
		summary.RequiredRefs = raw.RequiredRefsSnake
	}
	summary.FilesChecked = raw.FilesChecked || raw.FilesCheckedSnake
	summary.Error = raw.Error
	return nil
}

type phase0ReleaseSummarySnapshot struct {
	Path                            string                               `json:"path"`
	Present                         bool                                 `json:"present"`
	Status                          string                               `json:"status"`
	ReleaseReady                    bool                                 `json:"release_ready"`
	Verdict                         string                               `json:"verdict,omitempty"`
	ManifestHash                    string                               `json:"manifest_hash,omitempty"`
	EvidenceIndexVerificationStatus string                               `json:"evidence_index_verification_status,omitempty"`
	GateCounts                      phase0ReleaseSummaryGateCounts       `json:"gate_counts"`
	ExecutedDrillCounts             phase0ReleaseSummaryDrillCounts      `json:"executed_drill_counts"`
	EvidencePacketItemStatuses      []phase0ReleaseSummaryEvidenceItem   `json:"evidence_packet_item_statuses,omitempty"`
	Lanes                           []phase0ReleaseSummaryLane           `json:"lanes,omitempty"`
	BlockingReasons                 []phase0ReleaseSummaryBlockingReason `json:"blocking_reasons,omitempty"`
	Error                           string                               `json:"error,omitempty"`
}

type phase0ReleaseSummaryGateCounts struct {
	DrillRows             int            `json:"drill_rows"`
	DrillStatuses         map[string]int `json:"drill_statuses,omitempty"`
	AlertRules            int            `json:"alert_rules"`
	AlertValidationPassed bool           `json:"alert_validation_passed"`
}

type phase0ReleaseSummaryDrillCounts struct {
	Assertions int `json:"assertions"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
}

type phase0ReleaseSummaryEvidenceItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type phase0ReleaseSummaryLane struct {
	Lane          string   `json:"lane"`
	Status        string   `json:"status"`
	RequiredItems []string `json:"required_items,omitempty"`
	MissingItems  []string `json:"missing_items,omitempty"`
}

type phase0ReleaseSummaryBlockingReason struct {
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
}

type phase0OptionalValue struct {
	Available bool   `json:"available"`
	Value     string `json:"value,omitempty"`
	Source    string `json:"source,omitempty"`
}

type phase0OperatorNotesSummary struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Result    string `json:"result,omitempty"`
}

type phase0B5HostDrillMetadataSummary struct {
	SchemaVersion      string                         `json:"schema_version"`
	Status             string                         `json:"status"`
	IssueCodes         []string                       `json:"issue_codes"`
	Source             string                         `json:"source"`
	NoLiveHostProbe    bool                           `json:"no_live_host_probe"`
	Release            phase0B5ReleaseSnapshot        `json:"release"`
	Manifest           phase0B5DrillManifestSnapshot  `json:"manifest"`
	Portability        phase0B5PortabilitySummary     `json:"portability"`
	HostMetadata       phase0B5HostMetadataRollup     `json:"host_metadata"`
	Drills             []phase0B5HostDrillMetadataRow `json:"drills"`
	CandidateCatalog   phase0B6CandidateCatalog       `json:"candidate_catalog"`
	ReservationIndex   phase0B7ReservationIndex       `json:"reservation_index"`
	PromotionReadiness phase0B8PromotionReadiness     `json:"promotion_readiness"`
	ReleaseArchive     phase0B9ReleaseArchive         `json:"release_evidence_archive"`
}

type phase0B5ReleaseSnapshot struct {
	ReleaseEvidence                 phase0ProofArtifactStatus          `json:"release_evidence"`
	EvidenceIndex                   phase0ProofArtifactStatus          `json:"evidence_index"`
	ReleaseSummary                  phase0ProofArtifactStatus          `json:"release_summary"`
	ReleaseReady                    bool                               `json:"release_ready"`
	Verdict                         string                             `json:"verdict,omitempty"`
	EvidenceIndexVerificationStatus string                             `json:"evidence_index_verification_status,omitempty"`
	GateDrillRows                   int                                `json:"gate_drill_rows"`
	ExecutedDrillCounts             phase0ReleaseSummaryDrillCounts    `json:"executed_drill_counts"`
	RequiredDrillExecution          phase0B5DrillExecutionSummary      `json:"required_drill_execution,omitempty"`
	EvidencePacketItemStatuses      []phase0ReleaseSummaryEvidenceItem `json:"evidence_packet_item_statuses,omitempty"`
}

type phase0B5DrillExecutionSummary struct {
	Available     bool           `json:"available"`
	Adapter       string         `json:"adapter,omitempty"`
	AdapterType   string         `json:"adapter_type,omitempty"`
	Assertions    int            `json:"assertions"`
	Passed        int            `json:"passed"`
	Failed        int            `json:"failed"`
	Skipped       int            `json:"skipped"`
	DrillStatuses map[string]int `json:"drill_statuses,omitempty"`
}

type phase0B5DrillManifestSnapshot struct {
	Path         string `json:"path"`
	Present      bool   `json:"present"`
	Status       string `json:"status"`
	Version      int    `json:"version,omitempty"`
	Owner        string `json:"owner,omitempty"`
	DrillCount   int    `json:"drill_count"`
	ManifestHash string `json:"manifest_hash,omitempty"`
	Error        string `json:"error,omitempty"`
}

type phase0B5PortabilitySummary struct {
	WorkspaceRelativeRefs bool     `json:"workspace_relative_refs"`
	AbsolutePathRefs      []string `json:"absolute_path_refs"`
}

type phase0B5HostMetadataRollup struct {
	MetadataSource            string   `json:"metadata_source"`
	ManifestEnvelopeFields    []string `json:"manifest_envelope_fields"`
	HostScopedDrillCount      int      `json:"host_scoped_drill_count"`
	ComponentScopedDrillCount int      `json:"component_scoped_drill_count"`
	Components                []string `json:"components"`
	Runbooks                  []string `json:"runbooks"`
}

type phase0B5HostDrillMetadataRow struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	ReasonCode     string   `json:"reason_code"`
	Event          string   `json:"event"`
	Diagnostic     string   `json:"diagnostic"`
	Runbook        string   `json:"runbook"`
	Components     []string `json:"components"`
	HostScoped     bool     `json:"host_scoped"`
	MetadataSource string   `json:"metadata_source"`
	Candidate      bool     `json:"candidate,omitempty"`
	Phase3Domain   string   `json:"phase3_domain,omitempty"`
	Phase3Drill    string   `json:"phase3_drill,omitempty"`
	HostScope      string   `json:"host_scope,omitempty"`
	Execution      string   `json:"execution,omitempty"`
	EvidenceRef    string   `json:"evidence_ref,omitempty"`
}

type phase0DrillManifestFile struct {
	Version int
	Owner   string
	Drills  []phase0DrillManifestRow
	Hash    string
}

type phase0DrillManifestRow struct {
	ID               string
	Title            string
	Status           string
	ReasonCode       string
	Event            string
	Diagnostic       string
	Runbook          string
	MetadataTags     []string
	MetadataNotes    []string
	MetadataSubcases []string
}

type phase0B6CandidateCatalog struct {
	SchemaVersion    string                          `json:"schema_version"`
	Version          int                             `json:"version,omitempty"`
	Status           string                          `json:"status"`
	Source           string                          `json:"source"`
	OfflineByDefault bool                            `json:"offline_by_default"`
	NoLiveHostProbe  bool                            `json:"no_live_host_probe"`
	RequiredDomains  []string                        `json:"required_domains"`
	MissingDomains   []string                        `json:"missing_domains,omitempty"`
	DomainCoverage   []phase0B6DomainCoverage        `json:"domain_coverage"`
	CandidateCounts  phase0B6CandidateCounts         `json:"candidate_counts"`
	Candidates       []phase0B6HostDrillCandidateRow `json:"candidates,omitempty"`
}

type phase0B6DomainCoverage struct {
	Domain         string `json:"domain"`
	CandidateCount int    `json:"candidate_count"`
	Covered        bool   `json:"covered"`
}

type phase0B6CandidateCounts struct {
	Total       int            `json:"total"`
	HostScoped  int            `json:"host_scoped"`
	LocalOnly   int            `json:"local_only"`
	PlannerOnly int            `json:"planner_only"`
	ByDomain    map[string]int `json:"by_domain"`
}

type phase0B6HostDrillCandidateRow struct {
	ID          string `json:"id"`
	Domain      string `json:"domain"`
	Phase3Drill string `json:"phase3_drill"`
	HostScope   string `json:"host_scope"`
	Execution   string `json:"execution"`
	EvidenceRef string `json:"evidence_ref"`
}

type phase0B7ReservationIndex struct {
	SchemaVersion     string                    `json:"schema_version"`
	Version           int                       `json:"version,omitempty"`
	Status            string                    `json:"status"`
	Source            string                    `json:"source"`
	SourceManifest    string                    `json:"source_manifest,omitempty"`
	LocalOnly         bool                      `json:"local_only"`
	OfflineByDefault  bool                      `json:"offline_by_default"`
	NoLiveHostProbe   bool                      `json:"no_live_host_probe"`
	ReservationCounts phase0B7ReservationCounts `json:"reservation_counts"`
	DomainCoverage    []phase0B6DomainCoverage  `json:"domain_coverage"`
	MissingDomains    []string                  `json:"missing_domains,omitempty"`
	Reservations      []phase0B7ReservationRow  `json:"reservations,omitempty"`
}

type phase0B7ReservationCounts struct {
	Total       int            `json:"total"`
	Reserved    int            `json:"reserved"`
	FutureRig   int            `json:"future_rig"`
	PlannerOnly int            `json:"planner_only"`
	ByDomain    map[string]int `json:"by_domain"`
}

type phase0B7ReservationRow struct {
	ID                string   `json:"id"`
	Status            string   `json:"status"`
	Domain            string   `json:"domain"`
	CarrierID         string   `json:"carrier_id"`
	CarrierReasonCode string   `json:"carrier_reason_code"`
	PlannedDrill      string   `json:"planned_drill"`
	HostScope         string   `json:"host_scope"`
	Execution         string   `json:"execution"`
	Rig               string   `json:"rig"`
	Note              string   `json:"note,omitempty"`
	EvidenceRefs      []string `json:"evidence_refs"`
}

type phase0B7ReservationIndexFile struct {
	Version        int
	Owner          string
	Sprint         string
	LocalOnly      bool
	SourceManifest string
	Reservations   []phase0B7ReservationRow
}

type phase0B8PromotionReadiness struct {
	SchemaVersion    string                           `json:"schema_version"`
	Version          int                              `json:"version,omitempty"`
	Status           string                           `json:"status"`
	Source           string                           `json:"source"`
	SourceManifest   string                           `json:"source_manifest,omitempty"`
	LocalOnly        bool                             `json:"local_only"`
	OfflineByDefault bool                             `json:"offline_by_default"`
	NoLiveHostProbe  bool                             `json:"no_live_host_probe"`
	ReadinessCounts  phase0B8PromotionReadinessCounts `json:"readiness_counts"`
	DomainCoverage   []phase0B6DomainCoverage         `json:"domain_coverage"`
	MissingDomains   []string                         `json:"missing_domains,omitempty"`
	Unblockers       []phase0B8PromotionUnblocker     `json:"unblockers,omitempty"`
	ReadinessRows    []phase0B8PromotionReadinessRow  `json:"readiness_rows,omitempty"`
}

type phase0B8PromotionReadinessCounts struct {
	Total          int            `json:"total"`
	Ready          int            `json:"ready"`
	NeedsUnblocker int            `json:"needs_unblocker"`
	Blocked        int            `json:"blocked"`
	LocalOnly      int            `json:"local_only"`
	PlannerOnly    int            `json:"planner_only"`
	WithUnblockers int            `json:"with_unblockers"`
	ByDomain       map[string]int `json:"by_domain"`
}

type phase0B8PromotionReadinessRow struct {
	ID                string   `json:"id"`
	Status            string   `json:"status"`
	Domain            string   `json:"domain"`
	ReservationID     string   `json:"reservation_id,omitempty"`
	CarrierID         string   `json:"carrier_id,omitempty"`
	CarrierReasonCode string   `json:"carrier_reason_code,omitempty"`
	PlannedDrill      string   `json:"planned_drill,omitempty"`
	HostScope         string   `json:"host_scope"`
	Execution         string   `json:"execution"`
	Rig               string   `json:"rig,omitempty"`
	LocalOnly         bool     `json:"local_only"`
	Unblockers        []string `json:"unblockers,omitempty"`
	EvidenceRefs      []string `json:"evidence_refs,omitempty"`
	Note              string   `json:"note,omitempty"`
}

type phase0B8PromotionUnblocker struct {
	ID          string `json:"id"`
	Status      string `json:"status,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Description string `json:"description,omitempty"`
}

type phase0B9ReleaseArchive struct {
	SchemaVersion            string                        `json:"schema_version"`
	Version                  int                           `json:"version,omitempty"`
	Status                   string                        `json:"status"`
	IssueCodes               []string                      `json:"issue_codes,omitempty"`
	Source                   string                        `json:"source"`
	SourceManifest           string                        `json:"source_manifest,omitempty"`
	ArchiveRoot              string                        `json:"archive_root,omitempty"`
	LocalOnly                bool                          `json:"local_only"`
	OfflineByDefault         bool                          `json:"offline_by_default"`
	NoLiveHostProbe          bool                          `json:"no_live_host_probe"`
	Command                  phase0B9ReleaseArchiveCommand `json:"command"`
	EvidenceRefs             []phase0B9ReleaseArchiveRef   `json:"evidence_refs,omitempty"`
	VerificationRequirements []string                      `json:"verification_requirements,omitempty"`
	ReadinessCounts          phase0B9ReleaseArchiveCounts  `json:"readiness_counts"`
	ReadinessRows            []phase0B9ReleaseArchiveRow   `json:"readiness_rows,omitempty"`
	Error                    string                        `json:"error,omitempty"`
}

type phase0B9ReleaseArchiveCommand struct {
	Name         string `json:"name,omitempty"`
	Mode         string `json:"mode,omitempty"`
	CommandRef   string `json:"command_ref,omitempty"`
	RequiredFlag string `json:"required_flag,omitempty"`
}

type phase0B9ReleaseArchiveRef struct {
	Group string `json:"group"`
	Name  string `json:"name"`
	Path  string `json:"path"`
}

type phase0B9ReleaseArchiveCounts struct {
	Total               int            `json:"total"`
	Ready               int            `json:"ready"`
	NeedsReview         int            `json:"needs_review"`
	Blocked             int            `json:"blocked"`
	PendingLocalArchive int            `json:"pending_local_archive"`
	LocalOnly           int            `json:"local_only"`
	ByStatus            map[string]int `json:"by_status"`
}

type phase0B9ReleaseArchiveRow struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	RequiredRef  string   `json:"required_ref"`
	Verification []string `json:"verification,omitempty"`
}

type phase0B9ReleaseArchiveFile struct {
	Version                  int
	Owner                    string
	Sprint                   string
	LocalOnly                bool
	SourceManifest           string
	ArchiveRoot              string
	Command                  phase0B9ReleaseArchiveCommand
	EvidenceRefs             []phase0B9ReleaseArchiveRef
	VerificationRequirements []string
	ReadinessRows            []phase0B9ReleaseArchiveRow
}

type phase0B8PromotionReadinessFile struct {
	Version        int
	Owner          string
	Sprint         string
	LocalOnly      bool
	SourceManifest string
	ReadinessRows  []phase0B8PromotionReadinessRow
	Unblockers     []phase0B8PromotionUnblocker
}

type dashboardAuditSource interface {
	Evidence(context.Context, time.Time) (audit.Evidence, error)
	Events(context.Context, int) (audit.LedgerQueryResult, error)
	Drains(context.Context) ([]audit.DrainConfig, error)
	Export(context.Context, time.Time) ([]byte, audit.ExportManifest, error)
}

func dashboardSummaryHandler(operationStore OperationStore, projectStore ProjectStore, serviceStore ServiceStore, auditSource dashboardAuditSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}

		projects, err := projectStore.ListProjects(r.Context())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "project list failed", nil)
			return
		}
		services, err := serviceStore.ListServices(r.Context(), ResourceListFilter{})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service list failed", nil)
			return
		}
		operations, err := operationStore.List(r.Context(), OperationListFilter{})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation list failed", nil)
			return
		}

		auditEvidence, err := auditSource.Evidence(r.Context(), dashboardSampleTime)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "audit evidence read failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, dashboardSummary{
			Status:              "ready",
			APIVersion:          version.APIVersion,
			ServerVersion:       version.Version,
			ProjectCount:        len(projects),
			ServiceCount:        len(services),
			OperationCount:      len(operations),
			AuditStatus:         auditEvidence.Status,
			AuditEventCount:     auditEvidence.EventCount,
			TemplateCardCount:   len(templates.MarketplaceCards(dashboardSampleTime)),
			NotificationPreview: string(notifications.EventDeploymentFailed),
		})
	}
}

func dashboardRuntimeInventoryHandler(projectStore ProjectStore, serviceStore ServiceStore, operationStore OperationStore, registry *AgentRegistry, clock func() time.Time) http.HandlerFunc {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}

		projects, err := projectStore.ListProjects(r.Context())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "project list failed", nil)
			return
		}
		services, err := serviceStore.ListServices(r.Context(), ResourceListFilter{})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service list failed", nil)
			return
		}
		operations, err := operationStore.List(r.Context(), OperationListFilter{})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation list failed", nil)
			return
		}

		now := clock().UTC()
		agents := []AgentSnapshot{}
		if registry != nil {
			agents = registry.Snapshot()
		}
		serviceMetadata := dashboardRuntimeServiceMetadataByService(r.Context(), operationStore, operations)
		applyDashboardAgentRuntimeMetadata(serviceMetadata, agents)
		agentRoutes := dashboardRouteSignalsByService(services, agents)
		runtimeServices := make([]dashboardRuntimeService, 0, len(services))
		for _, service := range services {
			metadata := serviceMetadata[string(service.ID)]
			if metadata.Route == "" {
				metadata.Route = agentRoutes[string(service.ID)]
			}
			runtimeServices = append(runtimeServices, adaptDashboardRuntimeService(service, now, metadata))
		}
		hosts := dashboardRuntimeHosts(agents, now)
		WriteJSON(w, http.StatusOK, dashboardRuntimeInventory{
			SchemaVersion: "midnight.dashboard.runtime-inventory.v1",
			GeneratedAt:   now,
			Projects:      projects,
			Services:      runtimeServices,
			Operations:    operations,
			Agents:        agents,
			Hosts:         hosts,
			Routes:        dashboardRuntimeRoutes(runtimeServices, agents),
		})
	}
}

func dashboardTemplateDeployHandler(projectStore ProjectStore, serviceStore ServiceStore, environmentStore EnvironmentStore, operationStore OperationStore, queueStore queue.Store, clock func() time.Time) http.HandlerFunc {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		// The dashboard route shares the same safety boundary as the REST
		// template endpoint: planning/materialization is not deployment success.
		// Do not let MIDNIGHT_EXPERIMENTAL convert synthetic topology into a
		// successful database provision.
		if !dashboardTemplateRuntimeSagaAvailable() {
			writeRequestError(
				w,
				r,
				http.StatusNotImplemented,
				failurecodes.NotImplemented,
				"database template deployment is unavailable until volume attachment, readiness, and database smoke checks are wired",
				map[string]any{
					"capability": "database-template-deploy",
					"state":      "unavailable",
					"recovery":   "deploy a database image manually; this becomes available after volume attachment, private DNS, readiness, and smoke-test evidence are wired as one saga",
				},
			)
			return
		}

		var request dashboardTemplateDeployRequest
		body, ok := readBoundedJSONRequestBody(w, r, "invalid template deploy request body")
		if !ok || !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid template deploy request body") {
			return
		}
		templateID := strings.TrimSpace(request.TemplateID)
		if templateID == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "template_id is required", map[string]any{"field": "template_id"})
			return
		}
		serviceName := firstNonEmpty(request.ServiceName, templateID)
		plan, err := templates.PlanInstantiation(templates.InstantiationRequest{
			WorkspaceID:  "local-dashboard",
			TemplateID:   templateID,
			ServiceName:  serviceName,
			DatabaseName: request.DatabaseName,
		})
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), map[string]any{"template_id": templateID})
			return
		}
		if queueStore == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "dashboard template deploy queue is not wired", map[string]any{"template_id": templateID})
			return
		}
		project, err := getOrCreateDashboardProject(r, projectStore)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "project create failed", nil)
			return
		}
		environment, err := ensureProductionEnvironment(r.Context(), environmentStore, project)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "production environment create failed", map[string]any{"project_id": project.ID})
			return
		}
		service, err := createDashboardTemplateService(r, serviceStore, project.ID, plan)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service create failed", map[string]any{"template_id": templateID})
			return
		}
		operation, err := CreateQueuedOperation(r.Context(), operationStore, "deploy")
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation create failed", map[string]any{"template_id": templateID})
			return
		}
		now := clock().UTC()
		deployRequest := buildDashboardTemplateDeployRequest(plan, service, environment.ID, operation.ID)
		payload, err := buildDeployJobPayload(operation.ID, service, deployRequest, now)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deploy job payload encode failed", map[string]any{"operation_id": operation.ID, "service_id": service.ID, "template_id": templateID})
			return
		}
		job, err := queueStore.Enqueue(r.Context(), buildDeployEnqueueRequest(operation.ID, service, now, payload))
		if err != nil {
			failDeployIntakeOperation(r, operationStore, operation.ID, failurecodes.DeployJobEnqueueFailed, "deploy job enqueue failed")
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.DeployJobEnqueueFailed, "deploy job enqueue failed", map[string]any{"operation_id": operation.ID, "service_id": service.ID, "template_id": templateID})
			return
		}
		if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: "deploy job enqueued: job_id=" + job.ID + " operation_id=" + string(operation.ID)}); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation event append failed", map[string]any{"operation_id": operation.ID, "job_id": job.ID})
			return
		}
		if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{
			OperationID: operation.ID,
			State:       operation.State,
			Message:     dashboardTemplateDeployQueuedEventMessage(templateID, string(service.ID), job, plan),
			CreatedAt:   now,
		}); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation metadata event append failed", map[string]any{"operation_id": operation.ID, "service_id": service.ID, "template_id": templateID})
			return
		}

		metadata := dashboardRuntimeServiceMetadata{TemplateID: templateID, DatabaseName: plan.DatabaseName, OperationID: string(operation.ID), OperationState: operation.State, Queue: job.Queue, JobID: job.ID}
		WriteJSON(w, http.StatusAccepted, dashboardTemplateDeployResponse{
			SchemaVersion: "midnight.dashboard.template-deploy.v1",
			Service:       adaptDashboardRuntimeService(service, now, metadata),
			Operation:     operation,
			Plan:          plan,
			Queue:         string(job.Queue),
			JobID:         job.ID,
			JobState:      string(job.State),
			Message:       "Service and deploy job were queued. Worker execution and route promotion have not run yet.",
		})
	}
}

// The legacy local-dashboard template route still uses the old direct queue
// path and therefore cannot share the REST saga's atomic compensation.
func dashboardTemplateRuntimeSagaAvailable() bool { return false }

func buildDashboardTemplateDeployRequest(plan templates.InstantiationPlan, service Service, environmentID, operationID orailtypes.ID) DeployIntakeRequest {
	slug := slugifyDashboardID(plan.ServiceName)
	if slug == "" {
		slug = slugifyDashboardID(plan.TemplateID)
	}
	suffix := shortDashboardID(string(operationID))
	return DeployIntakeRequest{
		EnvironmentID: string(environmentID),
		SourceRef:     "template://" + plan.TemplateID,
		Image:         plan.Image,
		Mode:          "template",
		DeploymentID:  "dep_" + slug + "_" + suffix,
		InstanceID:    "inst_" + slug + "_" + suffix + "_0",
		Route: &DeployRouteIntent{
			Name: strings.TrimSpace(service.Slug),
		},
	}
}

func getOrCreateDashboardProject(r *http.Request, projectStore ProjectStore) (Project, error) {
	projects, err := projectStore.ListProjects(r.Context())
	if err != nil {
		return Project{}, err
	}
	for _, project := range projects {
		if project.Slug == "local-dashboard" {
			return project, nil
		}
	}
	return projectStore.CreateProject(r.Context(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "local-dashboard", DisplayName: "Local dashboard"})
}

func createDashboardTemplateService(r *http.Request, serviceStore ServiceStore, projectID orailtypes.ID, plan templates.InstantiationPlan) (Service, error) {
	baseSlug := slugifyDashboardID(plan.ServiceName)
	if baseSlug == "" {
		baseSlug = slugifyDashboardID(plan.TemplateID)
	}
	for attempt := 0; attempt < 5; attempt++ {
		slug := baseSlug
		if attempt > 0 {
			slug = baseSlug + "-" + strconv.Itoa(attempt+1)
		}
		service, err := serviceStore.CreateService(r.Context(), Service{
			ProjectID:   projectID,
			Slug:        slug,
			DisplayName: plan.ServiceName,
		})
		if errors.Is(err, ErrResourceConflict) {
			continue
		}
		return service, err
	}
	return serviceStore.CreateService(r.Context(), Service{
		ProjectID:   projectID,
		Slug:        baseSlug + "-" + strconv.FormatInt(time.Now().Unix(), 10),
		DisplayName: plan.ServiceName,
	})
}

func slugifyDashboardID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func shortDashboardID(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])[:12]
}

func adaptDashboardRuntimeService(service Service, now time.Time, metadata dashboardRuntimeServiceMetadata) dashboardRuntimeService {
	health := "created"
	runtimeState := "created"
	replicas := "0/0"
	updated := "created"
	if !service.UpdatedAt.IsZero() {
		updated = formatDashboardAge(now.Sub(service.UpdatedAt))
	}
	engine, engineVersion := inferDashboardServiceEngine(service)
	if metadata.TemplateID != "" {
		engine, engineVersion = inferDashboardTemplateEngine(metadata.TemplateID, engine, engineVersion)
	}
	if metadata.OperationID != "" {
		runtimeState, health, replicas = dashboardRuntimeStateForOperation(metadata.OperationState)
	}
	if metadata.InstanceState != "" {
		runtimeState, health, replicas = dashboardRuntimeStateForInstance(metadata.InstanceState, metadata.OperationState)
		if !metadata.InstanceAt.IsZero() {
			updated = formatDashboardAge(now.Sub(metadata.InstanceAt))
		}
	}
	var connection *templates.ConnectionGuidance
	var credentialRefs []templates.CredentialRef
	var volume *templates.VolumePlan
	var backup *templates.BackupPlan
	var restorePlan *templates.RestorePlanGuidance
	var networking *templates.NetworkPlan
	if metadata.TemplateID != "" {
		plan, err := templates.PlanInstantiation(templates.InstantiationRequest{
			WorkspaceID:  "local-dashboard",
			TemplateID:   metadata.TemplateID,
			ServiceName:  firstNonEmpty(service.DisplayName, service.Slug, metadata.TemplateID),
			DatabaseName: metadata.DatabaseName,
		})
		if err == nil {
			connection = &plan.ConnectionGuidance
			credentialRefs = append([]templates.CredentialRef(nil), plan.CredentialRefs...)
			volume = &plan.Volume
			backup = &plan.Backup
			restorePlan = &plan.RestorePlanGuidance
			networking = &plan.Networking
		}
	}
	return dashboardRuntimeService{
		ID:             string(service.ID),
		ProjectID:      string(service.ProjectID),
		Slug:           service.Slug,
		Name:           firstNonEmpty(service.DisplayName, service.Slug, string(service.ID)),
		Kind:           firstNonEmpty(engine, "service"),
		Engine:         engine,
		EngineVersion:  engineVersion,
		TemplateID:     metadata.TemplateID,
		DeploymentID:   metadata.DeploymentID,
		InstanceID:     metadata.InstanceID,
		OperationID:    metadata.OperationID,
		OperationState: string(metadata.OperationState),
		Queue:          string(metadata.Queue),
		JobID:          metadata.JobID,
		JobState:       string(metadata.JobState),
		RouteIntent:    metadata.RouteIntent,
		LastEvent:      metadata.LastEvent,
		LastEventAt:    dashboardTimePtr(metadata.LastEventAt),
		RuntimeState:   runtimeState,
		Version:        version.Version,
		Replicas:       replicas,
		Route:          firstNonEmpty(metadata.Route, dashboardRouteNotPromoted),
		Health:         health,
		Updated:        updated,
		CreatedAt:      service.CreatedAt,
		UpdatedAt:      service.UpdatedAt,
		Connection:     connection,
		CredentialRefs: credentialRefs,
		Volume:         volume,
		Backup:         backup,
		RestorePlan:    restorePlan,
		Networking:     networking,
	}
}

func dashboardRuntimeStateForOperation(state orailtypes.OperationState) (runtimeState string, health string, replicas string) {
	switch state {
	case orailtypes.OperationQueued:
		return "waiting-for-worker", "queued", "0/1 requested"
	case orailtypes.OperationRunning:
		return "deploying", "deploying", "0/1 starting"
	case orailtypes.OperationSucceeded:
		return "active", "active", "1/1"
	case orailtypes.OperationFailed:
		return "failed", "failed", "0/1 failed"
	case orailtypes.OperationCanceled:
		return "canceled", "canceled", "0/1 canceled"
	default:
		return "draft", "draft", "0/1 requested"
	}
}

func dashboardRuntimeStateForInstance(state string, operationState orailtypes.OperationState) (runtimeState string, health string, replicas string) {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "READY":
		return "active", "healthy", "1/1"
	case "RUNNING":
		return "active", "ready", "1/1"
	case "PULLING", "CREATING", "STARTING", "ALLOCATED", "RESTARTING":
		return "deploying", "deploying", "0/1 starting"
	case "DRAINING":
		return "draining", "draining", "1/1 draining"
	case "STOPPED", "REMOVED":
		return "stopped", "stopped", "0/1 stopped"
	case "UNHEALTHY":
		return "degraded", "unhealthy", "0/1 unhealthy"
	case "CRASHED":
		return "failed", "crashed", "0/1 crashed"
	case "LOST":
		return "lost", "lost", "0/1 lost"
	default:
		return dashboardRuntimeStateForOperation(operationState)
	}
}

func dashboardTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func inferDashboardServiceEngine(service Service) (string, string) {
	text := strings.ToLower(service.Slug + " " + service.DisplayName)
	for _, card := range templates.MarketplaceCards(dashboardSampleTime) {
		if strings.Contains(text, strings.ToLower(card.ID)) ||
			(card.Engine != "" && strings.Contains(text, strings.ToLower(string(card.Engine)))) ||
			(card.Title != "" && strings.Contains(text, strings.ToLower(card.Title))) {
			return string(card.Engine), card.EngineVersion
		}
	}
	return "", ""
}

func inferDashboardTemplateEngine(templateID string, fallbackEngine string, fallbackVersion string) (string, string) {
	for _, card := range templates.MarketplaceCards(dashboardSampleTime) {
		if card.ID == strings.TrimSpace(templateID) {
			return string(card.Engine), card.EngineVersion
		}
	}
	return fallbackEngine, fallbackVersion
}

func dashboardRuntimeServiceMetadataByService(ctx context.Context, operationStore OperationStore, operations []orailtypes.Operation) map[string]dashboardRuntimeServiceMetadata {
	metadata := make(map[string]dashboardRuntimeServiceMetadata)
	for _, operation := range operations {
		if operation.Kind != "deploy" {
			continue
		}
		events, err := operationStore.ListEvents(ctx, operation.ID)
		if err != nil {
			continue
		}
		var opTemplateID, opServiceID, opRoute string
		var opFields map[string]string
		var lastEvent string
		var lastEventAt time.Time
		for _, event := range events {
			if strings.TrimSpace(event.Message) != "" {
				lastEvent = event.Message
				lastEventAt = event.CreatedAt
			}
			templateID, serviceID, ok := parseDashboardTemplateDeployEventMessage(event.Message)
			if ok {
				opTemplateID = templateID
				opServiceID = serviceID
				opFields = fieldsFromDashboardEvent(event.Message)
			}
			if serviceID, fields, ok := parseDashboardDeployIntakeEventMessage(event.Message); ok {
				opServiceID = serviceID
				opFields = fields
			}
			if fields, ok := parseDashboardDeployWorkerEventMessage(event.Message); ok {
				opFields = mergeDashboardEventFields(opFields, fields)
				if opServiceID == "" {
					opServiceID = fields["service_id"]
				}
			}
			if route, ok := parseDashboardPromoteRouteEventMessage(event.Message); ok {
				opRoute = route
			}
		}
		if opServiceID == "" {
			continue
		}
		metadata[opServiceID] = dashboardRuntimeServiceMetadata{
			TemplateID:     opTemplateID,
			DatabaseName:   opFields["database_name"],
			DeploymentID:   opFields["deployment_id"],
			InstanceID:     opFields["instance_id"],
			OperationID:    string(operation.ID),
			OperationState: operation.State,
			Queue:          queue.Name(opFields["queue"]),
			JobID:          opFields["job_id"],
			JobState:       queue.JobState(opFields["job_state"]),
			RouteIntent:    opFields["route_intent"],
			Route:          opRoute,
			LastEvent:      lastEvent,
			LastEventAt:    lastEventAt,
		}
	}
	return metadata
}

func applyDashboardAgentRuntimeMetadata(metadata map[string]dashboardRuntimeServiceMetadata, agents []AgentSnapshot) {
	if len(metadata) == 0 || len(agents) == 0 {
		return
	}
	for serviceID, serviceMetadata := range metadata {
		for _, agent := range agents {
			for _, instance := range agent.StatusReport.Instances {
				if !dashboardInstanceMatchesService(serviceMetadata, instance) {
					continue
				}
				if serviceMetadata.InstanceAt.IsZero() || instance.ObservedAt.After(serviceMetadata.InstanceAt) {
					serviceMetadata.InstanceState = strings.TrimSpace(instance.State)
					serviceMetadata.InstanceReason = strings.TrimSpace(instance.Reason)
					serviceMetadata.InstanceAt = instance.ObservedAt
				}
			}
		}
		metadata[serviceID] = serviceMetadata
	}
}

func dashboardInstanceMatchesService(metadata dashboardRuntimeServiceMetadata, instance agentapi.InstanceStatus) bool {
	if strings.TrimSpace(metadata.DeploymentID) != "" && strings.TrimSpace(instance.DeploymentID) != "" && strings.TrimSpace(metadata.DeploymentID) != strings.TrimSpace(instance.DeploymentID) {
		return false
	}
	if strings.TrimSpace(metadata.InstanceID) != "" && strings.TrimSpace(instance.InstanceID) != "" && strings.TrimSpace(metadata.InstanceID) != strings.TrimSpace(instance.InstanceID) {
		return false
	}
	return strings.TrimSpace(metadata.DeploymentID) != "" && strings.TrimSpace(instance.DeploymentID) != "" ||
		strings.TrimSpace(metadata.InstanceID) != "" && strings.TrimSpace(instance.InstanceID) != ""
}

func dashboardTemplateDeployEventMessage(templateID string, serviceID string) string {
	return "dashboard.template_deploy template_id=" + strings.TrimSpace(templateID) + " service_id=" + strings.TrimSpace(serviceID)
}

func dashboardTemplateDeployQueuedEventMessage(templateID string, serviceID string, job queue.Job, plans ...templates.InstantiationPlan) string {
	message := dashboardTemplateDeployEventMessage(templateID, serviceID) +
		" queue=" + strings.TrimSpace(string(job.Queue)) +
		" job_id=" + strings.TrimSpace(job.ID) +
		" job_state=" + strings.TrimSpace(string(job.State))
	if len(plans) > 0 {
		message += " database_name=" + dashboardEventValue(plans[0].DatabaseName)
	}
	return message
}

func parseDashboardTemplateDeployEventMessage(message string) (templateID string, serviceID string, ok bool) {
	fields := fieldsFromDashboardEvent(message)
	templateID = fields["template_id"]
	serviceID = fields["service_id"]
	return templateID, serviceID, templateID != "" && serviceID != ""
}

func parseDashboardDeployIntakeEventMessage(message string) (serviceID string, fields map[string]string, ok bool) {
	if !strings.HasPrefix(strings.TrimSpace(message), "deploy.intake ") {
		return "", nil, false
	}
	fields = fieldsFromDashboardEvent(message)
	serviceID = fields["service_id"]
	return serviceID, fields, serviceID != ""
}

func parseDashboardDeployWorkerEventMessage(message string) (map[string]string, bool) {
	if !strings.HasPrefix(strings.TrimSpace(message), "deploy worker step ") {
		return nil, false
	}
	return fieldsFromDashboardEvent(message), true
}

func mergeDashboardEventFields(base map[string]string, fields map[string]string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}
	for key, value := range fields {
		if strings.TrimSpace(value) == "" {
			continue
		}
		base[key] = value
	}
	return base
}

func parseDashboardPromoteRouteEventMessage(message string) (route string, ok bool) {
	if !strings.Contains(message, "deploy worker step promote succeeded") {
		return "", false
	}
	fields := fieldsFromDashboardEvent(message)
	route = firstNonEmpty(fields["host"], fields["route"], fields["route_name"], fields["route_id"])
	return route, route != ""
}

func fieldsFromDashboardEvent(message string) map[string]string {
	fields := map[string]string{}
	for _, token := range strings.Fields(strings.TrimSpace(message)) {
		key, value, found := strings.Cut(token, "=")
		if !found {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return fields
}

func dashboardEventValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "app"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_',
			r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "app"
	}
	return out
}

func dashboardRuntimeHosts(agents []AgentSnapshot, now time.Time) []dashboardRuntimeHost {
	hosts := make([]dashboardRuntimeHost, 0, len(agents))
	for _, agent := range agents {
		drift := agent.StatusReport.Drift
		docker := firstNonEmpty(agent.Runtime)
		cgroup := ""
		if drift != nil {
			docker = firstNonEmpty(drift.EngineVersion, docker)
			cgroup = drift.CGroupVersion
		}
		doctor := firstNonEmpty(agent.Status, "registered")
		if agent.LastHeartbeat.IsZero() {
			doctor = "registered"
		} else if now.Sub(agent.LastHeartbeat) > 2*time.Minute {
			doctor = "stale"
		}
		hosts = append(hosts, dashboardRuntimeHost{
			Name:          firstNonEmpty(agent.Hostname, agent.AgentID),
			Profile:       agent.AgentID,
			Docker:        firstNonEmpty(docker, "not reported"),
			Cgroup:        firstNonEmpty(cgroup, "not reported"),
			Doctor:        doctor,
			AgentID:       agent.AgentID,
			Version:       agent.Version,
			LastHeartbeat: agent.LastHeartbeat,
		})
	}
	return hosts
}

func dashboardRuntimeRoutes(services []dashboardRuntimeService, agents []AgentSnapshot) []dashboardRuntimeRoute {
	routes := make([]dashboardRuntimeRoute, 0)
	for _, service := range services {
		state := "promoted"
		if service.Route == "" || service.Route == dashboardRouteNotPromoted {
			state = dashboardRouteNotPromoted
		}
		routes = append(routes, dashboardRuntimeRoute{
			Target:       service.Name,
			Route:        firstNonEmpty(service.Route, dashboardRouteNotPromoted),
			Source:       "service",
			State:        state,
			Detail:       service.Replicas + " replicas",
			ServiceID:    service.ID,
			Service:      service.Name,
			Host:         hostFromDashboardRoute(service.Route),
			RouteName:    service.Route,
			DeploymentID: service.DeploymentID,
			InstanceID:   service.InstanceID,
		})
	}
	for _, agent := range agents {
		for _, target := range agent.StatusReport.RouteTargets {
			routeName := firstNonEmpty(target.RouteName, target.RouteID)
			route := firstNonEmpty(routeName, dashboardRouteNotReported)
			state := firstNonEmpty(target.ReadinessStatus, dashboardRouteNotReported)
			if routeName == "" && (state == "" || state == "ready" || state == dashboardRouteNotReported) {
				state = dashboardRouteNotPromoted
			}
			routes = append(routes, dashboardRuntimeRoute{
				Target:        firstNonEmpty(routeName, target.DeploymentID, target.InstanceID, "route target"),
				Route:         route,
				Source:        firstNonEmpty(agent.AgentID, "agent"),
				State:         state,
				Detail:        dashboardRouteTargetDetail(target),
				ServiceID:     serviceIDFromRouteTarget(services, target),
				Service:       serviceNameFromRouteTarget(services, target),
				Host:          "",
				RouteName:     routeName,
				DeploymentID:  target.DeploymentID,
				InstanceID:    target.InstanceID,
				ProjectID:     target.ProjectID,
				EnvironmentID: target.EnvironmentID,
			})
		}
	}
	return routes
}

func serviceIDFromRouteTarget(services []dashboardRuntimeService, target agentapi.RouteTargetObservation) string {
	service := serviceFromRouteTarget(services, target)
	if service == nil {
		return ""
	}
	return service.ID
}

func serviceNameFromRouteTarget(services []dashboardRuntimeService, target agentapi.RouteTargetObservation) string {
	service := serviceFromRouteTarget(services, target)
	if service == nil {
		return ""
	}
	return service.Name
}

func serviceFromRouteTarget(services []dashboardRuntimeService, target agentapi.RouteTargetObservation) *dashboardRuntimeService {
	deploymentID := dashboardRouteMatchToken(target.DeploymentID)
	instanceID := dashboardRouteMatchToken(target.InstanceID)
	if deploymentID != "" || instanceID != "" {
		for i := range services {
			service := &services[i]
			if deploymentID != "" && deploymentID == dashboardRouteMatchToken(service.DeploymentID) {
				return service
			}
			if instanceID != "" && instanceID == dashboardRouteMatchToken(service.InstanceID) {
				return service
			}
		}
	}
	for i := range services {
		service := &services[i]
		for _, candidate := range []string{target.RouteID, target.RouteName} {
			if dashboardRouteMatchToken(candidate) == "" {
				continue
			}
			if dashboardRouteMatchToken(candidate) == dashboardRouteMatchToken(service.ID) ||
				dashboardRouteMatchToken(candidate) == dashboardRouteMatchToken(service.Slug) ||
				dashboardRouteMatchToken(candidate) == dashboardRouteMatchToken(service.Name) ||
				dashboardRouteMatchToken(candidate) == dashboardRouteMatchToken(service.Route) {
				return service
			}
		}
	}
	return nil
}

func hostFromDashboardRoute(route string) string {
	route = strings.TrimSpace(route)
	if route == "" || route == dashboardRouteNotPromoted || route == dashboardRouteNotReported {
		return ""
	}
	if strings.Contains(route, "://") {
		return ""
	}
	if host, _, ok := strings.Cut(route, "/"); ok {
		return host
	}
	return route
}

func dashboardRouteSignalsByService(services []Service, agents []AgentSnapshot) map[string]string {
	serviceIDsByToken := make(map[string]string)
	for _, service := range services {
		for _, candidate := range []string{
			string(service.ID),
			service.Slug,
			service.DisplayName,
			slugifyDashboardID(service.DisplayName),
		} {
			if token := dashboardRouteMatchToken(candidate); token != "" {
				serviceIDsByToken[token] = string(service.ID)
			}
		}
	}

	routes := make(map[string]string)
	for _, agent := range agents {
		for _, target := range agent.StatusReport.RouteTargets {
			route := firstNonEmpty(target.RouteName, target.RouteID)
			if route == "" {
				continue
			}
			for _, candidate := range []string{target.RouteID, target.RouteName} {
				serviceID := serviceIDsByToken[dashboardRouteMatchToken(candidate)]
				if serviceID == "" {
					continue
				}
				routes[serviceID] = route
				break
			}
		}
	}
	return routes
}

func dashboardRouteMatchToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func dashboardRouteTargetDetail(target agentapi.RouteTargetObservation) string {
	parts := make([]string, 0, 4)
	if scope := firstNonEmpty(target.ProjectID, target.EnvironmentID, target.DeploymentID); scope != "" {
		parts = append(parts, scope)
	}
	if target.ContainerIP != "" && target.InternalServicePort > 0 {
		parts = append(parts, "runtime target "+target.ContainerIP+":"+strconv.Itoa(target.InternalServicePort))
	} else if target.ContainerIP != "" {
		parts = append(parts, "runtime target "+target.ContainerIP)
	} else if target.ContainerName != "" {
		parts = append(parts, "runtime target "+target.ContainerName)
	}
	if target.ReadinessStatus != "" {
		parts = append(parts, "readiness "+target.ReadinessStatus)
	}
	if len(parts) == 0 {
		return dashboardRouteNotReported
	}
	return strings.Join(parts, "; ")
}

func formatDashboardAge(age time.Duration) string {
	if age < 0 {
		age = -age
	}
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return strconv.Itoa(int(age.Minutes())) + "m"
	case age < 24*time.Hour:
		return strconv.Itoa(int(age.Hours())) + "h"
	default:
		return strconv.Itoa(int(age.Hours()/24)) + "d"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func dashboardCompatibilityMatrixHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	compat := version.Compat()
	WriteJSON(w, http.StatusOK, compat)
}

type auditChainDashboardSource struct {
	chain audit.AuditChain
	keys  audit.VerifyingKeyProvider
}

func newAuditChainDashboardSource(chain audit.AuditChain) auditChainDashboardSource {
	return auditChainDashboardSource{chain: chain}
}

func (source auditChainDashboardSource) Evidence(ctx context.Context, now time.Time) (audit.Evidence, error) {
	snapshot, err := audit.ReadSnapshot(ctx, source.chain)
	if err != nil {
		return audit.Evidence{}, err
	}
	rows := snapshot.Rows
	var keys []audit.VerifyingKey
	var trustErr error
	if source.keys != nil {
		keys, trustErr = source.keys.VerifyingKeys(ctx)
	}
	report, err := audit.Verify(ctx, snapshot.Source(), keys)
	if err != nil {
		return audit.Evidence{}, err
	}
	if trustErr != nil {
		report.Outcome = audit.OutcomeSourceError
		report.Status, report.SignatureStatus, report.Valid = "failed", "failed", false
		report.Breaks = append(report.Breaks, audit.Break{Kind: "trust_read", Detail: trustErr.Error()})
	}
	evidence := audit.Evidence{
		SchemaVersion:   audit.LedgerSchemaVersion,
		Status:          "empty",
		EventCount:      len(rows),
		DrainCount:      0,
		Drains:          []audit.DrainConfig{},
		GeneratedAt:     now.UTC(),
		Verification:    &report,
		CheckpointCount: len(snapshot.Checkpoints),
	}
	if now.IsZero() {
		evidence.GeneratedAt = time.Now().UTC()
	}
	if len(rows) > 0 {
		evidence.Status = "ready"
		evidence.FirstRowHash = auditRowHash(rows[0].RowHash)
		evidence.LastRowHash = auditRowHash(rows[len(rows)-1].RowHash)
	}
	if !report.Valid || (len(rows) > 0 && report.SignatureStatus == "unsigned") {
		evidence.Status = "needs-review"
	}
	return evidence, nil
}

func (source auditChainDashboardSource) Events(ctx context.Context, limit int) (audit.LedgerQueryResult, error) {
	allRows, err := source.rows(ctx, 0)
	if err != nil {
		return audit.LedgerQueryResult{}, err
	}
	rows := allRows
	truncated := false
	if limit > 0 && len(allRows) > limit {
		rows = allRows[len(allRows)-limit:]
		truncated = true
	}
	events := make([]audit.DashboardLedgerEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, auditDashboardEvent(row))
	}
	nextCursor := int64(0)
	if len(events) > 0 {
		nextCursor = events[len(events)-1].ID
	}
	return audit.LedgerQueryResult{
		Events:     events,
		NextCursor: nextCursor,
		Truncated:  truncated,
	}, nil
}

func (source auditChainDashboardSource) Drains(context.Context) ([]audit.DrainConfig, error) {
	return []audit.DrainConfig{}, nil
}

func (source auditChainDashboardSource) Export(ctx context.Context, now time.Time) ([]byte, audit.ExportManifest, error) {
	if source.chain == nil {
		stream := make([]byte, 0)
		sum := sha256.Sum256(stream)
		manifest := audit.ExportManifest{
			Kind:          "audit-export",
			SchemaVersion: audit.LedgerSchemaVersion,
			TotalRows:     0,
			StreamSHA256:  "sha256:" + hex.EncodeToString(sum[:]),
			GeneratedAt:   exportGeneratedAt(now),
		}
		return stream, manifest, nil
	}
	snapshot, err := audit.ReadSnapshot(ctx, source.chain)
	if err != nil {
		return nil, audit.ExportManifest{}, err
	}
	rows := snapshot.Rows
	dst := audit.NewMemoryDestination("dashboard-audit-export")
	canonical, err := audit.ExportSnapshot(ctx, dst, snapshot)
	if err != nil {
		return nil, audit.ExportManifest{}, err
	}
	manifest := audit.ExportManifest{
		Kind:          "audit-export",
		SchemaVersion: audit.LedgerSchemaVersion,
		TotalRows:     int(canonical.RowCount),
		StreamSHA256:  "sha256:" + canonical.StreamSHA256,
		GeneratedAt:   exportGeneratedAt(now),
	}
	if len(rows) > 0 {
		manifest.FirstRowHash = auditRowHash(rows[0].RowHash)
		manifest.LastRowHash = auditRowHash(rows[len(rows)-1].RowHash)
	}
	return dst.Bytes(), manifest, nil
}

func exportGeneratedAt(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func (source auditChainDashboardSource) rows(ctx context.Context, limit int) ([]audit.Row, error) {
	if source.chain == nil {
		return nil, nil
	}
	return source.chain.Read(ctx, audit.Query{Limit: limit})
}

func auditDashboardEvent(row audit.Row) audit.DashboardLedgerEvent {
	payload := map[string]string{}
	if row.RequestID != "" {
		payload["request_id"] = row.RequestID
	}
	for key, value := range row.After {
		payload["after."+key] = auditPayloadString(value)
	}
	for key, value := range row.Before {
		payload["before."+key] = auditPayloadString(value)
	}
	if len(payload) == 0 {
		payload = nil
	}
	return audit.DashboardLedgerEvent{
		ID:                   row.Sequence,
		OccurredAt:           row.OccurredAt,
		Actor:                firstNonEmpty(row.ActorID, row.ActorType),
		Action:               row.Action,
		Resource:             auditResourceName(row),
		Outcome:              "recorded",
		Payload:              payload,
		PrevHash:             auditRowHash(row.PrevHash),
		RowHash:              auditRowHash(row.RowHash),
		CanonicalFormVersion: row.CanonicalFormVersion,
	}
}

func auditResourceName(row audit.Row) string {
	if row.ResourceKind == "" {
		return row.ResourceID
	}
	if row.ResourceID == "" {
		return row.ResourceKind
	}
	return row.ResourceKind + "/" + row.ResourceID
}

func auditPayloadString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

func auditRowHash(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	return "sha256:" + hex.EncodeToString(value)
}

func dashboardAuditEvidenceHandler(auditSource dashboardAuditSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		evidence, err := auditSource.Evidence(r.Context(), dashboardSampleTime)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "audit evidence read failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, evidence)
	}
}

func dashboardAuditEventsHandler(auditSource dashboardAuditSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		limit, err := parseStrictPageLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid page limit", nil)
			return
		}
		result, err := auditSource.Events(r.Context(), limit)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "audit event read failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

func dashboardAuditLogDrainsHandler(auditSource dashboardAuditSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		drains, err := auditSource.Drains(r.Context())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "audit drain read failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"schema_version": audit.LedgerSchemaVersion,
			"drains":         drains,
		})
	}
}

func dashboardAuditExportHandler(auditSource dashboardAuditSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		stream, manifest, err := auditSource.Export(r.Context(), time.Now().UTC())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "audit export failed", nil)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="midnight-audit.ndjson"`)
		w.Header().Set("X-Midnight-Audit-Manifest", manifest.StreamSHA256)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(stream)
	}
}

func dashboardPhase0ProofPacketSummaryHandler(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, buildPhase0ProofPacketSummary(root))
	}
}

func dashboardPhase0B5HostDrillMetadataSummaryHandler(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, buildPhase0B5HostDrillMetadataSummary(root))
	}
}

type phase0ReleaseEvidenceFile struct {
	GeneratedAt struct {
		Value  string `json:"value"`
		Source string `json:"source"`
	} `json:"generatedAt"`
	ModeFlags struct {
		Format                     string `json:"format"`
		Out                        string `json:"out"`
		LastGreen                  string `json:"lastGreen"`
		DrillAdapter               string `json:"drillAdapter"`
		ExecuteDrills              bool   `json:"executeDrills"`
		RequireExecutedDrills      bool   `json:"requireExecutedDrills"`
		ReleaseEvidence            string `json:"releaseEvidence"`
		ReleaseSummary             string `json:"releaseSummary"`
		ReleaseEvidenceIndex       string `json:"releaseEvidenceIndex"`
		VerifyReleaseEvidenceIndex string `json:"verifyReleaseEvidenceIndex"`
		VerifyEvidenceFiles        bool   `json:"verifyEvidenceFiles"`
		EvidenceRefs               []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"evidenceRefs"`
	} `json:"modeFlags"`
	GateReport struct {
		ReleaseEvidenceIndexVerification *phase0EvidenceIndexVerificationSummary `json:"releaseEvidenceIndexVerification"`
		DrillExecution                   phase0B5DrillExecutionSummary           `json:"drillExecution"`
	} `json:"gateReport"`
	RequiredDrillExecution phase0B5DrillExecutionSummary `json:"requiredDrillExecution"`
	ReleaseReady           *bool                         `json:"releaseReady"`
}

type phase0ReleaseEvidenceIndexFile struct {
	GeneratedAt struct {
		Value string `json:"value"`
	} `json:"generatedAt"`
	Entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Role string `json:"role"`
	} `json:"entries"`
}

type phase0ReleaseSummaryFile struct {
	ReleaseReady bool `json:"releaseReady"`
	GateCounts   struct {
		DrillRows             int            `json:"drillRows"`
		DrillStatuses         map[string]int `json:"drillStatuses"`
		AlertRules            int            `json:"alertRules"`
		AlertValidationPassed bool           `json:"alertValidationPassed"`
	} `json:"gateCounts"`
	ExecutedDrillCounts struct {
		Assertions int `json:"assertions"`
		Passed     int `json:"passed"`
		Failed     int `json:"failed"`
		Skipped    int `json:"skipped"`
	} `json:"executedDrillCounts"`
	EvidenceIndexVerificationStatus string                             `json:"evidenceIndexVerificationStatus"`
	EvidencePacketItemStatuses      []phase0ReleaseSummaryEvidenceItem `json:"evidencePacketItemStatuses"`
	Verdict                         string                             `json:"verdict"`
	BlockingReasons                 []struct {
		ReasonCode string `json:"reasonCode"`
		Message    string `json:"message"`
	} `json:"blockingReasons"`
	Lanes []struct {
		Lane          string   `json:"lane"`
		Status        string   `json:"status"`
		RequiredItems []string `json:"requiredItems"`
		MissingItems  []string `json:"missingItems"`
	} `json:"lanes"`
	ManifestHash string `json:"manifestHash"`
}

func buildPhase0ProofPacketSummary(root string) phase0ProofPacketSummary {
	if strings.TrimSpace(root) == "" {
		root = defaultPhase0ProofPacketDir
	}
	releaseEvidencePath := filepath.ToSlash(filepath.Join(root, "release-evidence.json"))
	evidenceIndexPath := filepath.ToSlash(filepath.Join(root, "release-evidence-index.json"))
	releaseSummaryPath := filepath.ToSlash(filepath.Join(root, "release-summary.json"))

	summary := phase0ProofPacketSummary{
		SchemaVersion: phase0ProofPacketSummarySchemaVersion,
		Status:        phase0ProofStatusReady,
		IssueCodes:    []string{},
		ReleaseEvidence: phase0ProofArtifactStatus{
			Path:   releaseEvidencePath,
			Status: phase0ProofStatusNeedsReview,
		},
		EvidenceIndex: phase0ProofArtifactStatus{
			Path:   evidenceIndexPath,
			Status: phase0ProofStatusNeedsReview,
		},
		EvidenceIndexVerification: phase0EvidenceIndexVerificationSummary{Status: "not-run"},
		ReleaseSummary: phase0ReleaseSummarySnapshot{
			Path:   releaseSummaryPath,
			Status: phase0ProofStatusNeedsReview,
		},
	}

	var releaseEvidence phase0ReleaseEvidenceFile
	if ok := readPhase0JSON(releaseEvidencePath, &releaseEvidence, &summary.ReleaseEvidence); !ok {
		addPhase0ProofIssue(&summary, phase0ProofIssueReleaseEvidenceMissing, phase0ProofIssueReleaseEvidenceInvalid, summary.ReleaseEvidence)
	} else {
		summary.ReleaseEvidence.Generated = releaseEvidence.GeneratedAt.Value
		summary.ReleaseEvidence.Status = phase0ProofStatusReady
		if releaseEvidence.ReleaseReady == nil || !*releaseEvidence.ReleaseReady {
			summary.ReleaseEvidence.Status = phase0ProofStatusNeedsReview
			summary.IssueCodes = append(summary.IssueCodes, phase0ProofIssueReleaseEvidenceNotReady)
		}
		summary.Command = phase0OptionalValue{
			Available: true,
			Value:     phase0ProofCommand(releaseEvidence),
			Source:    "release-evidence.modeFlags",
		}
		if releaseEvidence.GateReport.ReleaseEvidenceIndexVerification != nil {
			summary.EvidenceIndexVerification = *releaseEvidence.GateReport.ReleaseEvidenceIndexVerification
		}
		if isLikelyCommitSHA(releaseEvidence.ModeFlags.LastGreen) {
			summary.CommitSHA = phase0OptionalValue{Available: true, Value: releaseEvidence.ModeFlags.LastGreen, Source: "release-evidence.modeFlags.lastGreen"}
		}
	}

	var evidenceIndex phase0ReleaseEvidenceIndexFile
	if ok := readPhase0JSON(evidenceIndexPath, &evidenceIndex, &summary.EvidenceIndex); !ok {
		addPhase0ProofIssue(&summary, phase0ProofIssueEvidenceIndexMissing, phase0ProofIssueEvidenceIndexInvalid, summary.EvidenceIndex)
	} else {
		summary.EvidenceIndex.Generated = evidenceIndex.GeneratedAt.Value
		summary.EvidenceIndex.Status = phase0ProofStatusReady
		if summary.EvidenceIndexVerification.Status == "not-run" {
			summary.EvidenceIndexVerification = phase0EvidenceIndexVerificationSummary{
				Status:  "not-run",
				Path:    evidenceIndexPath,
				Entries: len(evidenceIndex.Entries),
			}
		}
	}

	var releaseSummary phase0ReleaseSummaryFile
	releaseSummaryArtifact := phase0ProofArtifactStatus{Path: releaseSummaryPath}
	if ok := readPhase0JSON(releaseSummaryPath, &releaseSummary, &releaseSummaryArtifact); !ok {
		summary.ReleaseSummary.Present = releaseSummaryArtifact.Present
		summary.ReleaseSummary.Status = releaseSummaryArtifact.Status
		summary.ReleaseSummary.Error = releaseSummaryArtifact.Error
		addPhase0ProofIssue(&summary, phase0ProofIssueReleaseSummaryMissing, phase0ProofIssueReleaseSummaryInvalid, releaseSummaryArtifact)
	} else {
		summary.ReleaseSummary = snapshotPhase0ReleaseSummary(releaseSummaryPath, releaseSummary)
		if summary.ReleaseSummary.Status != phase0ProofStatusReady {
			summary.IssueCodes = append(summary.IssueCodes, phase0ProofIssueReleaseSummaryNotReady)
		}
		if summary.EvidenceIndexVerification.Status == "not-run" && releaseSummary.EvidenceIndexVerificationStatus != "" {
			summary.EvidenceIndexVerification.Status = releaseSummary.EvidenceIndexVerificationStatus
			summary.EvidenceIndexVerification.Path = evidenceIndexPath
		}
	}

	if summary.EvidenceIndexVerification.Status != "pass" {
		summary.IssueCodes = append(summary.IssueCodes, phase0ProofIssueEvidenceIndexNotPass)
	}

	summary.RunnerOS = readPhase0OptionalValue(root, []string{"runner-os.txt", "runner_os.txt"}, "artifact")
	if !summary.CommitSHA.Available {
		summary.CommitSHA = readPhase0CommitSHA(root)
	}
	summary.OperatorNotes = readPhase0OperatorNotes(root)
	if !summary.CommitSHA.Available && summary.OperatorNotes.CommitSHA != "" {
		summary.CommitSHA = phase0OptionalValue{Available: true, Value: summary.OperatorNotes.CommitSHA, Source: summary.OperatorNotes.Path}
	}

	summary.IssueCodes = uniqueStrings(summary.IssueCodes)
	summary.Status = rollupPhase0ProofSummaryStatus(summary)
	return summary
}

func buildPhase0B5HostDrillMetadataSummary(root string) phase0B5HostDrillMetadataSummary {
	if strings.TrimSpace(root) == "" {
		root = defaultPhase0ProofPacketDir
	}
	releaseEvidencePath, releaseEvidenceReadPath := phase0B5ArtifactPath(root, "release-evidence.json")
	evidenceIndexPath, evidenceIndexReadPath := phase0B5ArtifactPath(root, "release-evidence-index.json")
	releaseSummaryPath, releaseSummaryReadPath := phase0B5ArtifactPath(root, "release-summary.json")
	summary := phase0B5HostDrillMetadataSummary{
		SchemaVersion:   phase0B5HostDrillSummarySchemaVersion,
		Status:          phase0ProofStatusReady,
		IssueCodes:      []string{},
		Source:          "manifest",
		NoLiveHostProbe: true,
		Release: phase0B5ReleaseSnapshot{
			ReleaseEvidence: phase0ProofArtifactStatus{Path: releaseEvidencePath, Status: phase0ProofStatusNeedsReview},
			EvidenceIndex:   phase0ProofArtifactStatus{Path: evidenceIndexPath, Status: phase0ProofStatusNeedsReview},
			ReleaseSummary:  phase0ProofArtifactStatus{Path: releaseSummaryPath, Status: phase0ProofStatusNeedsReview},
		},
		Manifest: phase0B5DrillManifestSnapshot{
			Path:   defaultPhase0DrillManifestPath,
			Status: phase0ProofStatusNeedsReview,
		},
		Portability: phase0B5PortabilitySummary{WorkspaceRelativeRefs: true},
		HostMetadata: phase0B5HostMetadataRollup{
			MetadataSource:         "manifest-envelope",
			ManifestEnvelopeFields: []string{"id", "title", "status", "reason-code", "event", "diagnostic", "runbook"},
		},
		CandidateCatalog:   phase0B6EmptyCandidateCatalog(defaultPhase0DrillManifestPath),
		ReservationIndex:   phase0B7EmptyReservationIndex(defaultPhase0B7ReservationIndexPath),
		PromotionReadiness: phase0B8EmptyPromotionReadiness(defaultPhase0B8PromotionReadinessPath),
		ReleaseArchive:     phase0B9EmptyReleaseArchive(defaultPhase0B9ReleaseArchivePath),
	}

	var releaseEvidence phase0ReleaseEvidenceFile
	if readPhase0JSON(releaseEvidenceReadPath, &releaseEvidence, &summary.Release.ReleaseEvidence) {
		summary.Release.ReleaseEvidence.Status = phase0ProofStatusReady
		summary.Release.ReleaseEvidence.Generated = releaseEvidence.GeneratedAt.Value
		execution := normalizePhase0B5DrillExecution(releaseEvidence.RequiredDrillExecution)
		if !execution.Available {
			execution = normalizePhase0B5DrillExecution(releaseEvidence.GateReport.DrillExecution)
		}
		summary.Release.RequiredDrillExecution = execution
		summary.Source = "release-artifacts+manifest"
	}

	var evidenceIndex phase0ReleaseEvidenceIndexFile
	if readPhase0JSON(evidenceIndexReadPath, &evidenceIndex, &summary.Release.EvidenceIndex) {
		summary.Release.EvidenceIndex.Status = phase0ProofStatusReady
		summary.Release.EvidenceIndex.Generated = evidenceIndex.GeneratedAt.Value
	}
	for _, ref := range phase0B5EvidenceIndexPaths(evidenceIndex) {
		if isAbsoluteArtifactPath(ref) {
			summary.Portability.WorkspaceRelativeRefs = false
			summary.Portability.AbsolutePathRefs = append(summary.Portability.AbsolutePathRefs, filepath.ToSlash(ref))
		}
	}
	if len(summary.Portability.AbsolutePathRefs) > 0 {
		summary.IssueCodes = append(summary.IssueCodes, phase0B5IssueAbsoluteArtifactRef)
	}

	var releaseSummary phase0ReleaseSummaryFile
	if readPhase0JSON(releaseSummaryReadPath, &releaseSummary, &summary.Release.ReleaseSummary) {
		summary.Release.ReleaseSummary.Status = phase0ProofStatusReady
		summary.Release.ReleaseReady = releaseSummary.ReleaseReady
		summary.Release.Verdict = releaseSummary.Verdict
		summary.Release.EvidenceIndexVerificationStatus = releaseSummary.EvidenceIndexVerificationStatus
		summary.Release.GateDrillRows = releaseSummary.GateCounts.DrillRows
		summary.Release.ExecutedDrillCounts = phase0ReleaseSummaryDrillCounts{
			Assertions: releaseSummary.ExecutedDrillCounts.Assertions,
			Passed:     releaseSummary.ExecutedDrillCounts.Passed,
			Failed:     releaseSummary.ExecutedDrillCounts.Failed,
			Skipped:    releaseSummary.ExecutedDrillCounts.Skipped,
		}
		summary.Release.EvidencePacketItemStatuses = releaseSummary.EvidencePacketItemStatuses
		summary.Source = "release-artifacts+manifest"
	}

	manifestPath := phase0B5ManifestPath(evidenceIndex)
	summary.Manifest.Path = manifestPath
	summary.CandidateCatalog = phase0B6EmptyCandidateCatalog(manifestPath)
	manifest, err := readPhase0B5DrillManifest(root, manifestPath)
	if err != nil {
		summary.Manifest.Present = !os.IsNotExist(err)
		summary.Manifest.Status = phase0ProofStatusNeedsReview
		if summary.Manifest.Present {
			summary.Manifest.Status = phase0ProofStatusBlocked
			summary.CandidateCatalog.Status = phase0ProofStatusBlocked
			summary.Manifest.Error = err.Error()
			summary.IssueCodes = append(summary.IssueCodes, phase0B5IssueDrillManifestInvalid)
		} else {
			summary.IssueCodes = append(summary.IssueCodes, phase0B5IssueDrillManifestMissing)
		}
	} else {
		summary.Manifest = phase0B5DrillManifestSnapshot{
			Path:         manifestPath,
			Present:      true,
			Status:       phase0ProofStatusReady,
			Version:      manifest.Version,
			Owner:        manifest.Owner,
			DrillCount:   len(manifest.Drills),
			ManifestHash: manifest.Hash,
		}
		summary.Drills = phase0B5HostDrillRows(manifest.Drills)
		summary.HostMetadata = phase0B5HostMetadata(summary.Drills)
		summary.CandidateCatalog = phase0B6CandidateCatalogFromManifest(manifestPath, summary.Manifest.Status, manifest)
		for _, ref := range phase0B6CatalogEvidenceRefs(manifest.Drills) {
			if isAbsoluteArtifactPath(ref) {
				summary.Portability.WorkspaceRelativeRefs = false
				summary.Portability.AbsolutePathRefs = append(summary.Portability.AbsolutePathRefs, filepath.ToSlash(ref))
			}
		}
	}

	reservationIndex, err := readPhase0B7ReservationIndex(root, defaultPhase0B7ReservationIndexPath)
	if err == nil {
		summary.ReservationIndex = phase0B7ReservationIndexFromFile(defaultPhase0B7ReservationIndexPath, reservationIndex)
		for _, ref := range phase0B7ReservationEvidenceRefs(reservationIndex.Reservations) {
			if isAbsoluteArtifactPath(ref) {
				summary.Portability.WorkspaceRelativeRefs = false
				summary.Portability.AbsolutePathRefs = append(summary.Portability.AbsolutePathRefs, filepath.ToSlash(ref))
			}
		}
	} else if !os.IsNotExist(err) {
		summary.ReservationIndex = phase0B7EmptyReservationIndex(defaultPhase0B7ReservationIndexPath)
		summary.ReservationIndex.Status = phase0ProofStatusBlocked
	}

	promotionReadiness, err := readPhase0B8PromotionReadiness(root, defaultPhase0B8PromotionReadinessPath)
	if err == nil {
		summary.PromotionReadiness = phase0B8PromotionReadinessFromFile(defaultPhase0B8PromotionReadinessPath, promotionReadiness)
		for _, ref := range phase0B8PromotionReadinessEvidenceRefs(promotionReadiness.ReadinessRows) {
			if isAbsoluteArtifactPath(ref) {
				summary.Portability.WorkspaceRelativeRefs = false
				summary.Portability.AbsolutePathRefs = append(summary.Portability.AbsolutePathRefs, filepath.ToSlash(ref))
			}
		}
	} else if !os.IsNotExist(err) {
		summary.PromotionReadiness = phase0B8EmptyPromotionReadiness(defaultPhase0B8PromotionReadinessPath)
		summary.PromotionReadiness.Status = phase0ProofStatusBlocked
		summary.IssueCodes = append(summary.IssueCodes, phase0B8IssuePromotionReadinessInvalid)
	}

	releaseArchive, err := readPhase0B9ReleaseArchive(root, defaultPhase0B9ReleaseArchivePath)
	if err == nil {
		summary.ReleaseArchive = phase0B9ReleaseArchiveFromFile(defaultPhase0B9ReleaseArchivePath, releaseArchive)
		for _, ref := range phase0B9ReleaseArchiveEvidenceRefs(releaseArchive) {
			if isAbsoluteArtifactPath(ref) {
				summary.Portability.WorkspaceRelativeRefs = false
				summary.Portability.AbsolutePathRefs = append(summary.Portability.AbsolutePathRefs, filepath.ToSlash(ref))
			}
		}
	} else if !os.IsNotExist(err) {
		summary.ReleaseArchive = phase0B9EmptyReleaseArchive(defaultPhase0B9ReleaseArchivePath)
		summary.ReleaseArchive.Status = phase0ProofStatusBlocked
		summary.ReleaseArchive.IssueCodes = []string{phase0B9IssueReleaseArchiveInvalid}
		summary.ReleaseArchive.Error = err.Error()
		summary.IssueCodes = append(summary.IssueCodes, phase0B9IssueReleaseArchiveInvalid)
	}

	if len(summary.Portability.AbsolutePathRefs) > 0 {
		summary.IssueCodes = append(summary.IssueCodes, phase0B5IssueAbsoluteArtifactRef)
	}
	summary.Portability.AbsolutePathRefs = uniqueStrings(summary.Portability.AbsolutePathRefs)
	summary.IssueCodes = uniqueStrings(summary.IssueCodes)
	if len(summary.IssueCodes) > 0 {
		summary.Status = phase0ProofStatusNeedsReview
	}
	for _, artifact := range []phase0ProofArtifactStatus{summary.Release.ReleaseEvidence, summary.Release.EvidenceIndex, summary.Release.ReleaseSummary, {Status: summary.Manifest.Status}, {Status: summary.PromotionReadiness.Status}, {Status: summary.ReleaseArchive.Status}} {
		if artifact.Status == phase0ProofStatusBlocked {
			summary.Status = phase0ProofStatusBlocked
			break
		}
	}
	return summary
}

func phase0B5ArtifactPath(root, name string) (displayPath string, readPath string) {
	readPath = filepath.Join(root, name)
	if isAbsoluteArtifactPath(root) {
		return filepath.ToSlash(name), readPath
	}
	return filepath.ToSlash(filepath.Join(root, name)), readPath
}

func normalizePhase0B5DrillExecution(in phase0B5DrillExecutionSummary) phase0B5DrillExecutionSummary {
	if in.Adapter != "" || in.AdapterType != "" || in.Assertions > 0 || in.Passed > 0 || in.Failed > 0 || in.Skipped > 0 || len(in.DrillStatuses) > 0 {
		in.Available = true
	}
	return in
}

func phase0B5EvidenceIndexPaths(index phase0ReleaseEvidenceIndexFile) []string {
	var paths []string
	for _, entry := range index.Entries {
		if strings.TrimSpace(entry.Path) != "" {
			paths = append(paths, entry.Path)
		}
	}
	return paths
}

func phase0B5ManifestPath(index phase0ReleaseEvidenceIndexFile) string {
	for _, entry := range index.Entries {
		if entry.Name == "drill-manifest" && strings.TrimSpace(entry.Path) != "" && !isAbsoluteArtifactPath(entry.Path) {
			return filepath.ToSlash(entry.Path)
		}
	}
	return defaultPhase0DrillManifestPath
}

func readPhase0B5DrillManifest(root, displayPath string) (phase0DrillManifestFile, error) {
	if isAbsoluteArtifactPath(displayPath) {
		return phase0DrillManifestFile{}, os.ErrNotExist
	}
	candidates := uniqueStrings([]string{
		filepath.Join(root, filepath.FromSlash(displayPath)),
		displayPath,
		defaultPhase0DrillManifestPath,
	})
	var lastErr error
	for _, candidate := range candidates {
		data, err := os.ReadFile(filepath.Clean(candidate))
		if err != nil {
			lastErr = err
			continue
		}
		manifest, err := parsePhase0DrillManifest(data)
		if err != nil {
			return phase0DrillManifestFile{}, err
		}
		return manifest, nil
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return phase0DrillManifestFile{}, lastErr
}

func isAbsoluteArtifactPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return true
	}
	return filepath.IsAbs(filepath.FromSlash(path))
}

func parsePhase0DrillManifest(data []byte) (phase0DrillManifestFile, error) {
	if len(data) == 0 {
		return phase0DrillManifestFile{}, errPhase0B5Manifest("empty drill manifest")
	}
	hash := sha256.Sum256(data)
	manifest := phase0DrillManifestFile{Hash: hex.EncodeToString(hash[:])}
	inDrills := false
	inMetadata := false
	metadataList := ""
	current := -1
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case line == "drills:":
			inDrills = true
			continue
		case !inDrills && strings.HasPrefix(line, "version:"):
			manifest.Version = atoiDefault(strings.TrimSpace(strings.TrimPrefix(line, "version:")))
			continue
		case !inDrills && strings.HasPrefix(line, "owner:"):
			manifest.Owner = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "owner:")))
			continue
		case inDrills && strings.HasPrefix(line, "- id:"):
			manifest.Drills = append(manifest.Drills, phase0DrillManifestRow{ID: cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- id:")))})
			current = len(manifest.Drills) - 1
			inMetadata = false
			metadataList = ""
			continue
		case inDrills && current >= 0 && line == "metadata:":
			inMetadata = true
			metadataList = ""
			continue
		case inDrills && current >= 0 && inMetadata && (line == "tags:" || line == "notes:" || line == "subcases:"):
			metadataList = strings.TrimSuffix(line, ":")
			continue
		case inDrills && current >= 0 && inMetadata && metadataList != "" && strings.HasPrefix(line, "- "):
			value := cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			switch metadataList {
			case "tags":
				manifest.Drills[current].MetadataTags = append(manifest.Drills[current].MetadataTags, value)
			case "notes":
				manifest.Drills[current].MetadataNotes = append(manifest.Drills[current].MetadataNotes, value)
			case "subcases":
				manifest.Drills[current].MetadataSubcases = append(manifest.Drills[current].MetadataSubcases, value)
			}
			continue
		}
		if !inDrills || current < 0 {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = cleanYAMLScalar(strings.TrimSpace(value))
		switch strings.TrimSpace(key) {
		case "title":
			inMetadata = false
			metadataList = ""
			manifest.Drills[current].Title = value
		case "status":
			inMetadata = false
			metadataList = ""
			manifest.Drills[current].Status = value
		case "reason-code":
			inMetadata = false
			metadataList = ""
			manifest.Drills[current].ReasonCode = value
		case "event":
			inMetadata = false
			metadataList = ""
			manifest.Drills[current].Event = value
		case "diagnostic":
			inMetadata = false
			metadataList = ""
			manifest.Drills[current].Diagnostic = value
		case "runbook":
			inMetadata = false
			metadataList = ""
			manifest.Drills[current].Runbook = value
		}
	}
	if manifest.Version == 0 || manifest.Owner == "" || len(manifest.Drills) == 0 {
		return phase0DrillManifestFile{}, errPhase0B5Manifest("missing version, owner, or drills")
	}
	for _, drill := range manifest.Drills {
		if drill.ID == "" || drill.ReasonCode == "" || drill.Event == "" || drill.Diagnostic == "" || drill.Runbook == "" {
			return phase0DrillManifestFile{}, errPhase0B5Manifest("drill row missing manifest envelope field")
		}
	}
	return manifest, nil
}

type phase0B5ManifestError string

func (e phase0B5ManifestError) Error() string {
	return string(e)
}

func errPhase0B5Manifest(message string) error {
	return phase0B5ManifestError(message)
}

func cleanYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return value
}

func atoiDefault(value string) int {
	out := 0
	for _, r := range strings.TrimSpace(value) {
		if r < '0' || r > '9' {
			return 0
		}
		out = out*10 + int(r-'0')
	}
	return out
}

func phase0B5HostDrillRows(drills []phase0DrillManifestRow) []phase0B5HostDrillMetadataRow {
	rows := make([]phase0B5HostDrillMetadataRow, 0, len(drills))
	for _, drill := range drills {
		components := inferPhase0B5DrillComponents(drill)
		candidate := phase0B6CandidateFromManifestRow(drill)
		rows = append(rows, phase0B5HostDrillMetadataRow{
			ID:             drill.ID,
			Title:          drill.Title,
			Status:         drill.Status,
			ReasonCode:     drill.ReasonCode,
			Event:          drill.Event,
			Diagnostic:     drill.Diagnostic,
			Runbook:        drill.Runbook,
			Components:     components,
			HostScoped:     phase0B5IsHostScoped(components),
			MetadataSource: "manifest-envelope",
			Candidate:      candidate.ID != "",
			Phase3Domain:   candidate.Domain,
			Phase3Drill:    candidate.Phase3Drill,
			HostScope:      candidate.HostScope,
			Execution:      candidate.Execution,
			EvidenceRef:    candidate.EvidenceRef,
		})
	}
	return rows
}

func phase0B5HostMetadata(rows []phase0B5HostDrillMetadataRow) phase0B5HostMetadataRollup {
	components := map[string]struct{}{}
	runbooks := map[string]struct{}{}
	rollup := phase0B5HostMetadataRollup{
		MetadataSource:         "manifest-envelope",
		ManifestEnvelopeFields: []string{"id", "title", "status", "reason-code", "event", "diagnostic", "runbook"},
	}
	for _, row := range rows {
		if row.HostScoped {
			rollup.HostScopedDrillCount++
		}
		if len(row.Components) > 0 {
			rollup.ComponentScopedDrillCount++
		}
		for _, component := range row.Components {
			components[component] = struct{}{}
		}
		if row.Runbook != "" {
			runbooks[row.Runbook] = struct{}{}
		}
	}
	rollup.Components = sortedKeys(components)
	rollup.Runbooks = sortedKeys(runbooks)
	return rollup
}

func inferPhase0B5DrillComponents(drill phase0DrillManifestRow) []string {
	text := strings.ToLower(strings.Join([]string{drill.Title, drill.ReasonCode, drill.Event, drill.Diagnostic, drill.Runbook}, " "))
	components := map[string]struct{}{}
	addIfContains := func(component string, needles ...string) {
		for _, needle := range needles {
			if strings.Contains(text, needle) {
				components[component] = struct{}{}
				return
			}
		}
	}
	addIfContains("host", "host", "agent", "node")
	addIfContains("storage", "disk", "filesystem", "volume")
	addIfContains("backup", "backup", "restore", "offhost")
	addIfContains("runtime", "deploy", "buildkit", "builder", "cache")
	addIfContains("network", "conntrack", "wireguard", "pmtu", "ipam", "network")
	addIfContains("ingress", "api target", "route", "ingress")
	addIfContains("crypto", "cert", "certificate", "key")
	addIfContains("support", "supportbundle", "support bundle")
	addIfContains("observability", "alert", "prometheus", "observability")
	addIfContains("gate", "gate", "manifest")
	return sortedKeys(components)
}

func phase0B5IsHostScoped(components []string) bool {
	hostDomains := map[string]struct{}{
		"backup":  {},
		"host":    {},
		"ingress": {},
		"network": {},
		"runtime": {},
		"storage": {},
	}
	for _, component := range components {
		if _, ok := hostDomains[component]; ok {
			return true
		}
	}
	return false
}

func phase0B6RequiredDomains() []string {
	return []string{"networking", "runtime_adapter", "registry_cache", "volume_attach"}
}

func phase0B6EmptyCandidateCatalog(source string) phase0B6CandidateCatalog {
	return phase0B6CandidateCatalog{
		SchemaVersion:    phase0B6CandidateCatalogSchemaVersion,
		Status:           phase0ProofStatusNeedsReview,
		Source:           filepath.ToSlash(source),
		OfflineByDefault: true,
		NoLiveHostProbe:  true,
		RequiredDomains:  phase0B6RequiredDomains(),
		DomainCoverage:   phase0B6DomainCoverageFromCounts(nil),
		CandidateCounts:  phase0B6CandidateCounts{ByDomain: map[string]int{}},
	}
}

func phase0B6CandidateCatalogFromManifest(source, manifestStatus string, manifest phase0DrillManifestFile) phase0B6CandidateCatalog {
	catalog := phase0B6EmptyCandidateCatalog(source)
	catalog.Version = manifest.Version
	catalog.Status = manifestStatus
	if manifestStatus != phase0ProofStatusReady {
		return catalog
	}

	counts := map[string]int{}
	for _, drill := range manifest.Drills {
		candidate := phase0B6CandidateFromManifestRow(drill)
		if candidate.ID == "" {
			continue
		}
		catalog.Candidates = append(catalog.Candidates, candidate)
		catalog.CandidateCounts.Total++
		if phase0B6HasMetadataValue(drill.MetadataTags, "host-scoped") {
			catalog.CandidateCounts.HostScoped++
		}
		if phase0B6HasMetadataValue(drill.MetadataTags, "local-only") {
			catalog.CandidateCounts.LocalOnly++
		}
		if candidate.Execution == "planner-only" {
			catalog.CandidateCounts.PlannerOnly++
		}
		if candidate.Domain != "" {
			counts[candidate.Domain]++
		}
	}

	catalog.CandidateCounts.ByDomain = counts
	catalog.DomainCoverage = phase0B6DomainCoverageFromCounts(counts)
	for _, coverage := range catalog.DomainCoverage {
		if !coverage.Covered {
			catalog.MissingDomains = append(catalog.MissingDomains, coverage.Domain)
		}
	}
	if catalog.CandidateCounts.Total == 0 || len(catalog.MissingDomains) > 0 {
		catalog.Status = phase0ProofStatusNeedsReview
	}
	return catalog
}

func phase0B6DomainCoverageFromCounts(counts map[string]int) []phase0B6DomainCoverage {
	coverage := make([]phase0B6DomainCoverage, 0, len(phase0B6RequiredDomains()))
	for _, domain := range phase0B6RequiredDomains() {
		count := 0
		if counts != nil {
			count = counts[domain]
		}
		coverage = append(coverage, phase0B6DomainCoverage{
			Domain:         domain,
			CandidateCount: count,
			Covered:        count > 0,
		})
	}
	return coverage
}

func phase0B6CandidateFromManifestRow(drill phase0DrillManifestRow) phase0B6HostDrillCandidateRow {
	if !phase0B6HasMetadataValue(drill.MetadataTags, "phase3") || !phase0B6HasMetadataValue(drill.MetadataTags, "host-scoped") {
		return phase0B6HostDrillCandidateRow{}
	}
	notes := phase0B6KeyValueMetadata(drill.MetadataNotes)
	subcases := phase0B6KeyValueMetadata(drill.MetadataSubcases)
	domain := notes["domain"]
	if domain == "" {
		domain = subcases["phase3.domain"]
	}
	evidenceRef := notes["evidence-ref"]
	if evidenceRef == "" {
		evidenceRef = subcases["phase3.evidence"]
	}
	return phase0B6HostDrillCandidateRow{
		ID:          drill.ID,
		Domain:      domain,
		Phase3Drill: notes["phase3-drill"],
		HostScope:   notes["host-scope"],
		Execution:   notes["execution"],
		EvidenceRef: evidenceRef,
	}
}

func phase0B6CatalogEvidenceRefs(drills []phase0DrillManifestRow) []string {
	var refs []string
	for _, drill := range drills {
		notes := phase0B6KeyValueMetadata(drill.MetadataNotes)
		subcases := phase0B6KeyValueMetadata(drill.MetadataSubcases)
		for _, ref := range []string{notes["evidence-ref"], subcases["phase3.evidence"]} {
			if strings.TrimSpace(ref) != "" {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func phase0B6HasMetadataValue(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func phase0B6KeyValueMetadata(values []string) map[string]string {
	out := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(strings.TrimSpace(raw), "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func readPhase0B7ReservationIndex(root, displayPath string) (phase0B7ReservationIndexFile, error) {
	if isAbsoluteArtifactPath(displayPath) {
		return phase0B7ReservationIndexFile{}, os.ErrNotExist
	}
	candidates := uniqueStrings([]string{
		filepath.Join(root, filepath.FromSlash(displayPath)),
		displayPath,
		defaultPhase0B7ReservationIndexPath,
	})
	var lastErr error
	for _, candidate := range candidates {
		data, err := os.ReadFile(filepath.Clean(candidate))
		if err != nil {
			lastErr = err
			continue
		}
		index, err := parsePhase0B7ReservationIndex(data)
		if err != nil {
			return phase0B7ReservationIndexFile{}, err
		}
		return index, nil
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return phase0B7ReservationIndexFile{}, lastErr
}

func parsePhase0B7ReservationIndex(data []byte) (phase0B7ReservationIndexFile, error) {
	if len(data) == 0 {
		return phase0B7ReservationIndexFile{}, errPhase0B5Manifest("empty reservation index")
	}
	index := phase0B7ReservationIndexFile{}
	inReservations := false
	evidenceRefs := false
	current := -1
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case line == "reservations:":
			inReservations = true
			continue
		case !inReservations && strings.HasPrefix(line, "version:"):
			index.Version = atoiDefault(strings.TrimSpace(strings.TrimPrefix(line, "version:")))
			continue
		case !inReservations && strings.HasPrefix(line, "owner:"):
			index.Owner = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "owner:")))
			continue
		case !inReservations && strings.HasPrefix(line, "sprint:"):
			index.Sprint = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "sprint:")))
			continue
		case !inReservations && strings.HasPrefix(line, "local-only:"):
			index.LocalOnly = parseYAMLBool(strings.TrimSpace(strings.TrimPrefix(line, "local-only:")))
			continue
		case !inReservations && strings.HasPrefix(line, "source-manifest:"):
			index.SourceManifest = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "source-manifest:")))
			continue
		case inReservations && strings.HasPrefix(line, "- id:"):
			index.Reservations = append(index.Reservations, phase0B7ReservationRow{ID: cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- id:")))})
			current = len(index.Reservations) - 1
			evidenceRefs = false
			continue
		case inReservations && current >= 0 && line == "evidence-refs:":
			evidenceRefs = true
			continue
		case inReservations && current >= 0 && evidenceRefs && strings.HasPrefix(line, "- "):
			index.Reservations[current].EvidenceRefs = append(index.Reservations[current].EvidenceRefs, cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- "))))
			continue
		}
		if !inReservations || current < 0 {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = cleanYAMLScalar(strings.TrimSpace(value))
		switch strings.TrimSpace(key) {
		case "status":
			evidenceRefs = false
			index.Reservations[current].Status = value
		case "domain":
			evidenceRefs = false
			index.Reservations[current].Domain = value
		case "carrier-id":
			evidenceRefs = false
			index.Reservations[current].CarrierID = value
		case "carrier-reason-code":
			evidenceRefs = false
			index.Reservations[current].CarrierReasonCode = value
		case "planned-drill":
			evidenceRefs = false
			index.Reservations[current].PlannedDrill = value
		case "host-scope":
			evidenceRefs = false
			index.Reservations[current].HostScope = value
		case "execution":
			evidenceRefs = false
			index.Reservations[current].Execution = value
		case "rig":
			evidenceRefs = false
			index.Reservations[current].Rig = value
		case "note":
			evidenceRefs = false
			index.Reservations[current].Note = value
		}
	}
	if index.Version == 0 || index.Owner == "" || index.SourceManifest == "" || len(index.Reservations) == 0 {
		return phase0B7ReservationIndexFile{}, errPhase0B5Manifest("missing version, owner, source-manifest, or reservations")
	}
	for _, reservation := range index.Reservations {
		if reservation.ID == "" || reservation.Status == "" || reservation.Domain == "" || reservation.CarrierID == "" || reservation.CarrierReasonCode == "" || reservation.PlannedDrill == "" || reservation.HostScope == "" || reservation.Execution == "" || reservation.Rig == "" {
			return phase0B7ReservationIndexFile{}, errPhase0B5Manifest("reservation row missing required field")
		}
	}
	return index, nil
}

func parseYAMLBool(value string) bool {
	switch strings.ToLower(cleanYAMLScalar(value)) {
	case "true", "yes", "y", "1":
		return true
	default:
		return false
	}
}

func phase0B7EmptyReservationIndex(source string) phase0B7ReservationIndex {
	return phase0B7ReservationIndex{
		SchemaVersion:     phase0B7ReservationIndexSchemaVersion,
		Status:            phase0ProofStatusNeedsReview,
		Source:            filepath.ToSlash(source),
		LocalOnly:         true,
		OfflineByDefault:  true,
		NoLiveHostProbe:   true,
		ReservationCounts: phase0B7ReservationCounts{ByDomain: map[string]int{}},
		DomainCoverage:    phase0B6DomainCoverageFromCounts(nil),
	}
}

func phase0B7ReservationIndexFromFile(source string, file phase0B7ReservationIndexFile) phase0B7ReservationIndex {
	index := phase0B7EmptyReservationIndex(source)
	index.Version = file.Version
	index.SourceManifest = filepath.ToSlash(file.SourceManifest)
	index.LocalOnly = file.LocalOnly
	index.Reservations = append(index.Reservations, file.Reservations...)

	counts := map[string]int{}
	for _, reservation := range file.Reservations {
		index.ReservationCounts.Total++
		if reservation.Status == "reserved" {
			index.ReservationCounts.Reserved++
		}
		if reservation.Rig == "future" {
			index.ReservationCounts.FutureRig++
		}
		if reservation.Execution == "planner-only" {
			index.ReservationCounts.PlannerOnly++
		}
		if reservation.Domain != "" {
			counts[reservation.Domain]++
		}
	}
	index.ReservationCounts.ByDomain = counts
	index.DomainCoverage = phase0B6DomainCoverageFromCounts(counts)
	for _, coverage := range index.DomainCoverage {
		if !coverage.Covered {
			index.MissingDomains = append(index.MissingDomains, coverage.Domain)
		}
	}
	index.Status = phase0ProofStatusReady
	if !index.LocalOnly || index.ReservationCounts.Total == 0 || len(index.MissingDomains) > 0 {
		index.Status = phase0ProofStatusNeedsReview
	}
	return index
}

func phase0B7ReservationEvidenceRefs(reservations []phase0B7ReservationRow) []string {
	var refs []string
	for _, reservation := range reservations {
		for _, ref := range reservation.EvidenceRefs {
			if strings.TrimSpace(ref) != "" {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func readPhase0B8PromotionReadiness(root, displayPath string) (phase0B8PromotionReadinessFile, error) {
	if isAbsoluteArtifactPath(displayPath) {
		return phase0B8PromotionReadinessFile{}, os.ErrNotExist
	}
	candidates := uniqueStrings([]string{
		filepath.Join(root, filepath.FromSlash(displayPath)),
		displayPath,
		defaultPhase0B8PromotionReadinessPath,
	})
	var lastErr error
	for _, candidate := range candidates {
		data, err := os.ReadFile(filepath.Clean(candidate))
		if err != nil {
			lastErr = err
			continue
		}
		readiness, err := parsePhase0B8PromotionReadiness(data)
		if err != nil {
			return phase0B8PromotionReadinessFile{}, err
		}
		return readiness, nil
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return phase0B8PromotionReadinessFile{}, lastErr
}

func parsePhase0B8PromotionReadiness(data []byte) (phase0B8PromotionReadinessFile, error) {
	if len(data) == 0 {
		return phase0B8PromotionReadinessFile{}, errPhase0B5Manifest("empty promotion readiness")
	}
	file := phase0B8PromotionReadinessFile{}
	section := ""
	rowList := ""
	currentRow := -1
	currentUnblocker := -1
	for _, raw := range strings.Split(string(data), "\n") {
		noCR := strings.TrimRight(raw, "\r")
		line := strings.TrimSpace(noCR)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		indent := len(noCR) - len(strings.TrimLeft(noCR, " "))
		if indent == 0 && !strings.HasPrefix(line, "- ") {
			currentRow = -1
			currentUnblocker = -1
			rowList = ""
		}
		switch {
		case indent == 0 && line == "checks:":
			section = "readiness"
			continue
		case indent == 0 && isPhase0B8ReadinessSection(line):
			section = "readiness"
			continue
		case indent == 0 && line == "unblockers:":
			section = "unblockers"
			continue
		case indent == 0 && strings.HasPrefix(line, "version:"):
			file.Version = atoiDefault(strings.TrimSpace(strings.TrimPrefix(line, "version:")))
			continue
		case indent == 0 && strings.HasPrefix(line, "owner:"):
			file.Owner = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "owner:")))
			continue
		case indent == 0 && strings.HasPrefix(line, "sprint:"):
			file.Sprint = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "sprint:")))
			continue
		case indent == 0 && (strings.HasPrefix(line, "local-only:") || strings.HasPrefix(line, "local_only:")):
			_, value, _ := strings.Cut(line, ":")
			file.LocalOnly = parseYAMLBool(value)
			continue
		case indent == 0 && (strings.HasPrefix(line, "source-manifest:") || strings.HasPrefix(line, "source_manifest:")):
			_, value, _ := strings.Cut(line, ":")
			file.SourceManifest = cleanYAMLScalar(value)
			continue
		case section == "readiness" && isPhase0B8ReadinessSection(line):
			continue
		case section == "readiness" && strings.HasPrefix(line, "- id:"):
			file.ReadinessRows = append(file.ReadinessRows, phase0B8PromotionReadinessRow{
				ID:        cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- id:"))),
				LocalOnly: file.LocalOnly,
			})
			currentRow = len(file.ReadinessRows) - 1
			currentUnblocker = -1
			rowList = ""
			continue
		case section == "unblockers" && strings.HasPrefix(line, "- id:"):
			file.Unblockers = append(file.Unblockers, phase0B8PromotionUnblocker{ID: cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- id:")))})
			currentUnblocker = len(file.Unblockers) - 1
			currentRow = -1
			continue
		case section == "readiness" && currentRow >= 0 && (line == "evidence-refs:" || line == "evidence_refs:" || line == "unblockers:" || line == "required-unblockers:" || line == "required_unblockers:"):
			rowList = strings.TrimSuffix(strings.ReplaceAll(line, "_", "-"), ":")
			if rowList == "required-unblockers" {
				rowList = "unblockers"
			}
			continue
		case section == "readiness" && currentRow >= 0 && rowList != "" && strings.HasPrefix(line, "- "):
			value := cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			switch rowList {
			case "evidence-refs":
				file.ReadinessRows[currentRow].EvidenceRefs = append(file.ReadinessRows[currentRow].EvidenceRefs, value)
			case "unblockers":
				file.ReadinessRows[currentRow].Unblockers = append(file.ReadinessRows[currentRow].Unblockers, value)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = cleanYAMLScalar(value)
		if section == "readiness" && currentRow >= 0 {
			if !phase0B8RowListKey(key) {
				rowList = ""
			}
			applyPhase0B8ReadinessRowField(&file.ReadinessRows[currentRow], key, value, file.LocalOnly)
			continue
		}
		if section == "unblockers" && currentUnblocker >= 0 {
			applyPhase0B8UnblockerField(&file.Unblockers[currentUnblocker], key, value)
		}
	}
	if file.Version == 0 || file.Owner == "" || file.SourceManifest == "" || len(file.ReadinessRows) == 0 {
		return phase0B8PromotionReadinessFile{}, errPhase0B5Manifest("missing version, owner, source-manifest, or readiness rows")
	}
	for _, row := range file.ReadinessRows {
		if row.ID == "" || row.Status == "" || row.Domain == "" {
			return phase0B8PromotionReadinessFile{}, errPhase0B5Manifest("promotion readiness row missing required field")
		}
	}
	return file, nil
}

func isPhase0B8ReadinessSection(line string) bool {
	switch strings.TrimSuffix(line, ":") {
	case "promotion-readiness", "promotion_readiness", "readiness", "readiness-rows", "readiness_rows", "rows", "checklist":
		return true
	default:
		return false
	}
}

func phase0B8RowListKey(key string) bool {
	switch key {
	case "evidence-refs", "evidence_refs", "unblockers", "required-unblockers", "required_unblockers":
		return true
	default:
		return false
	}
}

func applyPhase0B8ReadinessRowField(row *phase0B8PromotionReadinessRow, key, value string, fileLocalOnly bool) {
	switch key {
	case "status", "readiness":
		row.Status = normalizePhase0B8PromotionStatus(value)
	case "domain":
		row.Domain = value
	case "reservation-id", "reservation_id":
		row.ReservationID = value
	case "carrier-id", "carrier_id":
		row.CarrierID = value
	case "carrier-reason-code", "carrier_reason_code":
		row.CarrierReasonCode = value
	case "planned-drill", "planned_drill", "phase3-drill", "phase3_drill":
		row.PlannedDrill = value
	case "host-scope", "host_scope":
		row.HostScope = value
	case "execution":
		row.Execution = value
	case "rig":
		row.Rig = value
	case "local-only", "local_only":
		row.LocalOnly = parseYAMLBool(value)
	case "unblocker", "unblocker-id", "unblocker_id":
		if value != "" {
			row.Unblockers = append(row.Unblockers, value)
		}
	case "evidence-ref", "evidence_ref":
		if value != "" {
			row.EvidenceRefs = append(row.EvidenceRefs, value)
		}
	case "note":
		row.Note = value
	}
	if fileLocalOnly && !row.LocalOnly {
		row.LocalOnly = true
	}
}

func applyPhase0B8UnblockerField(unblocker *phase0B8PromotionUnblocker, key, value string) {
	switch key {
	case "status":
		unblocker.Status = value
	case "owner":
		unblocker.Owner = value
	case "description", "summary", "note":
		unblocker.Description = value
	}
}

func normalizePhase0B8PromotionStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ready", "promotable", "promotion-ready":
		return phase0ProofStatusReady
	case "blocked":
		return phase0ProofStatusBlocked
	case "needs-unblocker", "needs_unblocker", "needs-review", "needs_review", "not-ready", "not_ready", "pending":
		return phase0ProofStatusNeedsReview
	default:
		return cleanYAMLScalar(value)
	}
}

func phase0B8EmptyPromotionReadiness(source string) phase0B8PromotionReadiness {
	return phase0B8PromotionReadiness{
		SchemaVersion:    phase0B8PromotionReadinessSchemaVersion,
		Status:           phase0ProofStatusNeedsReview,
		Source:           filepath.ToSlash(source),
		LocalOnly:        true,
		OfflineByDefault: true,
		NoLiveHostProbe:  true,
		ReadinessCounts:  phase0B8PromotionReadinessCounts{ByDomain: map[string]int{}},
		DomainCoverage:   phase0B6DomainCoverageFromCounts(nil),
	}
}

func phase0B8PromotionReadinessFromFile(source string, file phase0B8PromotionReadinessFile) phase0B8PromotionReadiness {
	readiness := phase0B8EmptyPromotionReadiness(source)
	readiness.Version = file.Version
	readiness.SourceManifest = filepath.ToSlash(file.SourceManifest)
	readiness.LocalOnly = file.LocalOnly
	readiness.Unblockers = append(readiness.Unblockers, file.Unblockers...)
	readiness.ReadinessRows = append(readiness.ReadinessRows, file.ReadinessRows...)
	if len(readiness.Unblockers) == 0 {
		for _, unblocker := range phase0B8PromotionReadinessUnblockerIDs(file.ReadinessRows) {
			readiness.Unblockers = append(readiness.Unblockers, phase0B8PromotionUnblocker{ID: unblocker})
		}
	}

	counts := map[string]int{}
	for _, row := range file.ReadinessRows {
		readiness.ReadinessCounts.Total++
		switch row.Status {
		case phase0ProofStatusReady:
			readiness.ReadinessCounts.Ready++
		case phase0ProofStatusBlocked:
			readiness.ReadinessCounts.Blocked++
		case phase0ProofStatusNeedsReview:
			readiness.ReadinessCounts.NeedsUnblocker++
		}
		if row.LocalOnly {
			readiness.ReadinessCounts.LocalOnly++
		}
		if row.Execution == "planner-only" {
			readiness.ReadinessCounts.PlannerOnly++
		}
		if len(row.Unblockers) > 0 {
			readiness.ReadinessCounts.WithUnblockers++
			if row.Status == phase0ProofStatusReady {
				readiness.ReadinessCounts.Ready--
				readiness.ReadinessCounts.NeedsUnblocker++
			}
		}
		if row.Domain != "" {
			counts[row.Domain]++
		}
	}
	readiness.ReadinessCounts.ByDomain = counts
	readiness.DomainCoverage = phase0B6DomainCoverageFromCounts(counts)
	for _, coverage := range readiness.DomainCoverage {
		if !coverage.Covered {
			readiness.MissingDomains = append(readiness.MissingDomains, coverage.Domain)
		}
	}
	readiness.Status = phase0ProofStatusReady
	if !readiness.LocalOnly || readiness.ReadinessCounts.Total == 0 || readiness.ReadinessCounts.NeedsUnblocker > 0 || len(readiness.MissingDomains) > 0 {
		readiness.Status = phase0ProofStatusNeedsReview
	}
	if readiness.ReadinessCounts.Blocked > 0 {
		readiness.Status = phase0ProofStatusBlocked
	}
	return readiness
}

func phase0B8PromotionReadinessEvidenceRefs(rows []phase0B8PromotionReadinessRow) []string {
	var refs []string
	for _, row := range rows {
		for _, ref := range row.EvidenceRefs {
			if strings.TrimSpace(ref) != "" {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func phase0B8PromotionReadinessUnblockerIDs(rows []phase0B8PromotionReadinessRow) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, row := range rows {
		for _, unblocker := range row.Unblockers {
			unblocker = strings.TrimSpace(unblocker)
			if unblocker == "" {
				continue
			}
			if _, ok := seen[unblocker]; ok {
				continue
			}
			seen[unblocker] = struct{}{}
			out = append(out, unblocker)
		}
	}
	sort.Strings(out)
	return out
}

func readPhase0B9ReleaseArchive(root, displayPath string) (phase0B9ReleaseArchiveFile, error) {
	if isAbsoluteArtifactPath(displayPath) {
		return phase0B9ReleaseArchiveFile{}, os.ErrNotExist
	}
	candidates := uniqueStrings([]string{
		filepath.Join(root, filepath.FromSlash(displayPath)),
		displayPath,
		defaultPhase0B9ReleaseArchivePath,
	})
	var lastErr error
	for _, candidate := range candidates {
		data, err := os.ReadFile(filepath.Clean(candidate))
		if err != nil {
			lastErr = err
			continue
		}
		archive, err := parsePhase0B9ReleaseArchive(data)
		if err != nil {
			return phase0B9ReleaseArchiveFile{}, err
		}
		return archive, nil
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return phase0B9ReleaseArchiveFile{}, lastErr
}

func parsePhase0B9ReleaseArchive(data []byte) (phase0B9ReleaseArchiveFile, error) {
	if len(data) == 0 {
		return phase0B9ReleaseArchiveFile{}, errPhase0B5Manifest("empty release evidence archive")
	}
	file := phase0B9ReleaseArchiveFile{}
	section := ""
	evidenceGroup := ""
	rowList := ""
	currentRow := -1
	for _, raw := range strings.Split(string(data), "\n") {
		noCR := strings.TrimRight(raw, "\r")
		line := strings.TrimSpace(noCR)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		indent := len(noCR) - len(strings.TrimLeft(noCR, " "))
		if indent == 0 && !strings.HasPrefix(line, "- ") {
			section = ""
			evidenceGroup = ""
			currentRow = -1
			rowList = ""
		}
		switch {
		case indent == 0 && strings.HasPrefix(line, "version:"):
			file.Version = atoiDefault(strings.TrimSpace(strings.TrimPrefix(line, "version:")))
			continue
		case indent == 0 && strings.HasPrefix(line, "owner:"):
			file.Owner = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "owner:")))
			continue
		case indent == 0 && strings.HasPrefix(line, "sprint:"):
			file.Sprint = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "sprint:")))
			continue
		case indent == 0 && (strings.HasPrefix(line, "local-only:") || strings.HasPrefix(line, "local_only:")):
			_, value, _ := strings.Cut(line, ":")
			file.LocalOnly = parseYAMLBool(value)
			continue
		case indent == 0 && (strings.HasPrefix(line, "source-manifest:") || strings.HasPrefix(line, "source_manifest:")):
			_, value, _ := strings.Cut(line, ":")
			file.SourceManifest = cleanYAMLScalar(value)
			continue
		case indent == 0 && (strings.HasPrefix(line, "archive-root:") || strings.HasPrefix(line, "archive_root:")):
			_, value, _ := strings.Cut(line, ":")
			file.ArchiveRoot = cleanYAMLScalar(value)
			continue
		case indent == 0 && (line == "gate-command:" || line == "gate_command:" || line == "command:"):
			section = "command"
			continue
		case indent == 0 && (line == "evidence-refs:" || line == "evidence_refs:"):
			section = "evidence-refs"
			continue
		case indent == 0 && (line == "verification-requirements:" || line == "verification_requirements:"):
			section = "verification-requirements"
			continue
		case indent == 0 && line == "readiness:":
			section = "readiness"
			continue
		case section == "readiness" && (line == "rows:" || line == "readiness-rows:" || line == "readiness_rows:"):
			continue
		case section == "readiness" && strings.HasPrefix(line, "- id:"):
			file.ReadinessRows = append(file.ReadinessRows, phase0B9ReleaseArchiveRow{ID: cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- id:")))})
			currentRow = len(file.ReadinessRows) - 1
			rowList = ""
			continue
		case section == "readiness" && currentRow >= 0 && (line == "verification:" || line == "verifications:"):
			rowList = "verification"
			continue
		case section == "readiness" && currentRow >= 0 && rowList == "verification" && strings.HasPrefix(line, "- "):
			file.ReadinessRows[currentRow].Verification = append(file.ReadinessRows[currentRow].Verification, cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- "))))
			continue
		case section == "verification-requirements" && strings.HasPrefix(line, "- "):
			file.VerificationRequirements = append(file.VerificationRequirements, cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, "- "))))
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = cleanYAMLScalar(value)
		switch section {
		case "command":
			applyPhase0B9ReleaseArchiveCommandField(&file.Command, key, value)
		case "evidence-refs":
			if indent == 2 && value == "" {
				evidenceGroup = key
				continue
			}
			if evidenceGroup != "" && value != "" {
				file.EvidenceRefs = append(file.EvidenceRefs, phase0B9ReleaseArchiveRef{
					Group: evidenceGroup,
					Name:  key,
					Path:  value,
				})
			}
		case "readiness":
			if currentRow >= 0 {
				if key != "verification" && key != "verifications" {
					rowList = ""
				}
				applyPhase0B9ReleaseArchiveRowField(&file.ReadinessRows[currentRow], key, value)
			}
		}
	}
	if file.Version == 0 || file.Owner == "" || file.SourceManifest == "" || file.ArchiveRoot == "" || len(file.EvidenceRefs) == 0 || len(file.ReadinessRows) == 0 {
		return phase0B9ReleaseArchiveFile{}, errPhase0B5Manifest("missing version, owner, source-manifest, archive-root, evidence refs, or readiness rows")
	}
	for _, row := range file.ReadinessRows {
		if row.ID == "" || row.Status == "" || row.RequiredRef == "" {
			return phase0B9ReleaseArchiveFile{}, errPhase0B5Manifest("release archive readiness row missing required field")
		}
	}
	return file, nil
}

func applyPhase0B9ReleaseArchiveCommandField(command *phase0B9ReleaseArchiveCommand, key, value string) {
	switch key {
	case "name":
		command.Name = value
	case "mode":
		command.Mode = value
	case "command-ref", "command_ref":
		command.CommandRef = value
	case "required-flag", "required_flag":
		command.RequiredFlag = value
	}
}

func applyPhase0B9ReleaseArchiveRowField(row *phase0B9ReleaseArchiveRow, key, value string) {
	switch key {
	case "status":
		row.Status = normalizePhase0B9ArchiveStatus(value)
	case "required-ref", "required_ref":
		row.RequiredRef = value
	case "verification", "verification-ref", "verification_ref":
		if value != "" {
			row.Verification = append(row.Verification, value)
		}
	}
}

func normalizePhase0B9ArchiveStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ready", "archived", "complete":
		return phase0ProofStatusReady
	case "blocked":
		return phase0ProofStatusBlocked
	case "pending-local-archive", "pending_local_archive", "pending", "needs-review", "needs_review", "not-ready", "not_ready":
		return phase0ProofStatusNeedsReview
	default:
		return cleanYAMLScalar(value)
	}
}

func phase0B9EmptyReleaseArchive(source string) phase0B9ReleaseArchive {
	return phase0B9ReleaseArchive{
		SchemaVersion:    phase0B9ReleaseArchiveSchemaVersion,
		Status:           phase0ProofStatusNeedsReview,
		Source:           filepath.ToSlash(source),
		LocalOnly:        true,
		OfflineByDefault: true,
		NoLiveHostProbe:  true,
		ReadinessCounts:  phase0B9ReleaseArchiveCounts{ByStatus: map[string]int{}},
	}
}

func phase0B9ReleaseArchiveFromFile(source string, file phase0B9ReleaseArchiveFile) phase0B9ReleaseArchive {
	archive := phase0B9EmptyReleaseArchive(source)
	archive.Version = file.Version
	archive.SourceManifest = filepath.ToSlash(file.SourceManifest)
	archive.ArchiveRoot = filepath.ToSlash(file.ArchiveRoot)
	archive.LocalOnly = file.LocalOnly
	archive.Command = file.Command
	archive.EvidenceRefs = append(archive.EvidenceRefs, file.EvidenceRefs...)
	archive.VerificationRequirements = append(archive.VerificationRequirements, file.VerificationRequirements...)
	archive.ReadinessRows = append(archive.ReadinessRows, file.ReadinessRows...)

	counts := map[string]int{}
	for _, row := range archive.ReadinessRows {
		archive.ReadinessCounts.Total++
		counts[row.Status]++
		switch row.Status {
		case phase0ProofStatusReady:
			archive.ReadinessCounts.Ready++
		case phase0ProofStatusBlocked:
			archive.ReadinessCounts.Blocked++
		case phase0ProofStatusNeedsReview:
			archive.ReadinessCounts.NeedsReview++
		}
		if strings.Contains(strings.ToLower(row.Status), "pending") || row.Status == phase0ProofStatusNeedsReview {
			archive.ReadinessCounts.PendingLocalArchive++
		}
		if archive.LocalOnly {
			archive.ReadinessCounts.LocalOnly++
		}
	}
	archive.ReadinessCounts.ByStatus = counts
	archive.Status = phase0ProofStatusReady
	if !archive.LocalOnly || archive.ReadinessCounts.Total == 0 || archive.ReadinessCounts.NeedsReview > 0 {
		archive.Status = phase0ProofStatusNeedsReview
	}
	if archive.ReadinessCounts.Blocked > 0 {
		archive.Status = phase0ProofStatusBlocked
	}
	return archive
}

func phase0B9ReleaseArchiveEvidenceRefs(file phase0B9ReleaseArchiveFile) []string {
	refs := []string{file.ArchiveRoot, file.Command.CommandRef}
	for _, ref := range file.EvidenceRefs {
		if strings.TrimSpace(ref.Path) != "" {
			refs = append(refs, ref.Path)
		}
	}
	for _, row := range file.ReadinessRows {
		if strings.TrimSpace(row.RequiredRef) != "" {
			refs = append(refs, row.RequiredRef)
		}
	}
	return refs
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func readPhase0JSON(path string, out any, artifact *phase0ProofArtifactStatus) bool {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if artifact != nil {
			artifact.Present = false
			artifact.Status = phase0ProofStatusNeedsReview
			if !os.IsNotExist(err) {
				artifact.Status = phase0ProofStatusBlocked
				artifact.Error = err.Error()
			}
		}
		return false
	}
	if artifact != nil {
		artifact.Present = true
	}
	if err := json.Unmarshal(data, out); err != nil {
		if artifact != nil {
			artifact.Status = phase0ProofStatusBlocked
			artifact.Error = err.Error()
		}
		return false
	}
	return true
}

func addPhase0ProofIssue(summary *phase0ProofPacketSummary, missingCode, invalidCode string, artifact phase0ProofArtifactStatus) {
	if artifact.Present {
		summary.IssueCodes = append(summary.IssueCodes, invalidCode)
		return
	}
	summary.IssueCodes = append(summary.IssueCodes, missingCode)
}

func snapshotPhase0ReleaseSummary(path string, in phase0ReleaseSummaryFile) phase0ReleaseSummarySnapshot {
	out := phase0ReleaseSummarySnapshot{
		Path:                            path,
		Present:                         true,
		Status:                          phase0ProofStatusReady,
		ReleaseReady:                    in.ReleaseReady,
		Verdict:                         in.Verdict,
		ManifestHash:                    in.ManifestHash,
		EvidenceIndexVerificationStatus: in.EvidenceIndexVerificationStatus,
		GateCounts: phase0ReleaseSummaryGateCounts{
			DrillRows:             in.GateCounts.DrillRows,
			DrillStatuses:         in.GateCounts.DrillStatuses,
			AlertRules:            in.GateCounts.AlertRules,
			AlertValidationPassed: in.GateCounts.AlertValidationPassed,
		},
		ExecutedDrillCounts: phase0ReleaseSummaryDrillCounts{
			Assertions: in.ExecutedDrillCounts.Assertions,
			Passed:     in.ExecutedDrillCounts.Passed,
			Failed:     in.ExecutedDrillCounts.Failed,
			Skipped:    in.ExecutedDrillCounts.Skipped,
		},
		EvidencePacketItemStatuses: in.EvidencePacketItemStatuses,
	}
	if !in.ReleaseReady || (in.Verdict != "" && in.Verdict != phase0ProofStatusReady) {
		out.Status = phase0ProofStatusNeedsReview
	}
	if in.GateCounts.DrillStatuses["placeholder"] > 0 && !phase0ReleaseSummaryHasExecutedDrillEvidence(in) {
		out.Status = phase0ProofStatusNeedsReview
	}
	for _, lane := range in.Lanes {
		out.Lanes = append(out.Lanes, phase0ReleaseSummaryLane{
			Lane:          lane.Lane,
			Status:        lane.Status,
			RequiredItems: lane.RequiredItems,
			MissingItems:  lane.MissingItems,
		})
	}
	for _, reason := range in.BlockingReasons {
		out.BlockingReasons = append(out.BlockingReasons, phase0ReleaseSummaryBlockingReason{
			ReasonCode: reason.ReasonCode,
			Message:    reason.Message,
		})
	}
	return out
}

func phase0ReleaseSummaryHasExecutedDrillEvidence(in phase0ReleaseSummaryFile) bool {
	counts := in.ExecutedDrillCounts
	if counts.Assertions <= 0 || counts.Failed != 0 || counts.Skipped != 0 || counts.Passed != counts.Assertions {
		return false
	}
	for _, item := range in.EvidencePacketItemStatuses {
		if item.Name == "drill-execution" && item.Status == "pass" {
			return true
		}
	}
	for _, lane := range in.Lanes {
		if phase0ContainsString(lane.RequiredItems, "drill-execution") && len(lane.MissingItems) == 0 && lane.Status == phase0ProofStatusReady {
			return true
		}
	}
	return false
}

func phase0ContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func phase0ProofCommand(evidence phase0ReleaseEvidenceFile) string {
	args := []string{"go", "run", "./tools/phase0-gate"}
	if evidence.ModeFlags.Format != "" {
		args = append(args, "--format="+evidence.ModeFlags.Format)
	}
	if evidence.ModeFlags.Out != "" {
		args = append(args, "--out="+evidence.ModeFlags.Out)
	}
	if evidence.ModeFlags.LastGreen != "" && evidence.ModeFlags.LastGreen != "unknown" {
		args = append(args, "--last-green", evidence.ModeFlags.LastGreen)
	}
	if evidence.ModeFlags.DrillAdapter != "" && evidence.ModeFlags.DrillAdapter != "local" {
		args = append(args, "--drill-adapter", evidence.ModeFlags.DrillAdapter)
	}
	if evidence.ModeFlags.ExecuteDrills {
		args = append(args, "--execute-drills")
	}
	if evidence.ModeFlags.RequireExecutedDrills {
		args = append(args, "--require-executed-drills")
	}
	if evidence.ModeFlags.ReleaseEvidence != "" {
		args = append(args, "--release-evidence", evidence.ModeFlags.ReleaseEvidence)
	}
	if evidence.ModeFlags.ReleaseEvidenceIndex != "" {
		args = append(args, "--release-evidence-index", evidence.ModeFlags.ReleaseEvidenceIndex)
	}
	if evidence.ModeFlags.VerifyReleaseEvidenceIndex != "" {
		args = append(args, "--verify-release-evidence-index", evidence.ModeFlags.VerifyReleaseEvidenceIndex)
	}
	if evidence.ModeFlags.VerifyEvidenceFiles {
		args = append(args, "--verify-evidence-files")
	}
	if evidence.ModeFlags.ReleaseSummary != "" {
		args = append(args, "--release-summary", evidence.ModeFlags.ReleaseSummary)
	}
	for _, ref := range evidence.ModeFlags.EvidenceRefs {
		if ref.Name != "" && ref.Path != "" {
			args = append(args, "--evidence-ref", ref.Name+"="+ref.Path)
		}
	}
	return strings.Join(args, " ")
}

func rollupPhase0ProofSummaryStatus(summary phase0ProofPacketSummary) string {
	for _, artifact := range []phase0ProofArtifactStatus{summary.ReleaseEvidence, summary.EvidenceIndex} {
		if artifact.Status == phase0ProofStatusBlocked {
			return phase0ProofStatusBlocked
		}
	}
	if summary.ReleaseSummary.Status == phase0ProofStatusBlocked {
		return phase0ProofStatusBlocked
	}
	if len(summary.IssueCodes) > 0 {
		return phase0ProofStatusNeedsReview
	}
	return phase0ProofStatusReady
}

func readPhase0OptionalValue(root string, names []string, source string) phase0OptionalValue {
	for _, name := range names {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(data))
		if value != "" {
			return phase0OptionalValue{Available: true, Value: value, Source: source + ":" + filepath.ToSlash(path)}
		}
	}
	return phase0OptionalValue{}
}

func readPhase0CommitSHA(root string) phase0OptionalValue {
	paths := phase0ArtifactPaths(root, "commit.txt")
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := firstNonEmptyLine(string(data))
		if isLikelyCommitSHA(value) {
			return phase0OptionalValue{Available: true, Value: value, Source: filepath.ToSlash(path)}
		}
	}
	return phase0OptionalValue{}
}

func readPhase0OperatorNotes(root string) phase0OperatorNotesSummary {
	paths := phase0ArtifactPaths(root, "operator-notes.md")
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fields := parsePhase0OperatorNotes(string(data))
		return phase0OperatorNotesSummary{
			Available: true,
			Path:      filepath.ToSlash(path),
			RunID:     fields["run id"],
			CommitSHA: fields["commit"],
			Result:    fields["result"],
		}
	}
	return phase0OperatorNotesSummary{}
}

func phase0ArtifactPaths(root, name string) []string {
	var paths []string
	direct := filepath.Join(root, name)
	if fileExists(direct) {
		paths = append(paths, direct)
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != name || filepath.Clean(path) == filepath.Clean(direct) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return paths
}

func parsePhase0OperatorNotes(data string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "run id", "commit", "result":
			fields[key] = value
		}
	}
	return fields
}

func firstNonEmptyLine(data string) string {
	for _, line := range strings.Split(data, "\n") {
		if value := strings.TrimSpace(line); value != "" {
			return value
		}
	}
	return ""
}

func isLikelyCommitSHA(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func fileExists(path string) bool {
	info, err := os.Stat(filepath.Clean(path))
	return err == nil && !info.IsDir()
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dashboardTemplateMarketplaceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	cards := templates.MarketplaceCards(dashboardSampleTime)
	cards = templates.SearchMarketplace(cards, templates.MarketplaceFilter{Query: r.URL.Query().Get("q"), Tag: r.URL.Query().Get("tag")})
	WriteJSON(w, http.StatusOK, map[string]any{"cards": cards})
}

func dashboardNotificationPreviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	preview, err := notifications.BuildDeliveryPreview(sampleNotificationChannel(), sampleNotificationSubscription(), notifications.EventDeploymentFailed, dashboardSampleTime)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "notification preview build failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, preview)
}

func sampleNotificationChannel() notifications.ChannelConfig {
	return notifications.ChannelConfig{
		ID:          "chn_dashboard_slack",
		WorkspaceID: "wrk_demo",
		Type:        notifications.ChannelSlackWebhook,
		DisplayName: "Dashboard Slack Preview",
		Slack:       &notifications.SlackConfig{WebhookSecretRef: "secret://notifications/slack-webhook"},
	}
}

func sampleNotificationSubscription() notifications.Subscription {
	return notifications.Subscription{
		ID:          "sub_dashboard_deployments",
		WorkspaceID: "wrk_demo",
		ChannelID:   "chn_dashboard_slack",
		EventTypes:  []notifications.EventType{notifications.EventDeploymentSucceeded, notifications.EventDeploymentFailed},
	}
}
