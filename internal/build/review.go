package build

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/registry"
)

type ReviewStatus string

const (
	ReviewStatusClean       ReviewStatus = "clean"
	ReviewStatusNeedsReview ReviewStatus = "needs-review"
	ReviewStatusBlocked     ReviewStatus = "blocked"
)

type BuildReviewInput struct {
	RefusedContextFiles    []string
	OutputSecretFindings   []BuildOutputSecretFinding
	ProvenanceArtifactRefs []ArtifactRef
}

type BuildReviewSummary struct {
	Status                 ReviewStatus               `json:"status"`
	RefusedContextFiles    []string                   `json:"refusedContextFiles,omitempty"`
	OutputSecretFindings   []BuildOutputSecretFinding `json:"outputSecretFindings,omitempty"`
	ProvenanceArtifactRefs []ArtifactRef              `json:"provenanceArtifactRefs,omitempty"`
}

type ReleaseArtifactIssueCode string

const (
	ReleaseArtifactIssueMissingProvenanceRef               ReleaseArtifactIssueCode = "missing-provenance-ref"
	ReleaseArtifactIssueMissingSBOMRef                     ReleaseArtifactIssueCode = "missing-sbom-ref"
	ReleaseArtifactIssueInvalidProvenanceRef               ReleaseArtifactIssueCode = "invalid-provenance-ref"
	ReleaseArtifactIssueInvalidSBOMRef                     ReleaseArtifactIssueCode = "invalid-sbom-ref"
	ReleaseArtifactIssueMissingSourceDigest                ReleaseArtifactIssueCode = "missing-source-digest"
	ReleaseArtifactIssueMissingImageDigest                 ReleaseArtifactIssueCode = "missing-image-digest"
	ReleaseArtifactIssueUntrustedBuildReviewRequired       ReleaseArtifactIssueCode = "untrusted-build-review-required"
	ReleaseArtifactIssueUnsupportedUntrustedClassification ReleaseArtifactIssueCode = "unsupported-untrusted-build-classification"
)

type ReleaseArtifactReviewInput struct {
	SourceDigest   string
	ImageDigests   []string
	ProvenanceRef  ArtifactRef
	SBOMRef        ArtifactRef
	UntrustedBuild UntrustedBuildClassification
}

type ReleaseArtifactReviewSummary struct {
	Status        ReviewStatus                 `json:"status"`
	SourceDigest  string                       `json:"sourceDigest,omitempty"`
	ImageDigests  []string                     `json:"imageDigests,omitempty"`
	ProvenanceRef ArtifactRef                  `json:"provenanceRef,omitempty"`
	SBOMRef       ArtifactRef                  `json:"sbomRef,omitempty"`
	Issues        []ReleaseArtifactReviewIssue `json:"issues,omitempty"`
}

type ReleaseArtifactReviewIssue struct {
	Code   ReleaseArtifactIssueCode `json:"code"`
	Detail string                   `json:"detail,omitempty"`
}

type BuildKitCacheIntegrityStatus string

const (
	BuildKitCacheIntegrityStatusClean       BuildKitCacheIntegrityStatus = "clean"
	BuildKitCacheIntegrityStatusNeedsReview BuildKitCacheIntegrityStatus = "needs-review"
	BuildKitCacheIntegrityStatusCorrupt     BuildKitCacheIntegrityStatus = "corrupt"
)

type BuildKitCacheIntegrityIssueCode string

const (
	BuildKitCacheIntegrityIssueVerificationFinding BuildKitCacheIntegrityIssueCode = "verification-finding"
	BuildKitCacheIntegrityIssueCorruptRecords      BuildKitCacheIntegrityIssueCode = "corrupt-records"
	BuildKitCacheIntegrityIssueCorruptBlobs        BuildKitCacheIntegrityIssueCode = "corrupt-blobs"
)

type BuildKitCacheIntegrityReviewInput struct {
	VerificationFindings []BuildKitCacheVerificationFinding
	CorruptRecords       int
	CorruptBlobs         int
}

type BuildKitCacheVerificationFinding struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

type BuildKitCacheIntegrityReviewSummary struct {
	Status               BuildKitCacheIntegrityStatus       `json:"status"`
	VerificationFindings []BuildKitCacheVerificationFinding `json:"verificationFindings,omitempty"`
	CorruptRecords       int                                `json:"corruptRecords,omitempty"`
	CorruptBlobs         int                                `json:"corruptBlobs,omitempty"`
	Issues               []BuildKitCacheIntegrityIssue      `json:"issues,omitempty"`
}

