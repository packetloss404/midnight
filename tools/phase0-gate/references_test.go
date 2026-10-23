package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReferenceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "runbooks"), 0o755); err != nil {
		t.Fatalf("MkdirAll runbooks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll docs: %v", err)
	}
	for _, name := range []string{"backup-restore.md", "support-bundle.md", "key-rotation.md"} {
		if err := os.WriteFile(filepath.Join(root, "docs", "runbooks", name), []byte("# runbook\n"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	registry := "# Error Codes\n\n| Code | Owner | Notes |\n|---|---|---|\n| `INTERNAL_ERROR` | S03 | fallback |\n| `BACKUP_OFFHOST_MISSING` | S07 | warning |\n"
	if err := os.WriteFile(filepath.Join(root, "docs", "error-codes.md"), []byte(registry), 0o644); err != nil {
		t.Fatalf("WriteFile registry: %v", err)
	}
	return root
}

func TestLoadReferenceCatalogParsesRegistryAndRunbooks(t *testing.T) {
	root := writeReferenceFixture(t)

	catalog, err := loadReferenceCatalog(root)
	if err != nil {
		t.Fatalf("loadReferenceCatalog() error = %v", err)
	}
	if _, ok := catalog.ReasonCodes["INTERNAL_ERROR"]; !ok {
		t.Fatalf("INTERNAL_ERROR not in catalog: %+v", catalog.ReasonCodes)
	}
	if _, ok := catalog.ReasonCodes["BACKUP_OFFHOST_MISSING"]; !ok {
		t.Fatalf("BACKUP_OFFHOST_MISSING not in catalog: %+v", catalog.ReasonCodes)
	}
	for _, code := range phase0DrillReasonCodeAllowlist() {
		if _, ok := catalog.ReasonCodes[code]; !ok {
			t.Fatalf("phase 0 reason code %q not present", code)
		}
	}
	for _, ev := range phase0DrillEventAllowlist() {
		if _, ok := catalog.Events[ev]; !ok {
			t.Fatalf("phase 0 event %q not present", ev)
		}
	}
	if _, ok := catalog.Runbooks["docs/runbooks/backup-restore.md"]; !ok {
		t.Fatalf("expected runbook in catalog: %+v", catalog.Runbooks)
	}
}

func TestLoadReferenceCatalogMissingRegistry(t *testing.T) {
	root := t.TempDir()
	_, err := loadReferenceCatalog(root)
	if err == nil {
		t.Fatal("loadReferenceCatalog() error = nil, want missing registry error")
	}
}

func TestValidateDrillManifestReferencesAcceptsRealManifest(t *testing.T) {
	manifestData, err := os.ReadFile(filepath.Join("..", "..", "test", "drills", "manifest.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	manifest, err := parseDrillManifest(manifestData)
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	root := filepath.Join("..", "..")
	catalog, err := loadReferenceCatalog(root)
	if err != nil {
		t.Fatalf("loadReferenceCatalog() error = %v", err)
	}
	if err := validateDrillManifestReferences(manifest, catalog); err != nil {
		t.Fatalf("validateDrillManifestReferences() error = %v", err)
	}
}

func TestValidateDrillManifestReferencesRejectsUnknownReasonCode(t *testing.T) {
	root := writeReferenceFixture(t)
	catalog, err := loadReferenceCatalog(root)
	if err != nil {
		t.Fatalf("loadReferenceCatalog() error = %v", err)
	}
	manifest := drillManifest{
		Owner:   "S08",
		Version: "1",
		Drills: []drill{{
			ID:         "D-01",
			Title:      "x",
			Status:     "placeholder",
			ReasonCode: "phase0.unknown.code",
			Event:      "alert.api_target_down",
			Diagnostic: "x",
			Runbook:    "docs/runbooks/backup-restore.md",
		}},
	}
	err = validateDrillManifestReferences(manifest, catalog)
	if err == nil || !strings.Contains(err.Error(), "reason-code") {
		t.Fatalf("validateDrillManifestReferences() error = %v, want reason-code failure", err)
	}
}

func TestValidateDrillManifestReferencesRejectsUnknownEvent(t *testing.T) {
	root := writeReferenceFixture(t)
	catalog, err := loadReferenceCatalog(root)
	if err != nil {
		t.Fatalf("loadReferenceCatalog() error = %v", err)
	}
	manifest := drillManifest{
		Owner:   "S08",
		Version: "1",
		Drills: []drill{{
			ID:         "D-01",
			Title:      "x",
			Status:     "placeholder",
			ReasonCode: "phase0.alert.api-target-down",
			Event:      "phase0.unknown.event",
			Diagnostic: "x",
			Runbook:    "docs/runbooks/backup-restore.md",
		}},
	}
	err = validateDrillManifestReferences(manifest, catalog)
	if err == nil || !strings.Contains(err.Error(), "event") {
		t.Fatalf("validateDrillManifestReferences() error = %v, want event failure", err)
	}
}

func TestValidateDrillManifestReferencesRejectsMissingRunbook(t *testing.T) {
	root := writeReferenceFixture(t)
	catalog, err := loadReferenceCatalog(root)
	if err != nil {
		t.Fatalf("loadReferenceCatalog() error = %v", err)
	}
	manifest := drillManifest{
		Owner:   "S08",
		Version: "1",
		Drills: []drill{{
			ID:         "D-01",
			Title:      "x",
			Status:     "placeholder",
			ReasonCode: "phase0.alert.api-target-down",
			Event:      "alert.api_target_down",
			Diagnostic: "x",
			Runbook:    "docs/runbooks/missing.md",
		}},
	}
	err = validateDrillManifestReferences(manifest, catalog)
	if err == nil || !strings.Contains(err.Error(), "runbook") {
		t.Fatalf("validateDrillManifestReferences() error = %v, want runbook failure", err)
	}
}

func TestValidateDrillManifestWithReferencesIntegratesStructuralValidator(t *testing.T) {
	if err := validateDrillManifestWithReferences([]byte("version: 2\nowner: S08\ndrills: []\n"), "."); err == nil {
		t.Fatal("validateDrillManifestWithReferences() error = nil, want structural failure on bad version")
	}
}

func TestValidateRunbookReferencesStrictAcceptsRealManifest(t *testing.T) {
	manifestData, err := os.ReadFile(filepath.Join("..", "..", "test", "drills", "manifest.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	manifest, err := parseDrillManifest(manifestData)
	if err != nil {
		t.Fatalf("parseDrillManifest() error = %v", err)
	}
	if err := ValidateRunbookReferencesStrict(manifest, filepath.Join("..", "..")); err != nil {
		t.Fatalf("ValidateRunbookReferencesStrict() error = %v", err)
	}
}

func TestValidateRunbookReferencesStrictRejectsMissingFile(t *testing.T) {
	root := writeReferenceFixture(t)
	manifest := drillManifest{
		Owner:   "S08",
		Version: "1",
		Drills: []drill{{
			ID:         "D-01",
			Title:      "x",
			Status:     "placeholder",
			ReasonCode: "phase0.alert.api-target-down",
			Event:      "alert.api_target_down",
			Diagnostic: "x",
			Runbook:    "docs/runbooks/does-not-exist.md",
		}},
	}
	err := ValidateRunbookReferencesStrict(manifest, root)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("ValidateRunbookReferencesStrict() error = %v, want missing-file failure", err)
	}
}

func TestValidateRunbookReferencesStrictRejectsEmptyRunbookField(t *testing.T) {
	root := writeReferenceFixture(t)
	manifest := drillManifest{
		Owner:   "S08",
		Version: "1",
		Drills: []drill{{
			ID:         "D-01",
			Title:      "x",
			Status:     "placeholder",
			ReasonCode: "phase0.alert.api-target-down",
			Event:      "alert.api_target_down",
			Diagnostic: "x",
			Runbook:    "",
		}},
	}
	err := ValidateRunbookReferencesStrict(manifest, root)
	if err == nil || !strings.Contains(err.Error(), "missing runbook") {
		t.Fatalf("ValidateRunbookReferencesStrict() error = %v, want empty-field failure", err)
	}
}
