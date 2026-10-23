package registry

import (
	"testing"
	"time"
)

func TestRetentionPlanDeletions(t *testing.T) {
	plan := RetentionPlan{Actions: []RetentionAction{
		{Digest: "sha256:keep"},
		{Digest: "sha256:delete", Delete: true},
	}}

	deletions := plan.Deletions()
	if len(deletions) != 1 || deletions[0].Digest != "sha256:delete" {
		t.Fatalf("Deletions() = %#v", deletions)
	}
}

func TestPlanGCDryRunPreservesPinsAndLastN(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	plan := PlanGCDryRun(GCPlanRequest{
		GeneratedAt: now,
		Tags: []ImageTag{
			{Repository: "app", Name: "newest", Digest: "sha256:last", SizeBytes: 10, PushedAt: now.Add(-time.Hour)},
			{Repository: "app", Name: "active", Digest: "sha256:active", SizeBytes: 20, PushedAt: now.Add(-2 * time.Hour)},
			{Repository: "app", Name: "rollback", Digest: "sha256:rollback", SizeBytes: 30, PushedAt: now.Add(-3 * time.Hour)},
			{Repository: "app", Name: "old", Digest: "sha256:old", SizeBytes: 40, PushedAt: now.Add(-4 * time.Hour)},
		},
		Pins: []ImagePin{
			{Repository: "app", Digest: "sha256:active", Active: true},
			{Repository: "app", Digest: "sha256:rollback", RollbackUntil: now.Add(time.Hour)},
		},
		Rules: []RetentionRule{{Repository: "app", KeepLast: 1}},
	})

	if !plan.DryRun {
		t.Fatal("PlanGCDryRun() returned mutating plan")
	}

	deletions := plan.Deletions()
	if len(deletions) != 1 || deletions[0].Digest != "sha256:old" {
		t.Fatalf("Deletions() = %#v", deletions)
	}
	if deletions[0].SizeBytes != 40 {
		t.Fatalf("deletion size = %d, want 40", deletions[0].SizeBytes)
	}

	protected := map[string]string{}
	for _, action := range plan.Actions {
		if !action.Delete {
			protected[action.Digest] = action.Reason
		}
	}
	for digest, reason := range map[string]string{
		"sha256:last":     "last-N tag",
		"sha256:active":   "active pin",
		"sha256:rollback": "rollback window",
	} {
		if protected[digest] != reason {
			t.Fatalf("protected[%s] = %q, want %q; actions = %#v", digest, protected[digest], reason, plan.Actions)
		}
	}
}

func TestPlanGCDryRunRefusesProtectedTags(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	plan := PlanGCDryRun(GCPlanRequest{
		GeneratedAt: now,
		Tags: []ImageTag{
			{Repository: "app", Name: "v1.0.0", Digest: "sha256:release", SizeBytes: 10, PushedAt: now.Add(-4 * time.Hour)},
			{Repository: "app", Name: "old", Digest: "sha256:old", SizeBytes: 20, PushedAt: now.Add(-5 * time.Hour)},
		},
		Rules: []RetentionRule{{Repository: "app", KeepTags: []string{"v1.0.0"}}},
	})

	if len(plan.Actions) != 2 {
		t.Fatalf("actions = %#v", plan.Actions)
	}
	for _, action := range plan.Actions {
		switch action.Digest {
		case "sha256:release":
			if action.Delete || action.Reason != "protected tag" || len(action.Tags) != 1 || action.Tags[0] != "v1.0.0" {
				t.Fatalf("release action = %#v", action)
			}
		case "sha256:old":
			if !action.Delete || action.Reason != "unprotected" {
				t.Fatalf("old action = %#v", action)
			}
		default:
			t.Fatalf("unexpected action = %#v", action)
		}
	}
}

func TestPlanGCDryRunRefusesProtectedDigestWithMultipleTags(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	plan := PlanGCDryRun(GCPlanRequest{
		GeneratedAt: now,
		Tags: []ImageTag{
			{Repository: "app", Name: "stable", Digest: "sha256:shared", SizeBytes: 10, PushedAt: now.Add(-4 * time.Hour)},
			{Repository: "app", Name: "v1.0.0", Digest: "sha256:shared", SizeBytes: 10, PushedAt: now.Add(-5 * time.Hour)},
		},
		Rules: []RetentionRule{{Repository: "app", KeepTags: []string{"v1.0.0"}}},
	})

	if len(plan.Actions) != 1 {
		t.Fatalf("actions = %#v", plan.Actions)
	}
	action := plan.Actions[0]
	if action.Delete || action.Reason != "protected tag" || len(action.Tags) != 2 {
		t.Fatalf("action = %#v", action)
	}
}

func TestApplyGCPlanRequiresExplicitConfirmation(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	plan := RetentionPlan{
		GeneratedAt: now,
		DryRun:      true,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin"},
			{Repository: "app", Digest: "sha256:old", Reason: "unprotected", Delete: true},
		},
	}

	dryRun := ApplyGCPlan(GCApplyRequest{Plan: plan})
	if !dryRun.DryRun || dryRun.ConfirmApply || len(dryRun.Actions) != 0 {
		t.Fatalf("ApplyGCPlan(unconfirmed) = %#v", dryRun)
	}

	applied := ApplyGCPlan(GCApplyRequest{Plan: plan, ConfirmApply: true})
	if applied.DryRun || !applied.ConfirmApply || len(applied.Actions) != 1 || applied.Actions[0].Digest != "sha256:old" {
		t.Fatalf("ApplyGCPlan(confirmed) = %#v", applied)
	}
}

func TestApplyGCPlanAllowsUnconfirmedNoop(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	result := ApplyGCPlan(GCApplyRequest{Plan: RetentionPlan{
		GeneratedAt: now,
		DryRun:      true,
		Actions: []RetentionAction{
			{Repository: "app", Digest: "sha256:keep", Reason: "active pin", SizeBytes: 10},
		},
	}})

	if result.DryRun || result.ConfirmApply || len(result.Actions) != 0 {
		t.Fatalf("ApplyGCPlan(noop) = %#v", result)
	}
}
