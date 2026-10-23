package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/failurecodes"
	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestRouterServesCompat(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/compat", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var got version.Compatibility
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != version.APIVersion {
		t.Fatalf("api version = %q, want %q", got.APIVersion, version.APIVersion)
	}
}

func TestRouterServesOpenAPI(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/openapi.yaml", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Fatalf("content type = %q, want application/yaml", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "openapi: 3.1.2") {
		t.Fatalf("OpenAPI body missing version: %q", body)
	}
	for _, want := range []string{
		"/auth/claim:",
		"/auth/sessions:",
		"/auth/sessions/{session_id}:",
		"/auth/me:",
		"/workspaces:",
		"/workspaces/{workspace_id}/members:",
		"/workspaces/{workspace_id}/members/{user_id}:",
		"/projects/{project_id}/members:",
		"/projects/{project_id}/members/{user_id}:",
		"HumanSession:",
		"IdentityLoginResult:",
		"IdentityWorkspaceMember:",
		"IdentityProjectMember:",
		"/projects:",
		"/projects/{id}:",
		"/services:",
		"/services/{id}:",
		"/services/{id}/deployments:",
		"/services/{service_id}/environments/{environment_id}/domains:",
		"/services/{service_id}/environments/{environment_id}/domains/{domain_id}:",
		"TenantGeneratedDomain:",
		"TenantGeneratedDomainCreateRequest:",
		"TenantGeneratedDomainDeleteRequest:",
		"/backup/runs:",
		"/agents/register:",
		"/webhooks/inbound/{provider}/{endpoint_id}:",
		"createProject",
		"createService",
		"createServiceDeployment",
		"/audit/events:",
		"/audit/evidence:",
		"/audit/export:",
		"AuditEvidence",
		"/dashboard/summary:",
		"/dashboard/compatibility-matrix:",
		"/dashboard/template-marketplace:",
		"DashboardSummary",
		"/enterprise/identity/plan:",
		"EnterpriseIdentityPlanRequest",
		"/enterprise/audit/export-plan:",
		"EnterpriseAuditExportPlanRequest",
		"/enterprise/controls/plan:",
		"EnterpriseControlsPlanRequest",
		"/billing/metering-limits-abuse/plan:",
		"BillingMeteringLimitsAbusePlanRequest",
		"/isolation/build/plan:",
		"BuildIsolationEvidencePlan",
		"/networking/region-routing/plan:",
		"RegionRoutePlan",
		"/control-plane/ha/profile-c/readiness:",
		"ProfileCReadiness",
		"/release/disconnected-bundle/plan:",
		"DisconnectedBundlePlan",
		"/config/diff:",
		"/config/apply:",
		"/config/sync:",
		"/environments/{id}/duplicate:",
		"ConfigDocument",
		"RESOURCE_VALIDATION_FAILED",
		"IDEMPOTENCY_KEY_REQUIRED",
		"RESOURCE_NOT_FOUND",
		"workspace_id:",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("OpenAPI body missing %q", want)
		}
	}
	if strings.Contains(body, "/auth/bootstrap:") {
		t.Fatalf("OpenAPI body still advertises unwired auth bootstrap route")
	}
}

func TestRouterServesAsyncAPI(t *testing.T) {
	router := NewRouter()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1alpha/asyncapi.yaml", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/yaml" {
		t.Fatalf("content type = %q, want application/yaml", contentType)
	}
	if !strings.Contains(response.Body.String(), "asyncapi: 3.0.0") {
		t.Fatalf("AsyncAPI body missing version: %q", response.Body.String())
	}

	methodResponse := httptest.NewRecorder()
	router.ServeHTTP(methodResponse, httptest.NewRequest(http.MethodPost, "/api/v1alpha/asyncapi.yaml", nil))
	if methodResponse.Code != http.StatusMethodNotAllowed || methodResponse.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response status=%d allow=%q", methodResponse.Code, methodResponse.Header().Get("Allow"))
	}
}

func TestRouterBackupRestoreStatusReportsUnavailableBackend(t *testing.T) {
	t.Setenv(backup.EnvBackupDestinationURI, "")
	router := NewRouterWithDataStack(DataStack{})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/backup/restore/status", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusOK)
	}
	var got backup.RestoreAPIStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != backup.RestoreAPIStatusUnavailable || got.Available {
		t.Fatalf("restore status = %#v, want unavailable", got)
	}
	if got.ReasonCode != backup.RestoreAPIReasonUnavailable {
		t.Fatalf("reason_code = %q, want %q", got.ReasonCode, backup.RestoreAPIReasonUnavailable)
	}
	if got.RestoreExecution.ReasonCode != backup.RestoreAPIReasonUnavailable {
		t.Fatalf("restore execution = %#v, want restore API unavailable", got.RestoreExecution)
	}
	if got.Offhost.ReasonCode != backup.RestoreAPIReasonOffhostMissing {
		t.Fatalf("offhost = %#v, want offhost missing", got.Offhost)
	}
}

func TestRouterBackupRestoreStatusUsesConfiguredOffhostDestination(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{BackupDestinationURI: "s3://midnight-backups/platform"})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/backup/restore/status", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusOK)
	}
	var got backup.RestoreAPIStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Offhost.Configured || got.Offhost.Scheme != "s3" {
		t.Fatalf("offhost = %#v, want configured s3 destination", got.Offhost)
	}
	if got.ReasonCode != backup.RestoreAPIReasonUnavailable {
		t.Fatalf("reason_code = %q, want only restore unavailable", got.ReasonCode)
	}
}

func TestRouterBackupRestoreStatusRejectsNonGET(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/backup/restore/status", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func TestRouterBackupRunsCreateAndListLocalDestination(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	now := time.Date(2026, 5, 3, 22, 10, 0, 0, time.UTC)
	router := NewRouterWithDataStack(DataStack{
		BackupDestinationURI: localBackupURI(t.TempDir()),
		Clock:                func() time.Time { return now },
	})

	create := httptest.NewRecorder()
	router.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1alpha/backup/runs", strings.NewReader(`{
		"id": "backup-api-test",
		"operator_passphrase": "operator-confirmed",
		"database_dump_bytes": 10,
		"registry_metadata_bytes": 20,
		"config_snapshot_bytes": 30,
		"support_reference_bytes": 40,
		"support_reference_count": 2
	}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s, want %d", create.Code, create.Body.String(), http.StatusCreated)
	}
	var created backupRunResource
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != "backup-api-test" || created.Manifest.TotalBytes != 100 || created.Object.SHA256 == "" {
		t.Fatalf("created run = %#v, want persisted manifest metadata", created)
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1alpha/backup/runs", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s, want %d", list.Code, list.Body.String(), http.StatusOK)
	}
	var got backupRunListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0].ID != "backup-api-test" {
		t.Fatalf("backup runs = %#v, want created run", got.Data)
	}
}

