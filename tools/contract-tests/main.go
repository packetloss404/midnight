// Package main is the S08-owned OpenAPI contract checker. The binary verifies
// that every error code referenced by the locked OpenAPI document at
// `api/openapi/v1alpha/midnight.yaml` (or a path passed via `--openapi`) is
// present in `docs/error-codes.md`, that the required MVP workflow remains
// declared, and that every operation protected by an effective security
// requirement documents the auth middleware's possible 401 response. The
// reader is hand-rolled against the deliberately constrained canonical YAML
// shape; the live-router ownership/auth comparison lives in
// internal/api/router_contract_test.go.
//
// Exit codes mirror the phase 0 gate convention:
//
//   - 0: every referenced error code is present in the registry (or no error
//     codes are referenced).
//   - 2: invocation error — flag parsing, missing input file, malformed
//     OpenAPI structure that prevents enumeration.
//   - 3: conformance violation — at least one referenced error code is not in
//     the registry.
//
// The checker is invoked from the repository root:
//
//	go run ./tools/contract-tests
//
// It reads `api/openapi/v1alpha/midnight.yaml` and `docs/error-codes.md`
// relative to the working directory by default.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	exitOK             = 0
	exitInvocation     = 2
	exitNonconformance = 3
)

// defaultOpenAPIPath is the relative path of the locked Phase 0 OpenAPI doc.
const defaultOpenAPIPath = "api/openapi/v1alpha/midnight.yaml"

// defaultErrorCodesPath is the relative path of the Phase 0 error-code
// registry.
const defaultErrorCodesPath = "docs/error-codes.md"

const defaultReadmePath = "README.md"

const defaultMVPDocPath = "docs/mvp-acceptance.md"

const defaultMVPRunnerPath = "scripts/mvp-acceptance.sh"

var requiredMVPReadinessMarkers = []string{
	"## OSS Railway Readiness Checklist",
	"## Next Acceptance Slice: OSS Railway Happy Path",
	"queued draft/job state",
	"memory-mode diagnostics",
	"typed recovery states",
	"Dashboard/API/CLI agree",
	"fake/demo-only data",
}

