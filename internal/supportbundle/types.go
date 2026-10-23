// Package supportbundle defines the support bundle selection contract and
// manifest format used by Phase 0 diagnostics tooling.
package supportbundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultSizeCapBytes int64 = 200 * 1024 * 1024

// Selector describes which diagnostic inputs should be included in a bundle.
type Selector struct {
	Components []string  `json:"components,omitempty"`
	Namespaces []string  `json:"namespaces,omitempty"`
	Since      time.Time `json:"since,omitempty"`
	Logs       bool      `json:"logs"`
	Metrics    bool      `json:"metrics"`
	Events     bool      `json:"events"`
}

// Validate checks that the selector is bounded and names explicit targets.
func (s Selector) Validate() error {
	if !s.Logs && !s.Metrics && !s.Events {
		return errors.New("support bundle selector must include logs, metrics, or events")
	}
	if err := validateNames("component", s.Components); err != nil {
		return err
	}
	if err := validateNames("namespace", s.Namespaces); err != nil {
		return err
	}
	if !s.Since.IsZero() && s.Since.After(time.Now().Add(5*time.Minute)) {
		return errors.New("support bundle selector since must not be in the future")
	}
	return nil
}

// Manifest records the immutable contents of a collected support bundle.
type Manifest struct {
	ID        string              `json:"id"`
	CreatedAt time.Time           `json:"createdAt"`
	Selector  Selector            `json:"selector"`
	Files     []ManifestFile      `json:"files"`
	Excluded  []ManifestExclusion `json:"excluded,omitempty"`
}

// ManifestFile describes one file inside a support bundle archive.
type ManifestFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes"`
}

// ManifestExclusion describes one planned file intentionally left out of an archive.
type ManifestExclusion struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
}

// SizeCap records the maximum byte budget applied while collecting a bundle.
type SizeCap struct {
	MaxBytes int64 `json:"maxBytes"`
}

// TruncationMarker records a file that was intentionally shortened to honor a size cap.
type TruncationMarker struct {
	Path          string `json:"path"`
	OriginalBytes int64  `json:"originalBytes"`
	IncludedBytes int64  `json:"includedBytes"`
	Reason        string `json:"reason"`
}

