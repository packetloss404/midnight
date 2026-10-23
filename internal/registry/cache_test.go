package registry

import (
	"testing"
	"time"
)

func TestPlanCacheEvictsOldEntriesBeforeSize(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	plan := PlanCache([]CacheEntry{
		{Repository: "app", Digest: "sha256:pinned", SizeBytes: 90, LastAccessed: now.Add(-48 * time.Hour), Pinned: true},
		{Repository: "app", Digest: "sha256:old", SizeBytes: 40, LastAccessed: now.Add(-48 * time.Hour)},
		{Repository: "app", Digest: "sha256:recent-a", SizeBytes: 30, LastAccessed: now.Add(-2 * time.Hour)},
		{Repository: "app", Digest: "sha256:recent-b", SizeBytes: 30, LastAccessed: now.Add(-1 * time.Hour)},
	}, CachePolicy{Now: now, MaxAge: 24 * time.Hour, MaxBytes: 120})

	byDigest := map[string]CacheAction{}
	for _, action := range plan.Actions {
		byDigest[action.Digest] = action
	}
	if byDigest["sha256:pinned"].Kind != CacheKeep || byDigest["sha256:pinned"].Reason != "pinned" {
		t.Fatalf("pinned action = %+v", byDigest["sha256:pinned"])
	}
	if byDigest["sha256:old"].Kind != CacheEvict || byDigest["sha256:old"].Reason != "older than max age" {
		t.Fatalf("old action = %+v", byDigest["sha256:old"])
	}
	if byDigest["sha256:recent-a"].Kind != CacheEvict || byDigest["sha256:recent-a"].Reason != "over max bytes" {
		t.Fatalf("recent-a action = %+v", byDigest["sha256:recent-a"])
	}
	if byDigest["sha256:recent-b"].Kind != CacheKeep {
		t.Fatalf("recent-b action = %+v", byDigest["sha256:recent-b"])
	}
}

func TestPlanCacheReturnsDeterministicOrder(t *testing.T) {
	plan := PlanCache([]CacheEntry{
		{Repository: "repo-b", Digest: "sha256:2"},
		{Repository: "repo-a", Digest: "sha256:2"},
		{Repository: "repo-a", Digest: "sha256:1"},
	}, CachePolicy{Now: time.Unix(1, 0).UTC()})

	want := []string{"repo-a/sha256:1", "repo-a/sha256:2", "repo-b/sha256:2"}
	if len(plan.Actions) != len(want) {
		t.Fatalf("actions = %+v", plan.Actions)
	}
	for i, action := range plan.Actions {
		got := action.Repository + "/" + action.Digest
		if got != want[i] {
			t.Fatalf("actions[%d] = %s, want %s", i, got, want[i])
		}
	}
}

func TestPlanCacheRefreshesStaleMetadataWithoutEvicting(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	plan := PlanCache([]CacheEntry{
		{Repository: "app", Digest: "sha256:fresh", SizeBytes: 20, LastAccessed: now.Add(-time.Hour), LastVerified: now.Add(-time.Hour)},
		{Repository: "app", Digest: "sha256:stale", SizeBytes: 20, LastAccessed: now.Add(-time.Hour), LastVerified: now.Add(-72 * time.Hour)},
	}, CachePolicy{Now: now, StaleAfter: 24 * time.Hour, MaxBytes: 100})

	byDigest := map[string]CacheAction{}
	for _, action := range plan.Actions {
		byDigest[action.Digest] = action
	}
	if byDigest["sha256:stale"].Kind != CacheRefresh || byDigest["sha256:stale"].Reason != "metadata stale" {
		t.Fatalf("stale action = %+v", byDigest["sha256:stale"])
	}
	if byDigest["sha256:fresh"].Kind != CacheKeep {
		t.Fatalf("fresh action = %+v", byDigest["sha256:fresh"])
	}
}

func TestPlanCacheCanEvictRefreshCandidatesUnderSizePressure(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	plan := PlanCache([]CacheEntry{
		{Repository: "app", Digest: "sha256:oldest", SizeBytes: 60, LastAccessed: now.Add(-3 * time.Hour), LastVerified: now.Add(-72 * time.Hour)},
		{Repository: "app", Digest: "sha256:newest", SizeBytes: 60, LastAccessed: now.Add(-time.Hour), LastVerified: now.Add(-72 * time.Hour)},
	}, CachePolicy{Now: now, StaleAfter: 24 * time.Hour, MaxBytes: 60})

	byDigest := map[string]CacheAction{}
	for _, action := range plan.Actions {
		byDigest[action.Digest] = action
	}
	if byDigest["sha256:oldest"].Kind != CacheEvict || byDigest["sha256:oldest"].Reason != "over max bytes" {
		t.Fatalf("oldest action = %+v", byDigest["sha256:oldest"])
	}
	if byDigest["sha256:newest"].Kind != CacheRefresh {
		t.Fatalf("newest action = %+v", byDigest["sha256:newest"])
	}
}