var requiredWorkflowOperations = []OperationExpectation{
	{Path: "/backup/runs", Method: "get", OperationID: "listBackupRuns", StatusCodes: []string{"200", "400", "500"}},
	{Path: "/backup/runs", Method: "post", OperationID: "createBackupRun", StatusCodes: []string{"201", "400", "500"}},
	{Path: "/backup/restore/status", Method: "get", OperationID: "getBackupRestoreStatus", StatusCodes: []string{"200"}},
	{Path: "/backup/restore/plans", Method: "get", OperationID: "listBackupRestorePlans", StatusCodes: []string{"200", "400", "500"}},
	{Path: "/backup/restore/plans", Method: "post", OperationID: "createBackupRestorePlan", StatusCodes: []string{"201", "400", "500"}},
	{Path: "/agents/register", Method: "post", OperationID: "registerAgent", StatusCodes: []string{"200", "400"}},
	{Path: "/agents/{id}/status", Method: "post", OperationID: "recordAgentStatus", StatusCodes: []string{"200"}},
	{Path: "/projects", Method: "get", OperationID: "listProjects", StatusCodes: []string{"200"}},
	{Path: "/projects", Method: "post", OperationID: "createProject", StatusCodes: []string{"201", "400", "409"}},
	{Path: "/services", Method: "get", OperationID: "listServices", StatusCodes: []string{"200"}},
	{Path: "/services", Method: "post", OperationID: "createService", StatusCodes: []string{"201", "400", "409"}},
	{Path: "/services/{id}", Method: "delete", OperationID: "deleteService", StatusCodes: []string{"200", "400", "401", "403", "404", "409", "412", "428", "500", "503"}},
	{Path: "/environments/{id}", Method: "patch", OperationID: "updateEnvironment", StatusCodes: []string{"200", "400", "401", "403", "404", "409", "413", "500", "503"}},
	{Path: "/services/{id}/up", Method: "post", OperationID: "createServiceUp", StatusCodes: []string{"202", "400", "404", "409", "413", "415", "503", "500"}},
	{Path: "/variables", Method: "get", OperationID: "listVariables", StatusCodes: []string{"200"}},
	{Path: "/variables", Method: "post", OperationID: "createVariable", StatusCodes: []string{"201", "400", "409"}},
	{Path: "/variables/{id}", Method: "get", OperationID: "getVariable", StatusCodes: []string{"200", "404"}},
	{Path: "/services/{service_id}/environments/{environment_id}/variables", Method: "get", OperationID: "listTenantServiceEnvironmentVariables", StatusCodes: []string{"200", "401", "403", "404", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/variables", Method: "post", OperationID: "createTenantServiceEnvironmentVariable", StatusCodes: []string{"201", "400", "401", "403", "404", "409", "413", "500", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/variables/{variable_id}", Method: "get", OperationID: "getTenantServiceEnvironmentVariable", StatusCodes: []string{"200", "401", "403", "404", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/variables/{variable_id}", Method: "patch", OperationID: "updateTenantServiceEnvironmentVariable", StatusCodes: []string{"200", "400", "401", "403", "404", "409", "413", "500", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/variables/{variable_id}", Method: "delete", OperationID: "deleteTenantServiceEnvironmentVariable", StatusCodes: []string{"204", "400", "401", "403", "404", "409", "413", "500", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/domains", Method: "get", OperationID: "listTenantServiceEnvironmentGeneratedDomains", StatusCodes: []string{"200", "401", "403", "404", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/domains", Method: "post", OperationID: "createTenantServiceEnvironmentGeneratedDomain", StatusCodes: []string{"200", "201", "400", "401", "403", "404", "409", "413", "500", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/domains/{domain_id}", Method: "get", OperationID: "getTenantServiceEnvironmentGeneratedDomain", StatusCodes: []string{"200", "401", "403", "404", "503"}},
	{Path: "/services/{service_id}/environments/{environment_id}/domains/{domain_id}", Method: "delete", OperationID: "deleteTenantServiceEnvironmentGeneratedDomain", StatusCodes: []string{"204", "400", "401", "403", "404", "409", "413", "500", "503"}},
	{Path: "/services/{id}/deployments", Method: "get", OperationID: "listServiceDeployments", StatusCodes: []string{"200", "404", "500"}},
	{Path: "/services/{id}/deployments", Method: "post", OperationID: "createServiceDeployment", StatusCodes: []string{"202", "400", "404", "409", "500"}},
	{Path: "/services/{id}/deployments/latest-active", Method: "get", OperationID: "getLatestActiveServiceDeployment", StatusCodes: []string{"200", "404", "500"}},
	{Path: "/services/{id}/deployments/{deployment_id}", Method: "get", OperationID: "getServiceDeployment", StatusCodes: []string{"200", "404", "500"}},
	{Path: "/operations", Method: "get", OperationID: "listOperations", StatusCodes: []string{"200", "400"}},
	{Path: "/operations/{id}", Method: "get", OperationID: "getOperation", StatusCodes: []string{"200", "404"}},
	{Path: "/operations/{id}/events", Method: "get", OperationID: "streamOperationEvents", StatusCodes: []string{"200", "400", "404"}},
	{Path: "/operations/{id}/logs", Method: "get", OperationID: "getOperationLogs", StatusCodes: []string{"200", "400", "404", "500"}},
	{Path: "/operations/{id}/cancel", Method: "post", OperationID: "cancelOperation", StatusCodes: []string{"202", "400", "404", "409"}},
	{Path: "/_demo/schema-evidence", Method: "get", OperationID: "getDemoSchemaEvidence", StatusCodes: []string{"200"}},
	{Path: "/_demo/migration-plan", Method: "get", OperationID: "getDemoMigrationPlan", StatusCodes: []string{"200"}},
	{Path: "/_demo/release-evidence", Method: "get", OperationID: "getDemoReleaseEvidence", StatusCodes: []string{"200"}},
	{Path: "/_demo/evidence-index", Method: "get", OperationID: "getDemoEvidenceIndex", StatusCodes: []string{"200"}},
	{Path: "/_demo/api-data-readiness", Method: "get", OperationID: "getDemoAPIDataReadiness", StatusCodes: []string{"200"}},
	{Path: "/audit/events", Method: "get", OperationID: "listAuditEvents", StatusCodes: []string{"200", "400"}},
	{Path: "/audit/export", Method: "get", OperationID: "exportAuditEvents", StatusCodes: []string{"200"}},
	{Path: "/audit/evidence", Method: "get", OperationID: "getAuditEvidence", StatusCodes: []string{"200"}},
	{Path: "/dashboard/runtime-inventory", Method: "get", OperationID: "getDashboardRuntimeInventory", StatusCodes: []string{"200"}},
	{Path: "/dashboard/audit-evidence", Method: "get", OperationID: "getDashboardAuditEvidence", StatusCodes: []string{"200"}},
	{Path: "/dashboard/template-marketplace", Method: "get", OperationID: "listDashboardTemplateMarketplace", StatusCodes: []string{"200"}},
	// Database templates fail closed until the runtime saga can prove volume,
	// network, readiness, and smoke-test evidence. A queued draft is not a
	// successful deployment contract.
	{Path: "/dashboard/template-deploy", Method: "post", OperationID: "createDashboardTemplateDeploy", StatusCodes: []string{"501"}},
}

func main() {
	exit := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}

// run is the testable entry point that returns an integer exit code instead
// of calling os.Exit. Stdout receives the human-readable conformance report;
// stderr receives invocation errors.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("contract-tests", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	openapiPath := fs.String("openapi", defaultOpenAPIPath, "path to the OpenAPI document")
	errorCodesPath := fs.String("error-codes", defaultErrorCodesPath, "path to the error-codes registry markdown")
	readmePath := fs.String("readme", defaultReadmePath, "path to the repository README")
	mvpDocPath := fs.String("mvp-doc", defaultMVPDocPath, "path to the MVP acceptance markdown")
	mvpRunnerPath := fs.String("mvp-runner", defaultMVPRunnerPath, "path to the MVP acceptance runner")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: %v\n", err)
		return exitInvocation
	}
	openapiData, err := os.ReadFile(*openapiPath)
	if err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: read openapi %s: %v\n", *openapiPath, err)
		return exitInvocation
	}
	registryData, err := os.ReadFile(*errorCodesPath)
	if err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: read error-codes %s: %v\n", *errorCodesPath, err)
		return exitInvocation
	}
	readmeData, err := os.ReadFile(*readmePath)
	if err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: read readme %s: %v\n", *readmePath, err)
		return exitInvocation
	}
	mvpDocData, err := os.ReadFile(*mvpDocPath)
	if err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: read mvp doc %s: %v\n", *mvpDocPath, err)
		return exitInvocation
	}
	mvpRunnerData, err := os.ReadFile(*mvpRunnerPath)
	if err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: read mvp runner %s: %v\n", *mvpRunnerPath, err)
		return exitInvocation
	}
	report, err := CheckOpenAPIErrorCodes(openapiData, registryData)
	if err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: %v\n", err)
		return exitInvocation
	}
	mvpReport, err := CheckMVPReadinessDocs(readmeData, mvpDocData, mvpRunnerData)
	if err != nil {
		fmt.Fprintf(stderr, "contract-tests failed: %v\n", err)
		return exitInvocation
	}
	fmt.Fprint(stdout, report.Render())
	fmt.Fprint(stdout, mvpReport.Render())
	workflowReport := CheckWorkflowOperations(report.Operations, requiredWorkflowOperations)
	fmt.Fprint(stdout, workflowReport.Render())
	securityReport := CheckOperationSecurity(report.Operations)
	fmt.Fprint(stdout, securityReport.Render())
	if len(report.MissingCodes) > 0 ||
		!mvpReport.Pass() ||
		!workflowReport.Pass() ||
		!securityReport.Pass() {
		return exitNonconformance
	}
	return exitOK
}

