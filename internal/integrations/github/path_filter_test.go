// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// makeCommits builds n commits each touching a single unremarkable file that
// does NOT match the "services/api" watch path.
func makeCommits(n int) []PushEventCommit {
	commits := make([]PushEventCommit, 0, n)
	for i := 0; i < n; i++ {
		commits = append(commits, PushEventCommit{
			ID:       fmt.Sprintf("c%d", i),
			Modified: []string{fmt.Sprintf("docs/note-%d.md", i)},
		})
	}
	return commits
}

func TestCommitsMayBeTruncated(t *testing.T) {
	if CommitsMayBeTruncated(makeCommits(19)) {
		t.Fatalf("19 commits must not be treated as truncated")
	}
	if !CommitsMayBeTruncated(makeCommits(20)) {
		t.Fatalf("20 commits must be treated as possibly truncated")
	}
	if !CommitsMayBeTruncated(makeCommits(50)) {
		t.Fatalf("50 commits must be treated as possibly truncated")
	}
}

// (a) A large/truncated push whose delivered commits do not touch the watch
// path must FAIL OPEN and trigger, so a change hidden in a dropped commit is
// not silently skipped.
func TestEvaluateWatchPathsTruncatedFailsOpen(t *testing.T) {
	d := EvaluateWatchPaths([]string{"services/api"}, makeCommits(20))
	if !d.Matched {
		t.Fatalf("truncated push with a watch filter must trigger (fail open), got skip: %+v", d)
	}
	if d.Reason != WatchReasonTruncatedFailOpen {
		t.Fatalf("reason = %q, want %q", d.Reason, WatchReasonTruncatedFailOpen)
	}
	if !d.Truncated {
		t.Fatalf("decision must record Truncated=true")
	}
}

// (b) A small push (below the cap) that touches no watched path must still skip.
func TestEvaluateWatchPathsSmallPushNoMatchSkips(t *testing.T) {
	d := EvaluateWatchPaths([]string{"services/api"}, makeCommits(3))
	if d.Matched {
		t.Fatalf("small push with no matching path must skip, got trigger: %+v", d)
	}
	if d.Reason != WatchReasonNoMatchingPaths {
		t.Fatalf("reason = %q, want %q", d.Reason, WatchReasonNoMatchingPaths)
	}
	if d.Truncated {
		t.Fatalf("small push must not be flagged truncated")
	}
}

func TestEvaluateWatchPathsPreciseMatchWinsOnTruncatedPush(t *testing.T) {
	commits := append(makeCommits(20), PushEventCommit{ID: "match", Added: []string{"services/api/main.go"}})
	d := EvaluateWatchPaths([]string{"services/api"}, commits)
	if !d.Matched || d.Reason != WatchReasonMatchedWatchPath {
		t.Fatalf("expected precise match reason, got %+v", d)
	}
	if d.Root != "services/api" {
		t.Fatalf("root = %q", d.Root)
	}
}

func TestEvaluateWatchPathsNoFilterTriggers(t *testing.T) {
	d := EvaluateWatchPaths(nil, makeCommits(1))
	if !d.Matched || d.Reason != WatchReasonNoWatchPaths {
		t.Fatalf("no filter must trigger, got %+v", d)
	}
	// "/" normalizes to nothing → treated as no filter.
	d = EvaluateWatchPaths([]string{"/"}, makeCommits(1))
	if !d.Matched || d.Reason != WatchReasonNoWatchPaths {
		t.Fatalf("root-slash must be no-filter, got %+v", d)
	}
}

// Router-level: a truncated push routes to a deploy intent tagged with the
// fail-open trigger reason instead of skipping with watch_paths_no_match.
func TestRouterTruncatedPushFailsOpen(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(99, 4242, "acme/web", "main"): {{
			ServiceID: "svc_1",
			Link: ServiceLink{
				InstallationID: 99,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
				WatchPaths:     []string{"services/api"},
			},
		}},
	}}
	r := Router{Lookup: lookup}
	dec, err := r.Route(context.Background(), EventPush, truncatedPushBody("acme/web", "main", testSHA, 20))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if dec.Skipped {
		t.Fatalf("truncated push must not skip: %s", dec.SkipReason)
	}
	if len(dec.Intents) != 1 {
		t.Fatalf("intents = %d", len(dec.Intents))
	}
	if dec.Intents[0].TriggerReason != string(WatchReasonTruncatedFailOpen) {
		t.Fatalf("trigger reason = %q", dec.Intents[0].TriggerReason)
	}
}

func TestRouterSmallPushNoMatchStillSkips(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(99, 4242, "acme/web", "main"): {{
			ServiceID: "svc_1",
			Link: ServiceLink{
				InstallationID: 99,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
				WatchPaths:     []string{"services/api"},
			},
		}},
	}}
	r := Router{Lookup: lookup}
	dec, err := r.Route(context.Background(), EventPush, truncatedPushBody("acme/web", "main", testSHA, 3))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !dec.Skipped || dec.SkipReason != "watch_paths_no_match" {
		t.Fatalf("decision = %+v", dec)
	}
}

// truncatedPushBody builds a push webhook body whose commits[] has n entries,
// none of which touch the watch path.
func truncatedPushBody(repo, branch, sha string, n int) []byte {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(`{"id":"c%d","modified":["docs/note-%d.md"]}`, i, i))
	}
	return []byte(`{
		"ref":"refs/heads/` + branch + `",
		"before":"0000000000000000000000000000000000000000",
		"after":"` + sha + `",
		"repository":{"id":4242,"full_name":"` + repo + `","clone_url":"https://github.com/` + repo + `.git"},
		"sender":{"type":"User"},
		"pusher":{"name":"alice","email":"alice@example.com"},
		"head_commit":{"id":"` + sha + `","message":"feat: big push"},
		"commits":[` + strings.Join(parts, ",") + `],
		"installation":{"id":99}
	}`)
}
