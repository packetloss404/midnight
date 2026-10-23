package acme

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/timeline"
)

type IngressRouteReleaseEvidenceStatus string

const (
	IngressRouteReleaseEvidenceReady       IngressRouteReleaseEvidenceStatus = "ready"
	IngressRouteReleaseEvidenceNeedsReview IngressRouteReleaseEvidenceStatus = "needs-review"
	IngressRouteReleaseEvidenceBlocked     IngressRouteReleaseEvidenceStatus = "blocked"
)

type IngressRouteReleaseEvidenceRef struct {
	Kind   string                            `json:"kind"`
	Path   string                            `json:"path"`
	SHA256 string                            `json:"sha256,omitempty"`
	Status IngressRouteReleaseEvidenceStatus `json:"status,omitempty"`
}

type IngressRouteReleaseEvidenceIssue struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Count  int    `json:"count"`
}

type IngressRouteReleaseEvidenceIndexInput struct {
	RoutePublicationEvidenceRef IngressRouteReleaseEvidenceRef
	ACMEDiagnosticsRef          IngressRouteReleaseEvidenceRef
	IngressDiagnosticsReviewRef IngressRouteReleaseEvidenceRef
	DeploymentHeartbeatRef      IngressRouteReleaseEvidenceRef
	LogRefs                     []IngressRouteReleaseEvidenceRef
	TimelineRefs                []IngressRouteReleaseEvidenceRef
	RoutePublicationEvidence    RoutePublicationEvidencePacket
	ACMEDiagnostics             []DomainDiagnosticSummary
	IngressDiagnosticsReview    IngressDiagnosticsReviewSummary
	DeploymentHeartbeatReview   timeline.DeploymentHeartbeatGapReview
	NextActions                 []string
}

// IngressRouteReleaseEvidenceIndex is a support-safe manifest of release
// evidence artifacts. It stores relative references and status rollups only;
// referenced evidence owns any detailed diagnostics.
type IngressRouteReleaseEvidenceIndex struct {
	Status                   IngressRouteReleaseEvidenceStatus  `json:"status"`
	RoutePublicationEvidence IngressRouteReleaseEvidenceRef     `json:"routePublicationEvidence"`
	ACMEDiagnostics          IngressRouteReleaseEvidenceRef     `json:"acmeDiagnostics"`
	IngressDiagnosticsReview IngressRouteReleaseEvidenceRef     `json:"ingressDiagnosticsReview"`
	DeploymentHeartbeat      IngressRouteReleaseEvidenceRef     `json:"deploymentHeartbeat"`
	Logs                     []IngressRouteReleaseEvidenceRef   `json:"logs,omitempty"`
	Timeline                 []IngressRouteReleaseEvidenceRef   `json:"timeline,omitempty"`
	Issues                   []IngressRouteReleaseEvidenceIssue `json:"issues,omitempty"`
	NextActions              []string                           `json:"nextActions"`
}