// ContractReport is the deterministic result of running the OpenAPI contract
// check. Operations are listed in order of appearance; the missing-codes list
// is sorted alphabetically for stable output.
type ContractReport struct {
	Operations    []OperationSummary
	MissingCodes  []MissingCodeRef
	RegistrySize  int
	OpenAPIPath   string
	ErrorCodePath string
}

// OperationSummary captures one OpenAPI operation enumerated from the spec.
// Path is the full URL path (e.g. "/operations/{id}"), Method is the HTTP
// verb (lowercase), and StatusCodes lists the response status codes declared
// for the operation in document order.
type OperationSummary struct {
	Path        string
	Method      string
	OperationID string
	StatusCodes []string
	// Security lists the effective schemes for this operation after applying
	// the document-level default and any operation-level override. An empty
	// list means the operation explicitly or implicitly permits anonymous
	// requests.
	Security []string
}

type OperationExpectation struct {
	Path        string
	Method      string
	OperationID string
	StatusCodes []string
}

type WorkflowOperationReport struct {
	Required []OperationExpectation
	Missing  []OperationExpectation
	Drift    []WorkflowOperationDrift
}

type WorkflowOperationDrift struct {
	Expected OperationExpectation
	Actual   OperationSummary
	Missing  []string
}

// OperationSecurityReport enforces the minimum response contract shared by
// every authenticated API operation. The router's auth middleware can reject
// a request before its handler runs, so a protected operation that omits 401
// from OpenAPI is necessarily incomplete even if its happy-path responses are
// otherwise documented.
type OperationSecurityReport struct {
	Protected           int
	MissingUnauthorized []OperationSummary
}