func TestRouterBackupRunCreationFailsClosedOutsideExperimentalMode(t *testing.T) {
	t.Setenv(experimentalEnv, "")
	router := NewRouterWithDataStack(DataStack{BackupDestinationURI: localBackupURI(t.TempDir())})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/backup/runs", strings.NewReader(`{
		"id": "must-not-claim-backup",
		"database_dump_bytes": 10
	}`)))

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusNotImplemented)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.NotImplemented ||
		got.Error.Details["capability"] != "backup-execution" ||
		got.Error.Details["state"] != "unavailable" {
		t.Fatalf("error = %#v, want honest unavailable backup capability", got.Error)
	}
}

func TestRouterRestorePlansCreateAndListLocalDestination(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{BackupDestinationURI: localBackupURI(t.TempDir())})

	create := httptest.NewRecorder()
	router.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/v1alpha/backup/restore/plans", strings.NewReader(`{
		"backup_id": "backup-api-test",
		"operator": "ops@example.test",
		"rollback_owner": "release@example.test",
		"notes": "planned restore"
	}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s, want %d", create.Code, create.Body.String(), http.StatusCreated)
	}
	var created restorePlanResource
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.BackupID != "backup-api-test" || len(created.Plan.Steps) != 6 || created.Object.SHA256 == "" {
		t.Fatalf("created restore plan = %#v, want persisted plan", created)
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1alpha/backup/restore/plans", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s, want %d", list.Code, list.Body.String(), http.StatusOK)
	}
	var got restorePlanListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0].BackupID != "backup-api-test" {
		t.Fatalf("restore plans = %#v, want created plan", got.Data)
	}
}

func TestRouterBackupRunsRequiresConfiguredDestination(t *testing.T) {
	t.Setenv(backup.EnvBackupDestinationURI, "")
	router := NewRouterWithDataStack(DataStack{})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/backup/runs", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.BackupOffhostMissing {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.BackupOffhostMissing)
	}
}

func TestRouterBackupRunWriteFailureReturnsActionableReason(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{BackupDestinationURI: localBackupURI(rootFile)})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/backup/runs", strings.NewReader(`{
		"id": "backup-write-fails",
		"database_dump_bytes": 1
	}`)))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusInternalServerError)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.BackupDestinationWriteFailed {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.BackupDestinationWriteFailed)
	}
	if got.Error.Details["reason_code"] != string(failurecodes.BackupDestinationWriteFailed) || got.Error.Details["recovery_hint"] == "" {
		t.Fatalf("details = %#v, want actionable backup reason", got.Error.Details)
	}
}

func localBackupURI(root string) string {
	return "local://" + filepath.ToSlash(root)
}

func TestRouterAuthBootstrapContractRouteNotWiredYet(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/auth/bootstrap", strings.NewReader(`{"token_class":"admin"}`)))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestRouterOptionalBearerTokenProtectsDashboardAndProductRoutes(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{APIBearerToken: "test-token"})

	missing := httptest.NewRecorder()
	missingReq := httptest.NewRequest(http.MethodGet, "/api/v1alpha/dashboard/summary", nil)
	missingReq.Header.Set("X-Request-Id", "req-auth-missing")
	router.ServeHTTP(missing, missingReq)
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d body=%s, want %d", missing.Code, missing.Body.String(), http.StatusUnauthorized)
	}
	if challenge := missing.Header().Get("WWW-Authenticate"); challenge != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", challenge)
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(missing.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.AuthTokenHeaderInvalid || envelope.Error.RequestID != "req-auth-missing" {
		t.Fatalf("auth envelope = %#v", envelope.Error)
	}

	wrong := httptest.NewRecorder()
	wrongReq := httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects", nil)
	wrongReq.Header.Set("Authorization", "Bearer wrong-token")
	router.ServeHTTP(wrong, wrongReq)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d body=%s, want %d", wrong.Code, wrong.Body.String(), http.StatusUnauthorized)
	}

	authorized := httptest.NewRecorder()
	authorizedReq := httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects", nil)
	authorizedReq.Header.Set("Authorization", "Bearer test-token")
	router.ServeHTTP(authorized, authorizedReq)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s, want %d", authorized.Code, authorized.Body.String(), http.StatusOK)
	}
}

func TestRouterAgentCredentialIsIndependentFromRecoveryBearer(t *testing.T) {
	credentials, err := NewStaticAgentCredentials(map[string][]string{
		"agent-1": {"agent-1-credential-material-000001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		AgentCredentials: credentials,
	})

	missing := httptest.NewRecorder()
	missingBody := strings.NewReader(`{"agent_id":"agent-1","version":"` + version.Version + `"}`)
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, AgentRegisterPath, missingBody))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d body=%s, want %d", missing.Code, missing.Body.String(), http.StatusUnauthorized)
	}

	recovery := httptest.NewRecorder()
	recoveryBody := strings.NewReader(`{"agent_id":"agent-1","version":"` + version.Version + `"}`)
	recoveryReq := httptest.NewRequest(http.MethodPost, AgentRegisterPath, recoveryBody)
	recoveryReq.Header.Set("Authorization", "Bearer operator-recovery-token")
	router.ServeHTTP(recovery, recoveryReq)
	if recovery.Code != http.StatusUnauthorized {
		t.Fatalf("recovery bearer status = %d body=%s, want %d", recovery.Code, recovery.Body.String(), http.StatusUnauthorized)
	}

	authorized := httptest.NewRecorder()
	authorizedBody := strings.NewReader(`{"agent_id":"agent-1","version":"` + version.Version + `"}`)
	req := httptest.NewRequest(http.MethodPost, AgentRegisterPath, authorizedBody)
	req.Header.Set("Authorization", "Bearer agent-1-credential-material-000001")
	router.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("agent credential status = %d body=%s, want %d", authorized.Code, authorized.Body.String(), http.StatusOK)
	}
}

func TestRouterOptionalBearerTokenLeavesPublicRoutesOpen(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{APIBearerToken: "test-token"})
	for _, path := range []string{
		"/api/v1alpha/compat",
		"/api/v1alpha/openapi.yaml",
		"/api/v1alpha/_demo/health-target",
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s, want %d", path, rr.Code, rr.Body.String(), http.StatusOK)
		}
	}
}

func TestRouterProductionAuthRequiredFailsClosedWithoutToken(t *testing.T) {
	// Production mode now fails closed on the variable KEK: without an
	// operator KEK the router refuses to boot rather than fall back to the
	// public dev constant. Provision key material so this test can still
	// exercise the auth path.
	kekPath := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(kekPath, []byte("0123456789abcdef0123456789abcdef"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_KEK_PATH", kekPath)

	router := NewRouterWithDataStack(DataStack{ProductionAuthRequired: true})

	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects", nil))
	if read.Code != http.StatusUnauthorized {
		t.Fatalf("read status = %d body=%s, want %d", read.Code, read.Body.String(), http.StatusUnauthorized)
	}

	mutation := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"slug":"prod"}`))
	req.Header.Set("Idempotency-Key", "prod-auth-required")
	router.ServeHTTP(mutation, req)
	if mutation.Code != http.StatusUnauthorized {
		t.Fatalf("mutation status = %d body=%s, want %d", mutation.Code, mutation.Body.String(), http.StatusUnauthorized)
	}
	var got ErrorResponse
	if err := json.Unmarshal(mutation.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.AuthRequiredMissing || got.Error.Details["reason_code"] != "AUTH_REQUIRED_MISSING" {
		t.Fatalf("auth envelope = %#v", got.Error)
	}
}

