package github

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDefaultManifestValidates(t *testing.T) {
	m := DefaultManifest()
	m.Name = "midnight"
	m.URL = "https://midnight.example.com"
	m.HookAttributes.URL = "https://midnight.example.com/api/v1alpha/webhooks/inbound/github/ep1"
	m.RedirectURL = "https://midnight.example.com/setup/callback"
	m.SetupURL = "https://midnight.example.com/api/v1alpha/github/setup/callback"
	m.CallbackURLs = []string{m.SetupURL}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("validate default: %v", err)
	}
}

func TestManifestRejectsBannedPermissions(t *testing.T) {
	m := DefaultManifest()
	m.Name = "midnight"
	m.URL = "https://midnight.example.com"
	m.HookAttributes.URL = "https://midnight.example.com/x"
	m.DefaultPerms["administration"] = PermissionWrite
	if err := ValidateManifest(m); !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("expected banned permission rejection, got %v", err)
	}
	m.DefaultPerms = nil
	m.DefaultPerms = map[string]PermissionLevel{
		"contents":  PermissionRead,
		"metadata":  PermissionRead,
		"workflows": PermissionWrite,
	}
	if err := ValidateManifest(m); !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("expected workflows banned, got %v", err)
	}
}

func TestManifestRequiresPanelEvents(t *testing.T) {
	m := DefaultManifest()
	m.Name = "midnight"
	m.URL = "https://midnight.example.com"
	m.HookAttributes.URL = "https://midnight.example.com/x"
	m.DefaultEvents = []string{"push"} // missing pull_request etc.
	if err := ValidateManifest(m); !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("expected required events failure, got %v", err)
	}
}