type BuildKitCacheIntegrityIssue struct {
	Code        BuildKitCacheIntegrityIssueCode `json:"code"`
	Detail      string                          `json:"detail,omitempty"`
	Remediation string                          `json:"remediation"`
}

type ReleaseEvidenceIssueSource string

const (
	ReleaseEvidenceIssueSourceContext          ReleaseEvidenceIssueSource = "context"
	ReleaseEvidenceIssueSourceReleaseArtifacts ReleaseEvidenceIssueSource = "release-artifacts"
	ReleaseEvidenceIssueSourceCacheIntegrity   ReleaseEvidenceIssueSource = "cache-integrity"
	ReleaseEvidenceIssueSourceRegistryGC       ReleaseEvidenceIssueSource = "registry-gc"
)

type ReleaseEvidencePacketInput struct {
	ContextReview         BuildReviewSummary
	ReleaseArtifactReview ReleaseArtifactReviewSummary
	CacheIntegrityReview  BuildKitCacheIntegrityReviewSummary
	RegistryGCReview      registry.GCManifestReviewSummary
}

type ReleaseEvidencePacket struct {
	Status                ReviewStatus                        `json:"status"`
	ContextReview         BuildReviewSummary                  `json:"contextReview"`
	ReleaseArtifactReview ReleaseArtifactReviewSummary        `json:"releaseArtifactReview"`
	CacheIntegrityReview  BuildKitCacheIntegrityReviewSummary `json:"cacheIntegrityReview"`
	RegistryGCReview      registry.GCManifestReviewSummary    `json:"registryGCReview"`
	Issues                []ReleaseEvidenceIssue              `json:"issues,omitempty"`
}

type ReleaseEvidenceIssue struct {
	Source ReleaseEvidenceIssueSource `json:"source"`
	Status ReviewStatus               `json:"status"`
	Code   string                     `json:"code"`
	Detail string                     `json:"detail,omitempty"`
}

func NewBuildReviewSummary(input BuildReviewInput) (BuildReviewSummary, error) {
	summary := BuildReviewSummary{
		RefusedContextFiles:    normalizedReviewPaths(input.RefusedContextFiles),
		OutputSecretFindings:   sortedBuildOutputSecretFindings(input.OutputSecretFindings),
		ProvenanceArtifactRefs: sortedArtifactRefs(input.ProvenanceArtifactRefs),
	}
	summary.Status = deriveBuildReviewStatus(summary)

	if err := summary.Validate(); err != nil {
		return BuildReviewSummary{}, err
	}
	return summary, nil
}

func (s BuildReviewSummary) Validate() error {
	if !validReviewStatus(s.Status) {
		return fmt.Errorf("build review status %q is invalid", s.Status)
	}
	if want := deriveBuildReviewStatus(s); s.Status != want {
		return fmt.Errorf("build review status %q does not match findings status %q", s.Status, want)
	}

	for _, file := range s.RefusedContextFiles {
		if file == "" || cleanContextPath(file) != file {
			return fmt.Errorf("build review refused context file %q is invalid", file)
		}
	}
	if !sort.StringsAreSorted(s.RefusedContextFiles) {
		return errors.New("build review refused context files must be sorted")
	}

	for _, finding := range s.OutputSecretFindings {
		if finding.Prefix == "" || finding.Line < 1 || finding.Column < 1 || finding.Redacted == "" {
			return fmt.Errorf("build review output secret finding is invalid: %#v", finding)
		}
	}
	if !buildOutputSecretFindingsSorted(s.OutputSecretFindings) {
		return errors.New("build review output secret findings must be sorted")
	}

	for _, ref := range s.ProvenanceArtifactRefs {
		if ref.Name == "" && ref.Digest == "" && ref.MediaType == "" {
			return errors.New("build review provenance artifact ref must not be empty")
		}
		if err := validateArtifactRef("provenance artifact", ref); err != nil {
			return err
		}
	}
	if !artifactRefsSorted(s.ProvenanceArtifactRefs) {
		return errors.New("build review provenance artifact refs must be sorted")
	}

	return nil
}

