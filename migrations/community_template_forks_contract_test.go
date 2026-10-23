package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestCommunityTemplateForksContract(t *testing.T) {
	up, err := os.ReadFile("0067_community_template_forks.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	value := strings.ToLower(string(up))
	for _, required := range []string{
		"upstream_manifest jsonb not null",
		"draft_manifest jsonb not null",
		"provenance jsonb not null",
		"trust_class = 'local'",
		"provenance->>'ref' ~ '^[0-9a-f]{40}$'",
		"resource_version bigint not null",
		"community_template_forks_source_uniq",
	} {
		if !strings.Contains(value, required) {
			t.Fatalf("migration missing %q:\n%s", required, up)
		}
	}
}