func NewIngressRouteReleaseEvidenceIndex(input IngressRouteReleaseEvidenceIndexInput) IngressRouteReleaseEvidenceIndex {
	index := IngressRouteReleaseEvidenceIndex{
		RoutePublicationEvidence: normalizeIngressRouteReleaseEvidenceRef(input.RoutePublicationEvidenceRef, "route-publication-evidence", routePublicationReleaseStatus(input.RoutePublicationEvidence.Status)),
		ACMEDiagnostics:          normalizeIngressRouteReleaseEvidenceRef(input.ACMEDiagnosticsRef, "acme-diagnostics", acmeDiagnosticsReleaseStatus(input.ACMEDiagnostics)),
		IngressDiagnosticsReview: normalizeIngressRouteReleaseEvidenceRef(input.IngressDiagnosticsReviewRef, "ingress-diagnostics-review", ingressDiagnosticsReleaseStatus(input.IngressDiagnosticsReview.Status)),
		DeploymentHeartbeat:      normalizeIngressRouteReleaseEvidenceRef(input.DeploymentHeartbeatRef, "deployment-heartbeat-review", deploymentHeartbeatReleaseStatus(input.DeploymentHeartbeatReview)),
		Logs:                     normalizeIngressRouteReleaseEvidenceRefs(input.LogRefs),
		Timeline:                 normalizeIngressRouteReleaseEvidenceRefs(input.TimelineRefs),
	}

	issues := make(map[string]IngressRouteReleaseEvidenceIssue)
	for _, issue := range input.RoutePublicationEvidence.Issues {
		addIngressRouteReleaseEvidenceIssue(issues, "route-publication-evidence", issue.Code, issue.Count)
	}
	for _, summary := range input.ACMEDiagnostics {
		for _, action := range summary.Actions {
			if action != DomainDiagnosticActionMonitor {
				addIngressRouteReleaseEvidenceIssue(issues, "acme-diagnostics", string(action), 1)
			}
		}
	}
	for _, code := range input.DeploymentHeartbeatReview.IssueCodes {
		addIngressRouteReleaseEvidenceIssue(issues, "deployment-heartbeat-review", string(code), 1)
	}
	index.Issues = sortedIngressRouteReleaseEvidenceIssues(issues)

	statuses := []IngressRouteReleaseEvidenceStatus{
		index.RoutePublicationEvidence.Status,
		index.ACMEDiagnostics.Status,
		index.IngressDiagnosticsReview.Status,
		index.DeploymentHeartbeat.Status,
	}
	for _, ref := range index.Logs {
		statuses = append(statuses, ref.Status)
	}
	for _, ref := range index.Timeline {
		statuses = append(statuses, ref.Status)
	}
	index.Status = rollupIngressRouteReleaseEvidenceStatus(statuses)

	nextActions := make(map[string]struct{})
	for _, action := range input.NextActions {
		addIngressRouteReleaseEvidenceAction(nextActions, action)
	}
	for _, action := range input.RoutePublicationEvidence.NextActions {
		addIngressRouteReleaseEvidenceAction(nextActions, action)
	}
	for _, action := range input.IngressDiagnosticsReview.NextActions {
		addIngressRouteReleaseEvidenceAction(nextActions, action)
	}
	if index.Status == IngressRouteReleaseEvidenceReady {
		addIngressRouteReleaseEvidenceAction(nextActions, "release evidence index is ready")
	} else {
		addIngressRouteReleaseEvidenceAction(nextActions, "review referenced release evidence before publishing ingress routes")
	}
	index.NextActions = sortedIngressRouteReleaseEvidenceActions(nextActions)
	return index
}

func (index IngressRouteReleaseEvidenceIndex) Validate() error {
	if !validIngressRouteReleaseEvidenceStatus(index.Status) {
		return fmt.Errorf("release evidence index status %q is invalid", index.Status)
	}
	refs := []IngressRouteReleaseEvidenceRef{index.RoutePublicationEvidence, index.ACMEDiagnostics, index.IngressDiagnosticsReview, index.DeploymentHeartbeat}
	statuses := make([]IngressRouteReleaseEvidenceStatus, 0, 4+len(index.Logs)+len(index.Timeline))
	for _, ref := range refs {
		if err := validateIngressRouteReleaseEvidenceRef(ref, true); err != nil {
			return err
		}
		statuses = append(statuses, ref.Status)
	}
	if !ingressRouteReleaseEvidenceRefsSorted(index.Logs) || !ingressRouteReleaseEvidenceRefsSorted(index.Timeline) {
		return errors.New("release evidence log and timeline refs must be sorted")
	}
	for _, ref := range index.Logs {
		if err := validateIngressRouteReleaseEvidenceRef(ref, false); err != nil {
			return err
		}
		statuses = append(statuses, ref.Status)
	}
	for _, ref := range index.Timeline {
		if err := validateIngressRouteReleaseEvidenceRef(ref, false); err != nil {
			return err
		}
		statuses = append(statuses, ref.Status)
	}
	if want := rollupIngressRouteReleaseEvidenceStatus(statuses); index.Status != want {
		return fmt.Errorf("release evidence index status %q does not match rollup %q", index.Status, want)
	}
	if !ingressRouteReleaseEvidenceIssuesSorted(index.Issues) {
		return errors.New("release evidence issues must be sorted")
	}
	for _, issue := range index.Issues {
		if strings.TrimSpace(issue.Source) == "" || strings.TrimSpace(issue.Code) == "" || issue.Count <= 0 {
			return errors.New("release evidence issues require source, code, and positive count")
		}
	}
	if !sort.StringsAreSorted(index.NextActions) {
		return errors.New("release evidence next actions must be sorted")
	}
	return nil
}

