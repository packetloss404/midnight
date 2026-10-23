// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testFullSHA = "0123456789abcdef0123456789abcdef01234567"

func TestCommitStatusStateValidate(t *testing.T) {
	valid := []CommitStatusState{StatusPending, StatusSuccess, StatusError, StatusFailure}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
		if err := s.Validate(); err != nil {
			t.Errorf("%q Validate = %v", s, err)
		}
	}
	for _, s := range []CommitStatusState{"", "queued", "in_progress", "PENDING", "ok"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
		if err := s.Validate(); !errors.Is(err, ErrCommitStatusInvalid) {
			t.Errorf("%q Validate = %v, want ErrCommitStatusInvalid", s, err)
		}
	}
}

func TestPublishCommitStatusRequestValidate(t *testing.T) {
	base := PublishCommitStatusRequest{
		Owner: "acme", Repo: "web", SHA: testFullSHA,
		State: StatusSuccess, Token: NewSecret("ghs_x"),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("base should be valid: %v", err)
	}
	cases := map[string]PublishCommitStatusRequest{
		"no owner":    {Repo: "web", SHA: testFullSHA, State: StatusSuccess, Token: NewSecret("t")},
		"no repo":     {Owner: "acme", SHA: testFullSHA, State: StatusSuccess, Token: NewSecret("t")},
		"no sha":      {Owner: "acme", Repo: "web", State: StatusSuccess, Token: NewSecret("t")},
		"owner slash": {Owner: "a/b", Repo: "web", SHA: testFullSHA, State: StatusSuccess, Token: NewSecret("t")},
		"bad state":   {Owner: "acme", Repo: "web", SHA: testFullSHA, State: "nope", Token: NewSecret("t")},
		"no token":    {Owner: "acme", Repo: "web", SHA: testFullSHA, State: StatusSuccess},
	}
	for name, req := range cases {
		if err := req.Validate(); !errors.Is(err, ErrCommitStatusInvalid) {
			t.Errorf("%s: err = %v, want ErrCommitStatusInvalid", name, err)
		}
	}
}

func TestPublishCommitStatusPostsAndScrubsToken(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	var gotBody commitStatusRequestBody
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"id":42,"state":"success","context":"midnight/deploy"}`))
	}))
	defer srv.Close()

	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := c.PublishCommitStatus(context.Background(), PublishCommitStatusRequest{
		Owner: "acme", Repo: "web", SHA: testFullSHA,
		State: StatusSuccess, Context: "midnight/deploy",
		TargetURL: "https://midnight.example/d/1", Description: "Deployed",
		Token: NewSecret("ghs_live_token"),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if resp.ID != 42 || resp.State != StatusSuccess {
		t.Errorf("resp = %+v", resp)
	}
	if gotPath != "/repos/acme/web/statuses/"+testFullSHA {
		t.Errorf("path = %s", gotPath)
	}
	if gotAuth != "token ghs_live_token" {
		t.Errorf("auth = %s", gotAuth)
	}
	if gotBody.State != StatusSuccess || gotBody.Context != "midnight/deploy" {
		t.Errorf("body = %+v", gotBody)
	}
}

func TestPublishCommitStatusErrorScrubsToken(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	const token = "ghs_secret_should_not_leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		// Pathological server that echoes the token back in the error body.
		_, _ = w.Write([]byte(`{"message":"resource not accessible with token ` + token + `"}`))
	}))
	defer srv.Close()
	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = c.PublishCommitStatus(context.Background(), PublishCommitStatusRequest{
		Owner: "acme", Repo: "web", SHA: testFullSHA, State: StatusError, Token: NewSecret(token),
	})
	if !errors.Is(err, ErrAppHTTP) {
		t.Fatalf("want ErrAppHTTP, got %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked token: %v", err)
	}
	if !strings.Contains(err.Error(), redactedValue) {
		t.Fatalf("error did not redact token: %v", err)
	}
}

func TestPublishInstallationCommitStatusMintsThenPosts(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"ghs_minted","expires_at":"2026-05-01T13:00:00Z"}`))
	})
	var statusAuth string
	mux.HandleFunc("/repos/acme/web/statuses/"+testFullSHA, func(w http.ResponseWriter, r *http.Request) {
		statusAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":7,"state":"pending"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := c.PublishInstallationCommitStatus(context.Background(), PublishInstallationCommitStatusRequest{
		InstallationID: 42, RepositoryID: 11, RepoFullName: "acme/web",
		SHA: testFullSHA, State: StatusPending, TargetURL: "https://midnight.example/d/1",
	})
	if err != nil {
		t.Fatalf("publish installation: %v", err)
	}
	if resp.ID != 7 || resp.State != StatusPending {
		t.Errorf("resp = %+v", resp)
	}
	if statusAuth != "token ghs_minted" {
		t.Errorf("status auth = %s", statusAuth)
	}
}