func TestRouterRegistersPlanningContractEndpoints(t *testing.T) {
	paths := []string{
		"/api/v1alpha/enterprise/identity/plan",
		"/api/v1alpha/enterprise/audit/export-plan",
		"/api/v1alpha/enterprise/controls/plan",
		"/api/v1alpha/billing/metering-limits-abuse/plan",
		"/api/v1alpha/isolation/build/plan",
		"/api/v1alpha/networking/region-routing/plan",
		"/api/v1alpha/control-plane/ha/profile-c/readiness",
		"/api/v1alpha/release/disconnected-bundle/plan",
	}

	t.Setenv(experimentalEnv, "")
	router := NewRouter()
	for _, path := range paths {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want hidden outside experimental mode", path, rr.Code)
		}
	}

	t.Setenv(experimentalEnv, "1")
	router = NewRouter()
	for _, path := range paths {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if rr.Code == http.StatusNotFound {
			t.Fatalf("%s status = %d, planning endpoint is not registered", path, rr.Code)
		}
	}
}

func TestRouterServesSchemaEvidenceDemo(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/_demo/schema-evidence", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var got struct {
		SchemaVersion             int      `json:"schema_version"`
		AppliedCount              int      `json:"applied_count"`
		PendingCount              int      `json:"pending_count"`
		Status                    string   `json:"status"`
		DetectedTables            []string `json:"detected_tables"`
		UnsupportedStatementCount int      `json:"unsupported_statement_count"`
		InvalidStatementCount     int      `json:"invalid_statement_count"`
		Migrations                []struct {
			Version int    `json:"version"`
			Name    string `json:"name"`
			Applied bool   `json:"applied"`
		} `json:"migrations"`
		SourceFiles []struct {
			Filename  string `json:"filename"`
			Kind      string `json:"kind"`
			ByteCount int    `json:"byte_count"`
			SHA256    string `json:"sha256"`
		} `json:"source_files"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.AppliedCount != 1 || got.PendingCount != 0 || got.Status != "ready" || got.UnsupportedStatementCount != 0 || got.InvalidStatementCount != 0 || len(got.Migrations) != 1 || got.Migrations[0].Name != "alpha_core" || !got.Migrations[0].Applied {
		t.Fatalf("schema evidence = %#v", got)
	}
	if len(got.DetectedTables) != 2 || got.DetectedTables[0] != "operations" || got.DetectedTables[1] != "schema_migrations" {
		t.Fatalf("detected tables = %#v", got.DetectedTables)
	}
	if len(got.SourceFiles) != 2 || got.SourceFiles[0].Filename != "0001_alpha_core.down.sql" || got.SourceFiles[0].Kind != "down" || got.SourceFiles[1].Filename != "0001_alpha_core.up.sql" || got.SourceFiles[1].Kind != "up" || got.SourceFiles[1].ByteCount == 0 || got.SourceFiles[1].SHA256 == "" {
		t.Fatalf("source files = %#v", got.SourceFiles)
	}
}

func TestRouterServesMigrationPlanDemo(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/_demo/migration-plan", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var got struct {
		SchemaVersion     int    `json:"schema_version"`
		LatestVersion     int    `json:"latest_version"`
		Status            string `json:"status"`
		AppliedVersions   []int  `json:"applied_versions"`
		PendingMigrations []struct {
			Version int    `json:"version"`
			Name    string `json:"name"`
		} `json:"pending_migrations"`
		BlockedGaps []struct {
			Version int `json:"version"`
		} `json:"blocked_gaps"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 2 || got.LatestVersion != 2 || got.Status != "needs-review" || len(got.AppliedVersions) != 1 || got.AppliedVersions[0] != 1 {
		t.Fatalf("migration plan summary = %#v", got)
	}
	if len(got.PendingMigrations) != 1 || got.PendingMigrations[0].Version != 2 || got.PendingMigrations[0].Name != "beta_core" {
		t.Fatalf("pending migrations = %#v", got.PendingMigrations)
	}
	if len(got.BlockedGaps) != 0 {
		t.Fatalf("blocked gaps = %#v, want none", got.BlockedGaps)
	}
}