func (r OperationSecurityReport) Pass() bool {
	return len(r.MissingUnauthorized) == 0
}

func (r OperationSecurityReport) Render() string {
	var b strings.Builder
	b.WriteString("\n# OpenAPI Security Response Report\n\n")
	b.WriteString(fmt.Sprintf("Protected operations: %d\n", r.Protected))
	b.WriteString(fmt.Sprintf("Protected operations missing 401: %d\n", len(r.MissingUnauthorized)))
	if len(r.MissingUnauthorized) > 0 {
		b.WriteString("\n## Missing Unauthorized Responses\n")
		for _, op := range r.MissingUnauthorized {
			b.WriteString(fmt.Sprintf(
				"- %s %s [%s] security=%s responses=%s\n",
				strings.ToUpper(op.Method),
				op.Path,
				op.OperationID,
				strings.Join(op.Security, ","),
				strings.Join(op.StatusCodes, ","),
			))
		}
	}
	return b.String()
}

func CheckOperationSecurity(operations []OperationSummary) OperationSecurityReport {
	report := OperationSecurityReport{}
	for _, op := range operations {
		if len(op.Security) == 0 {
			continue
		}
		report.Protected++
		if !containsStatusCode(op.StatusCodes, "401") {
			report.MissingUnauthorized = append(report.MissingUnauthorized, op)
		}
	}
	return report
}

func containsStatusCode(codes []string, want string) bool {
	for _, code := range codes {
		if strings.TrimSpace(code) == want {
			return true
		}
	}
	return false
}

func (r WorkflowOperationReport) Pass() bool {
	return len(r.Missing) == 0 && len(r.Drift) == 0
}

func (r WorkflowOperationReport) Render() string {
	var b strings.Builder
	b.WriteString("\n# MVP Workflow API Surface Report\n\n")
	b.WriteString(fmt.Sprintf("Required operations: %d\n", len(r.Required)))
	b.WriteString(fmt.Sprintf("Missing operations: %d\n", len(r.Missing)))
	b.WriteString(fmt.Sprintf("Operations with missing status codes: %d\n", len(r.Drift)))
	if len(r.Missing) > 0 {
		b.WriteString("\n## Missing Operations\n")
		for _, op := range r.Missing {
			b.WriteString(fmt.Sprintf("- %s %s [%s]\n", strings.ToUpper(op.Method), op.Path, op.OperationID))
		}
	}
	if len(r.Drift) > 0 {
		b.WriteString("\n## Missing Status Codes\n")
		for _, drift := range r.Drift {
			b.WriteString(fmt.Sprintf("- %s %s [%s] missing=%s actual=%s\n",
				strings.ToUpper(drift.Expected.Method), drift.Expected.Path, drift.Expected.OperationID,
				strings.Join(drift.Missing, ","), strings.Join(drift.Actual.StatusCodes, ",")))
		}
	}
	return b.String()
}

func CheckWorkflowOperations(operations []OperationSummary, required []OperationExpectation) WorkflowOperationReport {
	seen := make(map[string]OperationSummary, len(operations))
	for _, op := range operations {
		seen[operationKey(op.Path, op.Method, op.OperationID)] = op
	}
	report := WorkflowOperationReport{Required: append([]OperationExpectation(nil), required...)}
	for _, expected := range required {
		actual, ok := seen[operationKey(expected.Path, expected.Method, expected.OperationID)]
		if !ok {
			report.Missing = append(report.Missing, expected)
			continue
		}
		if missing := missingStatusCodes(actual.StatusCodes, expected.StatusCodes); len(missing) > 0 {
			report.Drift = append(report.Drift, WorkflowOperationDrift{
				Expected: expected,
				Actual:   actual,
				Missing:  missing,
			})
		}
	}
	return report
}

