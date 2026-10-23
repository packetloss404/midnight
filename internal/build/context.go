package build

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Dockerignore is the parsed representation of a .dockerignore file. It
// preserves pattern order so later patterns override earlier ones (the
// dockerignore convention).
type Dockerignore struct {
	patterns []dockerignorePattern
	warnings []DockerignoreWarning
}

type dockerignorePattern struct {
	raw     string
	pattern string
	line    int
	negate  bool
	dirOnly bool
}

// DockerignoreWarningKind classifies non-fatal issues the parser surfaces.
type DockerignoreWarningKind string

const (
	// DockerignoreWarningRedundant marks two patterns that match exactly the
	// same paths in the same direction (both ignore or both negate).
	DockerignoreWarningRedundant DockerignoreWarningKind = "redundant"
	// DockerignoreWarningConflicting marks a later pattern that exactly
	// reverses the effect of an earlier one (one ignores, the other
	// re-includes the same path).
	DockerignoreWarningConflicting DockerignoreWarningKind = "conflicting"
)

// DockerignoreWarning is a non-fatal hint surfaced by ParseDockerignore. The
// build CLI may surface these to the operator, but they never block a build.
type DockerignoreWarning struct {
	Kind  DockerignoreWarningKind `json:"kind"`
	Lines []int                   `json:"lines"`
	Paths []string                `json:"paths,omitempty"`
}

// BuildContextPlan is the deterministic file list that will be packaged as the
// BuildKit build context. Files are sorted, normalized, and free of secret-
// looking patterns. The byte budget classifier reports warning vs. block
// outcomes to the caller.
type BuildContextPlan struct {
	Files       []string
	TotalBytes  int64
	Warnings    []DockerignoreWarning
	BudgetState BuildContextBudgetState
}

// BuildContextBudgetState classifies the overall byte size of the build
// context against operator-supplied warning and block thresholds.
type BuildContextBudgetState string

const (
	// BuildContextBudgetOK means the context fits comfortably within both
	// thresholds.
	BuildContextBudgetOK BuildContextBudgetState = "ok"
	// BuildContextBudgetWarn means the context is above the warn threshold
	// but below the block threshold.
	BuildContextBudgetWarn BuildContextBudgetState = "warn"
	// BuildContextBudgetBlocked means the context exceeds the block
	// threshold and PlanBuildContext returns an error.
	BuildContextBudgetBlocked BuildContextBudgetState = "blocked"
)

// BuildContextFile represents one file (or symlink) the planner is asked to
// classify. Symlinks that escape the context root are rejected.
type BuildContextFile struct {
	Path          string
	SizeBytes     int64
	Symlink       bool
	SymlinkTarget string
}

// BuildContextOptions controls the byte-budget classifier and the no-symlinks-
// escaping check. Zero thresholds mean "no enforcement".
type BuildContextOptions struct {
	WarnByteBudget  int64
	BlockByteBudget int64
}

