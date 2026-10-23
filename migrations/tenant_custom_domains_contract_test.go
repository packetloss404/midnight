package migrations

import (
	"strings"
	"testing"
)

func TestTenantCustomDomainsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0053_tenant_custom_domains.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ",
		"idx_domain_dns_challenges_pending",
		"service_domain_bindings",
		"VALUES (53, 'tenant_custom_domains'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0053 up migration missing %q", required)
		}
	}
	downBytes, err := FS.ReadFile("0053_tenant_custom_domains.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{"tenant custom domain bindings exist", "DROP COLUMN IF EXISTS verified_at", "version = 53", "name = 'tenant_custom_domains'"} {
		if !strings.Contains(down, required) {
			t.Fatalf("0053 down migration missing %q", required)
		}
	}
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 53\n    name: tenant_custom_domains") {
		t.Fatalf("manifest does not register migration 53")
	}
}
