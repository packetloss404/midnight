package acme

import (
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/router"
)

func TestNewIngressDiagnosticsReviewSummaryBlocksAndSortsSupportSafeOutput(t *testing.T) {
	summary := NewIngressDiagnosticsReviewSummary(IngressDiagnosticsReviewInput{
		Domains: []DomainDiagnosticsSupportView{
			{DomainHash: "z-redacted", Ownership: DomainOwnershipVerified, Routing: DomainRoutingReady, Challenge: DomainChallengeReady, CAA: DomainCAAAllowed},
			{DomainHash: "a-redacted", Ownership: DomainOwnershipVerified, Routing: DomainRoutingFailed, Challenge: DomainChallengeReady, CAA: DomainCAABlocked, HasLastError: true},
		},
		DomainSummaries: []DomainDiagnosticSummary{
			{Actions: []DomainDiagnosticAction{DomainDiagnosticActionReplaceCertificate, DomainDiagnosticActionFixCAA}},
			{Actions: []DomainDiagnosticAction{DomainDiagnosticActionFixCAA}},
		},
		RoutePublication: router.RoutePublicationSummary{
			Writes:      1,
			Skips:       2,
			Errors:      1,
			StaleCount:  1,
			NextActions: []string{"review support-safe route artifact diagnostics", "regenerate stale route artifacts before publishing"},
		},
	})

	if summary.Status != IngressDiagnosticsBlocked {
		t.Fatalf("Status = %q, want blocked", summary.Status)
	}
	if summary.DomainCount != 2 || summary.DomainErrorCount != 1 || summary.RouteStaleCount != 1 || summary.RouteErrorCount != 1 {
		t.Fatalf("unexpected counts: %#v", summary)
	}
	assertIngressActionCounts(t, summary.DomainActionCounts, []IngressDiagnosticsActionCount{
		{Action: "fix-caa", Count: 2},
		{Action: "replace-certificate", Count: 1},
	})
	assertIngressActionCounts(t, summary.RouteActionCounts, []IngressDiagnosticsActionCount{
		{Action: "error", Count: 1},
		{Action: "skip", Count: 2},
		{Action: "write", Count: 1},
	})

	encoded, err := MarshalIngressDiagnosticsReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalIngressDiagnosticsReviewSummaryJSON returned error: %v", err)
	}
	second, err := MarshalIngressDiagnosticsReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalIngressDiagnosticsReviewSummaryJSON returned second error: %v", err)
	}
	if string(encoded) != string(second) {
		t.Fatalf("JSON was not deterministic:\nfirst: %s\nsecond: %s", encoded, second)
	}
	for _, leaked := range []string{"example.test", "TXT token", "http://127.0.0.1", "secret"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("review summary leaked %q: %s", leaked, encoded)
		}
	}
	for _, want := range []string{
		"regenerate stale route artifacts before publishing",
		"replace expired certificates before routing traffic",
		"review CAA policy before issuing certificates",
		"review support-safe route artifact diagnostics",
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON missing next action %q: %s", want, encoded)
		}
	}
}

func TestNewIngressDiagnosticsReviewSummaryDerivesHealthyAndNeedsReview(t *testing.T) {
	healthy := NewIngressDiagnosticsReviewSummary(IngressDiagnosticsReviewInput{
		Domains: []DomainDiagnosticsSupportView{{
			DomainHash: "redacted",
			Ownership:  DomainOwnershipVerified,
			Routing:    DomainRoutingReady,
			Challenge:  DomainChallengeReady,
			CAA:        DomainCAAAllowed,
		}},
		DomainSummaries: []DomainDiagnosticSummary{{Actions: []DomainDiagnosticAction{DomainDiagnosticActionMonitor}}},
		RoutePublication: router.RoutePublicationSummary{
			Skips:       1,
			NextActions: []string{"no publication needed; artifacts already current"},
		},
	})
	if healthy.Status != IngressDiagnosticsHealthy {
		t.Fatalf("healthy status = %q, want healthy", healthy.Status)
	}

	needsReview := NewIngressDiagnosticsReviewSummary(IngressDiagnosticsReviewInput{
		Domains: []DomainDiagnosticsSupportView{{
			DomainHash: "redacted",
			Ownership:  DomainOwnershipPending,
			Routing:    DomainRoutingReady,
			Challenge:  DomainChallengePending,
			CAA:        DomainCAAUnknown,
		}},
		DomainSummaries: []DomainDiagnosticSummary{{Actions: []DomainDiagnosticAction{DomainDiagnosticActionCollectMoreTelemetry}}},
	})
	if needsReview.Status != IngressDiagnosticsNeedsReview {
		t.Fatalf("needsReview status = %q, want needs-review", needsReview.Status)
	}
}

func TestIngressDiagnosticsReviewSummaryValidateRejectsUnsortedActions(t *testing.T) {
	summary := IngressDiagnosticsReviewSummary{
		Status: IngressDiagnosticsHealthy,
		RouteActionCounts: []IngressDiagnosticsActionCount{
			{Action: "write", Count: 1},
			{Action: "error", Count: 1},
		},
		NextActions: []string{"monitor ingress diagnostics"},
	}

	if err := summary.Validate(); err == nil {
		t.Fatal("Validate returned nil, want unsorted action error")
	}
}

func assertIngressActionCounts(t *testing.T, got, want []IngressDiagnosticsActionCount) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("action counts length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("action count[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