func missingStatusCodes(actual, expected []string) []string {
	have := make(map[string]struct{}, len(actual))
	for _, code := range actual {
		have[strings.TrimSpace(code)] = struct{}{}
	}
	missing := make([]string, 0)
	for _, code := range expected {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, ok := have[code]; !ok {
			missing = append(missing, code)
		}
	}
	return missing
}

func operationKey(path, method, operationID string) string {
	return strings.ToLower(strings.TrimSpace(method)) + " " + strings.TrimSpace(path) + " " + strings.TrimSpace(operationID)
}

// MissingCodeRef captures one error code that is referenced by the OpenAPI
// document but is absent from the registry.
type MissingCodeRef struct {
	Code   string
	Source string
}

// CheckOpenAPIErrorCodes parses an OpenAPI document and an error-code
// registry markdown file and returns a deterministic ContractReport. The
// OpenAPI parser is intentionally minimal — it only walks the top-level
// `paths:` block, the per-operation `responses:` block, and any
// `components.schemas.*.code.enum` blocks. Both inputs are read in-memory;
// no network access is performed.
func CheckOpenAPIErrorCodes(openapiData, registryData []byte) (ContractReport, error) {
	registry, err := parseErrorCodeRegistry(registryData)
	if err != nil {
		return ContractReport{}, fmt.Errorf("parse error-codes registry: %w", err)
	}
	operations, codes, err := parseOpenAPI(openapiData)
	if err != nil {
		return ContractReport{}, fmt.Errorf("parse openapi: %w", err)
	}
	report := ContractReport{
		Operations:   operations,
		RegistrySize: len(registry),
	}
	missingSet := make(map[string]MissingCodeRef)
	for _, ref := range codes {
		if _, ok := registry[ref.Code]; ok {
			continue
		}
		// Deduplicate by code: report the first source we saw.
		if _, ok := missingSet[ref.Code]; !ok {
			missingSet[ref.Code] = ref
		}
	}
	for _, ref := range missingSet {
		report.MissingCodes = append(report.MissingCodes, ref)
	}
	sort.Slice(report.MissingCodes, func(i, j int) bool {
		return report.MissingCodes[i].Code < report.MissingCodes[j].Code
	})
	return report, nil
}

// Render returns a deterministic plain-text summary of the contract report.
// The format is stable across runs given identical inputs.
func (r ContractReport) Render() string {
	var b strings.Builder
	b.WriteString("# OpenAPI Contract Report\n\n")
	b.WriteString(fmt.Sprintf("Operations: %d\n", len(r.Operations)))
	b.WriteString(fmt.Sprintf("Registry codes: %d\n", r.RegistrySize))
	b.WriteString(fmt.Sprintf("Missing codes: %d\n\n", len(r.MissingCodes)))
	if len(r.Operations) > 0 {
		b.WriteString("## Operations\n")
		for _, op := range r.Operations {
			b.WriteString(fmt.Sprintf("- %s %s [%s] responses=%s\n",
				strings.ToUpper(op.Method), op.Path, op.OperationID,
				strings.Join(op.StatusCodes, ",")))
		}
	}
	if len(r.MissingCodes) > 0 {
		b.WriteString("\n## Missing Codes\n")
		for _, ref := range r.MissingCodes {
			b.WriteString(fmt.Sprintf("- %s (source: %s)\n", ref.Code, ref.Source))
		}
	}
	return b.String()
}

type MVPReadinessReport struct {
	RunnerModes            []string
	MissingReadmeMarkers   []string
	MissingMVPDocMarkers   []string
	MissingDocumentedModes []string
}

func (r MVPReadinessReport) Pass() bool {
	return len(r.MissingReadmeMarkers) == 0 &&
		len(r.MissingMVPDocMarkers) == 0 &&
		len(r.MissingDocumentedModes) == 0
}

func (r MVPReadinessReport) Render() string {
	var b strings.Builder
	b.WriteString("\n# MVP Readiness Docs Report\n\n")
	b.WriteString(fmt.Sprintf("Runner modes: %d\n", len(r.RunnerModes)))
	b.WriteString(fmt.Sprintf("Missing README markers: %d\n", len(r.MissingReadmeMarkers)))
	b.WriteString(fmt.Sprintf("Missing MVP doc markers: %d\n", len(r.MissingMVPDocMarkers)))
	b.WriteString(fmt.Sprintf("Missing documented runner modes: %d\n", len(r.MissingDocumentedModes)))
	if len(r.MissingReadmeMarkers) > 0 {
		b.WriteString("\n## Missing README Markers\n")
		for _, marker := range r.MissingReadmeMarkers {
			b.WriteString(fmt.Sprintf("- %s\n", marker))
		}
	}
	if len(r.MissingMVPDocMarkers) > 0 {
		b.WriteString("\n## Missing MVP Doc Markers\n")
		for _, marker := range r.MissingMVPDocMarkers {
			b.WriteString(fmt.Sprintf("- %s\n", marker))
		}
	}
	if len(r.MissingDocumentedModes) > 0 {
		b.WriteString("\n## Missing Documented Runner Modes\n")
		for _, mode := range r.MissingDocumentedModes {
			b.WriteString(fmt.Sprintf("- %s\n", mode))
		}
	}
	return b.String()
}