func TestPublishInstallationCommitStatusValidation(t *testing.T) {
	c := &AppClient{cfg: AppConfig{}}
	cases := map[string]PublishInstallationCommitStatusRequest{
		"bad repo":   {InstallationID: 1, RepoFullName: "noslash", SHA: testFullSHA, State: StatusPending},
		"no install": {RepoFullName: "acme/web", SHA: testFullSHA, State: StatusPending},
		"bad state":  {InstallationID: 1, RepoFullName: "acme/web", SHA: testFullSHA, State: "nope"},
	}
	for name, req := range cases {
		if _, err := c.PublishInstallationCommitStatus(context.Background(), req); !errors.Is(err, ErrCommitStatusInvalid) {
			t.Errorf("%s: err = %v, want ErrCommitStatusInvalid", name, err)
		}
	}
}

func TestNewCommitStatusMetadata(t *testing.T) {
	md, err := NewCommitStatusMetadata(CommitStatusInput{
		InstallationID: 1, RepositoryID: 2, RepoFullName: "acme/web",
		SHA: testFullSHA, State: "success", TargetURL: "https://midnight.example/d/1",
	})
	if err != nil {
		t.Fatalf("valid metadata: %v", err)
	}
	if md.ProviderHost != "github.com" {
		t.Errorf("provider host = %s", md.ProviderHost)
	}
	if md.Context != DefaultCommitStatusContext {
		t.Errorf("context = %s", md.Context)
	}
}

func TestNewCommitStatusMetadataRejects(t *testing.T) {
	valid := CommitStatusInput{
		InstallationID: 1, RepositoryID: 2, RepoFullName: "acme/web",
		SHA: testFullSHA, State: "success", TargetURL: "https://midnight.example/d/1",
	}
	mutate := func(f func(*CommitStatusInput)) CommitStatusInput {
		in := valid
		f(&in)
		return in
	}
	cases := map[string]CommitStatusInput{
		"bad state":          mutate(func(i *CommitStatusInput) { i.State = "queued" }),
		"no installation":    mutate(func(i *CommitStatusInput) { i.InstallationID = 0 }),
		"no repository":      mutate(func(i *CommitStatusInput) { i.RepositoryID = 0 }),
		"repo not owner/rep": mutate(func(i *CommitStatusInput) { i.RepoFullName = "acme" }),
		"repo dot git":       mutate(func(i *CommitStatusInput) { i.RepoFullName = "acme/web.git" }),
		"short sha":          mutate(func(i *CommitStatusInput) { i.SHA = "abc123" }),
		"target no scheme":   mutate(func(i *CommitStatusInput) { i.TargetURL = "midnight.example/d/1" }),
		"target ftp":         mutate(func(i *CommitStatusInput) { i.TargetURL = "ftp://midnight.example/x" }),
		"target creds":       mutate(func(i *CommitStatusInput) { i.TargetURL = "https://user:pass@midnight.example/x" }),
		"target missing":     mutate(func(i *CommitStatusInput) { i.TargetURL = "" }),
		"context too long":   mutate(func(i *CommitStatusInput) { i.Context = strings.Repeat("x", 101) }),
		"context control":    mutate(func(i *CommitStatusInput) { i.Context = "ci\x00deploy" }),
		"desc too long":      mutate(func(i *CommitStatusInput) { i.Description = strings.Repeat("y", 141) }),
		"desc control":       mutate(func(i *CommitStatusInput) { i.Description = "bad\ndesc" }),
		"provider slash":     mutate(func(i *CommitStatusInput) { i.ProviderHost = "github.com/enterprise" }),
	}
	for name, in := range cases {
		if _, err := NewCommitStatusMetadata(in); !errors.Is(err, ErrCommitStatusInvalid) {
			t.Errorf("%s: err = %v, want ErrCommitStatusInvalid", name, err)
		}
	}
}

// ---- drain worker ----

type fakeStatusStore struct {
	pending  []PendingCommitStatus
	posted   map[string]time.Time
	failed   map[string]StatusFailureRequest
	claimErr error
	markErr  error
}

func (f *fakeStatusStore) ClaimCommitStatuses(_ context.Context, request StatusClaimRequest) ([]PendingCommitStatus, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	limit := request.Limit
	if limit > len(f.pending) {
		limit = len(f.pending)
	}
	claimed := append([]PendingCommitStatus(nil), f.pending[:limit]...)
	for i := range claimed {
		claimed[i].LeaseToken = "lease-1"
		claimed[i].Attempt++
		if claimed[i].MaxAttempts == 0 {
			claimed[i].MaxAttempts = 8
		}
	}
	return claimed, nil
}

