package migrations

import (
	"strings"
	"testing"
)

func TestInstanceDomainSettingsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0072_instance_domain_settings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"CREATE TABLE instance_domain_settings",
		"singleton BOOLEAN PRIMARY KEY",
		"root_domain TEXT NOT NULL",
		"apps_domain TEXT NOT NULL",
		"ingress_target TEXT NOT NULL",
		"resource_version BIGINT NOT NULL",
		"VALUES (72, 'instance_domain_settings'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0072 up migration missing %q", required)
		}
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "schema_version: 72") ||
		!strings.Contains(manifest, "- version: 72\n    name: instance_domain_settings") {
		t.Fatal("manifest does not register migration 72")
	}
}
