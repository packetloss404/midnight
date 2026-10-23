package migrations

import (
	"strings"
	"testing"
)

func TestDNSRecordOwnershipMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0063_dns_record_ownerships.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"CREATE TABLE dns_record_owners",
		"CREATE TABLE dns_record_ownerships",
		"UNIQUE (provider, zone_id, provider_record_id)",
		"record_type IN ('A', 'AAAA', 'CNAME', 'TXT')",
		"content_sha256 ~ '^[a-f0-9]{64}$'",
		"FOREIGN KEY (project_id, workspace_id)",
		"FOREIGN KEY (environment_id, project_id)",
		"REFERENCES dns_record_owners (provider, zone_id, owner_kind, owner_id)",
		"ON DELETE RESTRICT",
		"VALUES (63, 'dns_record_ownerships'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0063 up migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(up), "api_token") || strings.Contains(strings.ToLower(up), "record_content") {
		t.Fatal("0063 must not persist provider credentials or plaintext record content")
	}

	downBytes, err := FS.ReadFile("0063_dns_record_ownerships.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "IF EXISTS (SELECT 1 FROM dns_record_ownerships") || !strings.Contains(down, "DROP TABLE dns_record_ownerships") || !strings.Contains(down, "DROP TABLE dns_record_owners") {
		t.Fatal("0063 down migration must refuse ownership loss before dropping the table")
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 63\n    name: dns_record_ownerships") {
		t.Fatal("manifest does not register dns_record_ownerships as migration 63")
	}
}