func TestRouterServesReleaseEvidenceDemo(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/_demo/release-evidence", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var got struct {
		SchemaVersion int      `json:"schema_version"`
		LatestVersion int      `json:"latest_version"`
		Status        string   `json:"status"`
		IssueCodes    []string `json:"issue_codes"`
		Schema        struct {
			AppliedCount int `json:"applied_count"`
		} `json:"schema"`
		MigrationPlan struct {
			PendingMigrations []struct {
				Version int `json:"version"`
			} `json:"pending_migrations"`
		} `json:"migration_plan"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 2 || got.LatestVersion != 2 || got.Status != "needs-review" || got.Schema.AppliedCount != 1 {
		t.Fatalf("release evidence summary = %#v", got)
	}
	if len(got.MigrationPlan.PendingMigrations) != 1 || got.MigrationPlan.PendingMigrations[0].Version != 2 {
		t.Fatalf("pending migrations = %#v", got.MigrationPlan.PendingMigrations)
	}
	if len(got.IssueCodes) != 2 || got.IssueCodes[0] != "MIGRATION_CHECKSUM_PLACEHOLDER" || got.IssueCodes[1] != "MIGRATION_PENDING" {
		t.Fatalf("issue codes = %#v", got.IssueCodes)
	}
}

func TestRouterServesEvidenceIndexDemo(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/_demo/evidence-index", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var got struct {
		SchemaVersion int    `json:"schema_version"`
		LatestVersion int    `json:"latest_version"`
		Status        string `json:"status"`
		References    []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"references"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 2 || got.LatestVersion != 2 || got.Status != "needs-review" || len(got.References) != 4 {
		t.Fatalf("evidence index summary = %#v", got)
	}
	if got.References[0].Name != "schema-evidence" || got.References[0].Path != "artifacts/phase0/schema-evidence.json" || got.References[0].Status != "needs-review" {
		t.Fatalf("schema evidence ref = %#v", got.References[0])
	}
	if got.References[3].Name != "openapi-spec" || got.References[3].Path != "api/openapi/v1alpha/midnight.yaml" || got.References[3].Status != "ready" {
		t.Fatalf("openapi ref = %#v", got.References[3])
	}
}

func TestRouterServesAPIDataReadinessDemo(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/_demo/api-data-readiness", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var got struct {
		Status        string                  `json:"status"`
		IssueCodes    []string                `json:"issue_codes"`
		Release       struct{ Status string } `json:"release"`
		EvidenceIndex struct{ Status string } `json:"evidence_index"`
		OpenAPI       struct {
			Status    string `json:"status"`
			Available bool   `json:"available"`
			ByteCount int    `json:"byte_count"`
		} `json:"openapi"`
		OperationStore struct {
			Status         string `json:"status"`
			ListAvailable  bool   `json:"list_available"`
			OperationCount int    `json:"operation_count"`
		} `json:"operation_store"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "needs-review" || got.Release.Status != "needs-review" || got.EvidenceIndex.Status != "needs-review" {
		t.Fatalf("readiness summary = %#v", got)
	}
	if got.OpenAPI.Status != "ready" || !got.OpenAPI.Available || got.OpenAPI.ByteCount == 0 {
		t.Fatalf("openapi = %#v, want ready available", got.OpenAPI)
	}
	if got.OperationStore.Status != "ready" || !got.OperationStore.ListAvailable || got.OperationStore.OperationCount != 0 {
		t.Fatalf("operation store = %#v, want ready empty", got.OperationStore)
	}
	if len(got.IssueCodes) != 2 || got.IssueCodes[0] != APIDataReadinessIssueReleaseEvidence || got.IssueCodes[1] != APIDataReadinessIssueEvidenceIndex {
		t.Fatalf("issue codes = %#v", got.IssueCodes)
	}
}

func TestRouterOperationNotFoundEnvelope(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-1", nil)
	req.Header.Set("X-Request-Id", "req-test")
	rr := httptest.NewRecorder()

	NewRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.OperationNotFound || got.Error.RequestID != "req-test" {
		t.Fatalf("unexpected envelope: %#v", got.Error)
	}
}

func TestRouterOperationMethodNotAllowedEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.MethodNotAllowed {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.MethodNotAllowed)
	}
}

func TestRouterReturnsStoredOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	operation, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var got orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != operation {
		t.Fatalf("operation = %#v, want %#v", got, operation)
	}
}

func TestRouterListsOperationsWithPaginationAndRateLimitHeaders(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	for _, operation := range []orailtypes.Operation{
		{ID: "op-1", State: orailtypes.OperationPending},
		{ID: "op-2", State: orailtypes.OperationRunning},
		{ID: "op-3", State: orailtypes.OperationSucceeded},
	} {
		if _, err := store.Create(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouterWithCursorSecret(store, []byte("test-secret"))

	firstRR := httptest.NewRecorder()
	router.ServeHTTP(firstRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations?limit=2", nil))

	if firstRR.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", firstRR.Code, http.StatusOK)
	}
	if firstRR.Header().Get("RateLimit-Limit") == "" || firstRR.Header().Get("RateLimit-Remaining") == "" || firstRR.Header().Get("RateLimit-Reset") == "" {
		t.Fatalf("missing rate limit headers: %#v", firstRR.Header())
	}
	var firstPage orailtypes.CursorPageResponse[orailtypes.Operation]
	if err := json.Unmarshal(firstRR.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Data) != 2 || firstPage.Data[0].ID != "op-1" || firstPage.Data[1].ID != "op-2" {
		t.Fatalf("first page data = %#v, want op-1/op-2", firstPage.Data)
	}
	if firstPage.Page.NextCursor == nil || *firstPage.Page.NextCursor == "" {
		t.Fatalf("first page next cursor = %#v, want token", firstPage.Page.NextCursor)
	}
	crossQueryRR := httptest.NewRecorder()
	router.ServeHTTP(crossQueryRR, httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/operations?limit=2&kind=deploy&cursor="+*firstPage.Page.NextCursor,
		nil,
	))
	if crossQueryRR.Code != http.StatusBadRequest {
		t.Fatalf("cross-query cursor status = %d body=%s, want 400", crossQueryRR.Code, crossQueryRR.Body.String())
	}

	secondRR := httptest.NewRecorder()
	router.ServeHTTP(secondRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations?limit=2&cursor="+*firstPage.Page.NextCursor, nil))

	if secondRR.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", secondRR.Code, http.StatusOK)
	}
	var secondPage orailtypes.CursorPageResponse[orailtypes.Operation]
	if err := json.Unmarshal(secondRR.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Data) != 1 || secondPage.Data[0].ID != "op-3" {
		t.Fatalf("second page data = %#v, want op-3", secondPage.Data)
	}
	if secondPage.Page.NextCursor != nil {
		t.Fatalf("second page next cursor = %#v, want nil", secondPage.Page.NextCursor)
	}
}

func TestRouterListOperationsFiltersStatusAndKind(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	for _, operation := range []orailtypes.Operation{
		{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued},
		{ID: "op-2", Kind: "backup", State: orailtypes.OperationRunning},
		{ID: "op-3", Kind: "deploy", State: orailtypes.OperationRunning},
	} {
		if _, err := store.Create(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}

	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations?status=running&kind=deploy", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var page orailtypes.CursorPageResponse[orailtypes.Operation]
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != "op-3" {
		t.Fatalf("page data = %#v, want filtered op-3", page.Data)
	}
}

func TestRouterListOperationsRejectsInvalidStatusFilter(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(NewMemoryOperationStore()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations?status=bogus", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.CursorInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.CursorInvalid)
	}
}

func TestRouterListOperationsRejectsInvalidCursor(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouterWithCursorSecret(NewMemoryOperationStore(), []byte("test-secret")).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations?cursor=not-a-token", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.CursorInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.CursorInvalid)
	}
}

func TestRouterOperationEventsSSE(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-1", State: orailtypes.OperationPending})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, OperationEvent{OperationID: operation.ID, State: operation.State, Message: "queued"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-1/events", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", ct)
	}
	if body := rr.Body.String(); !strings.Contains(body, "event: operation") || !strings.Contains(body, "op-1") || !strings.Contains(body, "queued") {
		t.Fatalf("unexpected SSE body: %q", body)
	}
}

