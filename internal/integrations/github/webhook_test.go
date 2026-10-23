// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func computeSig(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookVerifierHappy(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	header := computeSig("topsecret", body)
	v := NewWebhookVerifier("topsecret")
	if err := v.Verify(header, body); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestWebhookVerifierBadSignature(t *testing.T) {
	v := NewWebhookVerifier("topsecret")
	if err := v.Verify("sha256="+hex.EncodeToString(make([]byte, 32)), []byte("body")); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("expected invalid, got %v", err)
	}
}

func TestWebhookVerifierBadPrefix(t *testing.T) {
	v := NewWebhookVerifier("topsecret")
	if err := v.Verify("sha1=abc", []byte("body")); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("expected invalid, got %v", err)
	}
}

func TestWebhookVerifierEmptySecret(t *testing.T) {
	v := NewWebhookVerifier("")
	if err := v.Verify(computeSig("x", []byte("body")), []byte("body")); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("expected invalid for empty secret")
	}
}

func TestWatchPathsMatchEmpty(t *testing.T) {
	matched, root := WatchPathsMatch(nil, []PushEventCommit{{Added: []string{"main.go"}}})
	if !matched || root != "" {
		t.Fatalf("expected match-all, got matched=%v root=%q", matched, root)
	}
}

func TestWatchPathsMatchPrefix(t *testing.T) {
	commits := []PushEventCommit{
		{Modified: []string{"docs/readme.md"}},
		{Added: []string{"services/api/main.go"}},
	}
	matched, root := WatchPathsMatch([]string{"services/api"}, commits)
	if !matched || root != "services/api" {
		t.Fatalf("matched=%v root=%q", matched, root)
	}
}

func TestWatchPathsMatchNoMatch(t *testing.T) {
	commits := []PushEventCommit{{Modified: []string{"docs/readme.md"}}}
	matched, _ := WatchPathsMatch([]string{"services/api"}, commits)
	if matched {
		t.Fatalf("expected no match")
	}
}

func TestWatchPathsMatchRootSlashIsNoFilter(t *testing.T) {
	matched, _ := WatchPathsMatch([]string{"/"}, []PushEventCommit{{Modified: []string{"x.md"}}})
	if !matched {
		t.Fatalf("expected match")
	}
}

type fakeLookup struct {
	byKey   map[string][]LinkedService
	err     error
	calls   []lookupCall
	atCalls []temporalLookupCall
}

type lookupCall struct {
	installationID int64
	repositoryID   int64
	repo           string
	branch         string
}

type temporalLookupCall struct {
	lookupCall
	receivedAt time.Time
}

func sourceKey(installationID, repositoryID int64, repo, branch string) string {
	return fmt.Sprintf("%d:%d:%s@%s", installationID, repositoryID, repo, branch)
}

func (f *fakeLookup) FindByRepoBranch(_ context.Context, installationID, repositoryID int64, repo, branch string) ([]LinkedService, error) {
	f.calls = append(f.calls, lookupCall{installationID, repositoryID, repo, branch})
	if f.err != nil {
		return nil, f.err
	}
	return f.byKey[sourceKey(installationID, repositoryID, repo, branch)], nil
}

func (f *fakeLookup) FindByRepoBranchAt(
	_ context.Context,
	installationID,
	repositoryID int64,
	repo,
	branch string,
	receivedAt time.Time,
) ([]LinkedService, error) {
	f.atCalls = append(f.atCalls, temporalLookupCall{
		lookupCall: lookupCall{installationID, repositoryID, repo, branch},
		receivedAt: receivedAt,
	})
	if f.err != nil {
		return nil, f.err
	}
	return f.byKey[sourceKey(installationID, repositoryID, repo, branch)], nil
}

const (
	testInstallationID int64 = 99
	testRepositoryID   int64 = 4242
	testSHA                  = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
)

func pushBody(repo, branch, sha string) []byte {
	return []byte(`{
		"ref":"refs/heads/` + branch + `",
		"before":"0000000000000000000000000000000000000000",
		"after":"` + sha + `",
		"repository":{"id":4242,"full_name":"` + repo + `","clone_url":"https://github.com/` + repo + `.git"},
		"sender":{"type":"User"},
		"pusher":{"name":"alice","email":"alice@example.com"},
		"head_commit":{"id":"` + sha + `","message":"feat: ship"},
		"commits":[{"id":"` + sha + `","modified":["services/api/main.go"]}],
		"installation":{"id":99}
	}`)
}