func TestRenderManifestDeterministic(t *testing.T) {
	m := DefaultManifest()
	m.Name = "midnight"
	m.URL = "https://midnight.example.com"
	m.HookAttributes.URL = "https://midnight.example.com/api/v1alpha/webhooks/inbound/github/ep1"
	m.RedirectURL = "https://midnight.example.com/setup/callback"
	m.SetupURL = "https://midnight.example.com/api/v1alpha/github/setup/callback"
	m.CallbackURLs = []string{m.SetupURL}
	a, err := RenderManifest(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	b, err := RenderManifest(m)
	if err != nil {
		t.Fatalf("render2: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("render not deterministic")
	}
	if !strings.Contains(string(a), "\"contents\": \"read\"") {
		t.Fatalf("missing required permission in render: %s", a)
	}
	if !strings.Contains(string(a), "\"request_oauth_on_install\": true") ||
		!strings.Contains(string(a), "\"setup_url\": \"https://midnight.example.com/api/v1alpha/github/setup/callback\"") {
		t.Fatalf("missing secure setup handoff in render: %s", a)
	}
	// Verify the manifest is valid JSON.
	var raw map[string]any
	if err := json.Unmarshal(a, &raw); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
}

func TestParsePushEvent(t *testing.T) {
	const sha = "9f1c4b2e3d77a8e0bcf29f1c4b2e3d77a8e0bcf2"
	body := []byte(`{
		"ref": "refs/heads/main",
		"before": "0000000000000000000000000000000000000000",
		"after":  "` + sha + `",
		"installation": {"id": 99},
		"repository": {"id": 4242, "full_name": "acme/app", "private": true, "fork": false},
		"pusher": {"name": "alice", "email": "alice@example.com"},
		"head_commit": {"id":"` + sha + `","message":"feat: ship"},
		"sender": {"type":"User"}
	}`)
	ev, err := ParsePushEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Branch != "main" || ev.HeadSHA != sha || ev.InstallationID != 99 || ev.Repository.ID != 4242 {
		t.Fatalf("ev = %+v", ev)
	}
	if ev.Untrusted {
		t.Fatalf("expected trusted push for non-fork")
	}
	dr := PushEventToDeploy(ev)
	if dr.Provider != "github" || dr.Repo != "acme/app" || dr.SHA != ev.HeadSHA {
		t.Fatalf("deploy req = %+v", dr)
	}
	if dr.Idempotency != "gh-push-"+sha {
		t.Fatalf("idempotency key = %s", dr.Idempotency)
	}
}

func TestParsePushEventForkUntrusted(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/feature",
		"after":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"installation": {"id": 99},
		"repository": {"id": 4242, "full_name": "fork/app", "fork": true},
		"sender": {"type":"User"}
	}`)
	ev, err := ParsePushEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ev.Untrusted {
		t.Fatalf("fork push must be untrusted")
	}
}

func TestParsePushEventRequiresExactSourceIdentity(t *testing.T) {
	valid := `{
		"ref":"refs/heads/main",
		"after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"installation":{"id":99},
		"repository":{"id":4242,"full_name":"acme/app"},
		"sender":{"type":"User"}
	}`
	cases := map[string]string{
		"tag ref":          strings.Replace(valid, "refs/heads/main", "refs/tags/v1", 1),
		"empty branch":     strings.Replace(valid, "refs/heads/main", "refs/heads/", 1),
		"missing install":  strings.Replace(valid, `"installation":{"id":99}`, `"installation":{"id":0}`, 1),
		"missing repo id":  strings.Replace(valid, `"id":4242`, `"id":0`, 1),
		"short sha":        strings.Replace(valid, strings.Repeat("a", 40), "deadbeef", 1),
		"non-hex sha":      strings.Replace(valid, strings.Repeat("a", 40), strings.Repeat("z", 40), 1),
		"zero sha":         strings.Replace(valid, strings.Repeat("a", 40), strings.Repeat("0", 40), 1),
		"missing sender":   strings.Replace(valid, `"sender":{"type":"User"}`, `"sender":{}`, 1),
		"malformed repo":   strings.Replace(valid, "acme/app", "acme/app/extra", 1),
		"branch traversal": strings.Replace(valid, "refs/heads/main", "refs/heads/release..main", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePushEvent([]byte(body)); !errors.Is(err, ErrPushEventInvalid) {
				t.Fatalf("ParsePushEvent() error = %v, want ErrPushEventInvalid", err)
			}
		})
	}
}

func TestRequiredCheckPolicyAllSuccess(t *testing.T) {
	p := RequiredCheckPolicy{Required: []string{"build", "test"}}
	d := p.Evaluate([]CheckObservation{
		{Name: "build", Status: CheckSuccess},
		{Name: "test", Status: CheckSuccess},
	})
	if !d.Allowed {
		t.Fatalf("expected allowed; got %+v", d)
	}
}

func TestRequiredCheckPolicyMissingPending(t *testing.T) {
	p := RequiredCheckPolicy{Required: []string{"build", "test", "lint"}}
	d := p.Evaluate([]CheckObservation{
		{Name: "build", Status: CheckSuccess},
		{Name: "test", Status: CheckPending},
	})
	if d.Allowed {
		t.Fatalf("expected denied")
	}
	if d.ReasonCode != "DEPLOY_GATE_PENDING_CHECKS" {
		t.Fatalf("reason = %s", d.ReasonCode)
	}
	if len(d.MissingChecks) != 1 || d.MissingChecks[0] != "lint" {
		t.Fatalf("missing = %v", d.MissingChecks)
	}
	if len(d.PendingChecks) != 1 || d.PendingChecks[0] != "test" {
		t.Fatalf("pending = %v", d.PendingChecks)
	}
}

func TestRequiredCheckPolicyFailedCheck(t *testing.T) {
	p := RequiredCheckPolicy{Required: []string{"build"}}
	d := p.Evaluate([]CheckObservation{{Name: "build", Status: CheckFailure}})
	if d.Allowed {
		t.Fatalf("expected denied")
	}
	if len(d.FailedChecks) != 1 {
		t.Fatalf("failed = %v", d.FailedChecks)
	}
}

func TestGateEvaluateUsesChecker(t *testing.T) {
	gate := Gate{
		Checker: FixtureStatusChecker{Observations: []CheckObservation{{Name: "build", Status: CheckSuccess}}},
		Policy:  RequiredCheckPolicy{Required: []string{"build"}},
	}
	d, err := gate.Evaluate(context.Background(), "acme/app", "abc")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("not allowed: %+v", d)
	}
}

func TestStubAPIClientReturnsLiveDisabled(t *testing.T) {
	if _, err := (StubAPIClient{}).CreateDeploy(context.Background(), DeployRequest{}); !errors.Is(err, ErrLiveAPIDisabled) {
		t.Fatalf("expected ErrLiveAPIDisabled, got %v", err)
	}
	if _, err := (StubStatusChecker{}).CombinedStatus(context.Background(), "r", "s"); !errors.Is(err, ErrLiveGitHubDisabled) {
		t.Fatalf("expected ErrLiveGitHubDisabled, got %v", err)
	}
}
