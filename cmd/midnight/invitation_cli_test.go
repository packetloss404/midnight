// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/cliconfig"
)

const invitationTestExpiry = "2026-08-04T16:00:00Z"

func prepareInvitationCLI(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	if err := cliconfig.SetHumanSession(
		apiBase,
		cliconfig.HumanSessionCredential{Token: "midn_user_invitation_session"},
	); err != nil {
		t.Fatal(err)
	}
}

func requireInvitationHumanSession(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer midn_user_invitation_session" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestInvitationListUsesScopedMetadataOnlyRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireInvitationHumanSession(t, r)
		if r.Method != http.MethodGet || r.URL.Path != invitationPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		if len(query) != 4 ||
			query.Get("workspace_id") != "wrk_a" ||
			query.Get("project_id") != "prj_a" ||
			query.Get("limit") != "25" ||
			query.Get("cursor") != "cursor_page_2" {
			t.Fatalf("query = %v", query)
		}
		_, _ = w.Write([]byte(`{"items":[{
			"id":"inv_1",
			"email":"invitee@example.com",
			"workspace_id":"wrk_a",
			"project_id":"prj_a",
			"project_role":"editor",
			"token_prefix":"midn_inv_public",
			"expires_at":"2026-08-04T16:00:00Z"
		}],"next_cursor":"cursor_page_3"}`))
	}))
	defer server.Close()
	prepareInvitationCLI(t, server.URL)

	var stdout, stderr strings.Builder
	code := runInvitation([]string{
		"list",
		"--api-base", server.URL,
		"--workspace-id", "wrk_a",
		"--project-id", "prj_a",
		"--limit", "25",
		"--cursor", "cursor_page_2",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "invitee@example.com") ||
		!strings.Contains(stdout.String(), "editor") ||
		!strings.Contains(
			stdout.String(),
			"next page: midnight invitation list --workspace-id wrk_a --project-id prj_a --limit 25 --cursor cursor_page_3",
		) ||
		strings.Contains(stdout.String(), "invitation_token") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestInvitationListRejectsLimitOutsideContract(t *testing.T) {
	for _, limit := range []string{"0", "201"} {
		var stdout, stderr strings.Builder
		code := runInvitation([]string{
			"list",
			"--workspace-id", "wrk_a",
			"--limit", limit,
		}, &stdout, &stderr)
		if code != 64 ||
			!strings.Contains(stderr.String(), "--limit must be between 1 and 200") {
			t.Fatalf(
				"limit=%q code=%d stdout=%q stderr=%q",
				limit,
				code,
				stdout.String(),
				stderr.String(),
			)
		}
	}
}

func TestInvitationCreateSendsOneRoleAndPrintsCredentialOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireInvitationHumanSession(t, r)
		if r.Method != http.MethodPost || r.URL.Path != invitationPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "invite-release-1" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 4 ||
			body["email"] != "invitee@example.com" ||
			body["workspace_id"] != "wrk_a" ||
			body["workspace_role"] != "member" ||
			body["expires_at"] != invitationTestExpiry {
			t.Fatalf("body = %#v", body)
		}
		if _, ok := body["project_id"]; ok {
			t.Fatalf("workspace invitation sent project_id: %#v", body)
		}
		if _, ok := body["project_role"]; ok {
			t.Fatalf("workspace invitation sent project_role: %#v", body)
		}
		_, _ = w.Write([]byte(`{
			"invitation_token":"midn_inv_secret_once",
			"invitation":{
				"id":"inv_1",
				"email":"invitee@example.com",
				"workspace_id":"wrk_a",
				"workspace_role":"member",
				"token_prefix":"midn_inv_public",
				"expires_at":"2026-08-04T16:00:00Z"
			}
		}`))
	}))
	defer server.Close()
	prepareInvitationCLI(t, server.URL)

	var stdout, stderr strings.Builder
	code := runInvitation([]string{
		"create",
		"--api-base", server.URL,
		"--workspace-id", "wrk_a",
		"--workspace-role", "member",
		"--email", " Invitee@Example.com ",
		"--expires-at", invitationTestExpiry,
		"--idempotency-key", "invite-release-1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Count(stdout.String(), "midn_inv_secret_once") != 1 ||
		!strings.Contains(stdout.String(), "midnight invitation accept --invitation-token") ||
		!strings.Contains(stdout.String(), "does not send invitation email") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestInvitationRevokeUsesScopedQueryAndExplicitReplayKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireInvitationHumanSession(t, r)
		if r.Method != http.MethodDelete || r.URL.Path != invitationPath+"/inv_1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("workspace_id") != "wrk_a" ||
			r.URL.Query().Get("project_id") != "prj_a" ||
			r.Header.Get("Idempotency-Key") != "revoke-invite-1" {
			t.Fatalf("query=%v key=%q", r.URL.Query(), r.Header.Get("Idempotency-Key"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	prepareInvitationCLI(t, server.URL)

	var stdout, stderr strings.Builder
	code := runInvitation([]string{
		"revoke",
		"--api-base", server.URL,
		"--workspace-id", "wrk_a",
		"--project-id", "prj_a",
		"--idempotency-key", "revoke-invite-1",
		"inv_1",
	}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Revoked invitation inv_1") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestInvitationReissueSendsOnlyScopeAndFixedExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireInvitationHumanSession(t, r)
		if r.Method != http.MethodPost || r.URL.Path != invitationPath+"/inv_1/reissue" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != "reissue-invite-1" {
			t.Fatalf("Idempotency-Key = %q", r.Header.Get("Idempotency-Key"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 3 ||
			body["workspace_id"] != "wrk_a" ||
			body["project_id"] != "prj_a" ||
			body["expires_at"] != invitationTestExpiry {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{
			"invitation_token":"midn_inv_reissued_secret",
			"invitation":{
				"id":"inv_2",
				"email":"invitee@example.com",
				"workspace_id":"wrk_a",
				"project_id":"prj_a",
				"project_role":"editor",
				"expires_at":"2026-08-04T16:00:00Z"
			}
		}`))
	}))
	defer server.Close()
	prepareInvitationCLI(t, server.URL)

	var stdout, stderr strings.Builder
	code := runInvitation([]string{
		"reissue",
		"--api-base", server.URL,
		"--workspace-id", "wrk_a",
		"--project-id", "prj_a",
		"--expires-at", invitationTestExpiry,
		"--idempotency-key", "reissue-invite-1",
		"inv_1",
	}, &stdout, &stderr)
	if code != 0 ||
		strings.Count(stdout.String(), "midn_inv_reissued_secret") != 1 ||
		!strings.Contains(stdout.String(), "Reissued invitation inv_2") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestInvitationAcceptSendsTokenOnlyWithHumanSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireInvitationHumanSession(t, r)
		if r.Method != http.MethodPost || r.URL.Path != invitationPath+"/accept" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != "accept-invite-1" {
			t.Fatalf("Idempotency-Key = %q", r.Header.Get("Idempotency-Key"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["invitation_token"] != "midn_inv_secret" {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{
			"id":"inv_1",
			"email":"invitee@example.com",
			"workspace_id":"wrk_a",
			"workspace_role":"member",
			"accepted_at":"2026-07-28T16:00:00Z",
			"accepted_by_user_id":"usr_invitee"
		}`))
	}))
	defer server.Close()
	prepareInvitationCLI(t, server.URL)

	var stdout, stderr strings.Builder
	code := runInvitation([]string{
		"accept",
		"--api-base", server.URL,
		"--invitation-token", "midn_inv_secret",
		"--idempotency-key", "accept-invite-1",
	}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Accepted invitation inv_1") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestInvitationMutationsRequireReplaySafeInputs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "create idempotency key",
			args: []string{
				"create", "--workspace-id", "wrk_a", "--workspace-role", "member",
				"--email", "invitee@example.com", "--expires-at", invitationTestExpiry,
			},
			want: "--idempotency-key is required",
		},
		{
			name: "create fixed expiry",
			args: []string{
				"create", "--workspace-id", "wrk_a", "--workspace-role", "member",
				"--email", "invitee@example.com", "--idempotency-key", "invite-1",
			},
			want: "--expires-at is required",
		},
		{
			name: "two roles",
			args: []string{
				"create", "--workspace-id", "wrk_a", "--project-id", "prj_a",
				"--workspace-role", "member", "--project-role", "editor",
				"--email", "invitee@example.com", "--expires-at", invitationTestExpiry,
				"--idempotency-key", "invite-1",
			},
			want: "exactly one",
		},
		{
			name: "accept idempotency key",
			args: []string{"accept", "--invitation-token", "midn_inv_secret"},
			want: "--idempotency-key is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := runInvitation(test.args, &stdout, &stderr)
			if code != 64 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("code=%d stderr=%q, want %q", code, stderr.String(), test.want)
			}
		})
	}
}

func TestInvitationCommandsRequireHumanSession(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	if err := cliconfig.SetOperatorRecoveryToken(
		"https://midnight.example",
		"midn_admin_recovery",
	); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runInvitation([]string{
		"list",
		"--api-base", "https://midnight.example",
		"--workspace-id", "wrk_a",
	}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "requires a human session") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestInvitationCredentialRedirectIsRefused(t *testing.T) {
	received := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destination, err := url.Parse(target.URL + r.URL.Path)
		if err != nil {
			t.Fatal(err)
		}
		http.Redirect(w, r, destination.String(), http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	prepareInvitationCLI(t, source.URL)

	var stdout, stderr strings.Builder
	code := runInvitation([]string{
		"create",
		"--api-base", source.URL,
		"--workspace-id", "wrk_a",
		"--workspace-role", "member",
		"--email", "invitee@example.com",
		"--expires-at", invitationTestExpiry,
		"--idempotency-key", "redirect-invite-1",
	}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), errAuthRedirect.Error()) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	select {
	case authorization := <-received:
		t.Fatalf("redirect target received Authorization %q", authorization)
	default:
	}
}