func TestRouterRoutesPushToMatchingService(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(testInstallationID, testRepositoryID, "acme/web", "main"): {{
			ServiceID:           "svc_1",
			ProjectID:           "prj_1",
			WorkspaceID:         "wrk_1",
			LinkResourceVersion: 3,
			Link: ServiceLink{
				InstallationID: 99,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
			},
		}},
	}}
	r := Router{Lookup: lookup}
	dec, err := r.Route(context.Background(), EventPush, pushBody("acme/web", "main", testSHA))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if dec.Skipped {
		t.Fatalf("skipped: %s", dec.SkipReason)
	}
	if len(dec.Intents) != 1 {
		t.Fatalf("intents = %d", len(dec.Intents))
	}
	intent := dec.Intents[0]
	if intent.ServiceID != "svc_1" || intent.Repo != "acme/web" || intent.SHA != testSHA {
		t.Errorf("intent = %+v", intent)
	}
	if intent.InstallationID != 99 {
		t.Errorf("installation_id = %d", intent.InstallationID)
	}
	if intent.IdempotencyKey != "gh-push-99-4242-v3-"+testSHA+"-svc_1" {
		t.Errorf("idempotency = %s", intent.IdempotencyKey)
	}
	if len(lookup.calls) != 1 || lookup.calls[0] != (lookupCall{99, 4242, "acme/web", "main"}) {
		t.Fatalf("lookup calls = %#v", lookup.calls)
	}
}

func TestRouterRouteAtUsesTemporalAuthorityAndSnapshotsEveryFence(t *testing.T) {
	receivedAt := time.Date(2026, 7, 29, 15, 4, 5, 0, time.UTC)
	installationCreatedAt := receivedAt.Add(-5 * time.Hour)
	installationUpdatedAt := receivedAt.Add(-4 * time.Hour)
	repositoryCreatedAt := receivedAt.Add(-3 * time.Hour)
	repositoryUpdatedAt := receivedAt.Add(-2 * time.Hour)
	repositoryVerifiedAt := receivedAt.Add(-150 * time.Minute)
	linkCreatedAt := receivedAt.Add(-90 * time.Minute)
	linkUpdatedAt := receivedAt.Add(-time.Hour)
	linkVerifiedAt := receivedAt.Add(-75 * time.Minute)
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(testInstallationID, testRepositoryID, "acme/web", "main"): {{
			ServiceID:                        "svc_temporal",
			ProjectID:                        "prj_temporal",
			WorkspaceID:                      "wrk_temporal",
			InstallationGrantResourceVersion: 7,
			InstallationGrantCreatedAt:       installationCreatedAt,
			InstallationGrantUpdatedAt:       installationUpdatedAt,
			RepositoryGrantResourceVersion:   11,
			RepositoryGrantCreatedAt:         repositoryCreatedAt,
			RepositoryGrantUpdatedAt:         repositoryUpdatedAt,
			RepositoryAccessVerifiedAt:       repositoryVerifiedAt,
			LinkResourceVersion:              13,
			LinkCreatedAt:                    linkCreatedAt,
			LinkUpdatedAt:                    linkUpdatedAt,
			LinkRepositoryAccessVerifiedAt:   linkVerifiedAt,
			Link: ServiceLink{
				InstallationID: 99,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
			},
		}},
	}}

	decision, err := (Router{Lookup: lookup}).RouteAt(
		context.Background(),
		EventPush,
		pushBody("acme/web", "main", testSHA),
		receivedAt,
	)
	if err != nil {
		t.Fatalf("temporal route: %v", err)
	}
	if decision.Skipped || len(decision.Intents) != 1 {
		t.Fatalf("temporal decision = %#v", decision)
	}
	if len(lookup.calls) != 0 ||
		len(lookup.atCalls) != 1 ||
		lookup.atCalls[0].lookupCall != (lookupCall{99, 4242, "acme/web", "main"}) ||
		!lookup.atCalls[0].receivedAt.Equal(receivedAt) {
		t.Fatalf("lookup calls = current %#v temporal %#v", lookup.calls, lookup.atCalls)
	}
	intent := decision.Intents[0]
	if intent.InstallationGrantResourceVersion != 7 ||
		intent.RepositoryGrantResourceVersion != 11 ||
		intent.LinkVersion != 13 ||
		!intent.InstallationGrantCreatedAt.Equal(installationCreatedAt) ||
		!intent.InstallationGrantUpdatedAt.Equal(installationUpdatedAt) ||
		!intent.RepositoryGrantCreatedAt.Equal(repositoryCreatedAt) ||
		!intent.RepositoryGrantUpdatedAt.Equal(repositoryUpdatedAt) ||
		!intent.RepositoryAccessVerifiedAt.Equal(repositoryVerifiedAt) ||
		!intent.LinkCreatedAt.Equal(linkCreatedAt) ||
		!intent.LinkUpdatedAt.Equal(linkUpdatedAt) ||
		!intent.LinkRepositoryAccessVerifiedAt.Equal(linkVerifiedAt) {
		t.Fatalf("temporal authority snapshot = %#v", intent)
	}
}

