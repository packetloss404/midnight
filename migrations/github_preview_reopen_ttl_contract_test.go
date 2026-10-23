package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestGitHubPreviewReopenTTLContract(t *testing.T) {
	up, err := os.ReadFile("0066_github_preview_reopen_ttl.up.sql")
	if err != nil { t.Fatal(err) }
	value := strings.ToLower(string(up))
	if strings.Contains(value, "created_at + interval '30 days'") ||
		!strings.Contains(value, "expires_at > created_at") ||
		!strings.Contains(value, "github_pull_request_previews_lifecycle_chk") {
		t.Fatalf("migration does not admit safe long-lived PR reopen:\n%s", up)
	}
}
