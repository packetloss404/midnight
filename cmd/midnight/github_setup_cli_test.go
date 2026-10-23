package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/cliconfig"
)

func TestGitHubSetupCLIUsesHumanSessionAndPrintsInstallURL(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/workspaces/wrk_one/github/setup" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer midn_user_setup" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Fatalf("setup body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"install_url":"https://github.com/apps/midnight/installations/new?state=signed","expires_at":"2026-08-30T12:10:00Z"}`))
	}))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{Token: "midn_user_setup"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runGitHub([]string{"setup", "--workspace-id", "wrk_one", "--api-base", server.URL}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "https://github.com/apps/midnight/installations/new?state=signed") ||
		!strings.Contains(stdout.String(), "Expires: 2026-08-30T12:10:00Z") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