func TestRouterRouteAtFailsClosedWithoutReceiptOrTemporalLookup(t *testing.T) {
	router := Router{Lookup: currentOnlyLookup{}}
	if _, err := router.RouteAt(
		context.Background(),
		EventPush,
		pushBody("acme/web", "main", testSHA),
		time.Time{},
	); !errors.Is(err, ErrWebhookReceiptTimeRequired) {
		t.Fatalf("zero received_at error = %v", err)
	}
	decision, err := router.RouteAt(
		context.Background(),
		EventPush,
		pushBody("acme/web", "main", testSHA),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("route without temporal lookup: %v", err)
	}
	if !decision.Skipped || decision.SkipReason != "temporal_service_lookup_required" {
		t.Fatalf("decision without temporal lookup = %#v", decision)
	}
}

type currentOnlyLookup struct{}

func (currentOnlyLookup) FindByRepoBranch(
	context.Context,
	int64,
	int64,
	string,
	string,
) ([]LinkedService, error) {
	return nil, nil
}

func TestRouterMatchesCanonicalRepositoryNameCaseInsensitively(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(testInstallationID, testRepositoryID, "Acme/Web", "main"): {{
			ServiceID:           "svc_1",
			ProjectID:           "prj_1",
			WorkspaceID:         "wrk_1",
			LinkResourceVersion: 3,
			Link: ServiceLink{
				InstallationID: 99,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
			},
		}},
	}}
	decision, err := (Router{Lookup: lookup}).Route(
		context.Background(),
		EventPush,
		pushBody("Acme/Web", "main", testSHA),
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Skipped || len(decision.Intents) != 1 {
		t.Fatalf("decision = %#v, want one intent", decision)
	}
}

func TestRouterSkipsBranchDeletionWithoutLookup(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{}}
	body := strings.Replace(
		string(pushBody("acme/web", "main", testSHA)),
		`"after":"`+testSHA+`",`,
		`"after":"0000000000000000000000000000000000000000","deleted":true,`,
		1,
	)
	decision, err := (Router{Lookup: lookup}).Route(context.Background(), EventPush, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Skipped || decision.SkipReason != "branch_deleted" || len(lookup.calls) != 0 {
		t.Fatalf("decision=%#v calls=%#v", decision, lookup.calls)
	}
}

func TestRouterRejectsLookupResultFromAnotherInstallation(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(1234, testRepositoryID, "acme/web", "main"): {{
			ServiceID: "svc_1",
			Link: ServiceLink{
				InstallationID: 7,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
			},
		}},
	}}
	body := strings.Replace(string(pushBody("acme/web", "main", testSHA)), `"installation":{"id":99}`, `"installation":{"id":1234}`, 1)
	decision, err := (Router{Lookup: lookup}).Route(context.Background(), EventPush, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Skipped || len(decision.Intents) != 0 {
		t.Fatalf("decision = %#v, want mismatched result skipped", decision)
	}
}

func TestRouterSkipsPushOnUnlinkedBranch(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(99, 4242, "acme/web", "main"): {{ServiceID: "svc_1", Link: ServiceLink{Repo: "acme/web", Branch: "main", InstallationID: 99, RepositoryID: 4242}}},
	}}
	r := Router{Lookup: lookup}
	dec, err := r.Route(context.Background(), EventPush, pushBody("acme/web", "develop", testSHA))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !dec.Skipped {
		t.Fatalf("expected skip")
	}
}

