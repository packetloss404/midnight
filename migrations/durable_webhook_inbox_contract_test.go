package migrations

import (
	"strings"
	"testing"
)

func TestDurableWebhookInboxMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0050_durable_webhook_inbox.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"ALTER TABLE webhook_deliveries\n    RENAME TO webhook_deliveries_v5_history",
		"CREATE TABLE webhook_deliveries",
		"provider_delivery_id TEXT NOT NULL",
		"payload BYTEA",
		"payload_bytes BETWEEN 0 AND 26214400",
		"UNIQUE (provider, endpoint_id, provider_delivery_id)",
		"state IN (\n                'pending',\n                'processing',\n                'retry_wait',\n                'succeeded',\n                'ignored',\n                'dead_letter'",
		"state = 'processing'",
		"lease_token IS NOT NULL",
		"lease_expires_at IS NOT NULL",
		"webhook_deliveries_terminal_chk",
		"webhook_inbox_due_idx",
		"CREATE TABLE webhook_delivery_targets",
		"FOREIGN KEY (delivery_id, delivery_received_at)",
		"REFERENCES webhook_deliveries (id, received_at)",
		"UNIQUE (delivery_id, target_key)",
		"installation_grant_resource_version BIGINT NOT NULL",
		"repository_grant_resource_version BIGINT NOT NULL",
		"link_resource_version BIGINT NOT NULL",
		"installation_grant_updated_at <= delivery_received_at",
		"repository_grant_updated_at <= delivery_received_at",
		"link_updated_at <= delivery_received_at",
		"dispatch_payload BYTEA",
		"OCTET_LENGTH(dispatch_payload) BETWEEN 1 AND 262144",
		"dispatch_payload IS NULL",
		"dispatch_payload_sha256 ~ '^[0-9a-f]{64}$'",
		"webhook_delivery_targets_due_idx",
		"VALUES (50, 'durable_webhook_inbox'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0050 up migration missing %q", required)
		}
	}
	if strings.Contains(up, "UNIQUE (provider, event_hash)") ||
		strings.Contains(up, "UNIQUE (source, event_hash)") {
		t.Fatal("0050 must not deduplicate unrelated deliveries by event hash")
	}
	historyRename := strings.Index(up, "RENAME TO webhook_deliveries_v5_history")
	deliveryCreate := strings.Index(up, "CREATE TABLE webhook_deliveries")
	targetCreate := strings.Index(up, "CREATE TABLE webhook_delivery_targets")
	if historyRename < 0 || deliveryCreate < 0 || targetCreate < 0 ||
		historyRename >= deliveryCreate || deliveryCreate >= targetCreate {
		t.Fatal("0050 must preserve legacy rows before creating inbox and target truth")
	}

	downBytes, err := FS.ReadFile("0050_durable_webhook_inbox.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"IF EXISTS (SELECT 1 FROM webhook_deliveries LIMIT 1)",
		"migration 0050 rollback refused",
		"DROP TABLE IF EXISTS webhook_delivery_targets",
		"DROP TABLE IF EXISTS webhook_deliveries",
		"ALTER TABLE webhook_deliveries_v5_history\n    RENAME TO webhook_deliveries",
		"WHERE version = 50",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("0050 down migration missing %q", required)
		}
	}
	targetDrop := strings.Index(down, "DROP TABLE IF EXISTS webhook_delivery_targets")
	deliveryDrop := strings.Index(down, "DROP TABLE IF EXISTS webhook_deliveries")
	historyRestore := strings.Index(down, "RENAME TO webhook_deliveries")
	if targetDrop < 0 || deliveryDrop < 0 || historyRestore < 0 ||
		targetDrop >= deliveryDrop || deliveryDrop >= historyRestore {
		t.Fatal("0050 down must drop target/new inbox truth before restoring v5 history")
	}
}

func TestDurableWebhookInboxMigrationIsLatest(t *testing.T) {
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 50\n    name: durable_webhook_inbox") {
		t.Fatal("manifest does not register durable_webhook_inbox before the current schema version")
	}
}
