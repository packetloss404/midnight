package doctor

import (
	"context"
	"testing"
)

func TestValidateChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		checks  []Check
		wantErr bool
	}{
		{name: "valid registry", checks: Checks()},
		{name: "empty id", checks: []Check{{ID: "DOC-ONE"}, {ID: " "}}, wantErr: true},
		{name: "duplicate id", checks: []Check{{ID: "DOC-ONE"}, {ID: "DOC-ONE"}}, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateChecks(tt.checks)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateChecks() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChecksIncludeCatalogMetadata(t *testing.T) {
	t.Parallel()

	for _, check := range Checks() {
		if check.DocsURL == "" {
			t.Fatalf("check %s DocsURL is empty", check.ID)
		}
		if check.SafeFix.Available {
			t.Fatalf("check %s SafeFix.Available = true, want Phase 0 placeholder false", check.ID)
		}
	}
}

func TestRunChecks(t *testing.T) {
	t.Parallel()

	checks := []Check{
		{ID: "DOC-ONE", Description: "one", Severity: "fatal", Run: func(context.Context) Result { return Result{Status: StatusPass} }},
		{ID: "DOC-TWO", Description: "two", Severity: "warn"},
	}

	results, err := RunChecks(context.Background(), checks)
	if err != nil {
		t.Fatalf("RunChecks() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].ID != "DOC-ONE" || results[0].Status != StatusPass {
		t.Fatalf("results[0] = {ID:%q Status:%q}, want {ID:DOC-ONE Status:pass}", results[0].ID, results[0].Status)
	}
	if results[0].DocsURL != "" {
		t.Fatalf("results[0].DocsURL = %q, want empty for check without docs URL", results[0].DocsURL)
	}
	if results[1].ID != "DOC-TWO" || results[1].Status != StatusConfigError {
		t.Fatalf("results[1] = {ID:%q Status:%q}, want {ID:DOC-TWO Status:config_error}", results[1].ID, results[1].Status)
	}
}

func TestRunChecksRejectsDuplicateIDs(t *testing.T) {
	t.Parallel()

	_, err := RunChecks(context.Background(), []Check{{ID: "DOC-ONE"}, {ID: "DOC-ONE"}})
	if err == nil {
		t.Fatal("RunChecks() error = nil, want duplicate ID error")
	}
}