func RenderIngressRouteReleaseEvidenceIndexJSON(index IngressRouteReleaseEvidenceIndex) ([]byte, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(index); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func WriteIngressRouteReleaseEvidenceIndexJSON(path string, index IngressRouteReleaseEvidenceIndex) error {
	if err := validateIngressRouteReleaseEvidenceIndexJSONPath(path); err != nil {
		return err
	}
	encoded, err := RenderIngressRouteReleaseEvidenceIndexJSON(index)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o640)
}

func ReadIngressRouteReleaseEvidenceIndexJSON(path string) (IngressRouteReleaseEvidenceIndex, error) {
	if err := validateIngressRouteReleaseEvidenceIndexJSONPath(path); err != nil {
		return IngressRouteReleaseEvidenceIndex{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return IngressRouteReleaseEvidenceIndex{}, err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var index IngressRouteReleaseEvidenceIndex
	if err := decoder.Decode(&index); err != nil {
		return IngressRouteReleaseEvidenceIndex{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return IngressRouteReleaseEvidenceIndex{}, errors.New("release evidence index JSON has trailing data")
		}
		return IngressRouteReleaseEvidenceIndex{}, err
	}
	if err := index.Validate(); err != nil {
		return IngressRouteReleaseEvidenceIndex{}, err
	}
	return index, nil
}

func VerifyIngressRouteReleaseEvidenceIndexFiles(root string, index IngressRouteReleaseEvidenceIndex) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("release evidence root is required")
	}
	if err := index.Validate(); err != nil {
		return err
	}
	refs := []IngressRouteReleaseEvidenceRef{index.RoutePublicationEvidence, index.ACMEDiagnostics, index.IngressDiagnosticsReview, index.DeploymentHeartbeat}
	refs = append(refs, index.Logs...)
	refs = append(refs, index.Timeline...)
	for _, ref := range refs {
		if err := verifyIngressRouteReleaseEvidenceRefFile(root, ref); err != nil {
			return err
		}
	}
	return nil
}

func normalizeIngressRouteReleaseEvidenceRef(ref IngressRouteReleaseEvidenceRef, kind string, status IngressRouteReleaseEvidenceStatus) IngressRouteReleaseEvidenceRef {
	ref.Kind = strings.TrimSpace(ref.Kind)
	if ref.Kind == "" {
		ref.Kind = kind
	}
	ref.Path = strings.TrimSpace(ref.Path)
	ref.SHA256 = strings.TrimSpace(ref.SHA256)
	if ref.Status == "" {
		ref.Status = status
	}
	return ref
}

func normalizeIngressRouteReleaseEvidenceRefs(refs []IngressRouteReleaseEvidenceRef) []IngressRouteReleaseEvidenceRef {
	normalized := make([]IngressRouteReleaseEvidenceRef, 0, len(refs))
	for _, ref := range refs {
		ref.Kind = strings.TrimSpace(ref.Kind)
		ref.Path = strings.TrimSpace(ref.Path)
		ref.SHA256 = strings.TrimSpace(ref.SHA256)
		normalized = append(normalized, ref)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Kind != normalized[j].Kind {
			return normalized[i].Kind < normalized[j].Kind
		}
		if normalized[i].Path != normalized[j].Path {
			return normalized[i].Path < normalized[j].Path
		}
		if normalized[i].SHA256 != normalized[j].SHA256 {
			return normalized[i].SHA256 < normalized[j].SHA256
		}
		return normalized[i].Status < normalized[j].Status
	})
	return normalized
}