func MarshalBuildReviewSummaryJSON(summary BuildReviewSummary) ([]byte, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return marshalReviewJSON(summary)
}

func NewReleaseArtifactReviewSummary(input ReleaseArtifactReviewInput) ReleaseArtifactReviewSummary {
	summary := ReleaseArtifactReviewSummary{
		SourceDigest:  strings.TrimSpace(input.SourceDigest),
		ImageDigests:  normalizedDigestStrings(input.ImageDigests),
		ProvenanceRef: input.ProvenanceRef,
		SBOMRef:       input.SBOMRef,
	}
	summary.Issues = releaseArtifactReviewIssues(input, summary)
	summary.Status = deriveReleaseArtifactReviewStatus(summary.Issues)
	return summary
}

func (s ReleaseArtifactReviewSummary) Validate() error {
	if !validReviewStatus(s.Status) {
		return fmt.Errorf("release artifact review status %q is invalid", s.Status)
	}
	if !releaseArtifactIssuesSorted(s.Issues) {
		return errors.New("release artifact review issues must be sorted")
	}
	for _, issue := range s.Issues {
		if !validReleaseArtifactIssueCode(issue.Code) {
			return fmt.Errorf("release artifact review issue code %q is invalid", issue.Code)
		}
	}
	if want := deriveReleaseArtifactReviewStatus(s.Issues); s.Status != want {
		return fmt.Errorf("release artifact review status %q does not match issue status %q", s.Status, want)
	}
	return nil
}

func MarshalReleaseArtifactReviewSummaryJSON(summary ReleaseArtifactReviewSummary) ([]byte, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return marshalReviewJSON(summary)
}

func NewBuildKitCacheIntegrityReviewSummary(input BuildKitCacheIntegrityReviewInput) (BuildKitCacheIntegrityReviewSummary, error) {
	summary := BuildKitCacheIntegrityReviewSummary{
		VerificationFindings: sortedBuildKitCacheVerificationFindings(input.VerificationFindings),
		CorruptRecords:       input.CorruptRecords,
		CorruptBlobs:         input.CorruptBlobs,
	}
	summary.Issues = buildKitCacheIntegrityIssues(summary)
	summary.Status = deriveBuildKitCacheIntegrityStatus(summary)

	if err := summary.Validate(); err != nil {
		return BuildKitCacheIntegrityReviewSummary{}, err
	}
	return summary, nil
}

func (s BuildKitCacheIntegrityReviewSummary) Validate() error {
	if !validBuildKitCacheIntegrityStatus(s.Status) {
		return fmt.Errorf("BuildKit cache integrity status %q is invalid", s.Status)
	}
	if s.CorruptRecords < 0 || s.CorruptBlobs < 0 {
		return errors.New("BuildKit cache integrity corrupt counts must not be negative")
	}
	for _, finding := range s.VerificationFindings {
		if finding.Code == "" {
			return errors.New("BuildKit cache verification finding code must not be empty")
		}
	}
	if !buildKitCacheVerificationFindingsSorted(s.VerificationFindings) {
		return errors.New("BuildKit cache verification findings must be sorted")
	}
	if want := deriveBuildKitCacheIntegrityStatus(s); s.Status != want {
		return fmt.Errorf("BuildKit cache integrity status %q does not match issue status %q", s.Status, want)
	}
	for _, issue := range s.Issues {
		if !validBuildKitCacheIntegrityIssueCode(issue.Code) {
			return fmt.Errorf("BuildKit cache integrity issue code %q is invalid", issue.Code)
		}
		if issue.Remediation != buildKitCacheIntegrityRemediation(issue.Code) {
			return fmt.Errorf("BuildKit cache integrity remediation for issue code %q is invalid", issue.Code)
		}
	}
	if !buildKitCacheIntegrityIssuesSorted(s.Issues) {
		return errors.New("BuildKit cache integrity issues must be sorted")
	}
	if want := buildKitCacheIntegrityIssues(s); !sameBuildKitCacheIntegrityIssues(s.Issues, want) {
		return errors.New("BuildKit cache integrity issues do not match findings and corrupt counts")
	}
	return nil
}

func MarshalBuildKitCacheIntegrityReviewSummaryJSON(summary BuildKitCacheIntegrityReviewSummary) ([]byte, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return marshalReviewJSON(summary)
}