func TestRouterOperationEventsIncludesCreationAndFailureInOrder(t *testing.T) {
	store := NewMemoryOperationStore()
	router := NewRouterWithOperationStore(store)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", nil)
	createReq.Header.Set("Idempotency-Key", "events-create-fail")
	createRR := httptest.NewRecorder()
	router.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d", createRR.Code, http.StatusAccepted)
	}
	var operation orailtypes.Operation
	if err := json.Unmarshal(createRR.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}

	failRR := httptest.NewRecorder()
	router.ServeHTTP(failRR, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/"+string(operation.ID)+"/fail", strings.NewReader(`{"code":"WORKER_ERROR","message":"worker failed"}`)))
	if failRR.Code != http.StatusAccepted {
		t.Fatalf("fail status = %d, want %d", failRR.Code, http.StatusAccepted)
	}

	eventsRR := httptest.NewRecorder()
	router.ServeHTTP(eventsRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/"+string(operation.ID)+"/events", nil))
	if eventsRR.Code != http.StatusOK {
		t.Fatalf("events status = %d, want %d", eventsRR.Code, http.StatusOK)
	}
	body := eventsRR.Body.String()
	queuedIndex := strings.Index(body, `"seq":1`)
	failedIndex := strings.Index(body, `"seq":2`)
	if queuedIndex < 0 || failedIndex < 0 || queuedIndex > failedIndex || !strings.Contains(body, `"state":"QUEUED"`) || !strings.Contains(body, `"state":"FAILED"`) {
		t.Fatalf("unexpected event order body: %q", body)
	}
	if !strings.Contains(body, "event: cursor") || !strings.Contains(body, `"next_sequence":2`) {
		t.Fatalf("missing cursor event body: %q", body)
	}
}

func TestRouterOperationEventsFiltersAfterSequence(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-1", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []OperationEvent{
		{OperationID: operation.ID, State: orailtypes.OperationQueued, Message: "queued"},
		{OperationID: operation.ID, State: orailtypes.OperationRunning, Message: "running"},
		{OperationID: operation.ID, State: orailtypes.OperationSucceeded, Message: "done"},
	} {
		if _, err := store.AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-1/events?after=1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if strings.Contains(body, `"seq":1`) || !strings.Contains(body, `"seq":2`) || !strings.Contains(body, `"seq":3`) {
		t.Fatalf("unexpected filtered events body: %q", body)
	}
	if !strings.Contains(body, "event: cursor") || !strings.Contains(body, `"next_sequence":3`) {
		t.Fatalf("missing next sequence body: %q", body)
	}
}

func TestRouterOperationEventsRejectsMalformedAfter(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationQueued}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-1/events?after=bogus", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.CursorInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.CursorInvalid)
	}
}

func TestRouterOperationEventsNotFoundEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-missing/events", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.OperationNotFound {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.OperationNotFound)
	}
}

func TestRouterOperationLogsDistinguishesUnavailableAppLogs(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, OperationEvent{OperationID: operation.ID, State: operation.State, Message: "deploy worker transitioned operation to RUNNING"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-1/logs", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusOK)
	}
	var got struct {
		OperationID     string `json:"operation_id"`
		OperationEvents struct {
			Available bool   `json:"available"`
			Kind      string `json:"kind"`
			URL       string `json:"url"`
			Count     int    `json:"count"`
		} `json:"operation_events"`
		AppLogs []struct {
			Surface   string `json:"surface"`
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
		} `json:"app_logs"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OperationID != "op-1" || !got.OperationEvents.Available || got.OperationEvents.Kind != "operation_events" || got.OperationEvents.Count != 1 {
		t.Fatalf("operation event ref = %#v, want backed operation events", got.OperationEvents)
	}
	if got.OperationEvents.URL != "/api/v1alpha/operations/op-1/events" {
		t.Fatalf("operation events url = %q", got.OperationEvents.URL)
	}
	if len(got.AppLogs) != 3 {
		t.Fatalf("app logs = %#v, want build/runtime/http surfaces", got.AppLogs)
	}
	for _, surface := range got.AppLogs {
		if surface.Available || !strings.Contains(surface.Reason, "no backed app log store") {
			t.Fatalf("surface = %#v, want explicit unavailable backed-store reason", surface)
		}
	}
	if !strings.Contains(got.Message, "Operation events are available") || !strings.Contains(got.Message, "not wired") {
		t.Fatalf("message = %q, want operation-events/app-logs distinction", got.Message)
	}
}

func TestRouterOperationLogsReturnsBackedAppAndPlatformLogs(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-logs", Kind: "deploy", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message:     "deploy.intake service_id=svc_1 deployment_id=dep_1 instance_id=inst_1",
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "live.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []logrecords.Entry{
		{
			At:        time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			Level:     "info",
			Component: "app",
			Message:   "runtime ready",
			Fields:    map[string]any{"deployment_id": "dep_1"},
		},
		{
			At:        time.Date(2026, 5, 1, 12, 0, 1, 0, time.UTC),
			Level:     "warn",
			Component: "midnight-worker",
			Message:   "deploy retry scheduled",
			Fields:    map[string]any{"operation_id": "op-logs"},
		},
		{
			At:        time.Date(2026, 5, 1, 12, 0, 2, 0, time.UTC),
			Level:     "info",
			Component: "app",
			Message:   "different deploy",
			Fields:    map[string]any{"deployment_id": "dep_other"},
		},
	}
	if err := logrecords.EncodeFixtureNDJSON(file, entries); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	logStore, err := logrecords.NewFileStore(dir, "*.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store, LogStore: logStore})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-logs/logs?limit=5", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusOK)
	}
	var got struct {
		AppLogs []struct {
			Surface   string             `json:"surface"`
			Available bool               `json:"available"`
			Count     int                `json:"count"`
			Entries   []logrecords.Entry `json:"entries"`
		} `json:"app_logs"`
		PlatformLogs []struct {
			Surface   string             `json:"surface"`
			Available bool               `json:"available"`
			Count     int                `json:"count"`
			Entries   []logrecords.Entry `json:"entries"`
		} `json:"platform_logs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var runtimeLogs struct {
		Surface   string             `json:"surface"`
		Available bool               `json:"available"`
		Count     int                `json:"count"`
		Entries   []logrecords.Entry `json:"entries"`
	}
	for _, surface := range got.AppLogs {
		if surface.Surface == "runtime" {
			runtimeLogs = surface
			break
		}
	}
	if !runtimeLogs.Available || runtimeLogs.Count != 1 || runtimeLogs.Entries[0].Message != "runtime ready" {
		t.Fatalf("runtime logs = %#v, want one backed app log entry", runtimeLogs)
	}
	if len(got.PlatformLogs) != 1 || !got.PlatformLogs[0].Available || got.PlatformLogs[0].Count != 1 || got.PlatformLogs[0].Entries[0].Message != "deploy retry scheduled" {
		t.Fatalf("platform logs = %#v, want one backed platform log entry", got.PlatformLogs)
	}
}