func (f *fakeStatusStore) MarkCommitStatusPosted(_ context.Context, id, _ string, postedAt time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	if f.posted == nil {
		f.posted = map[string]time.Time{}
	}
	f.posted[id] = postedAt
	return nil
}

func (f *fakeStatusStore) MarkCommitStatusFailed(_ context.Context, request StatusFailureRequest) (bool, error) {
	if f.markErr != nil {
		return false, f.markErr
	}
	if f.failed == nil {
		f.failed = map[string]StatusFailureRequest{}
	}
	f.failed[request.ID] = request
	return request.Permanent, nil
}

type fakeStatusPublisher struct {
	failIDs map[string]bool
	calls   []PublishInstallationCommitStatusRequest
}

func (f *fakeStatusPublisher) PublishInstallationCommitStatus(_ context.Context, req PublishInstallationCommitStatusRequest) (CommitStatusResponse, error) {
	f.calls = append(f.calls, req)
	if f.failIDs[req.SHA] {
		return CommitStatusResponse{}, errors.New("boom")
	}
	return CommitStatusResponse{ID: 1, State: req.State}, nil
}

func TestStatusDrainWorkerRunOnce(t *testing.T) {
	store := &fakeStatusStore{pending: []PendingCommitStatus{
		{ID: "a", InstallationID: 1, RepositoryID: 2, ProviderHost: "github.com", RepoFullName: "acme/web", CommitSHA: testFullSHA, State: "success", Context: DefaultCommitStatusContext, TargetURL: "https://midnight.example/d/a"},
		{ID: "b", InstallationID: 1, RepositoryID: 2, ProviderHost: "github.com", RepoFullName: "acme/web", CommitSHA: strings.Repeat("b", 40), State: "pending", Context: DefaultCommitStatusContext, TargetURL: "https://midnight.example/d/b"},
	}}
	pub := &fakeStatusPublisher{}
	w := StatusDrainWorker{Store: store, Publisher: pub, BatchSize: 10, Now: func() time.Time { return time.Unix(1, 0).UTC() }}
	res, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if res.Fetched != 2 || res.Posted != 2 || res.Failed != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(store.posted) != 2 {
		t.Fatalf("posted = %v", store.posted)
	}
}

func TestStatusDrainWorkerRunOncePartialFailure(t *testing.T) {
	badSHA := strings.Repeat("b", 40)
	store := &fakeStatusStore{pending: []PendingCommitStatus{
		{ID: "a", InstallationID: 1, RepositoryID: 2, ProviderHost: "github.com", RepoFullName: "acme/web", CommitSHA: testFullSHA, State: "success", Context: DefaultCommitStatusContext, TargetURL: "https://midnight.example/d/a"},
		{ID: "b", InstallationID: 1, RepositoryID: 2, ProviderHost: "github.com", RepoFullName: "acme/web", CommitSHA: badSHA, State: "success", Context: DefaultCommitStatusContext, TargetURL: "https://midnight.example/d/b"},
	}}
	pub := &fakeStatusPublisher{failIDs: map[string]bool{badSHA: true}}
	w := StatusDrainWorker{Store: store, Publisher: pub}
	res, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if res.Posted != 1 || res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := store.posted["b"]; ok {
		t.Fatalf("failed status must not be marked posted")
	}
	if _, ok := store.posted["a"]; !ok {
		t.Fatalf("succeeded status must be marked posted")
	}
	if _, ok := store.failed["b"]; !ok {
		t.Fatalf("failed status must be durably rescheduled")
	}
}

func TestStatusDrainWorkerDeadLettersInvalidPersistedMetadata(t *testing.T) {
	store := &fakeStatusStore{pending: []PendingCommitStatus{{
		ID: "bad", InstallationID: 1, RepositoryID: 2, ProviderHost: "github.com",
		RepoFullName: "acme/web", CommitSHA: "not-a-sha", State: "success",
		Context: DefaultCommitStatusContext, TargetURL: "https://midnight.example/d/bad",
	}}}
	pub := &fakeStatusPublisher{}
	result, err := (StatusDrainWorker{Store: store, Publisher: pub}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if result.Failed != 1 || result.DeadLettered != 1 || len(pub.calls) != 0 {
		t.Fatalf("result=%+v calls=%d", result, len(pub.calls))
	}
	if failure := store.failed["bad"]; !failure.Permanent {
		t.Fatalf("invalid metadata failure=%+v, want permanent", failure)
	}
}

func TestStatusDrainWorkerNotConfigured(t *testing.T) {
	if _, err := (StatusDrainWorker{}).RunOnce(context.Background()); err == nil {
		t.Fatalf("want error when unconfigured")
	}
}
