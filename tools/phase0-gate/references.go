package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// referenceCatalog captures the known reason codes, events, and runbook files
// that drill rows may reference. It is intentionally an offline read of the
// repository — nothing is fetched.
type referenceCatalog struct {
	ReasonCodes map[string]struct{}
	Events      map[string]struct{}
	Runbooks    map[string]struct{}
}

// loadReferenceCatalog reads phase 0 reference inputs from disk. The error
// catalog is sourced from `docs/error-codes.md`. The runbook catalog is the
// set of files under `docs/runbooks/`. The cloudevents catalog is sourced
// from the gate's well-known phase 0 placeholder (currently the drill manifest
// itself plus the `internal/cloudevents` package's catalog) — phase 0 has not
// yet shipped a static cloudevents file, so the catalog accepts any event
// referenced by the locked alert pack and any drill row using a stable
// dotted-prefix scheme. Callers should treat reference errors as
// gate.ExitManifestInvalid.
func loadReferenceCatalog(root string) (referenceCatalog, error) {
	catalog := referenceCatalog{
		ReasonCodes: make(map[string]struct{}),
		Events:      make(map[string]struct{}),
		Runbooks:    make(map[string]struct{}),
	}
	if err := loadErrorCodeRegistry(filepath.Join(root, "docs", "error-codes.md"), catalog.ReasonCodes); err != nil {
		return referenceCatalog{}, err
	}
	for _, code := range phase0DrillReasonCodeAllowlist() {
		catalog.ReasonCodes[code] = struct{}{}
	}
	for _, name := range phase0DrillEventAllowlist() {
		catalog.Events[name] = struct{}{}
	}
	if err := loadRunbookCatalog(filepath.Join(root, "docs", "runbooks"), catalog.Runbooks); err != nil {
		return referenceCatalog{}, err
	}
	return catalog, nil
}

// phase0DrillReasonCodeAllowlist returns the set of `phase0.*` style reason
// codes that the QA/release-gate lane is permitted to surface even though they
// are not registered as user-facing API failure codes. These are gate-only
// markers and are owned by S08.
func phase0DrillReasonCodeAllowlist() []string {
	return []string{
		"phase0.alert.api-target-down",
		"phase0.alert.disk-pressure",
		"phase0.alert.agent-lost",
		"phase0.alert.backup-failed",
		"phase0.alert.cert-expiring-soon",
		"phase0.alert.deploy-job-stuck",
		"phase0.alert.buildkit-cache-corruption",
		"phase0.alert.conntrack-pressure",
		"phase0.backup.offhost-missing",
		"phase0.supportbundle.selector-invalid",
		"phase0.supportbundle.blocked-path",
		"phase0.supportbundle.manifest-review",
		"phase0.backup.local-uri",
		"phase0.backup.destination-placeholder",
		"phase0.backup.restore-checksum",
		"phase0.observability.alert-packaging",
		"phase0.gate.manifest-validation",
		"phase0.gate.alert-validation",
		"phase0.gate.release-readiness",
	}
}

// phase0DrillEventAllowlist returns the set of cloudevents-style event types
// that the drill manifest is permitted to emit. The phase 0 cloudevents
// catalog is still scaffolded (`internal/cloudevents`), so this allowlist is
// the offline source of truth until a generated catalog ships.
func phase0DrillEventAllowlist() []string {
	return []string{
		"alert.api_target_down",
		"alert.disk_pressure",
		"alert.agent_lost",
		"alert.backup_failed",
		"alert.cert_expiring_soon",
		"alert.deploy_job_stuck",
		"alert.buildkit_cache_corruption",
		"alert.conntrack_pressure",
		"backup.offhost_missing",
		"supportbundle.selector_invalid",
		"supportbundle.blocked_path",
		"supportbundle.manifest_review",
		"backup.local_uri",
		"backup.destination_placeholder",
		"backup.restore_checksum",
		"observability.alert_packaging",
		"gate.manifest_validation",
		"gate.alert_validation",
		"gate.release_readiness",
	}
}

var errorCodeRowPattern = regexp.MustCompile("^\\s*\\|\\s*`([A-Z][A-Z0-9_]*)`\\s*\\|")

func loadErrorCodeRegistry(path string, out map[string]struct{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read error-codes registry %s: %w", path, err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		match := errorCodeRowPattern.FindStringSubmatch(line)
		if len(match) == 2 {
			out[match[1]] = struct{}{}
		}
	}
	return scanner.Err()
}