func TestRouterOperationLogsUsesOpaqueScopeBoundCursor(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	operation, err := store.Create(ctx, orailtypes.Operation{ID: "op-page", Kind: "deploy", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message:     "deploy.intake deployment_id=dep_page",
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "host-runtime.ndjson")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := logrecords.EncodeFixtureNDJSON(file, []logrecords.Entry{
		{At: time.Now().UTC(), Level: "info", Component: "app", Message: "first", Fields: map[string]any{"deployment_id": "dep_page"}},
		{At: time.Now().UTC(), Level: "info", Component: "app", Message: "second", Fields: map[string]any{"deployment_id": "dep_page"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	logStore, err := logrecords.NewFileStore(dir, "*.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "opaque-log-cursor-secret-000000000"
	router := NewRouterWithDataStack(DataStack{
		OperationStore: store,
		LogStore:       logStore,
		CursorCodec:    NewCursorCodec([]byte(secret)),
	})

	readRuntime := func(target string) (string, []logrecords.Entry) {
		t.Helper()
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		var response operationLogsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		for _, surface := range response.AppLogs {
			if surface.Surface == "runtime" {
				token := ""
				if surface.NextCursor != nil {
					token = surface.NextCursor.Token
				}
				return token, surface.Entries
			}
		}
		t.Fatal("runtime surface missing")
		return "", nil
	}

	token, first := readRuntime("/api/v1alpha/operations/op-page/logs?limit=1")
	if len(first) != 1 || first[0].Message != "first" || token == "" {
		t.Fatalf("first page entries=%#v token=%q", first, token)
	}
	if strings.Contains(token, "host-runtime") || strings.Contains(token, ".ndjson") {
		t.Fatalf("opaque cursor exposes host filename: %q", token)
	}
	nextToken, second := readRuntime("/api/v1alpha/operations/op-page/logs?limit=1&surface=runtime&cursor=" + token)
	if len(second) != 1 || second[0].Message != "second" || nextToken != "" {
		t.Fatalf("second page entries=%#v token=%q", second, nextToken)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-page/logs?limit=1&surface=build&cursor="+token, nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-surface cursor status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRouterOperationLogsRejectsInvalidQuery(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-logs", Kind: "deploy", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-logs/logs?limit=0", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.LogQueryInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.LogQueryInvalid)
	}
}

func TestRouterOperationLogsRequiresSurfaceWithCursor(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-logs", Kind: "deploy", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-logs/logs?cursor=lc1.opaque", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.LogQueryInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.LogQueryInvalid)
	}
}

func TestRouterOperationLogsRejectsExposedFileCursor(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-logs", Kind: "deploy", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-logs/logs?surface=runtime&cursor_file=/var/lib/midnight/logs/live.ndjson&cursor_offset=12", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.CursorInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.CursorInvalid)
	}
	if strings.Contains(rr.Body.String(), "/var/lib/midnight") {
		t.Fatalf("response reflected host path: %s", rr.Body.String())
	}
}

func TestRouterOperationLogsNotFoundEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op-missing/logs", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.OperationNotFound {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.OperationNotFound)
	}
}

func TestRouterCancelOperationTransitionsQueuedToCanceled(t *testing.T) {
	store := NewMemoryOperationStore()
	operation, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()

	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusAccepted)
	}
	var got orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != operation.ID || got.State != orailtypes.OperationCanceled {
		t.Fatalf("operation = %#v, want canceled op-1", got)
	}
	if location := rr.Header().Get("Location"); location != "/api/v1alpha/operations/op-1" {
		t.Fatalf("Location = %q, want operation URL", location)
	}
	events, err := store.ListEvents(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != orailtypes.OperationCanceled || events[0].Message != "canceled" {
		t.Fatalf("events = %#v, want canceled event", events)
	}
}

func TestRouterCancelQueuedDeployCancelsQueueJobBeforeOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	operation, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatal(err)
	}
	queueStore := &cancelAwareQueueStore{
		result: queue.CancelQueuedResult{
			Job:      queue.Job{ID: "job-1", Queue: queue.Deploy, State: queue.JobCanceled},
			Canceled: true,
		},
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store, QueueStore: queueStore})
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusAccepted)
	}
	if queueStore.request.ParentOperationID != string(operation.ID) || queueStore.request.Queue != queue.Deploy || queueStore.request.Reason != queue.ReasonDeployCanceledBeforeLease {
		t.Fatalf("cancel request = %#v, want deploy parent operation cancellation", queueStore.request)
	}
	var got orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != orailtypes.OperationCanceled {
		t.Fatalf("operation = %#v, want canceled after queued job cancellation", got)
	}
	events, err := store.ListEvents(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != orailtypes.OperationCanceled || !strings.Contains(events[0].Message, queue.ReasonDeployCanceledBeforeLease) {
		t.Fatalf("events = %#v, want cancel-before-lease event", events)
	}
}

func TestRouterCancelQueuedDeployAlreadyLeasedStaysNonTerminal(t *testing.T) {
	store := NewMemoryOperationStore()
	operation, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatal(err)
	}
	queueStore := &cancelAwareQueueStore{
		result: queue.CancelQueuedResult{
			Job:           queue.Job{ID: "job-1", Queue: queue.Deploy, State: queue.JobLeased, LeaseID: "worker-1/lease-1"},
			AlreadyLeased: true,
		},
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store, QueueStore: queueStore})
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusAccepted)
	}
	var got orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != orailtypes.OperationQueued {
		t.Fatalf("operation = %#v, want queued until worker safe point", got)
	}
	events, err := store.ListEvents(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != orailtypes.OperationQueued || !strings.Contains(events[0].Message, "safe point before traffic promotion") {
		t.Fatalf("events = %#v, want safe-point cancel request", events)
	}
}

func TestRouterCancelRunningDeployRecordsSafePointRequest(t *testing.T) {
	store := NewMemoryOperationStore()
	operation, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store})
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusAccepted)
	}
	var got orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != orailtypes.OperationRunning {
		t.Fatalf("operation = %#v, want running until worker reaches a safe point", got)
	}
	events, err := store.ListEvents(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != orailtypes.OperationRunning || !strings.Contains(events[0].Message, "safe point before traffic promotion") {
		t.Fatalf("events = %#v, want running safe-point cancel request", events)
	}
}

