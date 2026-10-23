package migrations

import (
	"strings"
	"testing"
)

func TestMultiRouteDomainResourcesMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0052_multi_route_domain_resources.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"ADD COLUMN resource_version BIGINT NOT NULL DEFAULT 1",
		"domains_resource_version_chk",
		"CREATE TABLE retired_product_domain_ids",
		"domain_id TEXT PRIMARY KEY",
		"idx_retired_product_domain_ids_scope",
		"cannot enable generated-domain singleton",
		"idx_domains_one_generated_per_service_environment",
		"CREATE TABLE service_domain_bindings",
		"service_domain_bindings_route_domain_scope_fk",
		"INSERT INTO service_domain_bindings",
		"r.path_prefix = '/'",
		"PRIMARY KEY (deployment_id, route_id)",
		"idx_deployment_route_intents_scope_deployment",
		"INSERT INTO deployment_route_intents",
		"dep.state IN ('queued', 'building', 'deploying', 'active')",
		"ON CONFLICT (deployment_id, route_id) DO NOTHING",
		"VALUES (52, 'multi_route_domain_resources'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	serviceBackfill := strings.Index(up, "INSERT INTO service_domain_bindings")
	primaryKeyWidening := strings.Index(up, "PRIMARY KEY (deployment_id, route_id)")
	deploymentBackfill := strings.Index(up, "INSERT INTO deployment_route_intents")
	if serviceBackfill < 0 ||
		primaryKeyWidening < 0 ||
		deploymentBackfill < 0 ||
		serviceBackfill >= deploymentBackfill ||
		primaryKeyWidening >= deploymentBackfill {
		t.Fatal("open-deployment backfill must follow service binding and primary-key widening")
	}
	for _, forbidden := range []string{
		"d.kind = 'custom'",
		"d.tls_desired = TRUE",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration must not backfill unsupported intent %q", forbidden)
		}
	}

	downBytes, err := FS.ReadFile("0052_multi_route_domain_resources.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"HAVING count(*) > 1",
		"RAISE EXCEPTION",
		"service-managed domain bindings exist",
		"convert live bindings before retrying",
		"preserve schema 0052 if product identities have been retired",
		"retired product domain identity history exists",
		"preserve schema 0052 or restore from a pre-product-domain backup",
		"PRIMARY KEY (deployment_id)",
		"DROP TABLE IF EXISTS service_domain_bindings",
		"DROP TABLE IF EXISTS retired_product_domain_ids",
		"DROP COLUMN IF EXISTS resource_version",
		"version = 52",
		"name = 'multi_route_domain_resources'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("down migration missing %q", required)
		}
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 52\n    name: multi_route_domain_resources") {
		t.Fatalf("manifest does not register migration 52:\n%s", manifest)
	}
}