// RedactionFinding records one redaction rule hit found while preparing a bundle.
type RedactionFinding struct {
	Path  string `json:"path"`
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// RedactionSummary is the shareable aggregate of redaction findings.
type RedactionSummary struct {
	Findings []RedactionFinding `json:"findings"`
	Total    int                `json:"total"`
}

// ReviewDecision records the operator's review state for a support bundle archive.
type ReviewDecision string

const (
	ReviewDecisionNeedsReview ReviewDecision = "needs-review"
	ReviewDecisionApproved    ReviewDecision = "approved"
	ReviewDecisionRejected    ReviewDecision = "rejected"
)

// ArchiveReviewSummary is the shareable reviewer view of an archive manifest.
type ArchiveReviewSummary struct {
	ManifestID           string               `json:"manifestId"`
	EntryCount           int                  `json:"entryCount"`
	ExcludedCount        int                  `json:"excludedCount"`
	BytesByCategory      map[string]int64     `json:"bytesByCategory"`
	OverBudgetCategories []OverBudgetCategory `json:"overBudgetCategories,omitempty"`
	RedactionSummary     RedactionSummary     `json:"redactionSummary"`
	Decision             ReviewDecision       `json:"decision"`
}

// OverBudgetCategory records categories where manifest contents exceeded a budget.
type OverBudgetCategory struct {
	Category       string `json:"category"`
	BudgetBytes    int64  `json:"budgetBytes"`
	IncludedBytes  int64  `json:"includedBytes"`
	ExcludedBytes  int64  `json:"excludedBytes"`
	AttemptedBytes int64  `json:"attemptedBytes"`
}

// PreviewFile describes a candidate file before a bundle is collected.
type PreviewFile struct {
	Path  string
	Kind  string
	Bytes int64
}

// PlannedFile describes a candidate archive file after collection planning.
type PlannedFile struct {
	Path   string
	Kind   string
	Bytes  int64
	SHA256 string
}

// ArchivePlan is a dry-run plan for a future support bundle archive.
type ArchivePlan struct {
	MaxBytes          int64              `json:"maxBytes"`
	OriginalBytes     int64              `json:"originalBytes"`
	IncludedBytes     int64              `json:"includedBytes"`
	CategoryBudgets   map[string]int64   `json:"categoryBudgets"`
	CategoryIncluded  map[string]int64   `json:"categoryIncluded"`
	TruncationMarkers []TruncationMarker `json:"truncationMarkers,omitempty"`
	Files             []ArchivePlanFile  `json:"files"`
}

// ArchivePlanFile records the planned included bytes for one candidate file.
type ArchivePlanFile struct {
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	OriginalBytes int64  `json:"originalBytes"`
	IncludedBytes int64  `json:"includedBytes"`
}

// BlockedPathClass identifies hard-blocked bundle paths that may contain secrets.
type BlockedPathClass string

const (
	BlockedPathNone   BlockedPathClass = ""
	BlockedPathSecret BlockedPathClass = "secret"
	BlockedPathToken  BlockedPathClass = "token"
	BlockedPathACME   BlockedPathClass = "acme-key"
)

// BuildPreviewManifest validates a selector and returns the safe manifest preview.
func BuildPreviewManifest(id string, createdAt time.Time, selector Selector, files []PreviewFile) (Manifest, error) {
	manifest := Manifest{ID: id, CreatedAt: createdAt, Selector: selector, Files: make([]ManifestFile, 0, len(files))}
	if err := selector.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("selector: %w", err)
	}
	for i, file := range files {
		if class := ClassifyHardBlockedPath(file.Path); class != BlockedPathNone {
			return Manifest{}, fmt.Errorf("files[%d]: hard-blocked %s path", i, class)
		}
		entry := ManifestFile{Path: file.Path, Kind: file.Kind, Bytes: file.Bytes}
		if err := entry.Validate(); err != nil {
			return Manifest{}, fmt.Errorf("files[%d]: %w", i, err)
		}
		manifest.Files = append(manifest.Files, entry)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// MarshalManifestJSON validates and renders a stable, human-readable manifest JSON document.
func MarshalManifestJSON(manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(manifest, "", "  ")
}

// WriteManifestJSON validates and writes a stable, human-readable manifest JSON document.
func WriteManifestJSON(w io.Writer, manifest Manifest) error {
	data, err := MarshalManifestJSON(manifest)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// BuildArchiveManifest applies category budgets to planned files and records exclusions.
func BuildArchiveManifest(id string, createdAt time.Time, selector Selector, files []PlannedFile, categoryBudgets map[string]int64) (Manifest, error) {
	if err := selector.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("selector: %w", err)
	}
	budgets := make(map[string]int64, len(categoryBudgets))
	for category, budget := range categoryBudgets {
		category = strings.TrimSpace(category)
		if category == "" {
			return Manifest{}, errors.New("category budget name is required")
		}
		if budget < 0 {
			return Manifest{}, fmt.Errorf("category budget %q must be non-negative", category)
		}
		budgets[category] = budget
	}

	sortedFiles := append([]PlannedFile(nil), files...)
	sort.Slice(sortedFiles, func(i, j int) bool {
		if sortedFiles[i].Kind == sortedFiles[j].Kind {
			return sortedFiles[i].Path < sortedFiles[j].Path
		}
		return sortedFiles[i].Kind < sortedFiles[j].Kind
	})

	manifest := Manifest{ID: id, CreatedAt: createdAt, Selector: selector, Files: make([]ManifestFile, 0, len(files))}
	includedByCategory := make(map[string]int64, len(budgets))
	for i, file := range sortedFiles {
		entry := ManifestFile{Path: file.Path, Kind: file.Kind, SHA256: file.SHA256, Bytes: file.Bytes}
		if err := entry.Validate(); err != nil {
			return Manifest{}, fmt.Errorf("files[%d]: %w", i, err)
		}
		if class := ClassifyHardBlockedPath(file.Path); class != BlockedPathNone {
			manifest.Excluded = append(manifest.Excluded, ManifestExclusion{Path: file.Path, Kind: file.Kind, Bytes: file.Bytes, Reason: "hard-blocked-" + string(class)})
			continue
		}
		budget, ok := budgets[file.Kind]
		if !ok {
			manifest.Excluded = append(manifest.Excluded, ManifestExclusion{Path: file.Path, Kind: file.Kind, Bytes: file.Bytes, Reason: "missing-category-budget"})
			continue
		}
		if includedByCategory[file.Kind]+file.Bytes > budget {
			manifest.Excluded = append(manifest.Excluded, ManifestExclusion{Path: file.Path, Kind: file.Kind, Bytes: file.Bytes, Reason: "category-budget-exceeded"})
			continue
		}
		includedByCategory[file.Kind] += file.Bytes
		manifest.Files = append(manifest.Files, entry)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// BuildPreviewJSON returns the JSON preview for files that would be collected.
func BuildPreviewJSON(id string, createdAt time.Time, selector Selector, files []PreviewFile) ([]byte, error) {
	manifest, err := BuildPreviewManifest(id, createdAt, selector, files)
	if err != nil {
		return nil, err
	}
	return MarshalManifestJSON(manifest)
}

// BuildArchiveReviewSummary returns a pure reviewer summary for an archive manifest.
func BuildArchiveReviewSummary(manifest Manifest, categoryBudgets map[string]int64, redaction RedactionSummary, decision ReviewDecision) (ArchiveReviewSummary, error) {
	if err := manifest.Validate(); err != nil {
		return ArchiveReviewSummary{}, err
	}
	budgets := make(map[string]int64, len(categoryBudgets))
	for category, budget := range categoryBudgets {
		category = strings.TrimSpace(category)
		if category == "" {
			return ArchiveReviewSummary{}, errors.New("category budget name is required")
		}
		if budget < 0 {
			return ArchiveReviewSummary{}, fmt.Errorf("category budget %q must be non-negative", category)
		}
		budgets[category] = budget
	}

	summary := ArchiveReviewSummary{
		ManifestID:       manifest.ID,
		EntryCount:       len(manifest.Files),
		ExcludedCount:    len(manifest.Excluded),
		BytesByCategory:  make(map[string]int64),
		RedactionSummary: redaction,
		Decision:         decision,
	}
	budgetExcluded := make(map[string]int64)
	for _, file := range manifest.Files {
		summary.BytesByCategory[file.Kind] += file.Bytes
	}
	for _, exclusion := range manifest.Excluded {
		if exclusion.Reason == "category-budget-exceeded" {
			budgetExcluded[exclusion.Kind] += exclusion.Bytes
		}
	}
	for category, included := range summary.BytesByCategory {
		budget, ok := budgets[category]
		if ok && included <= budget && budgetExcluded[category] == 0 {
			continue
		}
		if ok && (included > budget || budgetExcluded[category] > 0) {
			summary.OverBudgetCategories = append(summary.OverBudgetCategories, OverBudgetCategory{
				Category:       category,
				BudgetBytes:    budget,
				IncludedBytes:  included,
				ExcludedBytes:  budgetExcluded[category],
				AttemptedBytes: included + budgetExcluded[category],
			})
		}
	}
	for category, excluded := range budgetExcluded {
		if _, ok := summary.BytesByCategory[category]; ok {
			continue
		}
		budget, ok := budgets[category]
		if !ok {
			continue
		}
		summary.OverBudgetCategories = append(summary.OverBudgetCategories, OverBudgetCategory{Category: category, BudgetBytes: budget, ExcludedBytes: excluded, AttemptedBytes: excluded})
	}
	sort.Slice(summary.OverBudgetCategories, func(i, j int) bool {
		return summary.OverBudgetCategories[i].Category < summary.OverBudgetCategories[j].Category
	})
	if err := summary.Validate(); err != nil {
		return ArchiveReviewSummary{}, err
	}
	return summary, nil
}

// MarshalArchiveReviewSummaryJSON validates and renders stable reviewer summary JSON.
func MarshalArchiveReviewSummaryJSON(summary ArchiveReviewSummary) ([]byte, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(summary, "", "  ")
}

// BuildArchivePlan computes per-category byte truncation without creating an archive.
func BuildArchivePlan(selector Selector, files []PreviewFile, cap SizeCap) (ArchivePlan, error) {
	if err := selector.Validate(); err != nil {
		return ArchivePlan{}, fmt.Errorf("selector: %w", err)
	}
	if cap.MaxBytes == 0 {
		cap.MaxBytes = DefaultSizeCapBytes
	}
	if err := cap.Validate(); err != nil {
		return ArchivePlan{}, err
	}
	categories := make(map[string]struct{})
	for i, file := range files {
		if class := ClassifyHardBlockedPath(file.Path); class != BlockedPathNone {
			return ArchivePlan{}, fmt.Errorf("files[%d]: hard-blocked %s path", i, class)
		}
		entry := ManifestFile{Path: file.Path, Kind: file.Kind, Bytes: file.Bytes}
		if err := entry.Validate(); err != nil {
			return ArchivePlan{}, fmt.Errorf("files[%d]: %w", i, err)
		}
		categories[file.Kind] = struct{}{}
	}

	plan := ArchivePlan{
		MaxBytes:         cap.MaxBytes,
		CategoryBudgets:  make(map[string]int64, len(categories)),
		CategoryIncluded: make(map[string]int64, len(categories)),
		Files:            make([]ArchivePlanFile, 0, len(files)),
	}
	if len(categories) == 0 {
		return plan, nil
	}
	perCategoryBudget := cap.MaxBytes / int64(len(categories))
	for category := range categories {
		plan.CategoryBudgets[category] = perCategoryBudget
	}
	for _, file := range files {
		remaining := plan.CategoryBudgets[file.Kind] - plan.CategoryIncluded[file.Kind]
		included := file.Bytes
		if remaining < included {
			included = remaining
		}
		if included < 0 {
			included = 0
		}
		plan.OriginalBytes += file.Bytes
		plan.IncludedBytes += included
		plan.CategoryIncluded[file.Kind] += included
		plan.Files = append(plan.Files, ArchivePlanFile{Path: file.Path, Kind: file.Kind, OriginalBytes: file.Bytes, IncludedBytes: included})
		if included < file.Bytes {
			marker := TruncationMarker{Path: file.Path, OriginalBytes: file.Bytes, IncludedBytes: included, Reason: "category-size-cap"}
			if err := marker.Validate(); err != nil {
				return ArchivePlan{}, err
			}
			plan.TruncationMarkers = append(plan.TruncationMarkers, marker)
		}
	}
	return plan, nil
}

// SummarizeRedactionFindings returns a shareable count-only redaction summary.
func SummarizeRedactionFindings(findings []RedactionFinding) RedactionSummary {
	aggregates := make(map[string]RedactionFinding, len(findings))
	for _, finding := range findings {
		if finding.Count <= 0 || strings.TrimSpace(finding.Path) == "" || strings.TrimSpace(finding.Rule) == "" {
			continue
		}
		key := finding.Path + "\x00" + finding.Rule
		aggregate := aggregates[key]
		aggregate.Path = finding.Path
		aggregate.Rule = finding.Rule
		aggregate.Count += finding.Count
		aggregates[key] = aggregate
	}
	summary := RedactionSummary{Findings: make([]RedactionFinding, 0, len(aggregates))}
	for _, finding := range aggregates {
		summary.Findings = append(summary.Findings, finding)
		summary.Total += finding.Count
	}
	sort.Slice(summary.Findings, func(i, j int) bool {
		if summary.Findings[i].Path == summary.Findings[j].Path {
			return summary.Findings[i].Rule < summary.Findings[j].Rule
		}
		return summary.Findings[i].Path < summary.Findings[j].Path
	})
	return summary
}

// ClassifyHardBlockedPath identifies paths that must never be included in support bundles.
func ClassifyHardBlockedPath(path string) BlockedPathClass {
	normalized := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))))
	if normalized == "." || normalized == "" {
		return BlockedPathNone
	}
	parts := strings.Split(normalized, "/")
	inACMEPath := false
	for _, part := range parts {
		if strings.Contains(part, "acme") {
			inACMEPath = true
		}
		if part == "acme.json" || (strings.Contains(part, "acme") && strings.Contains(part, "key")) {
			return BlockedPathACME
		}
		if inACMEPath && (strings.Contains(part, "key") || strings.HasSuffix(part, ".key")) {
			return BlockedPathACME
		}
		if strings.Contains(part, "token") || strings.Contains(part, "bearer") || strings.Contains(part, "jwt") {
			return BlockedPathToken
		}
		if strings.Contains(part, "authorization") || strings.Contains(part, "auth-header") || strings.Contains(part, "cookie") || strings.Contains(part, "set-cookie") {
			return BlockedPathToken
		}
		if part == ".env" || strings.Contains(part, "env-dump") || strings.Contains(part, "env_dump") || strings.Contains(part, "environment-dump") {
			return BlockedPathSecret
		}
		if strings.Contains(part, "secret") || strings.Contains(part, "password") || strings.Contains(part, "passwd") || strings.Contains(part, "credential") || strings.Contains(part, "private_key") || strings.Contains(part, "private-key") {
			return BlockedPathSecret
		}
	}
	return BlockedPathNone
}