func routePublicationReleaseStatus(status RoutePublicationEvidenceStatus) IngressRouteReleaseEvidenceStatus {
	switch status {
	case RoutePublicationEvidenceBlocked:
		return IngressRouteReleaseEvidenceBlocked
	case RoutePublicationEvidenceNeedsReview:
		return IngressRouteReleaseEvidenceNeedsReview
	default:
		return IngressRouteReleaseEvidenceReady
	}
}

func acmeDiagnosticsReleaseStatus(summaries []DomainDiagnosticSummary) IngressRouteReleaseEvidenceStatus {
	status := IngressRouteReleaseEvidenceReady
	for _, summary := range summaries {
		for _, action := range summary.Actions {
			switch action {
			case DomainDiagnosticActionFixCAA, DomainDiagnosticActionReplaceCertificate:
				return IngressRouteReleaseEvidenceBlocked
			case DomainDiagnosticActionMonitor:
			default:
				status = IngressRouteReleaseEvidenceNeedsReview
			}
		}
	}
	return status
}

func ingressDiagnosticsReleaseStatus(status IngressDiagnosticsReviewStatus) IngressRouteReleaseEvidenceStatus {
	switch status {
	case IngressDiagnosticsBlocked:
		return IngressRouteReleaseEvidenceBlocked
	case IngressDiagnosticsNeedsReview:
		return IngressRouteReleaseEvidenceNeedsReview
	default:
		return IngressRouteReleaseEvidenceReady
	}
}

func deploymentHeartbeatReleaseStatus(review timeline.DeploymentHeartbeatGapReview) IngressRouteReleaseEvidenceStatus {
	switch review.Status {
	case timeline.DeploymentHeartbeatGapStuck:
		return IngressRouteReleaseEvidenceBlocked
	case timeline.DeploymentHeartbeatGapWarning:
		return IngressRouteReleaseEvidenceNeedsReview
	default:
		if len(review.IssueCodes) > 0 {
			return IngressRouteReleaseEvidenceNeedsReview
		}
		return IngressRouteReleaseEvidenceReady
	}
}

func rollupIngressRouteReleaseEvidenceStatus(statuses []IngressRouteReleaseEvidenceStatus) IngressRouteReleaseEvidenceStatus {
	rolled := IngressRouteReleaseEvidenceReady
	for _, status := range statuses {
		switch status {
		case IngressRouteReleaseEvidenceBlocked:
			return IngressRouteReleaseEvidenceBlocked
		case IngressRouteReleaseEvidenceNeedsReview:
			rolled = IngressRouteReleaseEvidenceNeedsReview
		}
	}
	return rolled
}

func validateIngressRouteReleaseEvidenceRef(ref IngressRouteReleaseEvidenceRef, statusRequired bool) error {
	if strings.TrimSpace(ref.Kind) == "" {
		return errors.New("release evidence ref kind is required")
	}
	if err := validateIngressRouteReleaseEvidenceRelativePath(ref.Path); err != nil {
		return err
	}
	if err := validateIngressRouteReleaseEvidenceSHA256(ref.SHA256); err != nil {
		return err
	}
	if ref.Status == "" && !statusRequired {
		return nil
	}
	if !validIngressRouteReleaseEvidenceStatus(ref.Status) {
		return fmt.Errorf("release evidence ref status %q is invalid", ref.Status)
	}
	return nil
}

func validateIngressRouteReleaseEvidenceSHA256(value string) error {
	if value == "" {
		return nil
	}
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("release evidence ref sha256 %q must be 64 lowercase hex characters", value)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return fmt.Errorf("release evidence ref sha256 %q must be 64 lowercase hex characters", value)
	}
	return nil
}

