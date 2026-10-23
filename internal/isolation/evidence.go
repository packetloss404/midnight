package isolation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const BuildIsolationEvidenceSchemaVersion = "midnight.dev/build-isolation-evidence-plan/v1alpha1"

const (
	EvidenceKindProvenance         = "provenance"
	EvidenceKindSBOM               = "sbom"
	EvidenceKindReleasePacket      = "release-packet"
	EvidenceKindRegistryGCManifest = "gc-manifest"
)

type EvidenceStatus string

const (
	EvidenceStatusReady       EvidenceStatus = "ready"
	EvidenceStatusNeedsReview EvidenceStatus = "needs-review"
	EvidenceStatusBlocked     EvidenceStatus = "blocked"
)

type EvidenceIssue struct {
	Field    string `json:"field"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type EvidenceRef struct {
	Kind   string         `json:"kind"`
	Path   string         `json:"path"`
	SHA256 string         `json:"sha256"`
	Status EvidenceStatus `json:"status"`
}

type BuildEvidenceRequest struct {
	Build              BuildRequest  `json:"build"`
	EvidenceRefs       []EvidenceRef `json:"evidence_refs,omitempty"`
	SecretFingerprints []string      `json:"secret_fingerprints,omitempty"`
}

type BuildEvidencePlan struct {
	SchemaVersion      string          `json:"schema_version"`
	Status             EvidenceStatus  `json:"status"`
	Build              BuildPlan       `json:"build"`
	EvidenceRefs       []EvidenceRef   `json:"evidence_refs"`
	SecretFingerprints []string        `json:"secret_fingerprints,omitempty"`
	OfflineGuarantees  []string        `json:"offline_guarantees"`
	Issues             []EvidenceIssue `json:"issues,omitempty"`
}

var (
	evidencePathPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,255}$`)
	evidenceSHA256Pattern    = regexp.MustCompile(`(?i)^(sha256:)?[a-f0-9]{64}$`)
	secretFingerprintPattern = regexp.MustCompile(`(?i)^sha256:[a-f0-9]{64}$`)
)

func PlanBuildEvidence(request BuildEvidenceRequest) (BuildEvidencePlan, error) {
	buildPlan, err := PlanBuild(request.Build)
	if err != nil {
		return BuildEvidencePlan{}, err
	}
	if err := buildPlan.Validate(); err != nil {
		return BuildEvidencePlan{}, err
	}

	evidenceRefs, err := normalizeEvidenceRefs(request.EvidenceRefs)
	if err != nil {
		return BuildEvidencePlan{}, err
	}
	secretFingerprints, err := normalizeSecretFingerprints(request.SecretFingerprints)
	if err != nil {
		return BuildEvidencePlan{}, err
	}
	issues := buildEvidenceIssues(buildPlan.Risk, evidenceRefs)
	return BuildEvidencePlan{
		SchemaVersion:      BuildIsolationEvidenceSchemaVersion,
		Status:             buildEvidenceStatus(issues),
		Build:              buildPlan,
		EvidenceRefs:       evidenceRefs,
		SecretFingerprints: secretFingerprints,
		OfflineGuarantees:  BuildEvidenceOfflineGuarantees(),
		Issues:             issues,
	}, nil
}

func ValidateBuildEvidencePlan(plan BuildEvidencePlan) error {
	if plan.SchemaVersion != BuildIsolationEvidenceSchemaVersion {
		return fmt.Errorf("schema_version = %q, want %q", plan.SchemaVersion, BuildIsolationEvidenceSchemaVersion)
	}
	if !validEvidenceStatus(plan.Status) {
		return fmt.Errorf("evidence status %q is not supported", plan.Status)
	}
	if err := plan.Build.Validate(); err != nil {
		return err
	}
	if _, err := normalizeEvidenceRefs(plan.EvidenceRefs); err != nil {
		return err
	}
	if _, err := normalizeSecretFingerprints(plan.SecretFingerprints); err != nil {
		return err
	}
	return nil
}

