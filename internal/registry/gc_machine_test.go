package registry

import (
	"testing"
	"time"
)

func TestPlanEnforcesPanelLockedRetentionRule(t *testing.T) {
	now := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	tags := []ImageTag{}
	for i := 0; i < 12; i++ {
		tags = append(tags, ImageTag{
			Name:      "tag-" + itoa(i),
			Digest:    "sha256:tag-" + itoa(i),
			PushedAt:  now.Add(-time.Duration(i+1) * time.Hour),
			SizeBytes: 1024,
		})
	}
	tags = append(tags,
		ImageTag{Name: "rollback-window", Digest: "sha256:rb", PushedAt: now.Add(-200 * time.Hour), SizeBytes: 1024},
		ImageTag{Name: "snapshot", Digest: "sha256:ps", PushedAt: now.Add(-300 * time.Hour), SizeBytes: 1024},
	)

	plan, err := Plan(GCInput{
		GeneratedAt:           now,
		Repository:            "midnight/web",
		Tags:                  tags,
		RollbackWindowDigests: []string{"sha256:rb"},
		ProtectedSnapshots:    []string{"sha256:ps"},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.SchemaVersion != GCStateMachineSchemaVersion {
		t.Fatalf("schema = %q", plan.SchemaVersion)
	}
	if plan.KeepLast != PanelLockedKeepLast {
		t.Fatalf("KeepLast = %d, want %d", plan.KeepLast, PanelLockedKeepLast)
	}

	// Of the 12 tags, the last 10 by PushedAt must be retained. Only tag-10
	// and tag-11 are eligible for deletion. The rollback-window and
	// protected-snapshot digests must remain.
	gotDelete := map[string]bool{}
	gotProtected := map[string]string{}
	for _, action := range plan.Actions {
		if action.Delete {
			gotDelete[action.Digest] = true
			continue
		}
		gotProtected[action.Digest] = action.Reason
	}

	if len(gotDelete) != 2 || !gotDelete["sha256:tag-10"] || !gotDelete["sha256:tag-11"] {
		t.Fatalf("delete set = %#v", gotDelete)
	}
	if gotProtected["sha256:rb"] == "" {
		t.Fatalf("rollback-window digest not protected: %#v", gotProtected)
	}
	if gotProtected["sha256:ps"] == "" {
		t.Fatalf("protected snapshot digest not protected: %#v", gotProtected)
	}
}

func TestPlanEmitsExpectedStateMachineEvents(t *testing.T) {
	now := time.Date(2026, 4, 26, 11, 0, 0, 0, time.UTC)
	plan, err := Plan(GCInput{
		GeneratedAt: now,
		Repository:  "midnight/api",
		Tags: []ImageTag{
			{Name: "alpha", Digest: "sha256:alpha", PushedAt: now.Add(-time.Hour)},
		},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	wantOrder := []GCState{
		GCStateQuiesce,
		GCStateLock,
		GCStateDryRun,
		GCStateApply,
		GCStatePostGCVerify,
		GCStateRelease,
	}
	if len(plan.Events) != len(wantOrder) {
		t.Fatalf("events len = %d, want %d", len(plan.Events), len(wantOrder))
	}
	for i, event := range plan.Events {
		if event.To != wantOrder[i] {
			t.Fatalf("event[%d].To = %q, want %q", i, event.To, wantOrder[i])
		}
		if event.Sequence != i+1 {
			t.Fatalf("event[%d].Sequence = %d, want %d", i, event.Sequence, i+1)
		}
		if event.Description == "" {
			t.Fatalf("event[%d].Description must not be empty", i)
		}
	}
}

func TestApplyRefusesUnconfirmedDestructivePlans(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	tags := []ImageTag{}
	for i := 0; i < 12; i++ {
		tags = append(tags, ImageTag{
			Name:      "tag-" + itoa(i),
			Digest:    "sha256:tag-" + itoa(i),
			PushedAt:  now.Add(-time.Duration(i+1) * time.Hour),
			SizeBytes: 1,
		})
	}
	plan, err := Plan(GCInput{GeneratedAt: now, Repository: "midnight/web", Tags: tags})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.Summary.Deletions == 0 {
		t.Fatalf("expected destructive plan, summary = %#v", plan.Summary)
	}

	if _, err := Apply(plan); err == nil {
		t.Fatal("Apply(unconfirmed) error = nil, want refusal")
	}

	plan.ConfirmApply = true
	report, err := Apply(plan)
	if err != nil {
		t.Fatalf("Apply(confirmed) error = %v", err)
	}
	if report.DryRun {
		t.Fatalf("Apply(confirmed).DryRun = true, want false")
	}
	if report.Summary.Deletions != plan.Summary.Deletions {
		t.Fatalf("Apply summary deletions = %d, want %d", report.Summary.Deletions, plan.Summary.Deletions)
	}
	if len(report.Events) != len(plan.Events) {
		t.Fatalf("Apply events len = %d, want %d", len(report.Events), len(plan.Events))
	}
}

func TestApplyRecordsNoOpForCleanPlan(t *testing.T) {
	now := time.Date(2026, 4, 26, 13, 0, 0, 0, time.UTC)
	plan, err := Plan(GCInput{
		GeneratedAt: now,
		Repository:  "midnight/api",
		Tags: []ImageTag{
			{Name: "alpha", Digest: "sha256:alpha", PushedAt: now.Add(-time.Hour)},
		},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.Summary.Deletions != 0 {
		t.Fatalf("Plan summary deletions = %d, want 0", plan.Summary.Deletions)
	}
	report, err := Apply(plan)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if report.AppliedAt != now {
		t.Fatalf("AppliedAt = %s, want %s", report.AppliedAt, now)
	}
	if !report.DryRun {
		t.Fatalf("clean plan must remain dry-run when ConfirmApply=false")
	}
}

func TestPlanRejectsNegativeKeepLast(t *testing.T) {
	now := time.Date(2026, 4, 26, 14, 0, 0, 0, time.UTC)
	if _, err := Plan(GCInput{GeneratedAt: now, Repository: "x", KeepLast: -1}); err == nil {
		t.Fatal("Plan(KeepLast=-1) error = nil, want refusal")
	}
}

func TestPlanRejectsRepositoryMismatch(t *testing.T) {
	now := time.Date(2026, 4, 26, 15, 0, 0, 0, time.UTC)
	_, err := Plan(GCInput{
		GeneratedAt: now,
		Repository:  "midnight/api",
		Tags: []ImageTag{
			{Name: "alpha", Repository: "other/repo", Digest: "sha256:alpha", PushedAt: now.Add(-time.Hour)},
		},
	})
	if err == nil {
		t.Fatal("Plan(mismatched repository) error = nil, want refusal")
	}
}

func TestSortEventsRestoresSequenceOrder(t *testing.T) {
	in := []GCEvent{
		{Sequence: 3, To: GCStateApply},
		{Sequence: 1, To: GCStateQuiesce},
		{Sequence: 2, To: GCStateLock},
	}
	out := SortEvents(in)
	if out[0].Sequence != 1 || out[1].Sequence != 2 || out[2].Sequence != 3 {
		t.Fatalf("SortEvents() = %#v", out)
	}
}

func itoa(i int) string {
	digits := "0123456789"
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	out := ""
	for i > 0 {
		out = string(digits[i%10]) + out
		i /= 10
	}
	if negative {
		return "-" + out
	}
	return out
}