func TestRouterAppliesWatchPathsFilter(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(99, 4242, "acme/web", "main"): {{
			ServiceID: "svc_1",
			Link: ServiceLink{
				InstallationID: 99,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
				WatchPaths:     []string{"services/worker"},
			},
		}},
	}}
	r := Router{Lookup: lookup}
	dec, err := r.Route(context.Background(), EventPush, pushBody("acme/web", "main", testSHA))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !dec.Skipped || dec.SkipReason != "watch_paths_no_match" {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestRouterMultipleServicesOnSameBranch(t *testing.T) {
	lookup := &fakeLookup{byKey: map[string][]LinkedService{
		sourceKey(99, 4242, "acme/web", "main"): {
			{ServiceID: "svc_a", Link: ServiceLink{Repo: "acme/web", Branch: "main", InstallationID: 99, RepositoryID: 4242}},
			{ServiceID: "svc_b", Link: ServiceLink{
				Repo:           "acme/web",
				Branch:         "main",
				InstallationID: 99,
				RepositoryID:   4242,
				WatchPaths:     []string{"services/api"},
				RootDirectory:  "apps/web",
				ConfigPath:     "deploy/midnight.toml",
			}},
		},
	}}
	r := Router{Lookup: lookup}
	dec, err := r.Route(context.Background(), EventPush, pushBody("acme/web", "main", testSHA))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(dec.Intents) != 2 {
		t.Fatalf("intents = %d", len(dec.Intents))
	}
	// Watch-path matching decides whether to trigger; it must not rewrite the
	// independently configured build root or config path.
	if dec.Intents[1].RootDirectory != "apps/web" || dec.Intents[1].ConfigPath != "deploy/midnight.toml" {
		t.Errorf("source layout = root %q config %q", dec.Intents[1].RootDirectory, dec.Intents[1].ConfigPath)
	}
	if dec.Intents[1].TriggerReason != string(WatchReasonMatchedWatchPath) {
		t.Errorf("trigger reason = %q", dec.Intents[1].TriggerReason)
	}
}

func TestRouterPingSkipped(t *testing.T) {
	r := Router{}
	dec, err := r.Route(context.Background(), EventPing, []byte("{}"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !dec.Skipped || dec.SkipReason != "ping" {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestRouterInstallationEventsSkipped(t *testing.T) {
	r := Router{}
	for _, ev := range []EventType{EventInstallation, EventInstallationRepos, EventPullRequest} {
		dec, err := r.Route(context.Background(), ev, []byte("{}"))
		if err != nil {
			t.Errorf("%s: %v", ev, err)
		}
		if !dec.Skipped {
			t.Errorf("%s: expected skip", ev)
		}
	}
}

func TestRouterUnknownEventSkipped(t *testing.T) {
	r := Router{}
	dec, err := r.Route(context.Background(), EventType("star"), []byte("{}"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !dec.Skipped {
		t.Fatalf("expected skip")
	}
}

func TestRouterBranchDeleteSkipped(t *testing.T) {
	r := Router{Lookup: &fakeLookup{}}
	body := pushBody("acme/web", "main", "0000000000000000000000000000000000000000")
	if _, err := r.Route(context.Background(), EventPush, body); !errors.Is(err, ErrPushEventInvalid) {
		t.Fatalf("route error = %v, want ErrPushEventInvalid", err)
	}
}

func TestRouterSkipsUntrustedPushBeforeLookup(t *testing.T) {
	lookup := &fakeLookup{}
	body := strings.Replace(string(pushBody("acme/web", "main", testSHA)), `"clone_url"`, `"fork":true,"clone_url"`, 1)
	decision, err := (Router{Lookup: lookup}).Route(context.Background(), EventPush, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Skipped || decision.SkipReason != "untrusted_push_unsupported" {
		t.Fatalf("decision = %#v", decision)
	}
	if len(lookup.calls) != 0 {
		t.Fatalf("untrusted push performed lookup: %#v", lookup.calls)
	}
}

func TestPushEventInstallationIDParse(t *testing.T) {
	body := []byte(`{"installation":{"id":12345}}`)
	if id := PushEventInstallationID(body); id != 12345 {
		t.Errorf("id = %d", id)
	}
	if id := PushEventInstallationID([]byte("{}")); id != 0 {
		t.Errorf("id = %d", id)
	}
}

func TestServiceLinkValidate(t *testing.T) {
	cases := map[string]struct {
		l  ServiceLink
		ok bool
	}{
		"valid": {ServiceLink{InstallationID: 1, Repo: "acme/web", Branch: "main"}, true},
		"valid clone": {ServiceLink{
			InstallationID: 1,
			Repo:           "acme/web",
			Branch:         "main",
			CloneURL:       "https://github.com/acme/web.git",
		}, true},
		"no inst":   {ServiceLink{Repo: "acme/web", Branch: "main"}, false},
		"no repo":   {ServiceLink{InstallationID: 1, Branch: "main"}, false},
		"bad repo":  {ServiceLink{InstallationID: 1, Repo: "noslash", Branch: "main"}, false},
		"multi sl":  {ServiceLink{InstallationID: 1, Repo: "a/b/c", Branch: "main"}, false},
		"no branch": {ServiceLink{InstallationID: 1, Repo: "acme/web"}, false},
	}
	for name, tc := range cases {
		err := tc.l.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestServiceLinkNormalize(t *testing.T) {
	l := ServiceLink{
		InstallationID: 1,
		Repo:           "  acme/web  ",
		Branch:         "refs/heads/main",
		WatchPaths:     []string{"/services/api/", "  ", "docs/"},
		RootDirectory:  "apps/web/",
		ConfigPath:     "deploy/midnight.toml",
		RequiredChecks: []string{" build ", "", "test"},
	}
	out := l.Normalize()
	if out.Branch != "main" {
		t.Errorf("branch = %s", out.Branch)
	}
	if out.Repo != "acme/web" {
		t.Errorf("repo = %s", out.Repo)
	}
	if len(out.WatchPaths) != 2 || out.WatchPaths[0] != "services/api" || out.WatchPaths[1] != "docs" {
		t.Errorf("watchpaths = %+v", out.WatchPaths)
	}
	if len(out.RequiredChecks) != 2 {
		t.Errorf("checks = %+v", out.RequiredChecks)
	}
	if out.RootDirectory != "apps/web" || out.ConfigPath != "deploy/midnight.toml" {
		t.Errorf("layout = root %q config %q", out.RootDirectory, out.ConfigPath)
	}
}

func TestServiceLinkRejectsCredentialedOrMismatchedCloneURL(t *testing.T) {
	for name, cloneURL := range map[string]string{
		"credentialed": "https://user:token@github.com/acme/web.git",
		"wrong repo":   "https://github.com/acme/other.git",
		"not https":    "http://github.com/acme/web.git",
	} {
		t.Run(name, func(t *testing.T) {
			link := ServiceLink{
				InstallationID: 1,
				Repo:           "acme/web",
				Branch:         "main",
				CloneURL:       cloneURL,
			}
			if err := link.Validate(); err == nil {
				t.Fatalf("Validate(%q) error = nil", cloneURL)
			}
		})
	}
}

func TestServiceLinkRejectsAbsoluteOrEscapingSourceLayout(t *testing.T) {
	for name, mutate := range map[string]func(*ServiceLink){
		"absolute root": func(link *ServiceLink) { link.RootDirectory = "/apps/web" },
		"escaping root": func(link *ServiceLink) { link.RootDirectory = "../apps/web" },
		"absolute config": func(link *ServiceLink) {
			link.ConfigPath = "/deploy/midnight.toml"
		},
		"escaping config": func(link *ServiceLink) {
			link.ConfigPath = "deploy/../../midnight.toml"
		},
	} {
		t.Run(name, func(t *testing.T) {
			link := ServiceLink{InstallationID: 1, Repo: "acme/web", Branch: "main"}
			mutate(&link)
			if err := link.Normalize().Validate(); err == nil {
				t.Fatal("Normalize().Validate() error = nil")
			}
		})
	}
}

func TestAuthenticatedCloneURL(t *testing.T) {
	url := AuthenticatedCloneURL("https://github.com/acme/web.git", "acme/web", "ghs_test")
	want := "https://x-access-token:ghs_test@github.com/acme/web.git"
	if url != want {
		t.Errorf("url = %s, want %s", url, want)
	}
	// No token -> public clone URL.
	if got := AuthenticatedCloneURL("", "acme/web", ""); got != "https://github.com/acme/web.git" {
		t.Errorf("public = %s", got)
	}
	// Non-https URL passes through unchanged.
	if got := AuthenticatedCloneURL("git@github.com:acme/web.git", "acme/web", "ghs"); got != "git@github.com:acme/web.git" {
		t.Errorf("ssh = %s", got)
	}
}
