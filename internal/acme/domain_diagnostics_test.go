package acme

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDomainDiagnosticsSupportViewRedactsDomainAndError(t *testing.T) {
	expiresAt := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	diagnostics := DomainDiagnostics{
		Domain:               "App.Example.Test",
		Ownership:            DomainOwnershipVerified,
		Routing:              DomainRoutingReady,
		Challenge:            DomainChallengePending,
		CAA:                  DomainCAAAllowed,
		LastError:            "TXT token for app.example.test was rejected",
		CertificateExpiresAt: expiresAt,
	}

	view := diagnostics.SupportView()
	if view.DomainHash == "" || strings.Contains(view.DomainHash, "example") {
		t.Fatalf("DomainHash = %q, want non-empty redacted hash", view.DomainHash)
	}
	if view.Ownership != DomainOwnershipVerified || view.Routing != DomainRoutingReady || view.Challenge != DomainChallengePending || view.CAA != DomainCAAAllowed {
		t.Fatalf("support view statuses changed: %#v", view)
	}
	if !view.HasLastError || !view.CertificateExpiresAt.Equal(expiresAt) {
		t.Fatalf("support view lost safe metadata: %#v", view)
	}

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if strings.Contains(string(data), "example.test") || strings.Contains(string(data), "TXT token") {
		t.Fatalf("support view leaked sensitive values: %s", data)
	}
}

func TestDomainDiagnosticsSupportViewDefaultsUnknownStatuses(t *testing.T) {
	view := (DomainDiagnostics{}).SupportView()
	if view.Ownership != DomainOwnershipUnknown || view.Routing != DomainRoutingUnknown || view.Challenge != DomainChallengeUnknown || view.CAA != DomainCAAUnknown {
		t.Fatalf("statuses = %#v, want unknown defaults", view)
	}
	if view.DomainHash != "" || view.HasLastError {
		t.Fatalf("empty diagnostics should not invent identifiers/errors: %#v", view)
	}
}

func TestDomainDiagnosticsStoreUpsertGetAndListSupportViews(t *testing.T) {
	store := NewDomainDiagnosticsStore()
	store.Upsert(DomainDiagnostics{Domain: "B.Example.Test", Routing: DomainRoutingReady, LastError: " failed "})
	store.Upsert(DomainDiagnostics{Domain: "a.example.test", Ownership: DomainOwnershipVerified})
	store.Upsert(DomainDiagnostics{Domain: " b.example.test ", Routing: DomainRoutingFailed})

	diagnostics, ok := store.Get("B.EXAMPLE.TEST")
	if !ok {
		t.Fatal("expected stored diagnostics")
	}
	if diagnostics.Domain != "b.example.test" || diagnostics.Routing != DomainRoutingFailed || diagnostics.Ownership != DomainOwnershipUnknown {
		t.Fatalf("unexpected normalized diagnostics: %#v", diagnostics)
	}

	view, ok := store.GetSupportView("b.example.test")
	if !ok || view.DomainHash == "" || strings.Contains(view.DomainHash, "example") || view.HasLastError {
		t.Fatalf("unexpected support view: %#v", view)
	}

	views := store.ListSupportViews()
	if len(views) != 2 {
		t.Fatalf("views length = %d, want 2", len(views))
	}
	if views[0].Ownership != DomainOwnershipVerified || views[1].Routing != DomainRoutingFailed {
		t.Fatalf("views not sorted by normalized domain: %#v", views)
	}
}

func TestCAAPreflightResultNormalizesWithoutLookup(t *testing.T) {
	checkedAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	result := CAAPreflightResult{
		Domain:    " App.Example.Test ",
		Records:   []CAARecord{{Tag: "issue", Value: "letsencrypt.org"}},
		CheckedAt: checkedAt,
		LastError: "  none  ",
	}.Normalized()

	if result.Domain != "app.example.test" || result.Status != DomainCAAUnknown || result.LastError != "none" {
		t.Fatalf("unexpected normalized CAA preflight: %#v", result)
	}
	if len(result.Records) != 1 || !result.CheckedAt.Equal(checkedAt) {
		t.Fatalf("CAA preflight lost supplied observations: %#v", result)
	}
}