func verifyIngressRouteReleaseEvidenceRefFile(root string, ref IngressRouteReleaseEvidenceRef) error {
	path := filepath.Join(root, filepath.FromSlash(ref.Path))
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("release evidence ref %q: %w", ref.Path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("release evidence ref %q must be a file", ref.Path)
	}
	if ref.SHA256 == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("release evidence ref %q: %w", ref.Path, err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("release evidence ref %q: %w", ref.Path, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != ref.SHA256 {
		return fmt.Errorf("release evidence ref %q sha256 mismatch: got %s want %s", ref.Path, got, ref.SHA256)
	}
	return nil
}

func validateIngressRouteReleaseEvidenceRelativePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("release evidence ref path is required")
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return fmt.Errorf("release evidence ref path %q must be relative", path)
	}
	if strings.Contains(path, "\\") {
		return fmt.Errorf("release evidence ref path %q must use slash separators", path)
	}
	cleaned := pathpkg.Clean(path)
	if cleaned == "." || cleaned != path {
		return fmt.Errorf("release evidence ref path %q is not normalized", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return fmt.Errorf("release evidence ref path %q must not traverse parents", path)
		}
	}
	return nil
}

func validateIngressRouteReleaseEvidenceIndexJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("release evidence index path is required")
	}
	if filepath.Ext(path) != ".json" {
		return errors.New("release evidence index path must end with .json")
	}
	return nil
}

func validIngressRouteReleaseEvidenceStatus(status IngressRouteReleaseEvidenceStatus) bool {
	switch status {
	case IngressRouteReleaseEvidenceReady, IngressRouteReleaseEvidenceNeedsReview, IngressRouteReleaseEvidenceBlocked:
		return true
	default:
		return false
	}
}

func addIngressRouteReleaseEvidenceIssue(issues map[string]IngressRouteReleaseEvidenceIssue, source, code string, count int) {
	if source == "" || code == "" || count <= 0 {
		return
	}
	key := source + "\x00" + code
	issue := issues[key]
	issue.Source = source
	issue.Code = code
	issue.Count += count
	issues[key] = issue
}

func sortedIngressRouteReleaseEvidenceIssues(issues map[string]IngressRouteReleaseEvidenceIssue) []IngressRouteReleaseEvidenceIssue {
	sorted := make([]IngressRouteReleaseEvidenceIssue, 0, len(issues))
	for _, issue := range issues {
		sorted = append(sorted, issue)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Source != sorted[j].Source {
			return sorted[i].Source < sorted[j].Source
		}
		return sorted[i].Code < sorted[j].Code
	})
	return sorted
}

func ingressRouteReleaseEvidenceIssuesSorted(issues []IngressRouteReleaseEvidenceIssue) bool {
	return sort.SliceIsSorted(issues, func(i, j int) bool {
		if issues[i].Source != issues[j].Source {
			return issues[i].Source < issues[j].Source
		}
		return issues[i].Code < issues[j].Code
	})
}

func ingressRouteReleaseEvidenceRefsSorted(refs []IngressRouteReleaseEvidenceRef) bool {
	return sort.SliceIsSorted(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		if refs[i].Path != refs[j].Path {
			return refs[i].Path < refs[j].Path
		}
		if refs[i].SHA256 != refs[j].SHA256 {
			return refs[i].SHA256 < refs[j].SHA256
		}
		return refs[i].Status < refs[j].Status
	})
}

func addIngressRouteReleaseEvidenceAction(actions map[string]struct{}, action string) {
	if action = strings.TrimSpace(action); action != "" {
		actions[action] = struct{}{}
	}
}

func sortedIngressRouteReleaseEvidenceActions(actions map[string]struct{}) []string {
	sorted := make([]string, 0, len(actions))
	for action := range actions {
		sorted = append(sorted, action)
	}
	sort.Strings(sorted)
	return sorted
}