func NewReleaseEvidencePacket(input ReleaseEvidencePacketInput) (ReleaseEvidencePacket, error) {
	packet := ReleaseEvidencePacket{
		ContextReview:         input.ContextReview,
		ReleaseArtifactReview: input.ReleaseArtifactReview,
		CacheIntegrityReview:  input.CacheIntegrityReview,
		RegistryGCReview:      input.RegistryGCReview,
	}
	packet.Issues = releaseEvidenceIssues(packet)
	packet.Status = deriveReleaseEvidenceStatus(packet.Issues)

	if err := packet.Validate(); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	return packet, nil
}

func (p ReleaseEvidencePacket) Validate() error {
	if !validReviewStatus(p.Status) {
		return fmt.Errorf("release evidence packet status %q is invalid", p.Status)
	}
	if err := p.ContextReview.Validate(); err != nil {
		return fmt.Errorf("release evidence context review: %w", err)
	}
	if err := p.ReleaseArtifactReview.Validate(); err != nil {
		return fmt.Errorf("release evidence artifact review: %w", err)
	}
	if err := p.CacheIntegrityReview.Validate(); err != nil {
		return fmt.Errorf("release evidence cache integrity review: %w", err)
	}
	if err := p.RegistryGCReview.Validate(); err != nil {
		return fmt.Errorf("release evidence registry GC review: %w", err)
	}
	for _, issue := range p.Issues {
		if !validReleaseEvidenceIssueSource(issue.Source) {
			return fmt.Errorf("release evidence issue source %q is invalid", issue.Source)
		}
		if !validReviewStatus(issue.Status) {
			return fmt.Errorf("release evidence issue status %q is invalid", issue.Status)
		}
		if issue.Code == "" {
			return errors.New("release evidence issue code must not be empty")
		}
	}
	if !releaseEvidenceIssuesSorted(p.Issues) {
		return errors.New("release evidence issues must be sorted")
	}
	if want := releaseEvidenceIssues(p); !sameReleaseEvidenceIssues(p.Issues, want) {
		return errors.New("release evidence issues do not match review summaries")
	}
	if want := deriveReleaseEvidenceStatus(p.Issues); p.Status != want {
		return fmt.Errorf("release evidence packet status %q does not match issue status %q", p.Status, want)
	}
	return nil
}

func MarshalReleaseEvidencePacketJSON(packet ReleaseEvidencePacket) ([]byte, error) {
	if err := packet.Validate(); err != nil {
		return nil, err
	}
	return marshalReviewJSON(packet)
}

// WriteReleaseEvidencePacketJSONFile validates path and packet before writing
// deterministic release evidence JSON. It only checks the explicit parent path.
func WriteReleaseEvidencePacketJSONFile(path string, packet ReleaseEvidencePacket) error {
	if err := validateReleaseEvidenceJSONPath(path, osPathExists); err != nil {
		return err
	}
	data, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadReleaseEvidencePacketJSONFile validates path, decodes JSON, and validates
// the release evidence packet before returning it.
func ReadReleaseEvidencePacketJSONFile(path string) (ReleaseEvidencePacket, error) {
	if err := validateReleaseEvidenceJSONPath(path, osPathExists); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	packet, err := unmarshalReleaseEvidencePacketJSON(data)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	return packet, nil
}

func unmarshalReleaseEvidencePacketJSON(data []byte) (ReleaseEvidencePacket, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var packet ReleaseEvidencePacket
	if err := decoder.Decode(&packet); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("release evidence JSON must contain a single object")
		}
		return ReleaseEvidencePacket{}, err
	}
	if err := packet.Validate(); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	return packet, nil
}

func validateReleaseEvidenceJSONPath(path string, exists func(string) (bool, error)) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("release evidence path must not be empty")
	}
	cleaned := filepath.Clean(path)
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) || filepath.Ext(base) != ".json" {
		return fmt.Errorf("release evidence path %q must be an explicit .json file path", path)
	}
	parent := filepath.Dir(cleaned)
	ok, err := exists(parent)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("release evidence path parent %q does not exist", parent)
	}
	return nil
}

func osPathExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir(), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func marshalReviewJSON(value interface{}) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func deriveBuildReviewStatus(summary BuildReviewSummary) ReviewStatus {
	if len(summary.RefusedContextFiles) > 0 || len(summary.OutputSecretFindings) > 0 {
		return ReviewStatusBlocked
	}
	if len(summary.ProvenanceArtifactRefs) > 0 {
		return ReviewStatusNeedsReview
	}
	return ReviewStatusClean
}

func validReviewStatus(status ReviewStatus) bool {
	switch status {
	case ReviewStatusClean, ReviewStatusNeedsReview, ReviewStatusBlocked:
		return true
	default:
		return false
	}
}

func deriveReleaseEvidenceStatus(issues []ReleaseEvidenceIssue) ReviewStatus {
	status := ReviewStatusClean
	for _, issue := range issues {
		if issue.Status == ReviewStatusBlocked {
			return ReviewStatusBlocked
		}
		if issue.Status == ReviewStatusNeedsReview {
			status = ReviewStatusNeedsReview
		}
	}
	return status
}

func validReleaseEvidenceIssueSource(source ReleaseEvidenceIssueSource) bool {
	switch source {
	case ReleaseEvidenceIssueSourceContext,
		ReleaseEvidenceIssueSourceReleaseArtifacts,
		ReleaseEvidenceIssueSourceCacheIntegrity,
		ReleaseEvidenceIssueSourceRegistryGC:
		return true
	default:
		return false
	}
}

func releaseEvidenceIssues(packet ReleaseEvidencePacket) []ReleaseEvidenceIssue {
	issues := make([]ReleaseEvidenceIssue, 0)
	for _, file := range packet.ContextReview.RefusedContextFiles {
		issues = append(issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceContext, Status: ReviewStatusBlocked, Code: "refused-context-file", Detail: file})
	}
	for _, finding := range packet.ContextReview.OutputSecretFindings {
		issues = append(issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceContext, Status: ReviewStatusBlocked, Code: "output-secret-finding", Detail: buildOutputSecretFindingDetail(finding)})
	}
	for _, ref := range packet.ContextReview.ProvenanceArtifactRefs {
		issues = append(issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceContext, Status: ReviewStatusNeedsReview, Code: "provenance-artifact-ref", Detail: artifactRefDetail(ref)})
	}
	for _, issue := range packet.ReleaseArtifactReview.Issues {
		status := ReviewStatusNeedsReview
		if releaseArtifactIssueBlocks(issue.Code) {
			status = ReviewStatusBlocked
		}
		issues = append(issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceReleaseArtifacts, Status: status, Code: string(issue.Code), Detail: issue.Detail})
	}
	for _, issue := range packet.CacheIntegrityReview.Issues {
		status := ReviewStatusNeedsReview
		if issue.Code == BuildKitCacheIntegrityIssueCorruptRecords || issue.Code == BuildKitCacheIntegrityIssueCorruptBlobs {
			status = ReviewStatusBlocked
		}
		issues = append(issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceCacheIntegrity, Status: status, Code: string(issue.Code), Detail: issue.Detail})
	}
	for _, validationError := range packet.RegistryGCReview.ValidationErrors {
		issues = append(issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceRegistryGC, Status: ReviewStatusBlocked, Code: "validation-error", Detail: validationError})
	}
	if packet.RegistryGCReview.Deletions > 0 {
		issues = append(issues, ReleaseEvidenceIssue{Source: ReleaseEvidenceIssueSourceRegistryGC, Status: ReviewStatusNeedsReview, Code: "deletion-candidates", Detail: fmt.Sprintf("%d", packet.RegistryGCReview.Deletions)})
	}
	sortReleaseEvidenceIssues(issues)
	return issues
}

func buildOutputSecretFindingDetail(finding BuildOutputSecretFinding) string {
	return fmt.Sprintf("%d:%d %s", finding.Line, finding.Column, finding.Prefix)
}

func artifactRefDetail(ref ArtifactRef) string {
	parts := make([]string, 0, 3)
	if ref.Name != "" {
		parts = append(parts, ref.Name)
	}
	if ref.Digest != "" {
		parts = append(parts, ref.Digest)
	}
	if ref.MediaType != "" {
		parts = append(parts, ref.MediaType)
	}
	return strings.Join(parts, " ")
}

func sortReleaseEvidenceIssues(issues []ReleaseEvidenceIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return releaseEvidenceIssueLess(issues[i], issues[j])
	})
}