func TestRouterCancelQueuedDeployRequiresQueueCancellation(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{OperationStore: store})
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), http.StatusConflict)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.DeployCancelQueueUnavailable {
		t.Fatalf("error = %#v, want deploy cancel queue unavailable", got.Error)
	}
	operation, err := store.Get(context.Background(), "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != orailtypes.OperationQueued {
		t.Fatalf("operation = %#v, want queued after rejected cancel", operation)
	}
}

func TestRouterCancelOperationRejectsTerminalState(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationSucceeded}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()

	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.OperationInvalidTransition || got.Error.Details["from"] != string(orailtypes.OperationSucceeded) || got.Error.Details["to"] != string(orailtypes.OperationCanceled) {
		t.Fatalf("unexpected envelope: %#v", got.Error)
	}
}

func TestRouterFailOperationTransitionsRunningToFailed(t *testing.T) {
	store := NewMemoryOperationStore()
	operation, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()

	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/fail", strings.NewReader(`{"code":"WORKER_ERROR","message":"worker failed"}`)))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusAccepted)
	}
	var got orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != operation.ID || got.State != orailtypes.OperationFailed || got.Code != "WORKER_ERROR" || got.Reason != "worker failed" {
		t.Fatalf("operation = %#v, want failed op-1 with metadata", got)
	}
	if location := rr.Header().Get("Location"); location != "/api/v1alpha/operations/op-1" {
		t.Fatalf("Location = %q, want operation URL", location)
	}
	events, err := store.ListEvents(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != orailtypes.OperationFailed || events[0].Message != "worker failed" {
		t.Fatalf("events = %#v, want failed event", events)
	}
}

func TestRouterFailOperationRejectsTerminalState(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationCanceled}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()

	NewRouterWithOperationStore(store).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/fail", nil))

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.OperationInvalidTransition || got.Error.Details["from"] != string(orailtypes.OperationCanceled) || got.Error.Details["to"] != string(orailtypes.OperationFailed) {
		t.Fatalf("unexpected envelope: %#v", got.Error)
	}
}

func TestRouterCancelOperationReplaysIdempotencyKey(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithOperationStore(store)

	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil)
	firstReq.Header.Set("Idempotency-Key", "cancel-op-1")
	firstRR := httptest.NewRecorder()
	router.ServeHTTP(firstRR, firstReq)
	if firstRR.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d", firstRR.Code, http.StatusAccepted)
	}
	firstBody := firstRR.Body.String()

	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1/cancel", nil)
	secondReq.Header.Set("Idempotency-Key", "cancel-op-1")
	secondRR := httptest.NewRecorder()
	router.ServeHTTP(secondRR, secondReq)
	if secondRR.Code != http.StatusAccepted {
		t.Fatalf("second status = %d, want %d", secondRR.Code, http.StatusAccepted)
	}
	if replayed := secondRR.Header().Get("Idempotent-Replayed"); replayed != "true" {
		t.Fatalf("Idempotent-Replayed = %q, want true", replayed)
	}
	if secondRR.Header().Get("Content-Type") != firstRR.Header().Get("Content-Type") ||
		secondRR.Header().Get("Location") != firstRR.Header().Get("Location") ||
		secondRR.Body.String() != firstBody {
		t.Fatalf("replay = %q/%q/%s, want exact %q/%q/%s", secondRR.Header().Get("Content-Type"), secondRR.Header().Get("Location"), secondRR.Body.String(), firstRR.Header().Get("Content-Type"), firstRR.Header().Get("Location"), firstBody)
	}
	events, err := store.ListEvents(context.Background(), "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
}

type faultOperationStore struct {
	*MemoryOperationStore
	createErr   error
	appendErr   error
	createCalls int
	appendCalls int
}

func (s *faultOperationStore) Create(ctx context.Context, operation orailtypes.Operation) (orailtypes.Operation, error) {
	s.createCalls++
	if s.createErr != nil {
		return orailtypes.Operation{}, s.createErr
	}
	return s.MemoryOperationStore.Create(ctx, operation)
}

func (s *faultOperationStore) AppendEvent(ctx context.Context, event OperationEvent) (OperationEvent, error) {
	s.appendCalls++
	if s.appendErr != nil {
		return OperationEvent{}, s.appendErr
	}
	return s.MemoryOperationStore.AppendEvent(ctx, event)
}

func TestRouterCancelOperationReplaysPostReservationFailure(t *testing.T) {
	memory := NewMemoryOperationStore()
	if _, err := memory.Create(context.Background(), orailtypes.Operation{ID: "op-fault", State: orailtypes.OperationRunning}); err != nil {
		t.Fatal(err)
	}
	store := &faultOperationStore{MemoryOperationStore: memory, appendErr: errors.New("event store unavailable")}
	router := NewRouterWithOperationStore(store)
	doPost := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-fault/cancel", nil)
		req.Header.Set("Idempotency-Key", "cancel-op-fault")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := doPost()
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d body=%s, want 500", first.Code, first.Body.String())
	}
	replay := doPost()
	if replay.Code != first.Code ||
		replay.Header().Get("Idempotent-Replayed") != "true" ||
		replay.Header().Get("Content-Type") != first.Header().Get("Content-Type") ||
		replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d/%q/%q/%s, want exact %d/%q/%s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Header().Get("Content-Type"), replay.Body.String(), first.Code, first.Header().Get("Content-Type"), first.Body.String())
	}
	if store.appendCalls != 1 {
		t.Fatalf("append attempts = %d, want one", store.appendCalls)
	}
}

func TestWriteInvalidOperationTransitionEnvelope(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/op-1", nil)
	req.Header.Set("X-Request-ID", "req-transition")
	rr := httptest.NewRecorder()

	WriteInvalidOperationTransition(rr, req, orailtypes.OperationSucceeded, orailtypes.OperationRunning)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.OperationInvalidTransition || got.Error.Details["from"] != string(orailtypes.OperationSucceeded) || got.Error.Details["to"] != string(orailtypes.OperationRunning) {
		t.Fatalf("unexpected envelope: %#v", got.Error)
	}
}

func TestRouterDemoLongRequiresValidIdempotencyKey(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.IdempotencyKeyInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.IdempotencyKeyInvalid)
	}
}

func TestRouterDemoHealthTargetReturnsDeterministicReview(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/_demo/health-target", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var got TargetHealthReview
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TargetName != "demo-api" || got.Status != TargetHealthHealthy || got.ReasonCode != TargetHealthReasonUp {
		t.Fatalf("review = %#v, want deterministic healthy demo-api", got)
	}
	if !got.ObservedAt.Equal(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("observed_at = %s, want deterministic timestamp", got.ObservedAt)
	}
}