func CheckMVPReadinessDocs(readmeData, mvpDocData, runnerData []byte) (MVPReadinessReport, error) {
	if len(readmeData) == 0 {
		return MVPReadinessReport{}, errors.New("README payload is empty")
	}
	if len(mvpDocData) == 0 {
		return MVPReadinessReport{}, errors.New("MVP acceptance payload is empty")
	}
	if len(runnerData) == 0 {
		return MVPReadinessReport{}, errors.New("MVP runner payload is empty")
	}
	readme := string(readmeData)
	mvpDoc := string(mvpDocData)
	report := MVPReadinessReport{
		RunnerModes: parseMVPRunnerModes(runnerData),
	}
	if len(report.RunnerModes) == 0 {
		return MVPReadinessReport{}, errors.New("MVP runner declares no modes")
	}
	for _, marker := range []string{
		"## OSS Railway Readiness Checklist",
		"docs/mvp-acceptance.md",
		"scripts/mvp-acceptance.sh",
	} {
		if !strings.Contains(readme, marker) {
			report.MissingReadmeMarkers = append(report.MissingReadmeMarkers, marker)
		}
	}
	for _, marker := range requiredMVPReadinessMarkers {
		if !strings.Contains(mvpDoc, marker) {
			report.MissingMVPDocMarkers = append(report.MissingMVPDocMarkers, marker)
		}
	}
	for _, mode := range report.RunnerModes {
		if !strings.Contains(mvpDoc, mode) {
			report.MissingDocumentedModes = append(report.MissingDocumentedModes, mode)
		}
	}
	sort.Strings(report.MissingReadmeMarkers)
	sort.Strings(report.MissingMVPDocMarkers)
	sort.Strings(report.MissingDocumentedModes)
	return report, nil
}

func parseMVPRunnerModes(data []byte) []string {
	modeRe := regexp.MustCompile(`(?m)^\s{2}([a-z][a-z0-9-]*)\)\s+`)
	seen := make(map[string]bool)
	for _, match := range modeRe.FindAllStringSubmatch(string(data), -1) {
		if len(match) != 2 {
			continue
		}
		if match[1] == "help" {
			continue
		}
		seen[match[1]] = true
	}
	out := make([]string, 0, len(seen))
	for mode := range seen {
		out = append(out, mode)
	}
	sort.Strings(out)
	return out
}

var registryCodeRow = regexp.MustCompile("^\\s*\\|\\s*`([A-Z][A-Z0-9_]*)`\\s*\\|")

func parseErrorCodeRegistry(data []byte) (map[string]struct{}, error) {
	if len(data) == 0 {
		return nil, errors.New("registry payload is empty")
	}
	out := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		match := registryCodeRow.FindStringSubmatch(line)
		if len(match) == 2 {
			out[match[1]] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("registry contains no SCREAMING_SNAKE_CASE codes")
	}
	return out, nil
}