func releaseEvidenceIssuesSorted(issues []ReleaseEvidenceIssue) bool {
	return sort.SliceIsSorted(issues, func(i, j int) bool {
		return releaseEvidenceIssueLess(issues[i], issues[j])
	})
}

func releaseEvidenceIssueLess(left, right ReleaseEvidenceIssue) bool {
	if left.Source != right.Source {
		return releaseEvidenceIssueSourceRank(left.Source) < releaseEvidenceIssueSourceRank(right.Source)
	}
	if left.Status != right.Status {
		return left.Status < right.Status
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Detail < right.Detail
}

func releaseEvidenceIssueSourceRank(source ReleaseEvidenceIssueSource) int {
	switch source {
	case ReleaseEvidenceIssueSourceContext:
		return 0
	case ReleaseEvidenceIssueSourceReleaseArtifacts:
		return 1
	case ReleaseEvidenceIssueSourceCacheIntegrity:
		return 2
	case ReleaseEvidenceIssueSourceRegistryGC:
		return 3
	default:
		return 4
	}
}

func sameReleaseEvidenceIssues(left, right []ReleaseEvidenceIssue) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func normalizedReviewPaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	for _, file := range paths {
		file = cleanContextPath(file)
		if file == "" {
			continue
		}
		normalized = append(normalized, file)
	}
	sort.Strings(normalized)
	return normalized
}

func sortedBuildOutputSecretFindings(findings []BuildOutputSecretFinding) []BuildOutputSecretFinding {
	sorted := append([]BuildOutputSecretFinding(nil), findings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return buildOutputSecretFindingLess(sorted[i], sorted[j])
	})
	return sorted
}

func buildOutputSecretFindingsSorted(findings []BuildOutputSecretFinding) bool {
	return sort.SliceIsSorted(findings, func(i, j int) bool {
		return buildOutputSecretFindingLess(findings[i], findings[j])
	})
}

func buildOutputSecretFindingLess(left, right BuildOutputSecretFinding) bool {
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	if left.Column != right.Column {
		return left.Column < right.Column
	}
	if left.Prefix != right.Prefix {
		return left.Prefix < right.Prefix
	}
	return left.Redacted < right.Redacted
}

func sortedArtifactRefs(refs []ArtifactRef) []ArtifactRef {
	sorted := append([]ArtifactRef(nil), refs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return artifactRefLess(sorted[i], sorted[j])
	})
	return sorted
}

func artifactRefsSorted(refs []ArtifactRef) bool {
	return sort.SliceIsSorted(refs, func(i, j int) bool {
		return artifactRefLess(refs[i], refs[j])
	})
}

func artifactRefLess(left, right ArtifactRef) bool {
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.Digest != right.Digest {
		return left.Digest < right.Digest
	}
	return strings.Compare(left.MediaType, right.MediaType) < 0
}

func releaseArtifactReviewIssues(input ReleaseArtifactReviewInput, summary ReleaseArtifactReviewSummary) []ReleaseArtifactReviewIssue {
	issues := make([]ReleaseArtifactReviewIssue, 0)
	if summary.SourceDigest == "" {
		issues = append(issues, ReleaseArtifactReviewIssue{Code: ReleaseArtifactIssueMissingSourceDigest})
	}
	for i, digest := range input.ImageDigests {
		if strings.TrimSpace(digest) == "" {
			issues = append(issues, ReleaseArtifactReviewIssue{Code: ReleaseArtifactIssueMissingImageDigest, Detail: fmt.Sprintf("imageDigests[%d]", i)})
		}
	}
	if len(input.ImageDigests) == 0 {
		issues = append(issues, ReleaseArtifactReviewIssue{Code: ReleaseArtifactIssueMissingImageDigest})
	}
	issues = appendArtifactRefIssue(issues, "provenance", summary.ProvenanceRef, ReleaseArtifactIssueMissingProvenanceRef, ReleaseArtifactIssueInvalidProvenanceRef)
	issues = appendArtifactRefIssue(issues, "sbom", summary.SBOMRef, ReleaseArtifactIssueMissingSBOMRef, ReleaseArtifactIssueInvalidSBOMRef)

	switch input.UntrustedBuild {
	case "", UntrustedBuildTrusted:
	case UntrustedBuildUntrusted, UntrustedBuildIsolated:
		issues = append(issues, ReleaseArtifactReviewIssue{Code: ReleaseArtifactIssueUntrustedBuildReviewRequired, Detail: string(input.UntrustedBuild)})
	default:
		issues = append(issues, ReleaseArtifactReviewIssue{Code: ReleaseArtifactIssueUnsupportedUntrustedClassification, Detail: string(input.UntrustedBuild)})
	}
	sortReleaseArtifactIssues(issues)
	return issues
}

