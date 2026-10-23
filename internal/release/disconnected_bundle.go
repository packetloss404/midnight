package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const DisconnectedBundlePlanSchemaVersion = "midnight.dev/disconnected-install-bundle-plan/v1alpha1"

type BundleStatus string

const (
	BundleStatusReady       BundleStatus = "ready"
	BundleStatusNeedsReview BundleStatus = "needs-review"
	BundleStatusBlocked     BundleStatus = "blocked"
)

type BundleIssue struct {
	Field    string `json:"field"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type DisconnectedBundlePlan struct {
	SchemaVersion string          `json:"schema_version"`
	Status        BundleStatus    `json:"status"`
	Release       string          `json:"release"`
	GeneratedAt   string          `json:"generated_at"`
	APT           APTMirror       `json:"apt"`
	OCI           OCIMirror       `json:"oci"`
	Docs          DocsBundle      `json:"docs"`
	Vulnerability VulnerabilityDB `json:"vulnerability"`
	Signatures    SignatureSet    `json:"signatures"`
	Issues        []BundleIssue   `json:"issues,omitempty"`
}

type APTMirror struct {
	RepositoryURL  string   `json:"repository_url"`
	Suites         []string `json:"suites"`
	KeyFingerprint string   `json:"key_fingerprint"`
}

type OCIMirror struct {
	Registry   string   `json:"registry"`
	ImageRefs  []string `json:"image_refs"`
	DigestOnly bool     `json:"digest_only"`
}

type DocsBundle struct {
	Included bool     `json:"included"`
	Paths    []string `json:"paths"`
}

type VulnerabilityDB struct {
	Included  bool   `json:"included"`
	UpdatedAt string `json:"updated_at"`
}

type SignatureSet struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	CosignBundle   string `json:"cosign_bundle"`
	GPGDetachedSig string `json:"gpg_detached_sig"`
}

func NewDisconnectedBundlePlan(plan DisconnectedBundlePlan) DisconnectedBundlePlan {
	plan.SchemaVersion = DisconnectedBundlePlanSchemaVersion
	plan = normalizeBundlePlan(plan)
	plan.Issues = collectBundleIssues(plan)
	plan.Status = bundleStatusForIssues(plan.Issues)
	return normalizeBundlePlan(plan)
}

func ValidateDisconnectedBundlePlan(plan DisconnectedBundlePlan) error {
	if plan.SchemaVersion != DisconnectedBundlePlanSchemaVersion {
		return fmt.Errorf("disconnected bundle schema_version %q is invalid", plan.SchemaVersion)
	}
	if !validBundleStatus(plan.Status) {
		return fmt.Errorf("disconnected bundle status %q is invalid", plan.Status)
	}
	wantIssues := collectBundleIssues(plan)
	if plan.Status != bundleStatusForIssues(wantIssues) {
		return fmt.Errorf("disconnected bundle status %q does not match validation issues", plan.Status)
	}
	if !sameBundleIssues(plan.Issues, wantIssues) {
		return fmt.Errorf("disconnected bundle issues do not match validation result")
	}
	return nil
}

func MarshalDisconnectedBundlePlanJSON(plan DisconnectedBundlePlan) ([]byte, error) {
	plan = normalizeBundlePlan(plan)
	if err := ValidateDisconnectedBundlePlan(plan); err != nil {
		return nil, err
	}
	return json.MarshalIndent(plan, "", "  ")
}

func UnmarshalDisconnectedBundlePlanJSON(data []byte) (DisconnectedBundlePlan, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan DisconnectedBundlePlan
	if err := decoder.Decode(&plan); err != nil {
		return DisconnectedBundlePlan{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("disconnected bundle JSON must contain one document")
		}
		return DisconnectedBundlePlan{}, err
	}
	if err := ValidateDisconnectedBundlePlan(plan); err != nil {
		return DisconnectedBundlePlan{}, err
	}
	return plan, nil
}

func collectBundleIssues(plan DisconnectedBundlePlan) []BundleIssue {
	var issues []BundleIssue
	add := func(field, severity, message string) {
		issues = append(issues, BundleIssue{Field: field, Severity: severity, Message: message})
	}
	if strings.TrimSpace(plan.Release) == "" {
		add("release", "fatal", "release is required")
	}
	if _, err := time.Parse(time.RFC3339, plan.GeneratedAt); err != nil {
		add("generated_at", "fatal", "generated_at must be RFC3339")
	}
	if strings.TrimSpace(plan.APT.RepositoryURL) == "" {
		add("apt.repository_url", "fatal", "APT mirror repository_url is required")
	}
	if len(plan.APT.Suites) == 0 {
		add("apt.suites", "fatal", "at least one APT suite is required")
	}
	if strings.TrimSpace(plan.APT.KeyFingerprint) == "" {
		add("apt.key_fingerprint", "fatal", "APT signing key fingerprint is required")
	}
	if strings.TrimSpace(plan.OCI.Registry) == "" {
		add("oci.registry", "fatal", "OCI mirror registry is required")
	}
	if len(plan.OCI.ImageRefs) == 0 {
		add("oci.image_refs", "fatal", "at least one OCI image reference is required")
	}
	if !plan.OCI.DigestOnly {
		add("oci.digest_only", "fatal", "OCI images must be pinned by digest")
	}
	for i, ref := range plan.OCI.ImageRefs {
		if strings.TrimSpace(ref) == "" {
			add(fmt.Sprintf("oci.image_refs[%d]", i), "fatal", "OCI image reference must not be empty")
			continue
		}
		if !strings.Contains(ref, "@sha256:") {
			add(fmt.Sprintf("oci.image_refs[%d]", i), "fatal", "OCI image reference must include a sha256 digest")
		}
	}
	if !plan.Docs.Included || len(plan.Docs.Paths) == 0 {
		add("docs.paths", "fatal", "offline docs must be included")
	}
	if !plan.Vulnerability.Included {
		add("vulnerability.included", "warn", "offline vulnerability database is not included")
	} else if _, err := time.Parse(time.RFC3339, plan.Vulnerability.UpdatedAt); err != nil {
		add("vulnerability.updated_at", "fatal", "vulnerability updated_at must be RFC3339")
	}
	if strings.TrimSpace(plan.Signatures.ManifestSHA256) == "" {
		add("signatures.manifest_sha256", "fatal", "manifest sha256 is required")
	}
	if strings.TrimSpace(plan.Signatures.CosignBundle) == "" {
		add("signatures.cosign_bundle", "fatal", "cosign bundle path is required")
	}
	if strings.TrimSpace(plan.Signatures.GPGDetachedSig) == "" {
		add("signatures.gpg_detached_sig", "fatal", "GPG detached signature path is required")
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Field == issues[j].Field {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Field < issues[j].Field
	})
	return issues
}

func normalizeBundlePlan(plan DisconnectedBundlePlan) DisconnectedBundlePlan {
	plan.Issues = append([]BundleIssue(nil), plan.Issues...)
	plan.APT.Suites = append([]string(nil), plan.APT.Suites...)
	plan.OCI.ImageRefs = append([]string(nil), plan.OCI.ImageRefs...)
	plan.Docs.Paths = append([]string(nil), plan.Docs.Paths...)
	sort.Strings(plan.APT.Suites)
	sort.Strings(plan.OCI.ImageRefs)
	sort.Strings(plan.Docs.Paths)
	return plan
}

func bundleStatusForIssues(issues []BundleIssue) BundleStatus {
	status := BundleStatusReady
	for _, issue := range issues {
		if issue.Severity == "fatal" {
			return BundleStatusBlocked
		}
		if issue.Severity == "warn" {
			status = BundleStatusNeedsReview
		}
	}
	return status
}

func validBundleStatus(status BundleStatus) bool {
	switch status {
	case BundleStatusReady, BundleStatusNeedsReview, BundleStatusBlocked:
		return true
	default:
		return false
	}
}

func sameBundleIssues(a, b []BundleIssue) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