// Validate checks the manifest for required metadata and safe relative paths.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("support bundle manifest id is required")
	}
	if m.CreatedAt.IsZero() {
		return errors.New("support bundle manifest createdAt is required")
	}
	if err := m.Selector.Validate(); err != nil {
		return fmt.Errorf("selector: %w", err)
	}
	for i, file := range m.Files {
		if err := file.Validate(); err != nil {
			return fmt.Errorf("files[%d]: %w", i, err)
		}
	}
	for i, exclusion := range m.Excluded {
		if err := exclusion.Validate(); err != nil {
			return fmt.Errorf("excluded[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks that a manifest entry cannot escape the bundle root.
func (f ManifestFile) Validate() error {
	if strings.TrimSpace(f.Path) == "" {
		return errors.New("path is required")
	}
	if filepath.IsAbs(f.Path) || strings.HasPrefix(filepath.Clean(f.Path), "..") {
		return errors.New("path must be relative to the bundle root")
	}
	if strings.TrimSpace(f.Kind) == "" {
		return errors.New("kind is required")
	}
	if f.Bytes < 0 {
		return errors.New("bytes must be non-negative")
	}
	if f.SHA256 != "" {
		if err := ValidateSHA256(f.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks that an exclusion is safe to publish in a manifest.
func (e ManifestExclusion) Validate() error {
	if strings.TrimSpace(e.Path) == "" {
		return errors.New("exclusion path is required")
	}
	if filepath.IsAbs(e.Path) || strings.HasPrefix(filepath.Clean(e.Path), "..") {
		return errors.New("exclusion path must be relative to the bundle root")
	}
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("exclusion kind is required")
	}
	if e.Bytes < 0 {
		return errors.New("exclusion bytes must be non-negative")
	}
	if strings.TrimSpace(e.Reason) == "" {
		return errors.New("exclusion reason is required")
	}
	return nil
}

// Validate checks that the cap is an explicit positive bound.
func (c SizeCap) Validate() error {
	if c.MaxBytes <= 0 {
		return errors.New("support bundle size cap must be positive")
	}
	return nil
}

// Validate checks that a truncation marker is safe to publish in a manifest.
func (m TruncationMarker) Validate() error {
	if strings.TrimSpace(m.Path) == "" {
		return errors.New("truncation marker path is required")
	}
	if filepath.IsAbs(m.Path) || strings.HasPrefix(filepath.Clean(m.Path), "..") {
		return errors.New("truncation marker path must be relative to the bundle root")
	}
	if m.OriginalBytes < 0 || m.IncludedBytes < 0 {
		return errors.New("truncation marker byte counts must be non-negative")
	}
	if m.IncludedBytes > m.OriginalBytes {
		return errors.New("truncation marker included bytes must not exceed original bytes")
	}
	if strings.TrimSpace(m.Reason) == "" {
		return errors.New("truncation marker reason is required")
	}
	return nil
}

// Validate checks that a review summary is internally consistent and actionable.
func (s ArchiveReviewSummary) Validate() error {
	if strings.TrimSpace(s.ManifestID) == "" {
		return errors.New("archive review summary manifest id is required")
	}
	if s.EntryCount < 0 || s.ExcludedCount < 0 {
		return errors.New("archive review summary counts must be non-negative")
	}
	if err := s.Decision.Validate(); err != nil {
		return err
	}
	if err := s.RedactionSummary.Validate(); err != nil {
		return fmt.Errorf("redaction summary: %w", err)
	}
	for category, bytes := range s.BytesByCategory {
		if strings.TrimSpace(category) == "" {
			return errors.New("archive review summary category is required")
		}
		if bytes < 0 {
			return fmt.Errorf("archive review summary category %q bytes must be non-negative", category)
		}
	}
	for i, category := range s.OverBudgetCategories {
		if err := category.Validate(); err != nil {
			return fmt.Errorf("overBudgetCategories[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks that a reviewer decision is one of the allowed placeholders.
func (d ReviewDecision) Validate() error {
	switch d {
	case ReviewDecisionNeedsReview, ReviewDecisionApproved, ReviewDecisionRejected:
		return nil
	default:
		return errors.New("archive review decision must be needs-review, approved, or rejected")
	}
}

// Validate checks over-budget category accounting.
func (c OverBudgetCategory) Validate() error {
	if strings.TrimSpace(c.Category) == "" {
		return errors.New("category is required")
	}
	if c.BudgetBytes < 0 || c.IncludedBytes < 0 || c.ExcludedBytes < 0 || c.AttemptedBytes < 0 {
		return errors.New("byte counts must be non-negative")
	}
	if c.AttemptedBytes != c.IncludedBytes+c.ExcludedBytes {
		return errors.New("attempted bytes must equal included and excluded bytes")
	}
	if c.IncludedBytes <= c.BudgetBytes && c.ExcludedBytes == 0 {
		return errors.New("over-budget category must exceed budget or record budget exclusions")
	}
	return nil
}

// Validate checks that a redaction summary contains only count metadata.
func (s RedactionSummary) Validate() error {
	wantTotal := 0
	for i, finding := range s.Findings {
		if strings.TrimSpace(finding.Path) == "" {
			return fmt.Errorf("findings[%d]: path is required", i)
		}
		if strings.TrimSpace(finding.Rule) == "" {
			return fmt.Errorf("findings[%d]: rule is required", i)
		}
		if finding.Count < 0 {
			return fmt.Errorf("findings[%d]: count must be non-negative", i)
		}
		wantTotal += finding.Count
	}
	if s.Total != wantTotal {
		return errors.New("total must equal redaction finding counts")
	}
	return nil
}

func validateNames(kind string, names []string) error {
	for i, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s[%d] is required", kind, i)
		}
	}
	return nil
}

// ValidateSHA256 checks a materialized evidence checksum value.
func ValidateSHA256(value string) error {
	if strings.TrimSpace(value) != value || len(value) != 64 {
		return errors.New("sha256 must be 64 lowercase hexadecimal characters")
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return errors.New("sha256 must be 64 lowercase hexadecimal characters")
	}
	return nil
}