// parseOpenAPI walks the OpenAPI YAML document and returns the operations
// plus every SCREAMING_SNAKE_CASE error code it references. The parser is a
// minimal hand-rolled line-based reader that depends only on stable
// indentation conventions in the locked Phase 0 OpenAPI document. It
// recognizes:
//
//   - Top-level `paths:` section.
//   - Path entries (lines starting with two spaces and ending in `:`).
//   - HTTP method entries inside a path (`get:`, `post:`, etc.).
//   - `operationId:` and `responses:` blocks within an operation.
//   - Response status code entries (`'200':`, `'400':`, ...).
//   - `code:` enum blocks under `components.schemas.<Name>` whose entries are
//     SCREAMING_SNAKE_CASE.
//
// This is sufficient for the Phase 0 surface; richer YAML constructs (anchors,
// flow style, multi-document) are not used in the locked OpenAPI document.
func parseOpenAPI(data []byte) ([]OperationSummary, []MissingCodeRef, error) {
	if len(data) == 0 {
		return nil, nil, errors.New("openapi payload is empty")
	}
	lines := strings.Split(string(data), "\n")
	globalSecurity := parseGlobalSecurity(lines)
	var operations []OperationSummary
	var refs []MissingCodeRef

	inPaths := false
	inSchemas := false
	currentPath := ""
	pathIndent := -1
	currentOp := -1 // index into operations slice
	opIndent := -1
	inResponses := false
	responsesIndent := -1
	inSecurity := false
	securityIndent := -1
	currentSchemaName := ""
	schemaIndent := -1
	inCodeEnum := false
	codeEnumIndent := -1
	codeEnumSourceSchema := ""

	httpMethods := map[string]struct{}{
		"get": {}, "put": {}, "post": {}, "delete": {},
		"options": {}, "head": {}, "patch": {}, "trace": {},
	}
	statusRowRe := regexp.MustCompile(`^'?[1-5][0-9X][0-9X]'?:\s*$`)
	securityRowRe := regexp.MustCompile(`^-\s*([A-Za-z][A-Za-z0-9_-]*):\s*(?:\[\])?\s*$`)
	enumValueRe := regexp.MustCompile(`^-\s*([A-Z][A-Z0-9_]*)\s*$`)
	schemaCodeFieldRe := regexp.MustCompile(`^code:\s*$`)

	indentOf := func(s string) int {
		count := 0
		for _, r := range s {
			if r == ' ' {
				count++
				continue
			}
			break
		}
		return count
	}

	for rawLineIdx, raw := range lines {
		_ = rawLineIdx
		// Drop trailing CR for safety on CRLF inputs.
		raw = strings.TrimRight(raw, "\r")
		if strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		ind := indentOf(raw)

		// Top-level switches: paths/components.
		if ind == 0 {
			inPaths = trimmed == "paths:"
			inSchemas = false
			currentPath = ""
			pathIndent = -1
			currentOp = -1
			opIndent = -1
			inResponses = false
			responsesIndent = -1
			inSecurity = false
			securityIndent = -1
			currentSchemaName = ""
			schemaIndent = -1
			inCodeEnum = false
			if trimmed == "components:" {
				// components is handled below by walking child indents.
			}
			continue
		}

		// Detect components.schemas section: indent 2 = "schemas:" under components.
		if ind == 2 && trimmed == "schemas:" {
			inSchemas = true
			inPaths = false
			currentSchemaName = ""
			schemaIndent = -1
			inCodeEnum = false
			continue
		}

		if inPaths {
			handlePathsLine(
				raw,
				trimmed,
				ind,
				&currentPath,
				&pathIndent,
				&operations,
				&currentOp,
				&opIndent,
				&inResponses,
				&responsesIndent,
				&inSecurity,
				&securityIndent,
				globalSecurity,
				httpMethods,
				statusRowRe,
				securityRowRe,
			)
			continue
		}

		if inSchemas {
			// Schema name lines are at indent 4 (under schemas: at indent 2).
			if ind == 4 && strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
				currentSchemaName = strings.TrimSuffix(trimmed, ":")
				schemaIndent = ind
				inCodeEnum = false
				continue
			}
			// Inside a schema, look for a `code:` field whose immediate child
			// is `enum:` and whose enum entries are SCREAMING_SNAKE_CASE.
			if currentSchemaName != "" && ind > schemaIndent {
				if !inCodeEnum && schemaCodeFieldRe.MatchString(trimmed) {
					// Look ahead is unnecessary; we flip on `enum:` when seen.
					codeEnumSourceSchema = currentSchemaName
					codeEnumIndent = ind
					continue
				}
				if codeEnumIndent > 0 && ind > codeEnumIndent && trimmed == "enum:" {
					inCodeEnum = true
					continue
				}
				if inCodeEnum {
					if ind <= codeEnumIndent {
						inCodeEnum = false
						codeEnumIndent = -1
						// Re-process the current line as a non-enum line.
					} else if match := enumValueRe.FindStringSubmatch(trimmed); len(match) == 2 {
						refs = append(refs, MissingCodeRef{
							Code:   match[1],
							Source: fmt.Sprintf("components.schemas.%s.code.enum", codeEnumSourceSchema),
						})
						continue
					} else {
						// Non-matching enum entry (lower-case enum, etc.); leave the block.
						inCodeEnum = false
						codeEnumIndent = -1
					}
				}
			} else if ind <= 2 {
				inSchemas = false
				currentSchemaName = ""
				schemaIndent = -1
				inCodeEnum = false
			}
			continue
		}
	}

	if len(operations) == 0 {
		return nil, nil, errors.New("openapi document declared no operations")
	}
	return operations, refs, nil
}