func appendArtifactRefIssue(issues []ReleaseArtifactReviewIssue, name string, ref ArtifactRef, missingCode, invalidCode ReleaseArtifactIssueCode) []ReleaseArtifactReviewIssue {
	if ref.Name == "" && ref.Digest == "" && ref.MediaType == "" {
		return append(issues, ReleaseArtifactReviewIssue{Code: missingCode})
	}
	if ref.Name == "" || ref.Digest == "" {
		return append(issues, ReleaseArtifactReviewIssue{Code: missingCode})
	}
	if err := validateArtifactRef(name, ref); err != nil {
		return append(issues, ReleaseArtifactReviewIssue{Code: invalidCode, Detail: err.Error()})
	}
	return issues
}

func deriveReleaseArtifactReviewStatus(issues []ReleaseArtifactReviewIssue) ReviewStatus {
	for _, issue := range issues {
		if releaseArtifactIssueBlocks(issue.Code) {
			return ReviewStatusBlocked
		}
	}
	if len(issues) > 0 {
		return ReviewStatusNeedsReview
	}
	return ReviewStatusClean
}

func releaseArtifactIssueBlocks(code ReleaseArtifactIssueCode) bool {
	switch code {
	case ReleaseArtifactIssueUntrustedBuildReviewRequired:
		return false
	default:
		return true
	}
}

func validReleaseArtifactIssueCode(code ReleaseArtifactIssueCode) bool {
	switch code {
	case ReleaseArtifactIssueMissingProvenanceRef,
		ReleaseArtifactIssueMissingSBOMRef,
		ReleaseArtifactIssueInvalidProvenanceRef,
		ReleaseArtifactIssueInvalidSBOMRef,
		ReleaseArtifactIssueMissingSourceDigest,
		ReleaseArtifactIssueMissingImageDigest,
		ReleaseArtifactIssueUntrustedBuildReviewRequired,
		ReleaseArtifactIssueUnsupportedUntrustedClassification:
		return true
	default:
		return false
	}
}

func deriveBuildKitCacheIntegrityStatus(summary BuildKitCacheIntegrityReviewSummary) BuildKitCacheIntegrityStatus {
	if summary.CorruptRecords > 0 || summary.CorruptBlobs > 0 {
		return BuildKitCacheIntegrityStatusCorrupt
	}
	if len(summary.VerificationFindings) > 0 {
		return BuildKitCacheIntegrityStatusNeedsReview
	}
	return BuildKitCacheIntegrityStatusClean
}

func validBuildKitCacheIntegrityStatus(status BuildKitCacheIntegrityStatus) bool {
	switch status {
	case BuildKitCacheIntegrityStatusClean, BuildKitCacheIntegrityStatusNeedsReview, BuildKitCacheIntegrityStatusCorrupt:
		return true
	default:
		return false
	}
}

func buildKitCacheIntegrityIssues(summary BuildKitCacheIntegrityReviewSummary) []BuildKitCacheIntegrityIssue {
	issues := make([]BuildKitCacheIntegrityIssue, 0, len(summary.VerificationFindings)+2)
	for _, finding := range summary.VerificationFindings {
		issues = append(issues, BuildKitCacheIntegrityIssue{
			Code:        BuildKitCacheIntegrityIssueVerificationFinding,
			Detail:      buildKitCacheVerificationFindingDetail(finding),
			Remediation: buildKitCacheIntegrityRemediation(BuildKitCacheIntegrityIssueVerificationFinding),
		})
	}
	if summary.CorruptRecords > 0 {
		issues = append(issues, BuildKitCacheIntegrityIssue{
			Code:        BuildKitCacheIntegrityIssueCorruptRecords,
			Detail:      fmt.Sprintf("%d", summary.CorruptRecords),
			Remediation: buildKitCacheIntegrityRemediation(BuildKitCacheIntegrityIssueCorruptRecords),
		})
	}
	if summary.CorruptBlobs > 0 {
		issues = append(issues, BuildKitCacheIntegrityIssue{
			Code:        BuildKitCacheIntegrityIssueCorruptBlobs,
			Detail:      fmt.Sprintf("%d", summary.CorruptBlobs),
			Remediation: buildKitCacheIntegrityRemediation(BuildKitCacheIntegrityIssueCorruptBlobs),
		})
	}
	sortBuildKitCacheIntegrityIssues(issues)
	return issues
}