func loadRunbookCatalog(dir string, out map[string]struct{}) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read runbooks directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		// Store as forward-slash repo-relative path so manifest references
		// match deterministically regardless of OS path separator.
		out[filepath.ToSlash(filepath.Join("docs", "runbooks", name))] = struct{}{}
	}
	return nil
}

// validateDrillManifestReferences verifies every drill row references known
// reason codes, events, and runbook files. The catalog is sourced from disk
// (`docs/error-codes.md`, `docs/runbooks/*.md`) and the phase 0 reference
// allowlists for gate-owned markers. Returns a deterministic, sorted error
// listing every missing reference.
func validateDrillManifestReferences(manifest drillManifest, catalog referenceCatalog) error {
	var missing []string
	seen := make(map[string]struct{})
	add := func(s string) {
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		missing = append(missing, s)
	}
	for _, drill := range manifest.Drills {
		if code := strings.TrimSpace(drill.ReasonCode); code != "" {
			if _, ok := catalog.ReasonCodes[code]; !ok {
				add(fmt.Sprintf("drill %s reason-code %q not in error-codes registry or phase 0 allowlist", drill.ID, code))
			}
		}
		if event := strings.TrimSpace(drill.Event); event != "" {
			if _, ok := catalog.Events[event]; !ok {
				add(fmt.Sprintf("drill %s event %q not in cloudevents catalog or phase 0 allowlist", drill.ID, event))
			}
		}
		if runbook := strings.TrimSpace(drill.Runbook); runbook != "" {
			normalized := filepath.ToSlash(filepath.Clean(runbook))
			if _, ok := catalog.Runbooks[normalized]; !ok {
				add(fmt.Sprintf("drill %s runbook %q does not exist on disk", drill.ID, runbook))
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("drill manifest references invalid: %s", strings.Join(missing, "; "))
}

// validateDrillManifestWithReferences combines the structural manifest
// validator with the reference validator. The structural validator runs
// first; reference checks run only when the manifest parses cleanly.
func validateDrillManifestWithReferences(data []byte, root string) error {
	manifest, err := parseDrillManifest(data)
	if err != nil {
		return err
	}
	if err := validateParsedDrillManifest(manifest); err != nil {
		return err
	}
	catalog, err := loadReferenceCatalog(root)
	if err != nil {
		return err
	}
	return validateDrillManifestReferences(manifest, catalog)
}

// errReferenceCatalogEmpty is returned by helpers when the catalog is missing
// every required reference source. Tests assert this error path.
var errReferenceCatalogEmpty = errors.New("reference catalog is empty")

// ValidateRunbookReferencesStrict verifies that every drill row in the
// supplied manifest has a non-empty `runbook` field whose value resolves to
// an actual file under `docs/runbooks/` rooted at `root`. Strict mode is the
// gate we want during release-evidence emission: a missing or stub runbook
// reference is a phase 0 conformance violation and MUST fail the gate.
//
// The function is a thin wrapper around loadRunbookCatalog plus a per-row
// existence check; it never reads runbook contents. The returned error is
// deterministic and lists every offending drill row sorted by drill ID.
func ValidateRunbookReferencesStrict(manifest drillManifest, root string) error {
	runbooks := make(map[string]struct{})
	if err := loadRunbookCatalog(filepath.Join(root, "docs", "runbooks"), runbooks); err != nil {
		return err
	}
	if len(runbooks) == 0 {
		return errReferenceCatalogEmpty
	}
	type miss struct {
		id      string
		runbook string
		reason  string
	}
	var misses []miss
	for _, d := range manifest.Drills {
		runbook := strings.TrimSpace(d.Runbook)
		if runbook == "" {
			misses = append(misses, miss{id: d.ID, runbook: "", reason: "missing runbook reference"})
			continue
		}
		normalized := filepath.ToSlash(filepath.Clean(runbook))
		if _, ok := runbooks[normalized]; !ok {
			misses = append(misses, miss{id: d.ID, runbook: runbook, reason: "runbook does not exist on disk"})
		}
	}
	if len(misses) == 0 {
		return nil
	}
	sort.Slice(misses, func(i, j int) bool { return misses[i].id < misses[j].id })
	var lines []string
	for _, m := range misses {
		if m.runbook == "" {
			lines = append(lines, fmt.Sprintf("drill %s: %s", m.id, m.reason))
		} else {
			lines = append(lines, fmt.Sprintf("drill %s: runbook %q %s", m.id, m.runbook, m.reason))
		}
	}
	return fmt.Errorf("strict runbook references invalid: %s", strings.Join(lines, "; "))
}