func parseGlobalSecurity(lines []string) []string {
	securityRowRe := regexp.MustCompile(`^-\s*([A-Za-z][A-Za-z0-9_-]*):\s*(?:\[\])?\s*$`)
	inSecurity := false
	var schemes []string
	for _, raw := range lines {
		raw = strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent == 0 {
			if trimmed == "paths:" {
				break
			}
			inSecurity = trimmed == "security:"
			continue
		}
		if !inSecurity || indent != 2 {
			continue
		}
		if match := securityRowRe.FindStringSubmatch(trimmed); len(match) == 2 {
			schemes = append(schemes, match[1])
		}
	}
	return schemes
}

// handlePathsLine drives the small state machine inside the `paths:` section.
// It is split out so parseOpenAPI stays readable.
func handlePathsLine(
	raw, trimmed string,
	ind int,
	currentPath *string,
	pathIndent *int,
	operations *[]OperationSummary,
	currentOp *int,
	opIndent *int,
	inResponses *bool,
	responsesIndent *int,
	inSecurity *bool,
	securityIndent *int,
	globalSecurity []string,
	httpMethods map[string]struct{},
	statusRowRe *regexp.Regexp,
	securityRowRe *regexp.Regexp,
) {
	// New path entry: indent 2 and ends in ":".
	if ind == 2 && strings.HasSuffix(trimmed, ":") {
		*currentPath = strings.TrimSuffix(trimmed, ":")
		*pathIndent = ind
		*currentOp = -1
		*opIndent = -1
		*inResponses = false
		*responsesIndent = -1
		*inSecurity = false
		*securityIndent = -1
		return
	}
	if *currentPath == "" {
		return
	}
	// Operation entry: indent 4 and method in trimmed.
	if ind == 4 && strings.HasSuffix(trimmed, ":") {
		method := strings.TrimSuffix(trimmed, ":")
		if _, ok := httpMethods[method]; ok {
			*operations = append(*operations, OperationSummary{
				Path:     *currentPath,
				Method:   method,
				Security: append([]string(nil), globalSecurity...),
			})
			*currentOp = len(*operations) - 1
			*opIndent = ind
			*inResponses = false
			*responsesIndent = -1
			*inSecurity = false
			*securityIndent = -1
			return
		}
	}
	if *currentOp < 0 {
		return
	}
	if ind == *opIndent+2 && trimmed == "security: []" {
		(*operations)[*currentOp].Security = nil
		*inSecurity = false
		*securityIndent = -1
		return
	}
	if ind == *opIndent+2 && trimmed == "security:" {
		(*operations)[*currentOp].Security = nil
		*inSecurity = true
		*securityIndent = ind
		return
	}
	if *inSecurity {
		if ind == *securityIndent+2 {
			if match := securityRowRe.FindStringSubmatch(trimmed); len(match) == 2 {
				(*operations)[*currentOp].Security = append(
					(*operations)[*currentOp].Security,
					match[1],
				)
				return
			}
		}
		if ind <= *securityIndent {
			*inSecurity = false
			*securityIndent = -1
		}
	}
	// operationId: line.
	if ind == *opIndent+2 && strings.HasPrefix(trimmed, "operationId:") {
		(*operations)[*currentOp].OperationID = strings.TrimSpace(strings.TrimPrefix(trimmed, "operationId:"))
		return
	}
	// responses: header.
	if ind == *opIndent+2 && trimmed == "responses:" {
		*inResponses = true
		*responsesIndent = ind
		return
	}
	if *inResponses {
		// Status entries are immediate children of `responses:` (indent +2).
		if ind == *responsesIndent+2 && statusRowRe.MatchString(trimmed) {
			code := strings.TrimSuffix(trimmed, ":")
			code = strings.Trim(code, "'\"")
			(*operations)[*currentOp].StatusCodes = append((*operations)[*currentOp].StatusCodes, code)
			return
		}
		// Once indent falls back to operation level, leave responses.
		if ind <= *responsesIndent {
			*inResponses = false
			*responsesIndent = -1
		}
	}
}