func buildKitCacheVerificationFindingDetail(finding BuildKitCacheVerificationFinding) string {
	if finding.Detail == "" {
		return finding.Code
	}
	return finding.Code + ": " + finding.Detail
}

func buildKitCacheIntegrityRemediation(code BuildKitCacheIntegrityIssueCode) string {
	switch code {
	case BuildKitCacheIntegrityIssueVerificationFinding:
		return "Review BuildKit cache verification output and rebuild affected cache entries before promotion."
	case BuildKitCacheIntegrityIssueCorruptRecords:
		return "Prune corrupt BuildKit cache records and rebuild cache from trusted inputs."
	case BuildKitCacheIntegrityIssueCorruptBlobs:
		return "Remove corrupt BuildKit cache blobs and repopulate cache from a trusted build."
	default:
		return ""
	}
}

func validBuildKitCacheIntegrityIssueCode(code BuildKitCacheIntegrityIssueCode) bool {
	switch code {
	case BuildKitCacheIntegrityIssueVerificationFinding,
		BuildKitCacheIntegrityIssueCorruptRecords,
		BuildKitCacheIntegrityIssueCorruptBlobs:
		return true
	default:
		return false
	}
}

func sortedBuildKitCacheVerificationFindings(findings []BuildKitCacheVerificationFinding) []BuildKitCacheVerificationFinding {
	sorted := make([]BuildKitCacheVerificationFinding, 0, len(findings))
	for _, finding := range findings {
		finding.Code = strings.TrimSpace(finding.Code)
		finding.Detail = strings.TrimSpace(finding.Detail)
		if finding.Code == "" {
			continue
		}
		sorted = append(sorted, finding)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return buildKitCacheVerificationFindingLess(sorted[i], sorted[j])
	})
	return sorted
}

func buildKitCacheVerificationFindingsSorted(findings []BuildKitCacheVerificationFinding) bool {
	return sort.SliceIsSorted(findings, func(i, j int) bool {
		return buildKitCacheVerificationFindingLess(findings[i], findings[j])
	})
}

func buildKitCacheVerificationFindingLess(left, right BuildKitCacheVerificationFinding) bool {
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Detail < right.Detail
}

func sortBuildKitCacheIntegrityIssues(issues []BuildKitCacheIntegrityIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return buildKitCacheIntegrityIssueLess(issues[i], issues[j])
	})
}

func buildKitCacheIntegrityIssuesSorted(issues []BuildKitCacheIntegrityIssue) bool {
	return sort.SliceIsSorted(issues, func(i, j int) bool {
		return buildKitCacheIntegrityIssueLess(issues[i], issues[j])
	})
}

func buildKitCacheIntegrityIssueLess(left, right BuildKitCacheIntegrityIssue) bool {
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	if left.Detail != right.Detail {
		return left.Detail < right.Detail
	}
	return left.Remediation < right.Remediation
}

func sameBuildKitCacheIntegrityIssues(left, right []BuildKitCacheIntegrityIssue) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func normalizedDigestStrings(digests []string) []string {
	normalized := make([]string, 0, len(digests))
	for _, digest := range digests {
		digest = strings.TrimSpace(digest)
		if digest != "" {
			normalized = append(normalized, digest)
		}
	}
	sort.Strings(normalized)
	return normalized
}

func sortReleaseArtifactIssues(issues []ReleaseArtifactReviewIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return releaseArtifactIssueLess(issues[i], issues[j])
	})
}

func releaseArtifactIssuesSorted(issues []ReleaseArtifactReviewIssue) bool {
	return sort.SliceIsSorted(issues, func(i, j int) bool {
		return releaseArtifactIssueLess(issues[i], issues[j])
	})
}

func releaseArtifactIssueLess(left, right ReleaseArtifactReviewIssue) bool {
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Detail < right.Detail
}