func TestRouterDemoHealthTargetRejectsNonGET(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/health-target", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func TestRouterDemoLongCreatesQueuedOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", nil)
	req.Header.Set("Idempotency-Key", "demo-long-1")
	rr := httptest.NewRecorder()

	NewRouterWithOperationStore(store).ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusAccepted)
	}
	var operation orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.ID == "" || operation.State != orailtypes.OperationPending {
		t.Fatalf("unexpected operation: %#v", operation)
	}
	if location := rr.Header().Get("Location"); location != "/api/v1alpha/operations/"+string(operation.ID) {
		t.Fatalf("Location = %q, want operation URL", location)
	}
	if replayed := rr.Header().Get("Idempotent-Replayed"); replayed != "" {
		t.Fatalf("Idempotent-Replayed = %q, want empty", replayed)
	}
	events, err := store.ListEvents(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "queued" || events[0].State != orailtypes.OperationPending {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestRouterDemoLongReplaysSameOperationForSameIdempotencyKeyAndBody(t *testing.T) {
	store := NewMemoryOperationStore()
	router := NewRouterWithOperationStore(store)

	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", strings.NewReader(`{"job":"slow"}`))
	firstReq.Header.Set("Idempotency-Key", "demo-long-replay")
	firstRR := httptest.NewRecorder()
	router.ServeHTTP(firstRR, firstReq)

	if firstRR.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d", firstRR.Code, http.StatusAccepted)
	}
	var firstOperation orailtypes.Operation
	if err := json.Unmarshal(firstRR.Body.Bytes(), &firstOperation); err != nil {
		t.Fatal(err)
	}
	firstBody := firstRR.Body.String()
	if _, err := store.Transition(context.Background(), firstOperation.ID, orailtypes.OperationRunning); err != nil {
		t.Fatal(err)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", strings.NewReader(`{"job":"slow"}`))
	secondReq.Header.Set("Idempotency-Key", "demo-long-replay")
	secondRR := httptest.NewRecorder()
	router.ServeHTTP(secondRR, secondReq)

	if secondRR.Code != http.StatusAccepted {
		t.Fatalf("second status = %d, want %d", secondRR.Code, http.StatusAccepted)
	}
	var secondOperation orailtypes.Operation
	if err := json.Unmarshal(secondRR.Body.Bytes(), &secondOperation); err != nil {
		t.Fatal(err)
	}
	if secondOperation != firstOperation {
		t.Fatalf("replayed operation = %#v, want %#v", secondOperation, firstOperation)
	}
	if replayed := secondRR.Header().Get("Idempotent-Replayed"); replayed != "true" {
		t.Fatalf("Idempotent-Replayed = %q, want true", replayed)
	}
	if location := secondRR.Header().Get("Location"); location != "/api/v1alpha/operations/"+string(firstOperation.ID) {
		t.Fatalf("Location = %q, want replayed operation URL", location)
	}
	if secondRR.Header().Get("Content-Type") != firstRR.Header().Get("Content-Type") ||
		secondRR.Body.String() != firstBody {
		t.Fatalf("replay = %q/%s, want exact %q/%s", secondRR.Header().Get("Content-Type"), secondRR.Body.String(), firstRR.Header().Get("Content-Type"), firstBody)
	}
	events, err := store.ListEvents(context.Background(), firstOperation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
}

func TestRouterDemoLongReplaysPostReservationFailure(t *testing.T) {
	store := &faultOperationStore{MemoryOperationStore: NewMemoryOperationStore(), createErr: errors.New("operation store unavailable")}
	router := NewRouterWithOperationStore(store)
	doPost := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", strings.NewReader(`{"job":"fault"}`))
		req.Header.Set("Idempotency-Key", "demo-long-fault-replay")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := doPost()
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d body=%s, want 500", first.Code, first.Body.String())
	}
	replay := doPost()
	if replay.Code != first.Code ||
		replay.Header().Get("Idempotent-Replayed") != "true" ||
		replay.Header().Get("Content-Type") != first.Header().Get("Content-Type") ||
		replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d/%q/%q/%s, want exact %d/%q/%s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Header().Get("Content-Type"), replay.Body.String(), first.Code, first.Header().Get("Content-Type"), first.Body.String())
	}
	if store.createCalls != 1 {
		t.Fatalf("create attempts = %d, want one", store.createCalls)
	}
}

func TestRouterDemoLongRejectsIdempotencyKeyReuseWithDifferentBody(t *testing.T) {
	router := NewRouterWithOperationStore(NewMemoryOperationStore())

	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", strings.NewReader(`{"job":"one"}`))
	firstReq.Header.Set("Idempotency-Key", "demo-long-conflict")
	firstRR := httptest.NewRecorder()
	router.ServeHTTP(firstRR, firstReq)

	if firstRR.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d", firstRR.Code, http.StatusAccepted)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", strings.NewReader(`{"job":"two"}`))
	secondReq.Header.Set("Idempotency-Key", "demo-long-conflict")
	secondRR := httptest.NewRecorder()
	router.ServeHTTP(secondRR, secondReq)

	if secondRR.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want %d", secondRR.Code, http.StatusConflict)
	}
	var got ErrorResponse
	if err := json.Unmarshal(secondRR.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.IdempotencyKeyReusedDifferentBody {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.IdempotencyKeyReusedDifferentBody)
	}
}

type cancelAwareQueueStore struct {
	request queue.CancelQueuedRequest
	result  queue.CancelQueuedResult
	err     error
}

func (s *cancelAwareQueueStore) Enqueue(context.Context, queue.EnqueueRequest) (queue.Job, error) {
	return queue.Job{}, nil
}

func (s *cancelAwareQueueStore) Lease(context.Context, queue.LeaseRequest) (queue.Job, error) {
	return queue.Job{}, queue.ErrNoWork
}

func (s *cancelAwareQueueStore) Heartbeat(context.Context, string, string, time.Time) error {
	return nil
}

func (s *cancelAwareQueueStore) Complete(context.Context, string, string) error {
	return nil
}

func (s *cancelAwareQueueStore) Fail(context.Context, string, string, string) error {
	return nil
}

func (s *cancelAwareQueueStore) CancelQueued(_ context.Context, request queue.CancelQueuedRequest) (queue.CancelQueuedResult, error) {
	s.request = request
	if s.err != nil {
		return queue.CancelQueuedResult{}, s.err
	}
	return s.result, nil
}

var _ queue.Store = (*cancelAwareQueueStore)(nil)
var _ queue.QueuedCancelStore = (*cancelAwareQueueStore)(nil)