// ParseDockerignore parses a .dockerignore subset that covers the cases
// Phase 0 needs: comments, blank lines, leading-whitespace tolerance,
// negation, glob patterns, basename matches, and directory patterns ending
// in /. It returns warnings for redundant or conflicting rules.
func ParseDockerignore(contents string) Dockerignore {
	lines := strings.Split(contents, "\n")
	patterns := make([]dockerignorePattern, 0, len(lines))

	for index, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		// Tolerate leading whitespace; dockerignore implementations differ
		// on this but the common Go reference accepts it.
		line = strings.TrimLeft(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Trim trailing whitespace once the comment check is done.
		line = strings.TrimRight(line, " \t")

		pattern := dockerignorePattern{raw: line, line: index + 1}
		if strings.HasPrefix(line, "!") {
			pattern.negate = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		if line == "" {
			continue
		}

		line = filepath.ToSlash(line)
		line = strings.TrimPrefix(line, "/")
		if strings.HasSuffix(line, "/") {
			pattern.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}

		pattern.pattern = path.Clean(line)
		patterns = append(patterns, pattern)
	}

	return Dockerignore{patterns: patterns, warnings: deriveDockerignoreWarnings(patterns)}
}

// Match reports whether the supplied path is ignored by the parsed rules.
// Negation rules following an ignore re-include the path.
func (d Dockerignore) Match(file string) bool {
	file = cleanContextPath(file)
	if file == "" {
		return false
	}

	ignored := false
	for _, pattern := range d.patterns {
		if pattern.matches(file) {
			ignored = !pattern.negate
		}
	}

	return ignored
}

// Warnings returns the non-fatal warnings emitted by ParseDockerignore.
func (d Dockerignore) Warnings() []DockerignoreWarning {
	out := make([]DockerignoreWarning, len(d.warnings))
	copy(out, d.warnings)
	return out
}

// PlanBuildContext is the legacy string-only build-context planner. It
// rejects secret-looking paths and applies dockerignore rules. Files are
// returned in the input order (legacy behaviour).
func PlanBuildContext(files []string, dockerignore Dockerignore) (BuildContextPlan, error) {
	plan := BuildContextPlan{Files: make([]string, 0, len(files))}

	for _, file := range files {
		file = cleanContextPath(file)
		if file == "" || dockerignore.Match(file) {
			continue
		}
		if IsSecretBuildContextPath(file) {
			return BuildContextPlan{}, fmt.Errorf("build context refuses secret-looking file %q", file)
		}

		plan.Files = append(plan.Files, file)
	}
	plan.Warnings = dockerignore.Warnings()

	return plan, nil
}

// PlanBuildContextDetailed is the Phase 1 build-context planner. It enforces:
//   - deterministic alphabetical ordering of the resulting file list
//   - secret-looking-path refusal (extended deny list)
//   - byte-budget classifier (warn/block)
//   - no symlinks escaping the context root
func PlanBuildContextDetailed(files []BuildContextFile, dockerignore Dockerignore, opts BuildContextOptions) (BuildContextPlan, error) {
	plan := BuildContextPlan{
		Files:       make([]string, 0, len(files)),
		BudgetState: BuildContextBudgetOK,
		Warnings:    dockerignore.Warnings(),
	}

	for _, file := range files {
		cleaned := cleanContextPath(file.Path)
		if cleaned == "" || dockerignore.Match(cleaned) {
			continue
		}
		if IsSecretBuildContextPath(cleaned) {
			return BuildContextPlan{}, fmt.Errorf("build context refuses secret-looking file %q", cleaned)
		}
		if file.Symlink {
			if err := validateSymlinkInsideRoot(cleaned, file.SymlinkTarget); err != nil {
				return BuildContextPlan{}, err
			}
		}
		if file.SizeBytes < 0 {
			return BuildContextPlan{}, fmt.Errorf("build context file %q has negative size %d", cleaned, file.SizeBytes)
		}
		plan.Files = append(plan.Files, cleaned)
		plan.TotalBytes += file.SizeBytes
	}

	sort.Strings(plan.Files)
	plan.BudgetState = classifyBuildContextBudget(plan.TotalBytes, opts)
	if plan.BudgetState == BuildContextBudgetBlocked {
		return BuildContextPlan{}, fmt.Errorf("build context size %d bytes exceeds block budget %d", plan.TotalBytes, opts.BlockByteBudget)
	}
	return plan, nil
}

func classifyBuildContextBudget(total int64, opts BuildContextOptions) BuildContextBudgetState {
	if opts.BlockByteBudget > 0 && total > opts.BlockByteBudget {
		return BuildContextBudgetBlocked
	}
	if opts.WarnByteBudget > 0 && total > opts.WarnByteBudget {
		return BuildContextBudgetWarn
	}
	return BuildContextBudgetOK
}

func validateSymlinkInsideRoot(file, target string) error {
	if target == "" {
		return fmt.Errorf("build context symlink %q has empty target", file)
	}
	target = filepath.ToSlash(strings.TrimSpace(target))
	if strings.HasPrefix(target, "/") || filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
		return fmt.Errorf("build context symlink %q escapes context root via absolute target %q", file, target)
	}
	// Resolve target relative to the symlink's containing directory and reject
	// any "../" walk that escapes the root.
	dir := path.Dir(file)
	joined := path.Join(dir, target)
	cleaned := path.Clean(joined)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("build context symlink %q escapes context root (target %q)", file, target)
	}
	return nil
}