func BuildEvidenceOfflineGuarantees() []string {
	return []string{
		"no BuildKit worker launch",
		"no container, gVisor, Kata, or microVM launch",
		"no registry pull, build context fetch, network call, or filesystem evidence read",
		"no secret plaintext persistence or runtime mutation",
	}
}

func normalizeEvidenceRefs(refs []EvidenceRef) ([]EvidenceRef, error) {
	out := make([]EvidenceRef, 0, len(refs))
	for i, ref := range refs {
		kind := strings.TrimSpace(ref.Kind)
		if !validEvidenceKind(kind) {
			return nil, fmt.Errorf("evidence_refs[%d].kind %q is not supported", i, ref.Kind)
		}
		path := strings.TrimSpace(ref.Path)
		if !safeEvidencePath(path) {
			return nil, fmt.Errorf("evidence_refs[%d].path %q is not a safe relative evidence path", i, ref.Path)
		}
		sha := normalizeSHA256(ref.SHA256)
		if sha == "" {
			return nil, fmt.Errorf("evidence_refs[%d].sha256 must be a SHA-256 checksum", i)
		}
		status := ref.Status
		if status == "" {
			status = EvidenceStatusReady
		}
		if !validEvidenceStatus(status) {
			return nil, fmt.Errorf("evidence_refs[%d].status %q is not supported", i, ref.Status)
		}
		out = append(out, EvidenceRef{Kind: kind, Path: path, SHA256: sha, Status: status})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func normalizeSecretFingerprints(fingerprints []string) ([]string, error) {
	out := make([]string, 0, len(fingerprints))
	for i, fingerprint := range fingerprints {
		trimmed := strings.ToLower(strings.TrimSpace(fingerprint))
		if !secretFingerprintPattern.MatchString(trimmed) {
			return nil, fmt.Errorf("secret_fingerprints[%d] must be a sha256 fingerprint, not secret material", i)
		}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out, nil
}

func buildEvidenceIssues(risk RiskTier, refs []EvidenceRef) []EvidenceIssue {
	issues := make([]EvidenceIssue, 0)
	seen := map[string]bool{}
	for _, ref := range refs {
		seen[ref.Kind] = true
		switch ref.Status {
		case EvidenceStatusBlocked:
			issues = append(issues, EvidenceIssue{Field: "evidence_refs." + ref.Kind, Severity: "fatal", Message: "evidence artifact is blocked"})
		case EvidenceStatusNeedsReview:
			issues = append(issues, EvidenceIssue{Field: "evidence_refs." + ref.Kind, Severity: "warn", Message: "evidence artifact requires review"})
		}
	}
	if risk == RiskUntrusted || risk == RiskIsolated {
		for _, kind := range []string{EvidenceKindProvenance, EvidenceKindSBOM} {
			if !seen[kind] {
				issues = append(issues, EvidenceIssue{Field: "evidence_refs." + kind, Severity: "warn", Message: "untrusted and isolated builds require provenance and SBOM evidence references"})
			}
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Field != issues[j].Field {
			return issues[i].Field < issues[j].Field
		}
		return issues[i].Message < issues[j].Message
	})
	return issues
}

func buildEvidenceStatus(issues []EvidenceIssue) EvidenceStatus {
	status := EvidenceStatusReady
	for _, issue := range issues {
		if issue.Severity == "fatal" {
			return EvidenceStatusBlocked
		}
		status = EvidenceStatusNeedsReview
	}
	return status
}

func validEvidenceKind(kind string) bool {
	switch kind {
	case EvidenceKindProvenance, EvidenceKindSBOM, EvidenceKindReleasePacket, EvidenceKindRegistryGCManifest:
		return true
	default:
		return false
	}
}

func validEvidenceStatus(status EvidenceStatus) bool {
	switch status {
	case EvidenceStatusReady, EvidenceStatusNeedsReview, EvidenceStatusBlocked:
		return true
	default:
		return false
	}
}

func safeEvidencePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || strings.Contains(path, ":") || !evidencePathPattern.MatchString(path) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func normalizeSHA256(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if !evidenceSHA256Pattern.MatchString(trimmed) {
		return ""
	}
	return trimmed
}