// IsSecretBuildContextPath returns true when the supplied path matches one of
// the known secret-file deny patterns. The list is intentionally coarse to
// catch the common credential leaks operators routinely commit.
func IsSecretBuildContextPath(file string) bool {
	file = cleanContextPath(file)
	if file == "" {
		return false
	}

	parts := strings.Split(file, "/")
	for _, part := range parts {
		lower := strings.ToLower(part)
		switch lower {
		case "secrets", ".aws", ".ssh", ".config", ".gcloud", ".kube":
			// .config and .gcloud are common cloud SDK creds; .kube holds
			// kubeconfig.
			if lower != ".config" && lower != ".kube" && lower != ".gcloud" {
				return true
			}
			// For .config / .kube / .gcloud we only refuse when the basename
			// looks like a credential file (handled below).
		}
	}

	base := strings.ToLower(path.Base(file))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if base == ".npmrc" || base == ".netrc" || base == ".pypirc" || base == ".dockerconfigjson" || base == "credentials" {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".crt") || strings.HasSuffix(base, ".kubeconfig") {
		return true
	}
	if strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasPrefix(base, "id_ecdsa") || strings.HasPrefix(base, "id_dsa") {
		return true
	}
	if strings.HasPrefix(base, "terraform.tfstate") {
		return true
	}
	if strings.Contains(base, ".kubeconfig") {
		return true
	}
	return false
}

func deriveDockerignoreWarnings(patterns []dockerignorePattern) []DockerignoreWarning {
	warnings := make([]DockerignoreWarning, 0)
	type entry struct {
		index int
		neg   bool
	}
	seen := make(map[string][]entry)
	for i, pattern := range patterns {
		key := pattern.key()
		seen[key] = append(seen[key], entry{index: i, neg: pattern.negate})
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		entries := seen[key]
		if len(entries) < 2 {
			continue
		}
		// Group by direction.
		ignoreLines := []int{}
		negateLines := []int{}
		for _, e := range entries {
			if e.neg {
				negateLines = append(negateLines, patterns[e.index].line)
			} else {
				ignoreLines = append(ignoreLines, patterns[e.index].line)
			}
		}
		paths := []string{patterns[entries[0].index].pattern}
		if len(ignoreLines) > 1 {
			warnings = append(warnings, DockerignoreWarning{
				Kind:  DockerignoreWarningRedundant,
				Lines: ignoreLines,
				Paths: paths,
			})
		}
		if len(negateLines) > 1 {
			warnings = append(warnings, DockerignoreWarning{
				Kind:  DockerignoreWarningRedundant,
				Lines: negateLines,
				Paths: paths,
			})
		}
		if len(ignoreLines) > 0 && len(negateLines) > 0 {
			combined := append([]int(nil), ignoreLines...)
			combined = append(combined, negateLines...)
			sort.Ints(combined)
			warnings = append(warnings, DockerignoreWarning{
				Kind:  DockerignoreWarningConflicting,
				Lines: combined,
				Paths: paths,
			})
		}
	}

	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].Kind != warnings[j].Kind {
			return warnings[i].Kind < warnings[j].Kind
		}
		if len(warnings[i].Lines) > 0 && len(warnings[j].Lines) > 0 && warnings[i].Lines[0] != warnings[j].Lines[0] {
			return warnings[i].Lines[0] < warnings[j].Lines[0]
		}
		return strings.Join(warnings[i].Paths, ",") < strings.Join(warnings[j].Paths, ",")
	})
	return warnings
}

func (p dockerignorePattern) matches(file string) bool {
	if p.dirOnly {
		return file == p.pattern || strings.HasPrefix(file, p.pattern+"/")
	}

	if strings.Contains(p.pattern, "/") {
		matched, err := path.Match(p.pattern, file)
		return err == nil && matched
	}

	for _, part := range strings.Split(file, "/") {
		matched, err := path.Match(p.pattern, part)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func (p dockerignorePattern) key() string {
	suffix := ""
	if p.dirOnly {
		suffix = "/"
	}
	return p.pattern + suffix
}

func cleanContextPath(file string) string {
	file = filepath.ToSlash(strings.TrimSpace(file))
	file = strings.TrimPrefix(file, "/")
	file = path.Clean(file)
	if file == "." || strings.HasPrefix(file, "../") || file == ".." {
		return ""
	}
	return file
}
